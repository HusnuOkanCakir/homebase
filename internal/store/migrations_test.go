package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// The assistant permission has to reach administrators who already exist.
//
// Permissions are a JSON array frozen into the row when an account is created,
// so adding a constant to auth.AdministratorPermissions only affects accounts
// made afterwards. On a machine that has been running for months there is
// exactly one account — the person who installed it — and without the migration
// they would be the one user unable to see the feature, with nothing anywhere
// saying why. That failure is silent, which is why it is tested.
func TestAssistantPermissionReachesExistingAdministrators(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	insert := func(id string, permissions []string) {
		t.Helper()
		encoded, err := json.Marshal(permissions)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.DB().ExecContext(ctx,
			`INSERT INTO users (id, username, password_hash, permissions, created_at)
			 VALUES (?, ?, 'x', ?, datetime('now'))`,
			id, id, string(encoded))
		if err != nil {
			t.Fatal(err)
		}
	}

	// An administrator from before the assistant existed.
	insert("legacy-admin", []string{"system.read", "system.manage", "apps.read"})
	// Someone deliberately given less. They must not be promoted by a migration.
	insert("viewer", []string{"system.read", "apps.read"})
	// Already has it: the migration must not add a duplicate.
	insert("current-admin", []string{"system.manage", "assistant.use"})

	// Re-run the migration the way a database upgraded in place would.
	body, err := migrationFiles.ReadFile("migrations/0003_assistant_permission.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("the migration failed: %v", err)
	}

	permissions := func(id string) []string {
		t.Helper()
		var raw string
		if err := s.DB().QueryRowContext(ctx,
			`SELECT permissions FROM users WHERE id = ?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var out []string
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("permissions for %s are not a JSON array: %v", id, err)
		}
		return out
	}

	count := func(list []string, want string) int {
		n := 0
		for _, item := range list {
			if item == want {
				n++
			}
		}
		return n
	}

	if got := count(permissions("legacy-admin"), "assistant.use"); got != 1 {
		t.Fatalf("the existing administrator has %d assistant.use, want 1 — "+
			"they would be the only person who could not see the assistant", got)
	}
	if got := count(permissions("viewer"), "assistant.use"); got != 0 {
		t.Fatalf("a non-administrator was granted assistant.use by a migration")
	}
	if got := count(permissions("current-admin"), "assistant.use"); got != 1 {
		t.Fatalf("assistant.use appears %d times after re-running, want 1", got)
	}

	// Migrations get re-run on databases restored from backup; doing it twice
	// must not keep appending.
	if _, err := s.DB().ExecContext(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
	if got := count(permissions("legacy-admin"), "assistant.use"); got != 1 {
		t.Fatalf("re-running the migration duplicated the permission (%d copies)", got)
	}
}

// The same silent failure as 0003, twice over: an administrator who cannot add
// anybody to their own server, and a Files screen that never appears, with
// nothing on screen to say why.
func TestHouseholdPermissionsReachExistingAdministrators(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	insert := func(id string, permissions []string) {
		t.Helper()
		encoded, err := json.Marshal(permissions)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().ExecContext(ctx,
			`INSERT INTO users (id, username, password_hash, permissions, created_at)
			 VALUES (?, ?, 'x', ?, datetime('now'))`, id, id, string(encoded)); err != nil {
			t.Fatal(err)
		}
	}

	insert("legacy-admin", []string{"system.read", "system.manage", "apps.read"})
	insert("viewer", []string{"system.read", "apps.read"})

	body, err := migrationFiles.ReadFile("migrations/0004_household_accounts.sql")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // twice: migrations re-run on a database restored from backup
		if _, err := s.DB().ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("the migration failed: %v", err)
		}
	}

	permissions := func(id string) []string {
		t.Helper()
		var raw string
		if err := s.DB().QueryRowContext(ctx,
			`SELECT permissions FROM users WHERE id = ?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var out []string
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	count := func(list []string, want string) int {
		n := 0
		for _, item := range list {
			if item == want {
				n++
			}
		}
		return n
	}

	for _, permission := range []string{"files.read", "files.write", "accounts.manage"} {
		if got := count(permissions("legacy-admin"), permission); got != 1 {
			t.Errorf("administrator has %d %q, want exactly 1", got, permission)
		}
		if got := count(permissions("viewer"), permission); got != 0 {
			t.Errorf("a non-administrator was granted %q by a migration", permission)
		}
	}
}
