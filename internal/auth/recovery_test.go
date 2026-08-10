package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ADR-0015. These tests are about a person holding a piece of paper, so most of
// them are about the ways that goes wrong: the paper is transcribed badly, the
// code has already been used, the account is not the one they named.

func TestRecoveryCodeShape(t *testing.T) {
	code, err := GenerateRecoveryCode()
	if err != nil {
		t.Fatal(err)
	}

	if got := len(strings.ReplaceAll(code, "-", "")); got != recoveryLength {
		t.Errorf("code has %d characters, want %d (%q)", got, recoveryLength, code)
	}
	if groups := strings.Split(code, "-"); len(groups) != recoveryLength/recoveryGroup {
		t.Errorf("code has %d groups, want %d (%q)",
			len(groups), recoveryLength/recoveryGroup, code)
	}

	// The excluded glyphs are the whole reason this alphabet exists: somebody is
	// reading their own handwriting back, possibly years later.
	for _, bad := range []string{"I", "L", "O", "U"} {
		if strings.Contains(code, bad) {
			t.Errorf("code contains %q, which is ambiguous on paper: %q", bad, code)
		}
	}
}

func TestRecoveryCodesAreNotPredictable(t *testing.T) {
	seen := make(map[string]bool, 200)
	for range 200 {
		code, err := GenerateRecoveryCode()
		if err != nil {
			t.Fatal(err)
		}
		if seen[code] {
			t.Fatalf("generated the same code twice: %q", code)
		}
		seen[code] = true
	}
}

// The point of folding: a code typed back in the way a human types it.
func TestRecoveryCodeIsForgivingAboutTranscription(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	user, err := s.CreateAdministrator(ctx, "okan", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.IssueRecoveryCode(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Lower case, spaces instead of hyphens, and the classic O-for-zero and
	// l-for-one substitutions. All of these are the same code.
	mangled := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	mangled = strings.ReplaceAll(mangled, "0", "o")
	mangled = strings.ReplaceAll(mangled, "1", "l")

	if _, _, err := s.ResetPasswordWithCode(ctx, "okan", mangled, "a-brand-new-password"); err != nil {
		t.Fatalf("a code typed the way a person types it was refused: %v", err)
	}
}

func TestRecoveryResetsThePasswordAndReplacesTheCode(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	user, err := s.CreateAdministrator(ctx, "okan", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.IssueRecoveryCode(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	// A live session, which recovery must destroy: somebody reaching for this
	// may be doing so because they think they have lost control of the account.
	token, _, err := s.CreateSession(ctx, user.ID, "test")
	if err != nil {
		t.Fatal(err)
	}

	const newPassword = "an-entirely-different-password"
	recovered, replacement, err := s.ResetPasswordWithCode(ctx, "okan", code, newPassword)
	if err != nil {
		t.Fatalf("recovering: %v", err)
	}
	if recovered.ID != user.ID {
		t.Errorf("recovered the wrong account: %q", recovered.ID)
	}
	if replacement == "" || replacement == code {
		t.Error("recovery must hand back a new code, not the one just spent")
	}

	if _, err := s.Authenticate(ctx, "okan", newPassword); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
	if _, err := s.Authenticate(ctx, "okan", goodPassword); !errors.Is(err, ErrInvalidCredential) {
		t.Error("the old password still works after a reset")
	}
	if _, err := s.UserForSession(ctx, token); err == nil {
		t.Error("a session survived the reset")
	}

	// Single use. The paper the user is holding is now the replacement.
	if _, _, err := s.ResetPasswordWithCode(ctx, "okan", code, "yet-another-password"); !errors.Is(err, ErrInvalidRecoveryCode) {
		t.Error("a spent recovery code was accepted a second time")
	}
	if _, _, err := s.ResetPasswordWithCode(ctx, "okan", replacement, "yet-another-password"); err != nil {
		t.Errorf("the replacement code does not work: %v", err)
	}
}

func TestRecoveryRefusesWhatItShould(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	user, err := s.CreateAdministrator(ctx, "okan", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.IssueRecoveryCode(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		username string
		code     string
		password string
		want     error
	}{
		{"an empty code", "okan", "", goodPassword + "-new", ErrInvalidRecoveryCode},
		{"somebody else's code", "okan", "ABCDE-FGHJK-MNPQR-STVWX-YZ234", goodPassword + "-new", ErrInvalidRecoveryCode},
		{"an account that does not exist", "nobody", code, goodPassword + "-new", ErrInvalidRecoveryCode},
		{"a password that is too short", "okan", code, "short", ErrWeakPassword},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := s.ResetPasswordWithCode(ctx, tc.username, tc.code, tc.password)
			if !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}

	// None of that should have changed anything.
	if _, err := s.Authenticate(ctx, "okan", goodPassword); err != nil {
		t.Errorf("a refused recovery changed the password anyway: %v", err)
	}
}

// An account with no recovery code must fail exactly like a wrong code. The
// difference is worth hiding: it says which account has no second door.
func TestAnAccountWithoutACodeIsIndistinguishable(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	if _, err := s.CreateAdministrator(ctx, "okan", goodPassword); err != nil {
		t.Fatal(err)
	}

	_, _, err := s.ResetPasswordWithCode(ctx, "okan", "ABCDE-FGHJK-MNPQR-STVWX-YZ234", "a-new-password-here")
	if !errors.Is(err, ErrInvalidRecoveryCode) {
		t.Errorf("got %v, want %v", err, ErrInvalidRecoveryCode)
	}
}

func TestRecoveryStatusDoesNotRevealTheCode(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	user, err := s.CreateAdministrator(ctx, "okan", goodPassword)
	if err != nil {
		t.Fatal(err)
	}

	status, err := s.RecoveryStatusFor(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Exists {
		t.Error("an account that was never issued a code reports having one")
	}

	code, err := s.IssueRecoveryCode(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	status, err = s.RecoveryStatusFor(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Exists || status.IssuedAt == nil {
		t.Fatal("a code was issued but the status does not say so")
	}
	if status.LastUsedAt != nil {
		t.Error("a code that has never been used reports having been used")
	}

	// Whatever the status carries, it must not carry the code.
	if strings.Contains(status.IssuedAt.String(), code) {
		t.Error("the status leaks the code")
	}

	if _, _, err := s.ResetPasswordWithCode(ctx, "okan", code, "a-new-password-here"); err != nil {
		t.Fatal(err)
	}

	status, err = s.RecoveryStatusFor(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastUsedAt == nil {
		t.Error("a recovery happened and the status does not record it")
	}
}

// Reissuing must not erase the record that the account was once recovered.
func TestReissuingKeepsTheRecoveryHistory(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	user, err := s.CreateAdministrator(ctx, "okan", goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.IssueRecoveryCode(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResetPasswordWithCode(ctx, "okan", code, "a-new-password-here"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueRecoveryCode(ctx, user.ID); err != nil {
		t.Fatal(err)
	}

	status, err := s.RecoveryStatusFor(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastUsedAt == nil {
		t.Error("issuing a fresh code erased the fact that this account was recovered")
	}
}

func TestUsernamesForTheConsoleTool(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	if _, err := s.CreateAdministrator(ctx, "okan", goodPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "guest", goodPassword, []string{PermSystemRead}); err != nil {
		t.Fatal(err)
	}

	names, err := s.Usernames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "okan" || names[1] != "guest" {
		t.Errorf("got %v, want [okan guest] in creation order", names)
	}
}
