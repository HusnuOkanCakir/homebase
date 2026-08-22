package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
	"github.com/HusnuOkanCakir/homebase/internal/hostclient"
	"github.com/HusnuOkanCakir/homebase/internal/jobs"
	"github.com/HusnuOkanCakir/homebase/internal/store"
)

// --- A stand-in for hostd ----------------------------------------------------
//
// The default harness points core at a socket that does not exist, which is the
// right default for testing "hostd is down". These tests need the opposite: a
// hostd that answers, so that the code between the HTTP request and the
// privileged call can be exercised.
//
// It records every call it receives. That is the point — the assertions worth
// making here are about what core sends, and specifically about what it does not
// send.

type fakeHostd struct {
	socket string
	server *http.Server

	mu    sync.Mutex
	calls []hostdCall

	// responses maps an operation name to what to return. A missing entry is a
	// test asserting an operation is never reached.
	responses map[string]any

	// failures maps an operation name to an error envelope to return instead.
	failures map[string]hostclient.Error
}

type hostdCall struct {
	Operation string
	Body      map[string]any
	Confirmed bool
}

func newFakeHostd(t *testing.T) *fakeHostd {
	t.Helper()

	// Short path: a Unix socket address is limited to about 100 bytes, and
	// t.TempDir() with a long test name has overflowed that before.
	dir, err := os.MkdirTemp("", "hb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	fake := &fakeHostd{
		socket:    filepath.Join(dir, "hostd.sock"),
		responses: map[string]any{},
		failures:  map[string]hostclient.Error{},
	}

	listener, err := net.Listen("unix", fake.socket)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /v1/op/{name}", fake.handleOperation)

	fake.server = &http.Server{Handler: mux}
	go fake.server.Serve(listener)
	t.Cleanup(func() { fake.server.Close() })

	return fake
}

func (f *fakeHostd) handleOperation(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	body := map[string]any{}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	_ = json.Unmarshal(raw, &body)

	f.mu.Lock()
	f.calls = append(f.calls, hostdCall{
		Operation: name,
		Body:      body,
		Confirmed: r.Header.Get("X-Homebase-Confirmed") == "true",
	})
	failure, failing := f.failures[name]
	response, known := f.responses[name]
	f.mu.Unlock()

	if failing {
		status := failure.Status
		if status == 0 {
			status = http.StatusInternalServerError
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"error": failure})
		return
	}

	if !known {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"error": hostclient.Error{
			Code:    "op.unknown",
			Message: "This server does not have that operation.",
		}})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (f *fakeHostd) callsTo(operation string) []hostdCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	var matched []hostdCall
	for _, call := range f.calls {
		if call.Operation == operation {
			matched = append(matched, call)
		}
	}
	return matched
}

// --- A harness wired to it ---------------------------------------------------

func newAppHarness(t *testing.T) (*harness, *fakeHostd) {
	t.Helper()

	fake := newFakeHostd(t)
	fake.responses["app.status"] = map[string]any{
		"id": "hello-homebase", "name": "Hello Homebase",
		"state": "not_installed", "installed": false,
		"image": "traefik/whoami", "version": "v1.10.4",
		"data_path": "/srv/homebase/apps/hello-homebase",
	}

	s, err := store.Open(t.Context(), t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	authService := auth.NewService(s.DB())
	recorder := events.NewRecorder(s.DB(), log)

	server := NewServer(authService, jobs.NewManager(s.DB(), log),
		hostclient.New(fake.socket), recorder, log, "test")

	h := &harness{
		server: server, handler: server.Handler(),
		auth: authService, events: recorder,
	}
	return h, fake
}

// signedIn creates the administrator and returns a bearer token.
func (h *harness) signedIn(t *testing.T) map[string]string {
	t.Helper()

	rec := h.do("POST", "/api/v1/setup",
		`{"username":"alex","password":"`+goodPassword+`"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup returned %d: %s", rec.Code, rec.Body.String())
	}

	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == SessionCookie {
			return map[string]string{"Authorization": "Bearer " + cookie.Value}
		}
	}
	t.Fatal("no session cookie after setup")
	return nil
}

// awaitJob waits for a job to reach a terminal state.
func (h *harness) awaitJob(t *testing.T, id string, headers map[string]string) jobs.Job {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec := h.do("GET", "/api/v1/jobs/"+id, "", headers)
		var job jobs.Job
		if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
			t.Fatalf("decoding job: %v (%s)", err, rec.Body.String())
		}
		if job.State.Terminal() {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return jobs.Job{}
}

func submittedJobID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var job jobs.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	return job.ID
}

// --- The property that matters -----------------------------------------------

// core must never be able to describe a container. This is ADR-0012 expressed as
// a test: whatever the client sends, what reaches hostd is an application id.
//
// It is deliberately written against the recorded call rather than against the
// handler's source, so that a future refactor that starts forwarding fields
// fails here rather than passing review.
func TestCoreSendsOnlyAnApplicationIDToHostd(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.responses["app.install"] = map[string]any{"id": "hello-homebase", "installed": true}

	// A client attempting to smuggle a container specification through.
	hostile := `{"image":"attacker/evil","version":"latest","binds":["/:/host"],` +
		`"privileged":true,"environment":{"X":"y"},"command":["/bin/sh"]}`

	rec := h.do("POST", "/api/v1/apps/hello-homebase/install", hostile, headers)

	// Rejected outright rather than ignored. Accepting the request and installing
	// something other than what it asked for is the worst of the three answers,
	// because the client believes it chose.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	// And nothing describing a container reached hostd.
	for _, call := range fake.calls {
		for _, forbidden := range []string{
			"image", "binds", "privileged", "environment", "command",
			"version", "mounts", "ports", "capabilities",
		} {
			if _, present := call.Body[forbidden]; present {
				t.Errorf("%s carried %q to hostd: %v", call.Operation, forbidden, call.Body)
			}
		}
	}
}

func TestInstallSendsTheIDAndSucceeds(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.responses["app.install"] = map[string]any{"id": "hello-homebase", "installed": true}

	rec := h.do("POST", "/api/v1/apps/hello-homebase/install", "", headers)
	job := h.awaitJob(t, submittedJobID(t, rec), headers)

	if job.State != jobs.StateSucceeded {
		t.Fatalf("job state = %s, error = %+v", job.State, job.Error)
	}

	calls := fake.callsTo("app.install")
	if len(calls) != 1 {
		t.Fatalf("app.install called %d times", len(calls))
	}
	if calls[0].Body["id"] != "hello-homebase" {
		t.Errorf("body = %v", calls[0].Body)
	}
	if len(calls[0].Body) != 1 {
		t.Errorf("app.install carried more than an id: %v", calls[0].Body)
	}
}

// --- Confirmation -------------------------------------------------------------

// The destructive operations require the request to name what it is acting on.
// A `{"confirm": true}` that a client can send by default is not a confirmation.
func TestDestructiveOperationsRequireNamingTheApplication(t *testing.T) {
	for _, path := range []string{"stop", "restart", "uninstall", "data/remove"} {
		t.Run(path, func(t *testing.T) {
			h, fake := newAppHarness(t)
			headers := h.signedIn(t)

			endpoint := "/api/v1/apps/hello-homebase/" + path

			for _, body := range []string{
				``,                              // nothing at all
				`{}`,                            // an empty object
				`{"confirm":""}`,                // an empty confirmation
				`{"confirm":"something-else"}`,  // the wrong application
				`{"confirm":"HELLO-HOMEBASE"}`,  // nearly right
				`{"confirm":"hello-homebase "}`, // trailing space
			} {
				rec := h.do("POST", endpoint, body, headers)
				if rec.Code == http.StatusAccepted {
					t.Errorf("%q was accepted as confirmation", body)
				}
			}

			// Not one of those reached hostd.
			for _, call := range fake.calls {
				if strings.HasPrefix(call.Operation, "app.") && call.Operation != "app.status" {
					t.Errorf("%s reached hostd without a confirmation", call.Operation)
				}
			}
		})
	}
}

func TestConfirmedStopReachesHostdAsConfirmed(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.responses["app.stop"] = map[string]any{"id": "hello-homebase", "running": false}

	rec := h.do("POST", "/api/v1/apps/hello-homebase/stop",
		`{"confirm":"hello-homebase"}`, headers)
	job := h.awaitJob(t, submittedJobID(t, rec), headers)

	if job.State != jobs.StateSucceeded {
		t.Fatalf("job state = %s, error = %+v", job.State, job.Error)
	}

	calls := fake.callsTo("app.stop")
	if len(calls) != 1 {
		t.Fatalf("app.stop called %d times", len(calls))
	}
	// hostd requires the assertion; core is where the user was asked.
	if !calls[0].Confirmed {
		t.Error("app.stop reached hostd without the confirmed header")
	}
}

// Removing data is the one application operation with no undo, so the
// confirmation is checked in core and again in hostd. This asserts core does not
// simply forward whatever it was given.
func TestRemoveDataForwardsTheConfirmationItValidated(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.responses["app.remove_data"] = map[string]any{"id": "hello-homebase", "removed": true}

	rec := h.do("POST", "/api/v1/apps/hello-homebase/data/remove",
		`{"confirm":"hello-homebase"}`, headers)
	h.awaitJob(t, submittedJobID(t, rec), headers)

	calls := fake.callsTo("app.remove_data")
	if len(calls) != 1 {
		t.Fatalf("app.remove_data called %d times", len(calls))
	}
	if calls[0].Body["confirm"] != "hello-homebase" {
		t.Errorf("confirmation not forwarded: %v", calls[0].Body)
	}
	if !calls[0].Confirmed {
		t.Error("app.remove_data reached hostd without the confirmed header")
	}
}

// --- Permissions --------------------------------------------------------------

// apps.read must not be enough to change anything. Read and write are separate
// permissions throughout, and this is the endpoint set where confusing them
// would let a viewer uninstall somebody's media server.
func TestReadPermissionCannotChangeAnything(t *testing.T) {
	h, fake := newAppHarness(t)

	// Set up the administrator first, then a reader.
	admin := h.signedIn(t)
	_ = admin

	reader, err := h.auth.CreateUser(t.Context(), "reader", goodPassword,
		[]string{auth.PermAppsRead, auth.PermSystemRead})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := h.auth.CreateSession(t.Context(), reader.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer " + token}

	fake.responses["app.list"] = map[string]any{
		"applications": []any{}, "docker_available": true,
	}

	// Reading is allowed.
	if rec := h.do("GET", "/api/v1/apps", "", headers); rec.Code != http.StatusOK {
		t.Fatalf("apps.read could not list applications: %d %s", rec.Code, rec.Body.String())
	}

	before := len(fake.calls)

	for _, path := range []string{"install", "start", "stop", "restart", "uninstall", "data/remove"} {
		rec := h.do("POST", "/api/v1/apps/hello-homebase/"+path,
			`{"confirm":"hello-homebase"}`, headers)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s returned %d for a read-only user; want 403", path, rec.Code)
		}
	}

	// Refused before hostd was consulted at all.
	if len(fake.calls) != before {
		t.Errorf("a read-only user caused %d privileged calls", len(fake.calls)-before)
	}
}

func TestApplicationEndpointsRequireAuthentication(t *testing.T) {
	h, fake := newAppHarness(t)

	for _, request := range []struct{ method, path string }{
		{"GET", "/api/v1/apps"},
		{"GET", "/api/v1/apps/hello-homebase"},
		{"GET", "/api/v1/apps/hello-homebase/logs"},
		{"POST", "/api/v1/apps/hello-homebase/install"},
		{"POST", "/api/v1/apps/hello-homebase/uninstall"},
		{"POST", "/api/v1/apps/hello-homebase/data/remove"},
	} {
		rec := h.do(request.method, request.path, `{"confirm":"hello-homebase"}`, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s returned %d without a session; want 401",
				request.method, request.path, rec.Code)
		}
	}

	if len(fake.calls) != 0 {
		t.Errorf("unauthenticated requests reached hostd: %v", fake.calls)
	}
}

// --- Failures -----------------------------------------------------------------

// A failure that started in hostd must reach the user with its own code and
// explanation. Flattening it to "internal error" tells a user whose disk is full
// nothing they can act on.
func TestHostdFailureKeepsItsCodeOnTheJob(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.failures["app.install"] = hostclient.Error{
		Code:        "app.pull_failed",
		Message:     "Homebase could not download Hello Homebase.",
		Recoverable: true,
		Recovery:    "Check that the server is connected to the internet.",
		Status:      http.StatusBadGateway,
	}

	rec := h.do("POST", "/api/v1/apps/hello-homebase/install", "", headers)
	job := h.awaitJob(t, submittedJobID(t, rec), headers)

	if job.State != jobs.StateFailed {
		t.Fatalf("job state = %s", job.State)
	}
	if job.Error == nil {
		t.Fatal("a failed job carried no error")
	}
	if job.Error.Code != "app.pull_failed" {
		t.Errorf("code = %q, want app.pull_failed", job.Error.Code)
	}
	if !job.Error.Recoverable || job.Error.Recovery == "" {
		t.Error("the recovery advice was lost crossing the process boundary")
	}
}

// An unknown application is a 404, not a job that fails immediately. A job in
// the user's history for something that was never going to happen is noise.
func TestUnknownApplicationIsRejectedBeforeAJobExists(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.failures["app.status"] = hostclient.Error{
		Code:    "app.unknown",
		Message: "This server does not have that application.",
		Status:  http.StatusNotFound,
	}

	rec := h.do("POST", "/api/v1/apps/not-a-real-app/install", "", headers)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}

	if calls := fake.callsTo("app.install"); len(calls) != 0 {
		t.Error("install was attempted for an application that does not exist")
	}

	// And no job was created.
	list := h.do("GET", "/api/v1/jobs", "", headers)
	var jobList struct{ Total int }
	json.Unmarshal(list.Body.Bytes(), &jobList)
	if jobList.Total != 0 {
		t.Errorf("%d jobs exist for an application that does not", jobList.Total)
	}
}

// hostd being down must not look like "there are no applications".
func TestHostdDownIsReportedAsUnavailable(t *testing.T) {
	h := newHarness(t) // points at a socket that does not exist
	headers := h.signedIn(t)

	rec := h.do("GET", "/api/v1/apps", "", headers)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Error struct {
			Code        string `json:"code"`
			Recoverable bool   `json:"recoverable"`
		} `json:"error"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "hostd.unavailable" {
		t.Errorf("code = %q", body.Error.Code)
	}
	if !body.Error.Recoverable {
		t.Error("hostd being down was reported as unrecoverable; it usually clears on its own")
	}
}

// A manifest that failed to load must be visible, with its reason. An
// application that is silently absent is the harder thing to diagnose.
func TestRejectedManifestsAreReportedToTheClient(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.responses["app.list"] = map[string]any{
		"applications":     []any{},
		"docker_available": true,
		"rejected":         map[string]string{"broken.json": "unknown field \"permisions\""},
	}

	rec := h.do("GET", "/api/v1/apps", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var body struct {
		Unavailable map[string]string `json:"unavailable"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if reason := body.Unavailable["broken.json"]; !strings.Contains(reason, "permisions") {
		t.Errorf("the rejection reason did not reach the client: %v", body.Unavailable)
	}
}

// Docker being down must not empty the catalogue: the machine still knows what
// it can install.
func TestDockerDownStillListsApplications(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.responses["app.list"] = map[string]any{
		"applications": []any{map[string]any{
			"id": "hello-homebase", "name": "Hello Homebase",
			"state": "not_installed", "installed": false,
			"image": "traefik/whoami", "data_path": "/srv/homebase/apps/hello-homebase",
		}},
		"docker_available": false,
	}

	rec := h.do("GET", "/api/v1/apps", "", headers)

	var body struct {
		Total           int  `json:"total"`
		DockerAvailable bool `json:"docker_available"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)

	if body.Total != 1 {
		t.Errorf("total = %d; the catalogue emptied itself because Docker was down", body.Total)
	}
	if body.DockerAvailable {
		t.Error("docker_available was true when Docker was down")
	}
}

// --- Events -------------------------------------------------------------------

// An application operation is something the user should be able to see happened,
// after the fact, without having had the page open at the time.
func TestApplicationOperationsAreRecordedAsEvents(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.responses["app.install"] = map[string]any{"id": "hello-homebase", "installed": true}

	rec := h.do("POST", "/api/v1/apps/hello-homebase/install", "", headers)
	h.awaitJob(t, submittedJobID(t, rec), headers)

	list, err := h.events.List(t.Context(), events.Query{})
	if err != nil {
		t.Fatal(err)
	}

	for _, event := range list {
		if event.Type == "application_installed" {
			if event.Subject == nil || *event.Subject != "hello-homebase" {
				t.Errorf("subject = %v", event.Subject)
			}
			// An event is read by a person in a history list weeks later. The
			// operation name is in `type`; `message` is an account of what
			// happened, and "hello-homebase: app.install" is neither.
			if event.Message == nil {
				t.Fatal("the event carried no message")
			}
			if strings.Contains(*event.Message, "app.install") {
				t.Errorf("the message is a function name, not a sentence: %q", *event.Message)
			}
			if !strings.Contains(*event.Message, "Hello Homebase") {
				t.Errorf("the message does not name the application as the user knows it: %q",
					*event.Message)
			}
			return
		}
	}
	t.Errorf("no application_installed event; got %d events", len(list))
}

func TestFailedOperationRecordsAnErrorEventWithItsReason(t *testing.T) {
	h, fake := newAppHarness(t)
	headers := h.signedIn(t)

	fake.failures["app.install"] = hostclient.Error{
		Code: "app.pull_failed", Message: "Could not download it.",
		Status: http.StatusBadGateway,
	}

	rec := h.do("POST", "/api/v1/apps/hello-homebase/install", "", headers)
	h.awaitJob(t, submittedJobID(t, rec), headers)

	list, err := h.events.List(t.Context(), events.Query{Severity: events.SeverityError})
	if err != nil {
		t.Fatal(err)
	}

	for _, event := range list {
		if event.Type == "application_install_failed" {
			// The reason is machine-readable, so a consumer can branch on why
			// rather than on how it was worded.
			if event.Reason == nil || *event.Reason != "app.pull_failed" {
				t.Errorf("reason = %v, want app.pull_failed", event.Reason)
			}
			return
		}
	}
	t.Error("a failed install recorded no error event")
}

// hostclientError builds an error envelope for the fake hostd to return.
func hostclientError(code, message string, status int) hostclient.Error {
	return hostclient.Error{
		Code: code, Message: message, Status: status,
		Recoverable: true, Recovery: "Try again.",
	}
}
