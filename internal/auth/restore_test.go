package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HusnuOkanCakir/homebase/internal/store"
)

// Recovery across a restore.
//
// This is the case that made recovery urgent rather than merely missing. Before
// Milestone 5 a forgotten password meant a lost server. Afterwards it was
// worse: a backup faithfully restores the password hash, so somebody who
// restores *because* they are locked out restores the account they cannot sign
// into. Backups protect against a lost machine, not a lost password, and it was
// believing those were the same problem that left the gap open.
//
// What makes the answer work is that the recovery code lives in the same
// database, so the paper written at setup opens the machine rebuilt from the
// disk. That is a claim about a file surviving a round trip, so it is tested
// against the round trip the backup actually performs — `VACUUM INTO`, never a
// file copy — rather than against an assumption that the two are equivalent.

func TestARecoveryCodeSurvivesABackupAndRestore(t *testing.T) {
	ctx := context.Background()

	origin, err := store.Open(ctx, t.TempDir()+"/homebase.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = origin.Close() }()

	service := NewService(origin.DB())
	user, err := service.CreateAdministrator(ctx, "alex", goodPassword)
	if err != nil {
		t.Fatal(err)
	}

	// The code the user writes down and puts in a drawer.
	onPaper, err := service.IssueRecoveryCode(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	// A live session and an open write-ahead log, which is the state a naive
	// copy gets wrong — stale rather than corrupt, and therefore restored
	// successfully while quietly missing the last week.
	if _, _, err := service.CreateSession(ctx, user.ID, "before the disaster"); err != nil {
		t.Fatal(err)
	}

	// Exactly what hostd does when it backs up: VACUUM INTO, not a file copy.
	exported := t.TempDir() + "/system/homebase.db"
	if err := exportTo(ctx, origin, exported); err != nil {
		t.Fatalf("exporting the database: %v", err)
	}

	// A different machine. Nothing in common with the first but this file.
	replacement, err := store.Open(ctx, exported)
	if err != nil {
		t.Fatalf("the restored database will not open: %v", err)
	}
	defer func() { _ = replacement.Close() }()

	restored := NewService(replacement.DB())

	// The account is back, which is the part that was already true — and the
	// part that was not enough on its own.
	if _, err := restored.Authenticate(ctx, "alex", goodPassword); err != nil {
		t.Fatalf("the account did not survive the restore: %v", err)
	}

	// And the piece of paper still opens the machine that was rebuilt from the
	// disk. Without this, restoring after being locked out restores the lock.
	const chosenAfterTheDisaster = "a-password-chosen-afterwards"
	recovered, replacementCode, err := restored.ResetPasswordWithCode(
		ctx, "alex", onPaper, chosenAfterTheDisaster)
	if err != nil {
		t.Fatalf("the recovery code written down before the backup does not work "+
			"on the restored machine: %v", err)
	}
	if recovered.Username != "alex" {
		t.Errorf("recovered as %q", recovered.Username)
	}
	if replacementCode == "" {
		t.Error("the restored machine did not hand back a fresh code")
	}

	if _, err := restored.Authenticate(ctx, "alex", chosenAfterTheDisaster); err != nil {
		t.Errorf("the password set on the restored machine does not work: %v", err)
	}

	// The original is untouched by any of it: a restore reads the backup, and
	// recovering on the new machine must not reach back to the old one.
	if _, err := service.Authenticate(ctx, "alex", goodPassword); err != nil {
		t.Errorf("restoring changed the machine the backup came from: %v", err)
	}
}

// Spending the code on the restored machine must not leave the copy on the
// backup disk usable: somebody who restores twice from the same disk would
// otherwise have a code that works for ever.
func TestASpentCodeIsSpentOnTheMachineThatSpentIt(t *testing.T) {
	ctx := context.Background()

	origin, err := store.Open(ctx, t.TempDir()+"/homebase.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = origin.Close() }()

	service := NewService(origin.DB())
	user, err := service.CreateAdministrator(ctx, "alex", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	onPaper, err := service.IssueRecoveryCode(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	exported := t.TempDir() + "/system/homebase.db"
	if err := exportTo(ctx, origin, exported); err != nil {
		t.Fatal(err)
	}

	replacement, err := store.Open(ctx, exported)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close() }()
	restored := NewService(replacement.DB())

	if _, _, err := restored.ResetPasswordWithCode(ctx, "alex", onPaper, "a-new-password-here"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := restored.ResetPasswordWithCode(ctx, "alex", onPaper, "another-password-x"); !errors.Is(err, ErrInvalidRecoveryCode) {
		t.Error("the code still works on the machine it was already spent on")
	}
}

// exportTo is the backup's database export: VACUUM INTO, which is the only way
// to get a consistent copy of a database that is being written to.
func exportTo(ctx context.Context, db *store.Store, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	_, err := db.DB().ExecContext(ctx, `VACUUM INTO ?`, target)
	return err
}
