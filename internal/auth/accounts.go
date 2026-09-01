package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Adding the rest of the household.
//
// Until now this server has had exactly one account, made once at first-run
// setup, and no way to make another. Everything needed for more than one was
// already here — permissions are per account, `Can` is real, every route
// declares what it needs — except the part where somebody creates one.
//
// **Nobody sets anybody else's password.** An administrator creating an account
// gets a joining code, not a password: the same one-time code the recovery flow
// already issues, handed over once and exchanged by the new person for a
// password only they know. The alternative is an administrator who knows how to
// sign in as their father, which is a different product.
//
// So the row is created with a password that cannot be used. Not an empty hash
// and not a nullable column — thirty-two random bytes, hashed, and discarded
// unread. There is no code path where a missing password verifies, because
// there is no missing password.

// usernamePattern is what a name has to look like to be usable everywhere.
//
// The same shape as a share name in hostd, and that is not a coincidence: a
// person's file-sharing account is `hbshare-<username>`, so a name Linux accepts
// and Samba does not — or one that differs from another only by case, which SMB
// treats as the same and Linux does not — is a name that works in the dashboard
// and fails at the file server with an authentication box that never explains
// itself. Somebody already lost an evening to exactly that.
//
// Applied to accounts created from now on. The administrator made at first-run
// setup is never renamed for this; if their name does not fit, they are told
// where it will not work rather than having it changed under them.
var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)

// ValidUsername reports whether a name can be used for a new account.
func ValidUsername(name string) bool {
	return usernamePattern.MatchString(name)
}

var (
	// ErrInvalidUsername is returned rather than silently correcting a name.
	ErrInvalidUsername = errors.New("that name cannot be used")

	// ErrLastAdministrator is returned rather than leaving a server nobody can
	// administer. It is recoverable and the recovery is to promote somebody.
	ErrLastAdministrator = errors.New("this is the last administrator")

	// ErrUnknownRole is returned for a role name that is not one of the three.
	// Never defaulted: a typo must not quietly produce an account with
	// permissions nobody chose.
	ErrUnknownRole = errors.New("no such role")

	// ErrUserExists is returned rather than converging, because "create" and
	// "change" are different intentions and one of them is destructive.
	ErrUserExists = errors.New("that name is taken")
)

// Account is a person on this server, as an administrator sees them.
//
// Deliberately not the permission array. The whole point of naming roles is
// that nobody has to read fourteen strings to answer "what can my father do".
type Account struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	// HasSignedIn separates an invitation nobody has accepted from an account
	// somebody is using — the question an administrator actually has when
	// looking at this list a week later.
	HasSignedIn bool       `json:"has_signed_in"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// Accounts lists everybody on this server, oldest first.
func (s *Service) Accounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, permissions, created_at, last_login_at
		   FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	accounts := []Account{}
	for rows.Next() {
		var (
			id, username, rawPermissions, createdAt string
			lastLogin                               sql.NullString
		)
		if err := rows.Scan(&id, &username, &rawPermissions, &createdAt, &lastLogin); err != nil {
			return nil, err
		}
		var permissions []string
		if err := json.Unmarshal([]byte(rawPermissions), &permissions); err != nil {
			// A row whose permissions cannot be read is reported rather than
			// skipped. An account missing from this list is an account nobody
			// can remove.
			permissions = nil
		}
		account := Account{
			ID:       id,
			Username: username,
			Role:     RoleOf(&User{Permissions: permissions}),
		}
		account.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if lastLogin.Valid && lastLogin.String != "" {
			if at, err := time.Parse(time.RFC3339, lastLogin.String); err == nil {
				account.LastLoginAt = &at
				account.HasSignedIn = true
			}
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

// CreateInvitedAccount adds a person and returns the code they sign in with.
//
// The code is shown once, here, and is not recoverable — it is stored the way a
// password is. An administrator who loses it issues another; they never learn
// the password that replaces it.
func (s *Service) CreateInvitedAccount(ctx context.Context, username, role string) (*User, string, error) {
	// Validated as typed, not lowercased first. "Father" and "father" are the
	// same account to SMB and different accounts to Linux, so quietly turning
	// one into the other is how somebody ends up with a name they did not
	// choose and an authentication box that never explains itself. Refused, with
	// a message saying what a name may be.
	username = strings.TrimSpace(username)
	if !ValidUsername(username) {
		return nil, "", ErrInvalidUsername
	}

	permissions := PermissionsForRole(role)
	if permissions == nil {
		return nil, "", ErrUnknownRole
	}

	// A password that exists and cannot be guessed, rather than a special case
	// in the authentication path. Generated, hashed, and never seen again.
	var unusable [32]byte
	if _, err := rand.Read(unusable[:]); err != nil {
		return nil, "", err
	}

	user, err := s.createUser(ctx, username, base64.RawStdEncoding.EncodeToString(unusable[:]), permissions)
	if err != nil {
		// createUser reports a name collision as ErrAlreadySetUp, which is the
		// right sentence for first-run setup and the wrong one here: nothing is
		// "already set up", somebody already has that name.
		if errors.Is(err, ErrAlreadySetUp) {
			return nil, "", ErrUserExists
		}
		return nil, "", err
	}

	code, err := s.IssueRecoveryCode(ctx, user.ID)
	if err != nil {
		// The account exists and nobody can get into it. Removed rather than
		// left as a row an administrator would have to notice and clean up.
		_, _ = s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, user.ID)
		return nil, "", fmt.Errorf("issuing the joining code: %w", err)
	}
	return user, code, nil
}

// SetRole changes what somebody may do.
//
// Read, count and write in one transaction, so two administrators demoting each
// other at the same moment cannot both succeed and leave a server nobody can
// administer.
func (s *Service) SetRole(ctx context.Context, userID, role string) error {
	permissions := PermissionsForRole(role)
	if permissions == nil {
		return ErrUnknownRole
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := permissionsOfTx(ctx, tx, userID)
	if err != nil {
		return err
	}

	// assistant.unrestricted survives a role change.
	//
	// It is granted by hand at the machine, deliberately outside every role,
	// and a role change that silently revoked it would be a mystery with no
	// error message attached. Nothing else carries over.
	if slices.Contains(existing, PermAssistantUnrestricted) {
		permissions = append(permissions, PermAssistantUnrestricted)
	}

	if role != RoleAdministrator {
		if err := requireAnotherAdministrator(ctx, tx, userID); err != nil {
			return err
		}
	}

	encoded, err := json.Marshal(permissions)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE users SET permissions = ? WHERE id = ?`, string(encoded), userID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNoSuchUser
	}
	return tx.Commit()
}

// DeleteAccount removes a person, their sessions and their recovery code.
//
// Their files are deliberately left. "Remove the account" and "delete their
// photographs" are different intentions and collapsing them into one button is
// how somebody loses a decade of pictures by clicking Remove — the same rule
// that applies to uninstalling an application and to unsharing a folder.
func (s *Service) DeleteAccount(ctx context.Context, userID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := requireAnotherAdministrator(ctx, tx, userID); err != nil {
		return err
	}

	// Sessions and recovery codes cascade from the users row, so a removed
	// account's live session dies with it rather than lasting a fortnight.
	result, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNoSuchUser
	}
	return tx.Commit()
}

// UserByID is how a handler turns a path parameter into a person.
func (s *Service) UserByID(ctx context.Context, userID string) (*User, error) {
	var (
		username, rawPermissions, createdAt string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT username, permissions, created_at FROM users WHERE id = ?`, userID).
		Scan(&username, &rawPermissions, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSuchUser
	}
	if err != nil {
		return nil, err
	}
	user := &User{ID: userID, Username: username}
	if err := json.Unmarshal([]byte(rawPermissions), &user.Permissions); err != nil {
		return nil, err
	}
	user.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return user, nil
}

// requireAnotherAdministrator refuses to leave the server without one.
//
// Counted inside the caller's transaction, against the same predicate the
// migrations use for "administrator": whoever holds system.manage.
func requireAnotherAdministrator(ctx context.Context, tx *sql.Tx, excluding string) error {
	var others int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users
		  WHERE id != ?
		    AND json_valid(permissions)
		    AND EXISTS (SELECT 1 FROM json_each(users.permissions)
		                 WHERE value = ?)`,
		excluding, PermSystemManage).Scan(&others)
	if err != nil {
		return err
	}
	if others == 0 {
		return ErrLastAdministrator
	}
	return nil
}

func permissionsOfTx(ctx context.Context, tx *sql.Tx, userID string) ([]string, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT permissions FROM users WHERE id = ?`, userID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSuchUser
	}
	if err != nil {
		return nil, err
	}
	var permissions []string
	if err := json.Unmarshal([]byte(raw), &permissions); err != nil {
		return nil, err
	}
	return permissions, nil
}
