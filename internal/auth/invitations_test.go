package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func invitedAccount(t *testing.T, service *Service) (*User, string) {
	t.Helper()

	user, code, err := service.CreateInvitedAccount(context.Background(),
		"father", RoleMember, "alex")
	if err != nil {
		t.Fatal(err)
	}
	return user, code
}

// The ordinary case, and the one that was making the server look attacked.
func TestSomebodyJoinsWithTheirCodeAndChoosesAPassword(t *testing.T) {
	service := testService(t)
	invited, code := invitedAccount(t, service)

	user, recovery, err := service.ClaimAccount(context.Background(),
		"father", code, "a-password-of-their-own")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != invited.ID {
		t.Fatalf("claimed %s rather than %s", user.ID, invited.ID)
	}

	// A recovery code of their own, from this moment. Somebody who joins and is
	// never given one loses the account the first time they forget the
	// password.
	if recovery == "" {
		t.Fatal("they were given no way back in")
	}
	if _, err := service.Authenticate(context.Background(),
		"father", "a-password-of-their-own"); err != nil {
		t.Fatalf("the password they chose does not sign them in: %v", err)
	}
}

// A joining code is read out across a kitchen table or sent in a message. One
// still live in six months is a way into the server sitting in a chat history.
func TestAJoiningCodeStopsWorking(t *testing.T) {
	service := testService(t)
	_, code := invitedAccount(t, service)

	// Wound back rather than waited for.
	if _, err := service.db.ExecContext(context.Background(),
		`UPDATE invitations SET expires_at = ?`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	_, _, err := service.ClaimAccount(context.Background(),
		"father", code, "a-password-of-their-own")
	if !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("an expired code was accepted: %v", err)
	}
}

// Used once. Otherwise the code that got somebody in is a code that gets
// anybody else in afterwards, and it has been sent by message.
func TestAJoiningCodeCannotBeUsedTwice(t *testing.T) {
	service := testService(t)
	_, code := invitedAccount(t, service)

	if _, _, err := service.ClaimAccount(context.Background(),
		"father", code, "a-password-of-their-own"); err != nil {
		t.Fatal(err)
	}
	_, _, err := service.ClaimAccount(context.Background(),
		"father", code, "somebody-elses-password")
	if !errors.Is(err, ErrInvalidInvitation) {
		t.Fatalf("the code worked a second time: %v", err)
	}
	if _, err := service.Authenticate(context.Background(),
		"father", "a-password-of-their-own"); err != nil {
		t.Fatal("the second attempt changed the password anyway")
	}
}

// Their recovery code is theirs. An administrator who could issue one for
// somebody else's account would have a way to take it over, and the person's
// own paper code would silently stop working.
func TestReissuingAJoiningCodeDoesNotTouchTheirRecoveryCode(t *testing.T) {
	service := testService(t)
	_, code := invitedAccount(t, service)

	user, recovery, err := service.ClaimAccount(context.Background(),
		"father", code, "a-password-of-their-own")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.IssueInvitation(context.Background(), user.ID, "alex"); err != nil {
		t.Fatal(err)
	}

	// The paper code still works.
	if _, _, err := service.ResetPasswordWithCode(context.Background(),
		"father", recovery, "a-password-they-chose-later"); err != nil {
		t.Fatalf("their own recovery code stopped working: %v", err)
	}
}

// One error for a wrong code, an unknown name, a used code and an expired one.
// Telling them apart says which accounts exist and which are worth attacking.
func TestJoiningTellsAnAttackerNothing(t *testing.T) {
	service := testService(t)
	_, code := invitedAccount(t, service)

	for _, attempt := range []struct{ name, username, code string }{
		{"a wrong code", "father", "AAAAA-BBBBB-CCCCC-DDDDD-EEEEE"},
		{"a name nobody has", "nobody", code},
		{"a name with no invitation", "alex", code},
	} {
		_, _, err := service.ClaimAccount(context.Background(),
			attempt.username, attempt.code, "a-password-of-their-own")
		if !errors.Is(err, ErrInvalidInvitation) {
			t.Errorf("%s produced %v, which is a different answer", attempt.name, err)
		}
	}
}

// The list an administrator reads has to distinguish "invited on Tuesday" from
// "invited on Tuesday, and the code died on Sunday" — the second is where
// somebody is waiting for an answer that will never come.
func TestTheAccountListShowsAnOutstandingInvitation(t *testing.T) {
	service := testService(t)
	_, code := invitedAccount(t, service)

	accounts, err := service.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var father *Account
	for i := range accounts {
		if accounts[i].Username == "father" {
			father = &accounts[i]
		}
	}
	if father == nil {
		t.Fatal("the invited account is not listed")
	}
	if father.InvitationExpiresAt == nil {
		t.Fatal("nothing says their code expires, so nobody can see it has")
	}
	if father.InvitationExpired {
		t.Fatal("a code issued a moment ago is reported as expired")
	}

	// Accepted is history, not something to act on.
	if _, _, err := service.ClaimAccount(context.Background(),
		"father", code, "a-password-of-their-own"); err != nil {
		t.Fatal(err)
	}
	accounts, err = service.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accounts {
		if account.Username == "father" && account.InvitationExpiresAt != nil {
			t.Fatal("an accepted invitation is still shown as outstanding")
		}
	}
}
