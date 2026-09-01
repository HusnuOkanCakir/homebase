package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
)

func accountsOf(t *testing.T, h *harness, token string) []auth.Account {
	t.Helper()
	rec := h.do(http.MethodGet, "/api/v1/accounts", "", auth1(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("listing accounts returned %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Accounts []auth.Account `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Accounts
}

// The whole flow, as the household actually performs it.
func TestAddingSomebodyToTheHousehold(t *testing.T) {
	h := newHarness(t)
	token := h.signIn(t)

	rec := h.do(http.MethodPost, "/api/v1/accounts",
		`{"username":"father","role":"member"}`, auth1(token))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		ID          string `json:"id"`
		Username    string `json:"username"`
		JoiningCode string `json:"joining_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.JoiningCode == "" {
		t.Fatal("no joining code was returned; the account is unreachable")
	}

	// The new person exchanges the code for a password of their own, through
	// the recovery endpoint that already existed.
	rec = h.do(http.MethodPost, "/api/v1/auth/recover",
		`{"username":"father","recovery_code":"`+created.JoiningCode+
			`","new_password":"a-password-of-their-own"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("claiming the account returned %d: %s", rec.Code, rec.Body)
	}

	rec = h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"father","password":"a-password-of-their-own"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("signing in as the new person returned %d: %s", rec.Code, rec.Body)
	}
}

// An account with a reduced role must not be able to reach what it is not for.
func TestAMemberCannotAdministerTheServer(t *testing.T) {
	h := newHarness(t)
	token := h.signIn(t)

	rec := h.do(http.MethodPost, "/api/v1/accounts",
		`{"username":"father","role":"member"}`, auth1(token))
	var created struct {
		ID          string `json:"id"`
		JoiningCode string `json:"joining_code"`
	}
	json.Unmarshal(rec.Body.Bytes(), &created)
	h.do(http.MethodPost, "/api/v1/auth/recover",
		`{"username":"father","recovery_code":"`+created.JoiningCode+
			`","new_password":"a-password-of-their-own"}`, nil)

	login := h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"father","password":"a-password-of-their-own"}`, nil)
	var theirs string
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == SessionCookie {
			theirs = cookie.Value
		}
	}
	if theirs == "" {
		t.Fatal("no session for the new account")
	}

	for _, attempt := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/accounts", ""},
		{http.MethodPost, "/api/v1/accounts", `{"username":"x","role":"administrator"}`},
		{http.MethodPost, "/api/v1/system/reboot", `{}`},
	} {
		rec := h.do(attempt.method, attempt.path, attempt.body, auth1(theirs))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", attempt.method, attempt.path, rec.Code)
		}
	}

	// But the things a member is for still work.
	if rec := h.do(http.MethodGet, "/api/v1/auth/me", "", auth1(theirs)); rec.Code != http.StatusOK {
		t.Errorf("a member cannot read their own account: %d", rec.Code)
	}
}

// The refusal that stops somebody locking themselves out of their own server.
func TestTheLastAdministratorIsProtectedThroughTheAPI(t *testing.T) {
	h := newHarness(t)
	token := h.signIn(t)

	me := accountsOf(t, h, token)
	if len(me) != 1 {
		t.Fatalf("expected one account, got %d", len(me))
	}
	owner := me[0]

	for _, attempt := range []struct{ path, body string }{
		{"/api/v1/accounts/" + owner.ID + "/role", `{"role":"member"}`},
		{"/api/v1/accounts/" + owner.ID + "/remove", `{"confirm":"` + owner.Username + `"}`},
	} {
		rec := h.do(http.MethodPost, attempt.path, attempt.body, auth1(token))
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s = %d, want 409", attempt.path, rec.Code)
		}
		e := decodeError(t, rec)
		if e.Code != "accounts.last_administrator" {
			t.Fatalf("code = %q", e.Code)
		}
		// A refusal has to say what to do instead.
		if !e.Recoverable || !strings.Contains(e.Recovery, "administrator") {
			t.Fatalf("recovery = %q", e.Recovery)
		}
	}
}

// Removing an account is not deleting somebody's photographs.
func TestRemovingAnAccountNamesItAndKeepsTheirFiles(t *testing.T) {
	h := newHarness(t)
	token := h.signIn(t)

	h.do(http.MethodPost, "/api/v1/accounts",
		`{"username":"father","role":"limited"}`, auth1(token))
	var target auth.Account
	for _, account := range accountsOf(t, h, token) {
		if account.Username == "father" {
			target = account
		}
	}

	// Unconfirmed, or confirmed with the wrong name, is refused.
	rec := h.do(http.MethodPost, "/api/v1/accounts/"+target.ID+"/remove", `{}`, auth1(token))
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("an unconfirmed removal returned %d", rec.Code)
	}
	rec = h.do(http.MethodPost, "/api/v1/accounts/"+target.ID+"/remove",
		`{"confirm":"someone-else"}`, auth1(token))
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("a removal naming the wrong person returned %d", rec.Code)
	}

	rec = h.do(http.MethodPost, "/api/v1/accounts/"+target.ID+"/remove",
		`{"confirm":"father"}`, auth1(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "files were kept") {
		t.Fatalf("the reply does not say the files were kept:\n%s", rec.Body)
	}
	if len(accountsOf(t, h, token)) != 1 {
		t.Fatal("the account was not removed")
	}
}

func TestAccountNamesThatWouldBreakFileSharingAreExplained(t *testing.T) {
	h := newHarness(t)
	token := h.signIn(t)

	rec := h.do(http.MethodPost, "/api/v1/accounts",
		`{"username":"My Father","role":"member"}`, auth1(token))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	e := decodeError(t, rec)
	if e.Code != "accounts.invalid_name" {
		t.Fatalf("code = %q", e.Code)
	}
	// The rule is not guessable, so it is stated rather than implied.
	if !strings.Contains(e.Detail, "file-sharing") {
		t.Fatalf("detail does not explain why: %q", e.Detail)
	}
}

// The shell polls /system every five seconds to draw itself. An account without
// system.read used to get a 403 every five seconds and an error banner over the
// whole screen, with the one tab it was entitled to use underneath.
func TestTheShellRendersForAnAccountWithoutSystemRead(t *testing.T) {
	h := newHarness(t)
	token := h.signIn(t)

	rec := h.do(http.MethodPost, "/api/v1/accounts",
		`{"username":"brother","role":"limited"}`, auth1(token))
	var created struct {
		JoiningCode string `json:"joining_code"`
	}
	json.Unmarshal(rec.Body.Bytes(), &created)
	h.do(http.MethodPost, "/api/v1/auth/recover",
		`{"username":"brother","recovery_code":"`+created.JoiningCode+
			`","new_password":"another-long-password"}`, nil)
	login := h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"brother","password":"another-long-password"}`, nil)
	var theirs string
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == SessionCookie {
			theirs = cookie.Value
		}
	}

	rec = h.do(http.MethodGet, "/api/v1/system", "", auth1(theirs))
	if rec.Code == http.StatusForbidden {
		t.Fatal("a limited account cannot load the shell; the dashboard would " +
			"show an error banner over every screen it is entitled to use")
	}
	// hostd is unreachable in this harness, so 503 is the honest answer here —
	// what matters is that it is not a permission refusal.
	if rec.Code == http.StatusOK {
		var body map[string]any
		json.Unmarshal(rec.Body.Bytes(), &body)
		if _, leaked := body["memory"]; leaked {
			t.Fatal("an account without system.read was told the machine's memory")
		}
		if _, leaked := body["disks"]; leaked {
			t.Fatal("an account without system.read was told about the disks")
		}
		if body["hostname"] == nil {
			t.Fatal("the shell was not told the machine's name, which is all it needs")
		}
	}
}

// The Files screen is the only thing a Limited account is for, so the endpoint
// that tells them how to reach their files must not need a network permission
// they do not have.
func TestSomebodyWithOnlyFilesCanSeeHowToReachThem(t *testing.T) {
	h := newHarness(t)
	token := h.signIn(t)

	rec := h.do(http.MethodPost, "/api/v1/accounts",
		`{"username":"brother","role":"limited"}`, auth1(token))
	var created struct {
		JoiningCode string `json:"joining_code"`
	}
	json.Unmarshal(rec.Body.Bytes(), &created)
	h.do(http.MethodPost, "/api/v1/auth/recover",
		`{"username":"brother","recovery_code":"`+created.JoiningCode+
			`","new_password":"another-long-password"}`, nil)
	login := h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"brother","password":"another-long-password"}`, nil)
	var theirs string
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == SessionCookie {
			theirs = cookie.Value
		}
	}

	rec = h.do(http.MethodGet, "/api/v1/shares", "", auth1(theirs))
	if rec.Code == http.StatusForbidden {
		t.Fatal("a files-only account is refused the screen its role exists for")
	}

	// And it is still refused what it is not for.
	if rec := h.do(http.MethodGet, "/api/v1/network", "", auth1(theirs)); rec.Code != http.StatusForbidden {
		t.Fatalf("GET /network = %d, want 403", rec.Code)
	}
}
