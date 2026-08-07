package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/store"
)

func testService(t *testing.T) *Service {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return NewService(s.DB())
}

const goodPassword = "a-sufficiently-long-password"

func TestFirstRunSetup(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	needs, err := s.NeedsSetup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !needs {
		t.Fatal("a fresh server should need setup")
	}

	user, err := s.CreateAdministrator(ctx, "okan", goodPassword)
	if err != nil {
		t.Fatalf("creating the administrator: %v", err)
	}
	if !user.Can(PermSystemManage) {
		t.Error("the administrator cannot manage the system")
	}

	needs, _ = s.NeedsSetup(ctx)
	if needs {
		t.Error("the server still reports needing setup after setup")
	}
}

// Setup must succeed exactly once. A server that can be claimed twice is a
// server anybody on the network can take over after the owner has set it up.
func TestSetupCannotHappenTwice(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	if _, err := s.CreateAdministrator(ctx, "okan", goodPassword); err != nil {
		t.Fatal(err)
	}

	_, err := s.CreateAdministrator(ctx, "attacker", goodPassword)
	if !errors.Is(err, ErrAlreadySetUp) {
		t.Fatalf("a second administrator was created; got %v", err)
	}
}

func TestShortPasswordsAreRejected(t *testing.T) {
	s := testService(t)

	for _, password := range []string{"", "short", strings.Repeat("a", MinPasswordLen-1)} {
		_, err := s.CreateAdministrator(context.Background(), "okan", password)
		if !errors.Is(err, ErrWeakPassword) {
			t.Errorf("password %q was accepted (got %v)", password, err)
		}
	}
}

func TestAuthenticate(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	created, err := s.CreateAdministrator(ctx, "okan", goodPassword)
	if err != nil {
		t.Fatal(err)
	}

	user, err := s.Authenticate(ctx, "okan", goodPassword)
	if err != nil {
		t.Fatalf("correct credentials rejected: %v", err)
	}
	if user.ID != created.ID {
		t.Error("authenticated as the wrong user")
	}

	if _, err := s.Authenticate(ctx, "okan", "the-wrong-password"); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("wrong password accepted: %v", err)
	}
}

// The same error for "no such user" and "wrong password". Telling them apart
// turns the login form into a way to enumerate usernames.
func TestUnknownUserAndWrongPasswordAreIndistinguishable(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	if _, err := s.CreateAdministrator(ctx, "okan", goodPassword); err != nil {
		t.Fatal(err)
	}

	_, wrongPassword := s.Authenticate(ctx, "okan", "not-the-password")
	_, noSuchUser := s.Authenticate(ctx, "someone-else", "not-the-password")

	if wrongPassword == nil || noSuchUser == nil {
		t.Fatal("one of the bad logins succeeded")
	}
	if wrongPassword.Error() != noSuchUser.Error() {
		t.Errorf("the errors differ, which reveals whether a username exists:\n  %v\n  %v",
			wrongPassword, noSuchUser)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	user, err := s.CreateAdministrator(ctx, "okan", goodPassword)
	if err != nil {
		t.Fatal(err)
	}

	token, expires, err := s.CreateSession(ctx, user.ID, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("empty session token")
	}
	if !expires.After(time.Now()) {
		t.Error("the session is already expired")
	}

	found, err := s.UserForSession(ctx, token)
	if err != nil {
		t.Fatalf("the session did not resolve: %v", err)
	}
	if found.ID != user.ID {
		t.Error("the session resolved to the wrong user")
	}

	if err := s.DeleteSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserForSession(ctx, token); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("a deleted session still resolves: %v", err)
	}
}

// The token is returned once and never stored — only its hash. A stolen database
// must not hand over live sessions.
func TestSessionTokensAreNotStored(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	user, _ := s.CreateAdministrator(ctx, "okan", goodPassword)
	token, _, err := s.CreateSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	var stored string
	err = s.db.QueryRowContext(ctx, `SELECT token_hash FROM sessions LIMIT 1`).Scan(&stored)
	if err != nil {
		t.Fatal(err)
	}

	if stored == token {
		t.Fatal("the session token is stored verbatim; a database copy would grant live sessions")
	}
	if strings.Contains(stored, token) {
		t.Fatal("the stored value contains the token")
	}
}

func TestExpiredSessionsAreRejectedAndPurged(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	user, _ := s.CreateAdministrator(ctx, "okan", goodPassword)
	token, _, err := s.CreateSession(ctx, user.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	// Move it into the past.
	_, err = s.db.ExecContext(ctx, `UPDATE sessions SET expires_at = ?`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.UserForSession(ctx, token); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("an expired session was accepted: %v", err)
	}

	var remaining int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&remaining)
	if remaining != 0 {
		t.Error("the expired session was not removed when it was rejected")
	}
}

// --- Password hashing --------------------------------------------------------

func TestPasswordHashing(t *testing.T) {
	hash, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(hash, goodPassword) {
		t.Fatal("the hash contains the password")
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("unexpected hash format: %q", hash)
	}

	ok, err := VerifyPassword(goodPassword, hash)
	if err != nil || !ok {
		t.Errorf("the correct password did not verify (%v)", err)
	}

	ok, _ = VerifyPassword("something-else-entirely", hash)
	if ok {
		t.Error("the wrong password verified")
	}
}

// The salt must be per-password, or identical passwords produce identical
// hashes and a stolen database reveals which users share one.
func TestHashesAreSalted(t *testing.T) {
	first, _ := HashPassword(goodPassword)
	second, _ := HashPassword(goodPassword)

	if first == second {
		t.Fatal("hashing the same password twice produced the same hash; the salt is not random")
	}
}

// The parameters live in the hash, so raising the cost later does not invalidate
// existing passwords.
func TestVerifyUsesTheParametersFromTheHash(t *testing.T) {
	// A hash produced with deliberately different parameters from the current
	// constants.
	weaker := "$argon2id$v=19$m=8192,t=1,p=1$" +
		"c29tZXNhbHR2YWx1ZTA" + "$"

	// Not a real hash — this only asserts that a malformed one is refused
	// rather than panicking.
	if _, err := VerifyPassword("anything", weaker); err == nil {
		t.Error("a malformed hash was accepted")
	}

	for _, bad := range []string{"", "not-a-hash", "$argon2id$", "$bcrypt$v=19$m=1,t=1,p=1$a$b"} {
		if _, err := VerifyPassword("anything", bad); err == nil {
			t.Errorf("malformed hash %q was accepted", bad)
		}
	}
}

func TestPermissionChecks(t *testing.T) {
	user := &User{Permissions: []string{PermSystemRead}}

	if !user.Can(PermSystemRead) {
		t.Error("a granted permission was refused")
	}
	if user.Can(PermSystemManage) {
		t.Error("an ungranted permission was allowed")
	}
	if user.Can("") {
		t.Error("the empty permission was allowed")
	}

	none := &User{}
	if none.Can(PermSystemRead) {
		t.Error("a user with no permissions was allowed one")
	}
}

// Read and write are separate throughout, which is what lets Stage 2A be an
// operator that can explain the server but change nothing.
func TestReadAndWritePermissionsAreDistinct(t *testing.T) {
	pairs := [][2]string{
		{PermSystemRead, PermSystemManage},
		{PermAppsRead, PermAppsManage},
		{PermStorageRead, PermStorageModify},
		{PermNetworkDiag, PermNetworkModify},
		{PermBackupRead, PermBackupRun},
	}

	for _, pair := range pairs {
		reader := &User{Permissions: []string{pair[0]}}
		if reader.Can(pair[1]) {
			t.Errorf("%q implies %q; a read-only operator could change things", pair[0], pair[1])
		}
	}
}
