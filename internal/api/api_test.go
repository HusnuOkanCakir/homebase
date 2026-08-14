package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
	"github.com/HusnuOkanCakir/homebase/internal/hostclient"
	"github.com/HusnuOkanCakir/homebase/internal/jobs"
	"github.com/HusnuOkanCakir/homebase/internal/store"
)

const goodPassword = "a-sufficiently-long-password"

type harness struct {
	server  *Server
	handler http.Handler
	auth    *auth.Service
	events  *events.Recorder
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	s, err := store.Open(context.Background(), t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	authService := auth.NewService(s.DB())

	// A socket that does not exist: hostd is unreachable. That is the honest
	// default for these tests, and it means the "hostd is down" paths get
	// exercised rather than assumed.
	host := hostclient.New(t.TempDir() + "/absent.sock")

	recorder := events.NewRecorder(s.DB(), log)

	server := NewServer(authService, jobs.NewManager(s.DB(), log), host, recorder, log, "test")
	return &harness{
		server: server, handler: server.Handler(),
		auth: authService, events: recorder,
	}
}

func (h *harness) do(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// signIn completes setup and returns a session token.
func (h *harness) signIn(t *testing.T) string {
	t.Helper()

	rec := h.do(http.MethodPost, "/api/v1/setup",
		`{"username":"okan","password":"`+goodPassword+`"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup returned %d: %s", rec.Code, rec.Body)
	}

	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == SessionCookie {
			return cookie.Value
		}
	}
	t.Fatal("setup did not issue a session cookie")
	return ""
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) apiError {
	t.Helper()
	var envelope struct {
		Error apiError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("the error body is not valid JSON: %v\n%s", err, rec.Body)
	}
	return envelope.Error
}

// --- Setup and authentication ------------------------------------------------

func TestSetupFlow(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodGet, "/api/v1/setup", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var status struct {
		NeedsSetup bool `json:"needs_setup"`
	}
	json.Unmarshal(rec.Body.Bytes(), &status)
	if !status.NeedsSetup {
		t.Error("a fresh server does not report needing setup")
	}

	h.signIn(t)

	rec = h.do(http.MethodGet, "/api/v1/setup", "", nil)
	json.Unmarshal(rec.Body.Bytes(), &status)
	if status.NeedsSetup {
		t.Error("the server still reports needing setup afterwards")
	}
}

// Setup is unauthenticated by necessity — nobody can sign in before it — so it
// must refuse to run a second time. Otherwise anyone on the network can claim a
// server that is already set up.
func TestSetupCannotBeRunTwice(t *testing.T) {
	h := newHarness(t)
	h.signIn(t)

	rec := h.do(http.MethodPost, "/api/v1/setup",
		`{"username":"attacker","password":"`+goodPassword+`"}`, nil)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if code := decodeError(t, rec).Code; code != "setup.already_complete" {
		t.Errorf("error code = %q", code)
	}
}

func TestShortPasswordIsRejectedWithAdvice(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodPost, "/api/v1/setup", `{"username":"okan","password":"short"}`, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}

	e := decodeError(t, rec)
	if !e.Recoverable || e.Recovery == "" {
		t.Error("the error is recoverable but does not say how to recover")
	}
}

func TestLogin(t *testing.T) {
	h := newHarness(t)
	h.signIn(t)

	rec := h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"okan","password":"`+goodPassword+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}

	rec = h.do(http.MethodPost, "/api/v1/auth/login",
		`{"username":"okan","password":"wrong"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong password returned %d", rec.Code)
	}
}

// The session cookie must not be readable by JavaScript, must not travel over
// plain HTTP, and must not be sent cross-site.
func TestSessionCookieIsProtected(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodPost, "/api/v1/setup",
		`{"username":"okan","password":"`+goodPassword+`"}`, nil)

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	if !cookie.HttpOnly {
		t.Error("the session cookie is readable by JavaScript")
	}
	if cookie.SameSite != http.SameSiteLaxMode && cookie.SameSite != http.SameSiteStrictMode {
		t.Error("the session cookie is sent cross-site")
	}

	// Secure is deliberately *not* asserted here.
	//
	// This test used to require it unconditionally, which sounded like the
	// stricter choice and was the reason Homebase did not work: browsers
	// discard a Secure cookie that arrives over plain HTTP, so on a real
	// installation the session vanished and /auth/me answered 401 straight
	// after a correct password. The flag now follows the connection, and the
	// property worth pinning is that pairing — see
	// TestTheSessionCookieMatchesTheConnectionItWasIssuedOn, and
	// TestTheSessionCookieIsSecureOverTLS for the other half.
	if cookie.Secure {
		t.Error("this request carried no TLS, so a Secure cookie would be discarded")
	}
}

// The other half: over TLS the flag must be on, or the cookie would travel in
// the clear the moment somebody reached the server over plain HTTP as well.
func TestTheSessionCookieIsSecureOverTLS(t *testing.T) {
	h := newHarness(t)

	server := httptest.NewTLSServer(h.handler)
	defer server.Close()

	client := server.Client()
	response, err := client.Post(server.URL+"/api/v1/setup", "application/json",
		strings.NewReader(`{"username":"okan","password":"`+goodPassword+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusCreated {
		t.Fatalf("setup returned %d", response.StatusCode)
	}

	for _, cookie := range response.Cookies() {
		if cookie.Name != SessionCookie {
			continue
		}
		if !cookie.Secure {
			t.Error("the session cookie is not Secure over TLS, so it would also " +
				"travel over plain HTTP")
		}
		return
	}
	t.Fatal("no session cookie")
}

// --- Authorisation -----------------------------------------------------------

func TestProtectedEndpointsRefuseAnonymousRequests(t *testing.T) {
	h := newHarness(t)
	h.signIn(t)

	protected := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/system"},
		{http.MethodPost, "/api/v1/system/reboot"},
		{http.MethodGet, "/api/v1/jobs"},
		{http.MethodGet, "/api/v1/jobs/job_ANYTHING"},
		{http.MethodGet, "/api/v1/auth/me"},
	}

	for _, ep := range protected {
		rec := h.do(ep.method, ep.path, "{}", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s returned %d without a session, want 401", ep.method, ep.path, rec.Code)
		}
	}
}

func TestInvalidSessionIsRefused(t *testing.T) {
	h := newHarness(t)
	h.signIn(t)

	for _, token := range []string{"not-a-token", "", strings.Repeat("a", 64)} {
		rec := h.do(http.MethodGet, "/api/v1/auth/me", "",
			map[string]string{"Cookie": SessionCookie + "=" + token})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q returned %d, want 401", token, rec.Code)
		}
	}
}

// A user without the permission must be refused even with a valid session.
// Authentication and authorisation are different questions, and conflating them
// is how a read-only account ends up able to reboot the machine.
func TestPermissionIsCheckedSeparatelyFromAuthentication(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	admin, err := h.auth.CreateAdministrator(ctx, "okan", goodPassword)
	if err != nil {
		t.Fatal(err)
	}

	// Strip everything but system.read, so this is a genuinely signed-in user
	// who simply may not manage anything.
	if _, err := h.auth.DB().ExecContext(ctx,
		`UPDATE users SET permissions = ? WHERE id = ?`,
		`["`+auth.PermSystemRead+`"]`, admin.ID); err != nil {
		t.Fatal(err)
	}

	token, _, err := h.auth.CreateSession(ctx, admin.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	cookie := map[string]string{"Cookie": SessionCookie + "=" + token}

	// Authentication succeeds.
	if rec := h.do(http.MethodGet, "/api/v1/auth/me", "", cookie); rec.Code != http.StatusOK {
		t.Fatalf("the session is not valid (%d)", rec.Code)
	}

	// Authorisation does not: 403, not 401. The distinction matters to the
	// dashboard, which should say "you cannot do that" rather than sign the
	// user out.
	rec := h.do(http.MethodPost, "/api/v1/system/reboot", `{"confirm":"anything"}`, cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a read-only user got %d from reboot, want 403", rec.Code)
	}
	if code := decodeError(t, rec).Code; code != "auth.permission_denied" {
		t.Errorf("error code = %q", code)
	}
}

func TestLogoutClearsTheSession(t *testing.T) {
	h := newHarness(t)
	token := h.signIn(t)
	cookie := map[string]string{"Cookie": SessionCookie + "=" + token}

	if rec := h.do(http.MethodGet, "/api/v1/auth/me", "", cookie); rec.Code != http.StatusOK {
		t.Fatalf("me returned %d before logout", rec.Code)
	}

	if rec := h.do(http.MethodPost, "/api/v1/auth/logout", "", cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("logout returned %d", rec.Code)
	}

	if rec := h.do(http.MethodGet, "/api/v1/auth/me", "", cookie); rec.Code != http.StatusUnauthorized {
		t.Errorf("the session still works after logout (%d)", rec.Code)
	}
}

// --- hostd unavailable -------------------------------------------------------

// When hostd is unreachable the server is not healthy, and saying "ok" would
// show a green dashboard on a machine where every operation fails.
func TestHealthReportsDegradedWhenHostdIsUnreachable(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodGet, "/api/v1/health", "", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 with hostd down", rec.Code)
	}

	var body struct {
		Status         string `json:"status"`
		HostdReachable bool   `json:"hostd_reachable"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Status != "degraded" || body.HostdReachable {
		t.Errorf("body = %+v", body)
	}
}

// An unreachable hostd must not read as an internal error: the difference
// between "Homebase is starting up" and "Homebase is broken" is the difference
// between waiting and reinstalling.
func TestUnreachableHostdIsReportedHonestly(t *testing.T) {
	h := newHarness(t)
	token := h.signIn(t)

	rec := h.do(http.MethodGet, "/api/v1/system", "",
		map[string]string{"Cookie": SessionCookie + "=" + token})

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	e := decodeError(t, rec)
	if e.Code != "hostd.unavailable" {
		t.Errorf("code = %q, want hostd.unavailable", e.Code)
	}
	if !e.Recoverable || e.Recovery == "" {
		t.Error("the error should tell the user this usually clears by itself")
	}
}

// --- Request handling --------------------------------------------------------

func TestUnknownFieldsAreRejected(t *testing.T) {
	h := newHarness(t)

	rec := h.do(http.MethodPost, "/api/v1/setup",
		`{"username":"okan","password":"`+goodPassword+`","admin":true}`, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — an unexpected field was accepted", rec.Code)
	}
}

func TestEveryResponseCarriesARequestID(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/api/v1/health", "/api/v1/setup", "/api/v1/system"} {
		rec := h.do(http.MethodGet, path, "", nil)
		if rec.Header().Get("X-Request-Id") == "" {
			t.Errorf("%s returned no X-Request-Id; nothing correlates it with the logs", path)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodGet, "/api/v1/health", "", nil)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("Content-Security-Policy = %q", csp)
	}
}

// Internal failures must not leak implementation detail. The user gets a
// request id to quote; the detail goes to the log.
func TestInternalErrorsDoNotLeakDetail(t *testing.T) {
	h := newHarness(t)
	token := h.signIn(t)

	rec := h.do(http.MethodGet, "/api/v1/jobs/job_DOES_NOT_EXIST", "",
		map[string]string{"Cookie": SessionCookie + "=" + token})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	body := rec.Body.String()
	for _, leak := range []string{"goroutine", ".go:", "sql:", "/home/"} {
		if strings.Contains(body, leak) {
			t.Errorf("the response contains implementation detail (%q): %s", leak, body)
		}
	}
}

// --- Renaming the server ------------------------------------------------------

// The name is how the machine is found, and it is what a restart demands as its
// confirmation — so a rename changes the answer to a question the user will be
// asked later. What is checked here is that the endpoint is guarded the same way
// everything else that changes the machine is.
func TestRenamingRequiresPermissionAndASession(t *testing.T) {
	h := newHarness(t)
	token := h.signIn(t)

	rec := h.do(http.MethodPost, "/api/v1/system/name", `{"name":"living-room"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("renaming without a session returned %d, want 401", rec.Code)
	}

	// With a session it reaches hostd — which is absent in these tests, so the
	// interesting assertion is that it got that far rather than being refused
	// earlier.
	rec = h.do(http.MethodPost, "/api/v1/system/name", `{"name":"living-room"}`,
		map[string]string{"Cookie": SessionCookie + "=" + token})
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Errorf("an administrator was refused: %d %s", rec.Code, rec.Body)
	}
	if rec.Code == http.StatusNotFound {
		t.Error("the endpoint is not registered")
	}
}

// Read permission must not be enough to rename the machine, the same way it is
// not enough to erase a disk. Read and write are separate throughout, which is
// what makes a Stage 2 operator that can explain but not change expressible.
func TestRenamingNeedsMoreThanReadPermission(t *testing.T) {
	h := newHarness(t)
	h.signIn(t)

	reader, err := h.auth.CreateUser(t.Context(), "reader", goodPassword,
		[]string{auth.PermSystemRead})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := h.auth.CreateSession(t.Context(), reader.ID, "test")
	if err != nil {
		t.Fatal(err)
	}

	rec := h.do(http.MethodPost, "/api/v1/system/name", `{"name":"living-room"}`,
		map[string]string{"Authorization": "Bearer " + token})
	if rec.Code != http.StatusForbidden {
		t.Errorf("a read-only account renamed the server: %d %s", rec.Code, rec.Body)
	}
}
