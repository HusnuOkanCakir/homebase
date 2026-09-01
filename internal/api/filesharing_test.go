package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// The whole point of the arrangement: somebody joins with a code, chooses one
// password, and that password opens a folder from a Windows laptop. Two
// passwords for one person on one machine is what this replaces.
func TestJoiningSetsTheFileSharingPasswordToTheSameOne(t *testing.T) {
	h, fake := newAppHarness(t)
	fake.responses["share.status"] = map[string]any{
		"installed": true, "running": true, "users": []any{"alex"},
		"server_name": "homebase",
	}
	fake.responses["share.set_password"] = map[string]any{"username": "father"}
	fake.responses["share.make_personal_folder"] = map[string]any{"username": "father"}

	token := h.signIn(t)
	code := inviteAccount(t, h, token, "father")

	rec := h.do(http.MethodPost, "/api/v1/auth/recover",
		`{"username":"father","recovery_code":"`+code+
			`","new_password":"a-password-of-their-own"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("claiming the account returned %d: %s", rec.Code, rec.Body)
	}

	calls := fake.callsTo("share.set_password")
	if len(calls) != 1 {
		t.Fatalf("the file server was told about the password %d times", len(calls))
	}
	if calls[0].Body["username"] != "father" || calls[0].Body["password"] != "a-password-of-their-own" {
		t.Fatalf("the file server got %v, which is not the password that was chosen",
			calls[0].Body["username"])
	}
}

// A file server that is not installed must never be a reason somebody cannot
// sign in, or cannot claim the account they were invited to.
func TestJoiningWorksOnAServerWithNoFileSharing(t *testing.T) {
	h, fake := newAppHarness(t)
	fake.responses["share.status"] = map[string]any{
		"installed": false, "running": false, "server_name": "homebase",
	}
	fake.responses["share.make_personal_folder"] = map[string]any{"username": "father"}

	token := h.signIn(t)
	code := inviteAccount(t, h, token, "father")

	rec := h.do(http.MethodPost, "/api/v1/auth/recover",
		`{"username":"father","recovery_code":"`+code+
			`","new_password":"a-password-of-their-own"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("claiming the account returned %d: %s", rec.Code, rec.Body)
	}
	if calls := fake.callsTo("share.set_password"); len(calls) != 0 {
		t.Fatalf("the file server was configured on a machine that has none: %v", calls)
	}
}

// Sharing switched on after somebody had already joined. Their password exists
// only as a hash from then on, so their next sign-in is the one chance to give
// the file server the same one — and it must happen exactly once, not on every
// sign-in for ever.
func TestSigningInCatchesUpAFileSharingAccountOnlyWhenItIsMissing(t *testing.T) {
	h, fake := newAppHarness(t)
	fake.responses["share.status"] = map[string]any{
		"installed": true, "running": true, "users": []any{},
		"server_name": "homebase",
	}
	fake.responses["share.set_password"] = map[string]any{"username": "alex"}

	token := h.signIn(t)
	_ = token

	rec := h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"alex","password":"`+goodPassword+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("signing in returned %d: %s", rec.Code, rec.Body)
	}

	// In the background, so that setting it never stands between a person and
	// their dashboard.
	waitForCall(t, fake, "share.set_password", 1)

	// Now they have one. Signing in again must not run the account helper and
	// two smbpasswd commands all over again.
	fake.responses["share.status"] = map[string]any{
		"installed": true, "running": true, "users": []any{"alex"},
		"server_name": "homebase",
	}
	h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"alex","password":"`+goodPassword+`"}`, nil)

	time.Sleep(200 * time.Millisecond)
	if calls := fake.callsTo("share.set_password"); len(calls) != 1 {
		t.Fatalf("the password was set %d times; a sign-in is not a password change",
			len(calls))
	}
}

// The half that would have been missed. An account removed from Homebase whose
// SMB login still works is somebody who cannot open the dashboard and can still
// map the drive.
func TestRemovingAnAccountAlsoRemovesTheirFileSharingLogin(t *testing.T) {
	h, fake := newAppHarness(t)
	fake.responses["share.status"] = map[string]any{
		"installed": true, "running": true, "users": []any{"alex", "father"},
		"server_name": "homebase",
	}
	fake.responses["share.set_password"] = map[string]any{"username": "father"}
	fake.responses["share.make_personal_folder"] = map[string]any{"username": "father"}
	fake.responses["share.remove_user"] = map[string]any{"removed": true}
	fake.responses["share.retire_personal_folder"] = map[string]any{"retired": false}

	token := h.signIn(t)
	id := inviteAccountID(t, h, token, "brother")

	rec := h.do(http.MethodPost, "/api/v1/accounts/"+id+"/remove",
		`{"confirm":"brother"}`, auth1(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("removing the account returned %d: %s", rec.Code, rec.Body)
	}

	calls := fake.callsTo("share.remove_user")
	if len(calls) != 1 {
		t.Fatalf("the file-sharing login was removed %d times", len(calls))
	}
	if calls[0].Body["username"] != "brother" {
		t.Fatalf("the wrong file-sharing login was removed: %v", calls[0].Body)
	}
}

// --- helpers ---------------------------------------------------------------------

func inviteAccount(t *testing.T, h *harness, token, username string) string {
	t.Helper()

	rec := h.do(http.MethodPost, "/api/v1/accounts",
		`{"username":"`+username+`","role":"member"}`, auth1(token))
	if rec.Code != http.StatusCreated {
		t.Fatalf("inviting %s returned %d: %s", username, rec.Code, rec.Body)
	}
	var created struct {
		JoiningCode string `json:"joining_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return created.JoiningCode
}

func inviteAccountID(t *testing.T, h *harness, token, username string) string {
	t.Helper()

	rec := h.do(http.MethodPost, "/api/v1/accounts",
		`{"username":"`+username+`","role":"member"}`, auth1(token))
	if rec.Code != http.StatusCreated {
		t.Fatalf("inviting %s returned %d: %s", username, rec.Code, rec.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return created.ID
}

// waitForCall waits for work that deliberately does not block the response.
func waitForCall(t *testing.T, fake *fakeHostd, operation string, want int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(fake.callsTo(operation)) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s was not called %d times within five seconds", operation, want)
}
