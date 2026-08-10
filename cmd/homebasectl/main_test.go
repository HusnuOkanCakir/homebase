package main

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/store"
)

// ADR-0015. This tool is used by somebody who is locked out and standing at the
// machine, so the thing worth testing is that what it prints actually gets them
// back in — not that it printed something.

const password = "a-sufficiently-long-password"

var codePattern = regexp.MustCompile(`[0-9A-HJ-KM-NP-TV-Z]{5}(-[0-9A-HJ-KM-NP-TV-Z]{5}){4}`)

func testDatabase(t *testing.T, accounts ...string) (path string, service *auth.Service) {
	t.Helper()

	path = t.TempDir() + "/homebase.db"
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	service = auth.NewService(db.DB())
	for i, name := range accounts {
		if i == 0 {
			if _, err := service.CreateAdministrator(context.Background(), name, password); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if _, err := service.CreateUser(context.Background(), name, password, nil); err != nil {
			t.Fatal(err)
		}
	}
	return path, service
}

func TestRecoveryCodeFromTheConsoleActuallyWorks(t *testing.T) {
	path, service := testDatabase(t, "okan")

	var out, errOut bytes.Buffer
	if err := run([]string{"recovery-code", "--database", path}, &out, &errOut); err != nil {
		t.Fatalf("recovery-code: %v\n%s", err, errOut.String())
	}

	code := codePattern.FindString(out.String())
	if code == "" {
		t.Fatalf("no recovery code in the output:\n%s", out.String())
	}

	// The whole point: this code, typed into the browser, opens the server.
	if _, _, err := service.ResetPasswordWithCode(
		context.Background(), "okan", code, "a-brand-new-password"); err != nil {
		t.Fatalf("the code printed at the console did not work: %v", err)
	}

	// And the instructions have to say where to use it, because somebody
	// holding a code and no idea what to do with it is still locked out.
	if !strings.Contains(out.String(), "forgotten my password") {
		t.Error("the output does not say where to use the code")
	}
	if !strings.Contains(out.String(), "shown once") {
		t.Error("the output does not warn that the code is not shown again")
	}
}

// Issuing a new code must invalidate the old one, including when the old one
// came from setup rather than from here.
func TestConsoleCodeReplacesThePreviousOne(t *testing.T) {
	path, service := testDatabase(t, "okan")

	first, err := service.IssueRecoveryCode(context.Background(), mustUser(t, service, "okan"))
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := run([]string{"recovery-code", "--database", path}, &out, &out); err != nil {
		t.Fatal(err)
	}

	if _, _, err := service.ResetPasswordWithCode(
		context.Background(), "okan", first, "a-brand-new-password"); err == nil {
		t.Error("the previous code still works after a new one was issued")
	}
}

// With more than one account it must refuse rather than guess: a wasted trip to
// the machine is the cost of picking wrong.
func TestSeveralAccountsRequireANameAndSayWhichExist(t *testing.T) {
	path, _ := testDatabase(t, "okan", "guest")

	var out bytes.Buffer
	err := run([]string{"recovery-code", "--database", path}, &out, &out)
	if err == nil {
		t.Fatal("with two accounts the tool picked one on its own")
	}
	if !strings.Contains(err.Error(), "okan") || !strings.Contains(err.Error(), "guest") {
		t.Errorf("the refusal does not say which accounts there are: %v", err)
	}
	if codePattern.FindString(out.String()) != "" {
		t.Error("a code was printed despite the refusal")
	}

	// Named, it works.
	out.Reset()
	if err := run([]string{"recovery-code", "--database", path, "--user", "guest"}, &out, &out); err != nil {
		t.Fatalf("naming the account: %v", err)
	}
	if codePattern.FindString(out.String()) == "" {
		t.Error("no code printed for the named account")
	}
}

func TestUnknownAccountIsExplained(t *testing.T) {
	path, _ := testDatabase(t, "okan")

	var out bytes.Buffer
	err := run([]string{"recovery-code", "--database", path, "--user", "nobody"}, &out, &out)
	if err == nil {
		t.Fatal("an account that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "list-accounts") {
		t.Errorf("the error does not say how to find the real names: %v", err)
	}
}

func TestListAccounts(t *testing.T) {
	path, _ := testDatabase(t, "okan", "guest")

	var out bytes.Buffer
	if err := run([]string{"list-accounts", "--database", path}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "okan") || !strings.Contains(out.String(), "guest") {
		t.Errorf("accounts missing from the listing:\n%s", out.String())
	}
}

// A missing database is the likeliest mistake — somebody running this on the
// wrong machine, or with Homebase installed somewhere else.
func TestAMissingDatabaseSaysWhatToDo(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"recovery-code", "--database", t.TempDir() + "/absent.db"}, &out, &out)
	if err == nil {
		t.Fatal("a missing database was not reported")
	}
	if !strings.Contains(err.Error(), "--database") {
		t.Errorf("the error does not say how to point at the right one: %v", err)
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"delete-everything"}, &out, &errOut); err == nil {
		t.Fatal("an unknown command was accepted")
	}
	if !strings.Contains(errOut.String(), "recovery-code") {
		t.Error("an unknown command does not show what the real ones are")
	}
}

func mustUser(t *testing.T, service *auth.Service, name string) string {
	t.Helper()
	user, err := service.UserByName(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return user.ID
}
