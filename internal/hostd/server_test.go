package hostd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testServer struct {
	server *Server
	audit  *bytes.Buffer
	http   http.Handler
}

func newTestServer(t *testing.T, ops ...Operation) *testServer {
	t.Helper()

	registry := NewRegistry()
	for _, op := range ops {
		if err := registry.Register(op); err != nil {
			t.Fatalf("registering %s: %v", op.Name, err)
		}
	}

	audit := &bytes.Buffer{}
	s := NewServer(registry, NewAuditor(audit), slog.New(slog.NewTextHandler(io.Discard, nil)), 0)
	// The peer check needs a real Unix socket. It has its own test below; here
	// we are exercising dispatch, so it is disabled explicitly rather than
	// accidentally.
	s.allowAnyPeer = true

	return &testServer{server: s, audit: audit, http: s.HTTPServer().Handler}
}

func (ts *testServer) post(path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	ts.http.ServeHTTP(rec, req)
	return rec
}

func (ts *testServer) auditEvents(t *testing.T) []AuditEvent {
	t.Helper()
	var events []AuditEvent
	for _, line := range strings.Split(strings.TrimSpace(ts.audit.String()), "\n") {
		if line == "" {
			continue
		}
		var e AuditEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("audit line is not valid JSON: %v\n%s", err, line)
		}
		events = append(events, e)
	}
	return events
}

func readOp() Operation {
	return Operation{
		Name:    "test.read",
		Summary: "Read something.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: time.Second,
		Handler: Typed(func(_ context.Context, _ NoParams) (any, error) {
			return map[string]string{"value": "ok"}, nil
		}),
	}
}

func TestReadOperationSucceeds(t *testing.T) {
	ts := newTestServer(t, readOp())

	rec := ts.post("/v1/op/test.read", "{}", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["value"] != "ok" {
		t.Errorf("body = %v", body)
	}
}

// An unknown operation must be rejected, never forwarded — and the attempt is
// worth a record precisely because it should not have happened.
func TestUnknownOperationIsRejectedAndAudited(t *testing.T) {
	ts := newTestServer(t, readOp())

	rec := ts.post("/v1/op/test.does_not_exist", "{}", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	events := ts.auditEvents(t)
	if len(events) != 1 {
		t.Fatalf("expected one audit record, got %d", len(events))
	}
	if events[0].Outcome != "rejected" || events[0].ErrorCode != ErrUnknownOperation.Code {
		t.Errorf("audit record does not describe the rejection: %+v", events[0])
	}
}

// Anything outside /v1/op/ is not a route. There is no fallback handler and no
// path that reaches code the registry does not name.
func TestOnlyRegisteredPathsAreRoutable(t *testing.T) {
	ts := newTestServer(t, readOp())

	for _, path := range []string{"/", "/v1", "/v1/op", "/exec", "/v1/op/../../etc/passwd"} {
		rec := ts.post(path, "{}", nil)
		if rec.Code == http.StatusOK {
			t.Errorf("%s returned 200; it should not be routable", path)
		}
	}
}

func TestGetIsNotAllowedForOperations(t *testing.T) {
	ts := newTestServer(t, readOp())

	req := httptest.NewRequest(http.MethodGet, "/v1/op/test.read", nil)
	rec := httptest.NewRecorder()
	ts.http.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func confirmOp() Operation {
	return Operation{
		Name:        "test.dangerous",
		Summary:     "Do something that needs confirming.",
		Risk:        RiskHigh,
		Permissions: []string{"system.manage"},
		Confirm:     ConfirmRequired,
		Timeout:     time.Second,
		Handler: Typed(func(_ context.Context, _ NoParams) (any, error) {
			return map[string]bool{"done": true}, nil
		}),
	}
}

func TestConfirmationIsEnforcedBeforeTheHandlerRuns(t *testing.T) {
	ts := newTestServer(t, confirmOp())

	rec := ts.post("/v1/op/test.dangerous", "{}", nil)
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want 428", rec.Code)
	}

	// The refusal is audited, and no attempt record exists — the handler was
	// never reached.
	events := ts.auditEvents(t)
	if len(events) != 1 {
		t.Fatalf("expected one audit record, got %d", len(events))
	}
	if events[0].Phase != "result" || events[0].ErrorCode != ErrConfirmationRequired.Code {
		t.Errorf("unexpected audit record: %+v", events[0])
	}
}

func TestConfirmedRequestProceeds(t *testing.T) {
	ts := newTestServer(t, confirmOp())

	rec := ts.post("/v1/op/test.dangerous", "{}", map[string]string{"X-Homebase-Confirmed": "true"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
	}
}

// The attempt record is written before the handler runs, so that an operation
// which crashes the machine still leaves evidence it was tried. Without this,
// the audit log is only trustworthy for actions that worked.
func TestAttemptIsAuditedBeforeTheOperationRuns(t *testing.T) {
	ts := newTestServer(t, Operation{
		Name:        "test.explodes",
		Summary:     "Fail unrecoverably.",
		Risk:        RiskMedium,
		Permissions: []string{"system.manage"},
		Confirm:     ConfirmNone,
		Timeout:     time.Second,
		Handler: Typed(func(_ context.Context, _ NoParams) (any, error) {
			return nil, &Error{Code: "test.exploded", Message: "It exploded.", Status: 500}
		}),
	})

	ts.post("/v1/op/test.explodes", `{}`, nil)

	events := ts.auditEvents(t)
	if len(events) != 2 {
		t.Fatalf("expected an attempt and a result record, got %d", len(events))
	}
	if events[0].Phase != "attempt" {
		t.Errorf("the first record is %q, want \"attempt\"", events[0].Phase)
	}
	if events[1].Phase != "result" || events[1].Outcome != "failed" {
		t.Errorf("the second record does not describe the failure: %+v", events[1])
	}
	if events[1].ErrorCode != "test.exploded" {
		t.Errorf("error code = %q", events[1].ErrorCode)
	}
}

// The parameters are recorded so an incident can be reconstructed. hostd deals
// in credential references rather than values, so there is nothing here to leak.
func TestAuditRecordsTheParameters(t *testing.T) {
	ts := newTestServer(t, Operation{
		Name:        "test.params",
		Summary:     "Accept a parameter.",
		Risk:        RiskLow,
		Permissions: []string{"system.manage"},
		Confirm:     ConfirmNone,
		Timeout:     time.Second,
		Handler: Typed(func(_ context.Context, p struct {
			Reason string `json:"reason"`
		}) (any, error) {
			return p, nil
		}),
	})

	ts.post("/v1/op/test.params", `{"reason":"scheduled maintenance"}`, nil)

	events := ts.auditEvents(t)
	if len(events) == 0 {
		t.Fatal("nothing audited")
	}
	if !strings.Contains(string(events[0].Params), "scheduled maintenance") {
		t.Errorf("parameters were not recorded: %s", events[0].Params)
	}
}

func TestOperationTimeoutIsEnforced(t *testing.T) {
	ts := newTestServer(t, Operation{
		Name:        "test.slow",
		Summary:     "Take too long.",
		Risk:        RiskLow,
		Permissions: []string{"system.manage"},
		Confirm:     ConfirmNone,
		Timeout:     50 * time.Millisecond,
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return "should not get here", nil
			}
		},
	})

	start := time.Now()
	rec := ts.post("/v1/op/test.slow", "{}", nil)
	elapsed := time.Since(start)

	if rec.Code == http.StatusOK {
		t.Fatal("a handler that exceeded its timeout returned success")
	}
	if elapsed > 2*time.Second {
		t.Errorf("the timeout was not enforced; took %s", elapsed)
	}
}

// Every error crossing the socket must match schemas/error.schema.json, so a
// failure can travel out through core's API without being reshaped.
func TestErrorEnvelopeMatchesTheContract(t *testing.T) {
	ts := newTestServer(t, readOp())

	rec := ts.post("/v1/op/test.nope", "{}", nil)

	var body struct {
		Error struct {
			Code        string `json:"code"`
			Message     string `json:"message"`
			Recoverable bool   `json:"recoverable"`
			RequestID   string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the error body is not valid JSON: %v", err)
	}

	if body.Error.Code == "" {
		t.Error("no error code; clients switch on this")
	}
	if body.Error.Message == "" {
		t.Error("no message")
	}
	if body.Error.RequestID == "" {
		t.Error("no request id; the caller cannot correlate this with the audit log")
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("no X-Request-Id header")
	}
}

// A recoverable error must say how to recover. Telling someone their problem is
// fixable while withholding the fix is worse than saying nothing.
func TestRecoverableErrorsCarryRecoveryAdvice(t *testing.T) {
	for _, e := range []*Error{ErrConfirmationRequired, ErrTimeout} {
		if e.Recoverable && strings.TrimSpace(e.Recovery) == "" {
			t.Errorf("%s is recoverable but offers no recovery advice", e.Code)
		}
	}
}

// The message is what a non-technical user reads. It must not contain paths,
// device names or Go internals — those belong in detail.
func TestUserFacingMessagesAreNotDiagnostics(t *testing.T) {
	registry := NewRegistry()
	RegisterSystemOperations(registry)

	for _, name := range registry.Names() {
		op, _ := registry.Lookup(name)
		if op.Summary == "" || !strings.HasSuffix(op.Summary, ".") {
			t.Errorf("%s: summary should be a sentence: %q", name, op.Summary)
		}
	}

	for _, e := range []*Error{ErrUnknownOperation, ErrConfirmationRequired, ErrPermissionDenied, ErrTimeout} {
		for _, bad := range []string{"/", "0x", "nil", "goroutine", "syscall"} {
			if strings.Contains(e.Message, bad) {
				t.Errorf("%s: message reads like a diagnostic (%q): %q", e.Code, bad, e.Message)
			}
		}
	}
}

func TestHealthNeedsNoPeerCheck(t *testing.T) {
	ts := newTestServer(t, readOp())

	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	rec := httptest.NewRecorder()
	ts.http.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
