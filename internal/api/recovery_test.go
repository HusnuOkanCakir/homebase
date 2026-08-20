package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HusnuOkanCakir/homebase/internal/events"
)

// ADR-0015, through the API. The interesting cases are the ones where recovery
// is being abused rather than used, because this is the only unauthenticated
// way to change a credential in Homebase.

// setupWithCode completes first-run setup and returns the code it handed back
// along with the session it issued.
func setupWithCode(t *testing.T, h *harness) (code, token string) {
	t.Helper()

	rec := h.do(http.MethodPost, "/api/v1/setup",
		`{"username":"alex","password":"`+goodPassword+`"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup returned %d: %s", rec.Code, rec.Body)
	}

	var body struct {
		RecoveryCode string `json:"recovery_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("setup response is not valid JSON: %v", err)
	}
	if body.RecoveryCode == "" {
		t.Fatal("setup did not hand back a recovery code, so this server cannot be recovered")
	}

	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == SessionCookie {
			token = cookie.Value
		}
	}
	if token == "" {
		t.Fatal("setup did not issue a session cookie")
	}
	return body.RecoveryCode, token
}

func TestSetupIssuesARecoveryCode(t *testing.T) {
	h := newHarness(t)
	code, _ := setupWithCode(t, h)

	if n := len(strings.ReplaceAll(code, "-", "")); n != 25 {
		t.Errorf("code has %d characters: %q", n, code)
	}

	// And it is shown once. Signing in again must not repeat it — anything the
	// server can show twice, it can show to somebody else.
	rec := h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"alex","password":"`+goodPassword+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login returned %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), code) {
		t.Error("signing in repeated the recovery code")
	}
}

func TestRecoveringWithTheCode(t *testing.T) {
	h := newHarness(t)
	code, _ := setupWithCode(t, h)

	const newPassword = "a-completely-new-password"
	rec := h.do(http.MethodPost, "/api/v1/auth/recover",
		`{"username":"alex","recovery_code":"`+code+`","new_password":"`+newPassword+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("recovery returned %d: %s", rec.Code, rec.Body)
	}

	var body struct {
		RecoveryCode string `json:"recovery_code"`
		User         struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.User.Username != "alex" {
		t.Errorf("recovered as %q", body.User.Username)
	}
	if body.RecoveryCode == "" || body.RecoveryCode == code {
		t.Error("recovery must leave the user holding a fresh code")
	}

	// Signed in immediately: somebody who has just proved they own the server
	// should not then be asked to sign in with a password they set ten seconds
	// ago on a page that has already gone.
	var signedIn bool
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == SessionCookie && cookie.Value != "" {
			signedIn = true
		}
	}
	if !signedIn {
		t.Error("recovery did not sign the user in")
	}

	if rec := h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"alex","password":"`+newPassword+`"}`, nil); rec.Code != http.StatusOK {
		t.Errorf("the new password does not work: %d", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"alex","password":"`+goodPassword+`"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Error("the old password still works")
	}
}

// The one that matters: a reset nobody asked for has to be visible.
func TestRecoveryAnnouncesItself(t *testing.T) {
	h := newHarness(t)
	code, _ := setupWithCode(t, h)

	rec := h.do(http.MethodPost, "/api/v1/auth/recover",
		`{"username":"alex","recovery_code":"`+code+`","new_password":"a-completely-new-password"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("recovery returned %d: %s", rec.Code, rec.Body)
	}

	list, err := h.events.List(t.Context(), events.Query{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}

	var found *events.Event
	for i := range list {
		if list[i].Type == "auth.password_recovered" {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatal("a password was reset and nothing was recorded")
	}
	if found.Severity != events.SeverityError {
		t.Errorf("severity is %q; a reset the owner did not perform is the most "+
			"important thing this server can report", found.Severity)
	}
	if found.Message == nil || !strings.Contains(*found.Message, "signed out") {
		t.Error("the event does not say that existing sessions were ended")
	}
}

func TestRecoveryRefusalsLookTheSame(t *testing.T) {
	h := newHarness(t)
	code, _ := setupWithCode(t, h)

	// A wrong code, an account that does not exist, and a code belonging to
	// nobody must be answered identically. Anything else is a way to enumerate
	// which accounts are worth attacking.
	cases := []struct {
		name string
		body string
	}{
		{"a wrong code", `{"username":"alex","recovery_code":"ABCDE-FGHJK-MNPQR-STVWX-YZ234","new_password":"a-new-password-x"}`},
		{"an unknown account", `{"username":"nobody","recovery_code":"` + code + `","new_password":"a-new-password-x"}`},
		{"an empty code", `{"username":"alex","recovery_code":"","new_password":"a-new-password-x"}`},
	}

	var seen []string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(http.MethodPost, "/api/v1/auth/recover", tc.body, nil)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body)
			}
			failure := decodeError(t, rec)
			if failure.Code != "auth.invalid_recovery_code" {
				t.Errorf("code = %q", failure.Code)
			}
			seen = append(seen, failure.Code+"|"+failure.Message)
		})
	}

	for _, got := range seen[1:] {
		if got != seen[0] {
			t.Errorf("refusals differ:\n  %s\n  %s", seen[0], got)
		}
	}

	// And nothing was changed by any of it.
	if rec := h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"alex","password":"`+goodPassword+`"}`, nil); rec.Code != http.StatusOK {
		t.Error("a refused recovery changed the password anyway")
	}
}

// The recovery endpoint verifies an argon2id hash, so an unlimited one is a way
// to exhaust the memory of a machine that may have four gigabytes.
func TestRecoveryIsRateLimited(t *testing.T) {
	h := newHarness(t)
	_, _ = setupWithCode(t, h)

	body := `{"username":"alex","recovery_code":"ABCDE-FGHJK-MNPQR-STVWX-YZ234","new_password":"a-new-password-x"}`

	var limited *httptest.ResponseRecorder
	for range authBurst + 5 {
		rec := h.do(http.MethodPost, "/api/v1/auth/recover", body, nil)
		if rec.Code == http.StatusTooManyRequests {
			limited = rec
			break
		}
	}

	if limited == nil {
		t.Fatal("guessing recovery codes was never rate limited")
	}
	if limited.Header().Get("Retry-After") == "" {
		t.Error("a refusal with no Retry-After leaves a legitimate user guessing")
	}
	if failure := decodeError(t, limited); failure.Code != "auth.too_many_attempts" {
		t.Errorf("code = %q", failure.Code)
	}
}

// Signing in correctly must not count against the limit. A household where
// several people sign in over an evening is not an attack, and rationing it
// would make the limiter something the user experiences rather than the
// attacker.
func TestSuccessfulSignInIsNotRationed(t *testing.T) {
	h := newHarness(t)
	_, _ = setupWithCode(t, h)

	for i := range authBurst * 4 {
		rec := h.do(http.MethodPost, "/api/v1/auth/login",
			`{"username":"alex","password":"`+goodPassword+`"}`, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("correct sign-in %d was refused with %d: %s", i+1, rec.Code, rec.Body)
		}
	}

	// And the allowance is still there for somebody who then mistypes.
	rec := h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"alex","password":"wrong"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong password after many correct ones returned %d", rec.Code)
	}
}

func TestSignInIsRateLimited(t *testing.T) {
	h := newHarness(t)
	_, _ = setupWithCode(t, h)

	var limited bool
	for range authBurst + 5 {
		rec := h.do(http.MethodPost, "/api/v1/auth/login",
			`{"username":"alex","password":"not-the-right-password"}`, nil)
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("guessing passwords was never rate limited")
	}
}

func TestRecoveryStatusAndReissue(t *testing.T) {
	h := newHarness(t)
	code, token := setupWithCode(t, h)

	cookie := map[string]string{"Cookie": SessionCookie + "=" + token}

	status := h.do(http.MethodGet, "/api/v1/auth/recovery-code", "", cookie)
	if status.Code != http.StatusOK {
		t.Fatalf("status returned %d: %s", status.Code, status.Body)
	}
	if strings.Contains(status.Body.String(), strings.Split(code, "-")[0]) {
		t.Error("the status endpoint leaks the code itself")
	}

	var state struct {
		Exists bool `json:"exists"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.Exists {
		t.Error("setup issued a code but the status says there is none")
	}

	// A fresh one, for the user who has lost the paper.
	reissued := h.do(http.MethodPost, "/api/v1/auth/recovery-code", "", cookie)
	if reissued.Code != http.StatusOK {
		t.Fatalf("reissue returned %d: %s", reissued.Code, reissued.Body)
	}
	var issued struct {
		RecoveryCode string `json:"recovery_code"`
	}
	if err := json.Unmarshal(reissued.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.RecoveryCode == "" || issued.RecoveryCode == code {
		t.Fatal("reissue did not produce a different code")
	}

	// The old one must stop working the moment a new one is issued.
	old := h.do(http.MethodPost, "/api/v1/auth/recover",
		`{"username":"alex","recovery_code":"`+code+`","new_password":"a-new-password-x"}`, nil)
	if old.Code != http.StatusUnauthorized {
		t.Errorf("the replaced code still works: %d", old.Code)
	}
}

// Neither status nor reissue may be reachable without signing in: the second
// would hand an attacker a working key to the server.
func TestRecoveryCodeEndpointsNeedASession(t *testing.T) {
	h := newHarness(t)
	_, _ = setupWithCode(t, h)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rec := h.do(method, "/api/v1/auth/recovery-code", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a session returned %d, want 401", method, rec.Code)
		}
	}
}

// The bug that made Homebase unusable on a real network.
//
// The session cookie was hard-coded Secure. Browsers refuse a Secure cookie
// from a non-secure origin — but exempt localhost, which is the one origin
// every test in this repository reached the server on. So on a real
// installation at http://192.168.1.50:8080 the browser silently discarded the
// session and /auth/me answered 401 straight after a correct password, and
// nothing here noticed for four milestones.
func TestTheSessionCookieMatchesTheConnectionItWasIssuedOn(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodPost, "/api/v1/setup",
		`{"username":"alex","password":"`+goodPassword+`"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup returned %d", rec.Code)
	}

	// httptest requests carry no TLS, which is the plain-HTTP case.
	var found bool
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name != SessionCookie {
			continue
		}
		found = true
		if cookie.Secure {
			t.Error("a Secure cookie was issued over plain HTTP; a browser would " +
				"discard it and the user could never sign in")
		}
		if !cookie.HttpOnly {
			t.Error("the session cookie is readable from JavaScript")
		}
	}
	if !found {
		t.Fatal("no session cookie at all")
	}

	// And signing out has to clear the cookie it actually set: a browser will
	// not replace a non-Secure cookie with a Secure one.
	token := ""
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == SessionCookie {
			token = cookie.Value
		}
	}
	out := h.do(http.MethodPost, "/api/v1/auth/logout", "",
		map[string]string{"Cookie": SessionCookie + "=" + token})
	if out.Code != http.StatusNoContent {
		t.Fatalf("logout returned %d", out.Code)
	}
	for _, cookie := range out.Result().Cookies() {
		if cookie.Name == SessionCookie && cookie.Secure {
			t.Error("signing out sent a Secure clearing cookie over plain HTTP, " +
				"which the browser ignores — leaving the session in place")
		}
	}
}
