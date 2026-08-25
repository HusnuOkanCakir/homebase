// Package auth owns users, passwords, sessions and permissions.
//
// Authorisation decisions are made here, in core, before any privileged
// operation is requested. hostd re-checks independently — not as a division of
// labour but as defence in depth, because core is the component talking to the
// network and therefore the one most likely to be compromised.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Permissions. Read and write are separate throughout, which is what makes
// Stage 2A — an AI that can explain the server but change nothing — expressible
// as a token rather than as a promise.
const (
	PermSystemRead    = "system.read"
	PermSystemManage  = "system.manage"
	PermAppsRead      = "apps.read"
	PermAppsManage    = "apps.manage"
	PermStorageRead   = "storage.read"
	PermStorageModify = "storage.modify"
	PermNetworkDiag   = "network.diagnose"
	PermNetworkModify = "network.modify"
	PermBackupRead    = "backup.read"
	PermBackupRun     = "backup.run"
	PermUpdateRead    = "update.read"
	PermUpdateManage  = "update.manage"

	// PermAssistantUse is the right to ask the local model a question.
	//
	// Its own permission rather than a fold into system.read, because it is a
	// different kind of thing: everything else here reads or changes the
	// machine, and this spends it. A question occupies all four cores for the
	// length of an answer, so somebody who should be able to see the dashboard
	// is not automatically somebody who should be able to make the server
	// unresponsive for a minute.
	PermAssistantUse = "assistant.use"

	// PermAssistantUnrestricted is the right to talk to a model whose refusal
	// behaviour a third party removed.
	//
	// Deliberately absent from AdministratorPermissions, which every other
	// permission here belongs to. It is not granted by setting the machine up,
	// it is not granted by being an administrator, and there is no way to grant
	// it through the API — because an endpoint that grants it is an endpoint a
	// stolen session can call. It is added by hand, at the machine, with
	// `homebasectl assistant unrestricted`.
	//
	// The distinction it draws is not "trusted user" but "user who has decided
	// to". A household member given an account to see whether the backup ran
	// should not silently acquire this by inheriting the administrator's
	// defaults, which is exactly what would happen if it lived in the list
	// below.
	PermAssistantUnrestricted = "assistant.unrestricted"
)

// AdministratorPermissions is what the first-run administrator receives.
var AdministratorPermissions = []string{
	PermSystemRead, PermSystemManage,
	PermAppsRead, PermAppsManage,
	PermStorageRead, PermStorageModify,
	PermNetworkDiag, PermNetworkModify,
	PermBackupRead, PermBackupRun,
	PermUpdateRead, PermUpdateManage,
	PermAssistantUse,
}

const (
	SessionLifetime = 14 * 24 * time.Hour
	MinPasswordLen  = 12
)

var (
	ErrNoSuchUser        = errors.New("no such user")
	ErrInvalidCredential = errors.New("invalid credentials")
	ErrSessionExpired    = errors.New("session expired")
	ErrAlreadySetUp      = errors.New("an administrator already exists")
	ErrWeakPassword      = errors.New("password is too short")
)

type User struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	Permissions []string  `json:"permissions"`
	CreatedAt   time.Time `json:"created_at"`
}

func (u *User) Can(permission string) bool {
	for _, p := range u.Permissions {
		if p == permission {
			return true
		}
	}
	return false
}

type Service struct {
	db *sql.DB
}

func NewService(db *sql.DB) *Service { return &Service{db: db} }

// DB exposes the handle for tests that need to construct a user state the
// public API deliberately cannot produce — a signed-in account with reduced
// permissions, for instance.
func (s *Service) DB() *sql.DB { return s.db }

// NeedsSetup reports whether the server has no administrator yet.
//
// Until it does, the dashboard shows first-run setup and nothing else. A server
// that answers requests before anyone has claimed it is a server anybody on the
// network can claim.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	return count == 0, err
}

// CreateAdministrator performs first-run setup. It succeeds exactly once.
func (s *Service) CreateAdministrator(ctx context.Context, username, password string) (*User, error) {
	needs, err := s.NeedsSetup(ctx)
	if err != nil {
		return nil, err
	}
	if !needs {
		// The check and the insert are not atomic, but the username unique
		// constraint catches the race and the second caller gets this error
		// either way.
		return nil, ErrAlreadySetUp
	}
	return s.createUser(ctx, username, password, AdministratorPermissions)
}

// CreateUser creates an account with exactly the permissions given.
//
// Not reachable through the API yet — additional accounts are a later milestone —
// but the distinction between a reader and an administrator has to be real from
// the start, because retrofitting it means auditing every handler again. The
// permission set is the caller's choice on purpose: there is no "default user"
// here that quietly turns out to be an administrator.
func (s *Service) CreateUser(ctx context.Context, username, password string, permissions []string) (*User, error) {
	return s.createUser(ctx, username, password, permissions)
}

func (s *Service) createUser(ctx context.Context, username, password string, permissions []string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}
	if len([]rune(password)) < MinPasswordLen {
		return nil, ErrWeakPassword
	}

	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}

	encodedPerms, err := json.Marshal(permissions)
	if err != nil {
		return nil, err
	}

	user := &User{
		ID:          newID("usr"),
		Username:    username,
		Permissions: permissions,
		CreatedAt:   time.Now().UTC(),
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, permissions, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		user.ID, user.Username, hash, string(encodedPerms),
		user.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrAlreadySetUp
		}
		return nil, err
	}

	return user, nil
}

// Authenticate verifies a username and password.
//
// The same error is returned whether the user does not exist or the password is
// wrong, and a hash is computed either way — otherwise the response time tells
// an attacker which usernames are real.
func (s *Service) Authenticate(ctx context.Context, username, password string) (*User, error) {
	var (
		id          string
		hash        string
		permissions string
		created     string
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT id, password_hash, permissions, created_at FROM users WHERE username = ?`,
		strings.TrimSpace(username)).Scan(&id, &hash, &permissions, &created)

	if errors.Is(err, sql.ErrNoRows) {
		// Deliberate work against a dummy hash, so a missing user and a wrong
		// password take comparable time.
		_, _ = VerifyPassword(password, dummyHash)
		return nil, ErrInvalidCredential
	}
	if err != nil {
		return nil, err
	}

	ok, err := VerifyPassword(password, hash)
	if err != nil || !ok {
		return nil, ErrInvalidCredential
	}

	user := &User{ID: id, Username: username}
	_ = json.Unmarshal([]byte(permissions), &user.Permissions)
	user.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)

	_, _ = s.db.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), id)

	return user, nil
}

// UserByName looks up an account without authenticating as it.
//
// Only for callers that have already established their right to act — the
// console tool, which needs root on the machine to run at all. Nothing reachable
// from the network may use this to turn a username into a yes-or-no answer about
// whether that account exists.
func (s *Service) UserByName(ctx context.Context, username string) (*User, error) {
	var (
		id          string
		permissions string
		created     string
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT id, permissions, created_at FROM users WHERE username = ?`,
		strings.TrimSpace(username)).Scan(&id, &permissions, &created)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSuchUser
	}
	if err != nil {
		return nil, err
	}

	user := &User{ID: id, Username: strings.TrimSpace(username)}
	_ = json.Unmarshal([]byte(permissions), &user.Permissions)
	user.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return user, nil
}

// CreateSession issues a session token.
//
// The token is returned once and never stored — only its SHA-256 is kept, so a
// stolen database does not hand over live sessions.
func (s *Service) CreateSession(ctx context.Context, userID, userAgent string) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().UTC().Add(SessionLifetime)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, user_id, created_at, expires_at, user_agent)
		VALUES (?, ?, ?, ?, ?)`,
		hashToken(token), userID,
		time.Now().UTC().Format(time.RFC3339Nano),
		expires.Format(time.RFC3339Nano),
		truncate(userAgent, 200))
	if err != nil {
		return "", time.Time{}, err
	}

	return token, expires, nil
}

// UserForSession resolves a token to its user, or reports why not.
func (s *Service) UserForSession(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrInvalidCredential
	}

	var (
		userID      string
		expires     string
		username    string
		permissions string
		created     string
	)

	err := s.db.QueryRowContext(ctx, `
		SELECT s.user_id, s.expires_at, u.username, u.permissions, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ?`, hashToken(token)).
		Scan(&userID, &expires, &username, &permissions, &created)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCredential
	}
	if err != nil {
		return nil, err
	}

	expiry, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || time.Now().UTC().After(expiry) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
		return nil, ErrSessionExpired
	}

	user := &User{ID: userID, Username: username}
	_ = json.Unmarshal([]byte(permissions), &user.Permissions)
	user.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return user, nil
}

func (s *Service) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// PurgeExpiredSessions removes sessions that have lapsed.
func (s *Service) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// --- Password hashing --------------------------------------------------------

// argon2id parameters.
//
// Chosen for a machine that may have 4 GB of RAM and other work to do: 64 MiB
// and one pass, which is OWASP's minimum recommendation rather than a
// comfortable margin. The encoded hash carries its own parameters, so raising
// these later does not invalidate existing passwords.
const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// hashSlots bounds how many argon2 computations run at once.
//
// Each one reserves 64 MiB by design, and the endpoints that trigger them —
// sign in, first-run setup, recovery — are all unauthenticated. Without a bound,
// enough simultaneous requests exhaust the memory of a machine that may only
// have four gigabytes, and no credentials are needed to send them. Four at a
// time caps that at 256 MiB; the rest queue, which is the correct behaviour when
// the alternative is the kernel choosing a process to kill.
var hashSlots = make(chan struct{}, 4)

func acquireHashSlot() { hashSlots <- struct{}{} }
func releaseHashSlot() { <-hashSlots }

// dummyHash is verified against when a username does not exist, so that the
// response time does not reveal which usernames are real.
var dummyHash = mustHash("this password is never correct")

func mustHash(password string) string {
	h, err := HashPassword(password)
	if err != nil {
		panic("auth: " + err.Error())
	}
	return h
}

// HashPassword returns an encoded argon2id hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	acquireHashSlot()
	defer releaseHashSlot()

	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword checks a password against an encoded hash, using the
// parameters recorded in the hash rather than the current constants.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, fmt.Errorf("unrecognised password hash format")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, err
	}

	var memory uint32
	var iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, err
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}

	// argon2.IDKey panics inside blake2b when asked for a zero-length key, and
	// a malformed hash reaching this function must not take the process down —
	// this is the authentication path, so a panic here is a way to stop anyone
	// logging in at all. Refuse implausible parameters rather than passing them
	// to a function that assumes they are sane.
	if len(salt) == 0 || len(want) == 0 {
		return false, fmt.Errorf("password hash has an empty salt or key")
	}
	if memory == 0 || iterations == 0 || threads == 0 {
		return false, fmt.Errorf("password hash has zeroed parameters")
	}

	acquireHashSlot()
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	releaseHashSlot()

	// Constant time: a byte-by-byte comparison leaks how much of the hash
	// matched, which is enough to attack it given enough attempts.
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return prefix + "_" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return prefix + "_" + hex.EncodeToString(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
