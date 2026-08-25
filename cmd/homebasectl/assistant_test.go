package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/store"
)

// withAccount makes a database holding one account with these permissions.
func withAccount(t *testing.T, permissions []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "homebase.db")
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	encoded, err := json.Marshal(permissions)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.DB().ExecContext(context.Background(),
		`INSERT INTO users (id, username, password_hash, permissions, created_at)
		 VALUES ('u1', 'alex', 'x', ?, datetime('now'))`, string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func permissionsOf(t *testing.T, path string) []string {
	t.Helper()
	database, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	got, err := readPermissions(context.Background(), database.DB(), "alex")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func runAssistant(t *testing.T, path string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := assistantCommand(append(args, "--db", path), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return out.String()
}

func TestGrantingAndWithdrawingTheUnrestrictedPermission(t *testing.T) {
	path := withAccount(t, []string{auth.PermSystemRead, auth.PermAssistantUse})

	if got := runAssistant(t, path, "unrestricted", "alex"); !strings.Contains(got, "may not use") {
		t.Fatalf("reported %q before being granted", got)
	}

	out := runAssistant(t, path, "unrestricted", "alex", "on")
	if !strings.Contains(out, "may now use") {
		t.Fatalf("granting said %q", out)
	}
	// Said every time, because the model is contained and the person is not.
	if !strings.Contains(out, "Homebase cannot start it") {
		t.Fatal("granting did not say that Homebase cannot start it")
	}
	got := permissionsOf(t, path)
	if !contains(got, auth.PermAssistantUnrestricted) {
		t.Fatalf("permission not written: %v", got)
	}
	// The account's other permissions must survive being rewritten.
	if !contains(got, auth.PermSystemRead) || !contains(got, auth.PermAssistantUse) {
		t.Fatalf("granting dropped other permissions: %v", got)
	}

	runAssistant(t, path, "unrestricted", "alex", "off")
	if contains(permissionsOf(t, path), auth.PermAssistantUnrestricted) {
		t.Fatal("withdrawing left the permission in place")
	}
}

func TestGrantingTwiceDoesNotDuplicate(t *testing.T) {
	path := withAccount(t, []string{auth.PermAssistantUse})
	runAssistant(t, path, "unrestricted", "alex", "on")
	out := runAssistant(t, path, "unrestricted", "alex", "on")
	if !strings.Contains(out, "No change") {
		t.Fatalf("second grant said %q", out)
	}
	count := 0
	for _, p := range permissionsOf(t, path) {
		if p == auth.PermAssistantUnrestricted {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("permission appears %d times", count)
	}
}

func TestAnUnknownAccountIsNamed(t *testing.T) {
	path := withAccount(t, []string{auth.PermAssistantUse})
	var out bytes.Buffer
	err := assistantCommand([]string{"unrestricted", "nobody", "on", "--db", path}, &out)
	if err == nil {
		t.Fatal("granting to an account that does not exist succeeded")
	}
	if !strings.Contains(err.Error(), "nobody") {
		t.Fatalf("error does not name the account: %v", err)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
