package auth

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/HusnuOkanCakir/homebase/internal/store"
)

func householdService(t *testing.T) *Service {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return NewService(s.DB())
}

func withOwner(t *testing.T) (*Service, *User) {
	t.Helper()
	service := householdService(t)
	owner, err := service.CreateAdministrator(t.Context(), "okan", "a-sufficiently-long-password")
	if err != nil {
		t.Fatal(err)
	}
	return service, owner
}

// The whole point: an administrator hands over a code, never a password.
func TestAnInvitedAccountIsClaimedWithItsCodeAndNotAPassword(t *testing.T) {
	service, _ := withOwner(t)

	invited, code, err := service.CreateInvitedAccount(t.Context(), "father", RoleMember, "alex")
	if err != nil {
		t.Fatal(err)
	}
	if code == "" {
		t.Fatal("no joining code was issued")
	}

	// The row exists and cannot be signed into, because the password it was
	// created with was random and discarded.
	if _, err := service.Authenticate(t.Context(), "father", code); err == nil {
		t.Fatal("the joining code worked as a password; it must not")
	}

	// It is exchanged for a password only that person knows — through the
	// joining path, not the recovery one. See invitations.go for why those are
	// different mechanisms.
	claimed, _, err := service.ClaimAccount(
		t.Context(), "father", code, "a-password-of-their-own")
	if err != nil {
		t.Fatalf("claiming the account failed: %v", err)
	}
	if claimed.ID != invited.ID {
		t.Fatal("claiming produced a different account")
	}
	if _, err := service.Authenticate(t.Context(), "father", "a-password-of-their-own"); err != nil {
		t.Fatalf("could not sign in after claiming: %v", err)
	}
	// And the code is spent.
	if _, _, err := service.ResetPasswordWithCode(
		t.Context(), "father", code, "another-password-entirely"); err == nil {
		t.Fatal("the joining code worked twice")
	}
}

func TestRolesCarryTheirPermissionsAndNothingElse(t *testing.T) {
	service, _ := withOwner(t)

	for _, c := range []struct {
		role        string
		mustHave    []string
		mustNotHave []string
	}{
		{RoleAdministrator,
			[]string{PermSystemManage, PermAccountsManage, PermFilesWrite},
			[]string{PermAssistantUnrestricted}},
		{RoleMember,
			[]string{PermSystemRead, PermBackupRead, PermFilesRead, PermFilesWrite},
			[]string{PermSystemManage, PermAccountsManage, PermStorageModify}},
		{RoleLimited,
			[]string{PermFilesRead, PermFilesWrite},
			[]string{PermSystemRead, PermAppsRead, PermAccountsManage}},
	} {
		t.Run(c.role, func(t *testing.T) {
			user, _, err := service.CreateInvitedAccount(t.Context(), "person-"+c.role, c.role, "alex")
			if err != nil {
				t.Fatal(err)
			}
			for _, permission := range c.mustHave {
				if !user.Can(permission) {
					t.Errorf("%s lacks %s", c.role, permission)
				}
			}
			for _, permission := range c.mustNotHave {
				if user.Can(permission) {
					t.Errorf("%s has %s and should not", c.role, permission)
				}
			}
			if got := RoleOf(user); got != c.role {
				t.Errorf("RoleOf = %q, want %q", got, c.role)
			}
		})
	}
}

// A typo must not produce an account with permissions nobody chose.
func TestAnUnknownRoleIsRefusedRatherThanDefaulted(t *testing.T) {
	service, _ := withOwner(t)
	if _, _, err := service.CreateInvitedAccount(t.Context(), "someone", "superuser", "alex"); !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("err = %v, want ErrUnknownRole", err)
	}
}

// The name becomes hbshare-<name> at the file server, so it has to be a name
// both Linux and SMB accept.
func TestUsernamesThatWouldBreakFileSharingAreRefused(t *testing.T) {
	service, _ := withOwner(t)
	for _, name := range []string{"Father", "my father", "f", "-nope", "nope-",
		"a_b", "ünlü", ""} {
		if _, _, err := service.CreateInvitedAccount(t.Context(), name, RoleMember, "alex"); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	for _, name := range []string{"father", "my-father", "ab", "person2"} {
		if _, _, err := service.CreateInvitedAccount(t.Context(), name, RoleMember, "alex"); err != nil {
			t.Errorf("refused %q: %v", name, err)
		}
	}
}

// The two refusals that stop somebody locking themselves out of their own server.
func TestTheLastAdministratorCannotBeRemovedOrDemoted(t *testing.T) {
	service, owner := withOwner(t)

	if err := service.SetRole(t.Context(), owner.ID, RoleMember); !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("demoting the only administrator returned %v", err)
	}
	if err := service.DeleteAccount(t.Context(), owner.ID); !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("removing the only administrator returned %v", err)
	}

	// Both become possible once somebody else can administer the machine.
	second, _, err := service.CreateInvitedAccount(t.Context(), "deputy", RoleAdministrator, "alex")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SetRole(t.Context(), owner.ID, RoleMember); err != nil {
		t.Fatalf("demoting with another administrator present: %v", err)
	}
	if err := service.DeleteAccount(t.Context(), second.ID); !errors.Is(err, ErrLastAdministrator) {
		t.Fatal("removed the last administrator after the first was demoted")
	}
}

// Granted by hand at the machine, deliberately outside every role. A role change
// that revoked it would be a mystery with no error message attached.
func TestARoleChangeKeepsTheUnrestrictedAssistantPermission(t *testing.T) {
	service, _ := withOwner(t)
	person, _, err := service.CreateInvitedAccount(t.Context(), "researcher", RoleMember, "alex")
	if err != nil {
		t.Fatal(err)
	}

	permissions := append(PermissionsForRole(RoleMember), PermAssistantUnrestricted)
	if err := writeRawPermissions(t, service, person.ID, permissions); err != nil {
		t.Fatal(err)
	}

	if err := service.SetRole(t.Context(), person.ID, RoleLimited); err != nil {
		t.Fatal(err)
	}
	after, err := service.UserByID(t.Context(), person.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Can(PermAssistantUnrestricted) {
		t.Fatal("a role change silently revoked assistant.unrestricted")
	}
	if after.Can(PermSystemRead) {
		t.Fatal("the new role did not otherwise apply")
	}
}

func TestAccountsListsEverybodyWithTheirRole(t *testing.T) {
	service, _ := withOwner(t)
	if _, _, err := service.CreateInvitedAccount(t.Context(), "father", RoleLimited, "alex"); err != nil {
		t.Fatal(err)
	}

	accounts, err := service.Accounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("listed %d accounts, want 2", len(accounts))
	}
	byName := map[string]Account{}
	for _, account := range accounts {
		byName[account.Username] = account
	}
	if byName["okan"].Role != RoleAdministrator {
		t.Errorf("owner role = %q", byName["okan"].Role)
	}
	// An invitation nobody has accepted is distinguishable from an account in
	// use, which is the question an administrator has a week later.
	if byName["father"].HasSignedIn {
		t.Error("an account nobody has signed into is reported as signed in")
	}
	if !slices.Contains([]string{RoleLimited}, byName["father"].Role) {
		t.Errorf("father role = %q", byName["father"].Role)
	}
}

func writeRawPermissions(t *testing.T, service *Service, userID string, permissions []string) error {
	t.Helper()
	encoded, err := json.Marshal(permissions)
	if err != nil {
		return err
	}
	_, err = service.DB().ExecContext(t.Context(),
		`UPDATE users SET permissions = ? WHERE id = ?`, string(encoded), userID)
	return err
}

// The flag separates an invitation nobody has accepted from an account in use.
// It was answering neither: setting the server up produces a session without
// going through Authenticate, so the owner was told they had never signed in
// while they were signed in, looking at the screen that said it.
func TestClaimingAnAccountCountsAsSigningIn(t *testing.T) {
	service, owner := withOwner(t)

	// First-run setup issues a session directly.
	if _, _, err := service.CreateSession(t.Context(), owner.ID, "test"); err != nil {
		t.Fatal(err)
	}
	accounts, err := service.Accounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !accounts[0].HasSignedIn {
		t.Fatal("the account that set this server up is reported as never signed in")
	}

	// An invitation nobody has accepted is still reported as such.
	if _, _, err := service.CreateInvitedAccount(t.Context(), "father", RoleMember, "alex"); err != nil {
		t.Fatal(err)
	}
	accounts, err = service.Accounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accounts {
		if account.Username == "father" && account.HasSignedIn {
			t.Fatal("an unclaimed invitation is reported as signed in")
		}
	}
}
