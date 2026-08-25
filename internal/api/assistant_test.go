package api

import (
	"encoding/json"
	"fmt"
	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeModel stands in for llama-server.
//
// It speaks the same OpenAI-compatible SSE the real one does, which is what
// makes these tests worth having: the relay is the part most likely to break
// when the inference server is upgraded, and it is exercised here against the
// real wire format rather than against a mock of our own shape.
type fakeModel struct {
	server *httptest.Server

	mu       sync.Mutex
	requests int
	lastBody map[string]any

	// tokens are emitted one SSE chunk at a time.
	tokens []string
	// finish is the finish_reason sent with the last chunk; empty sends none,
	// which is how a stream that ends without one gets tested.
	finish string
	// status, when set, is returned instead of a stream.
	status int
	// hold blocks the completion until closed, so concurrency can be tested.
	hold chan struct{}

	// openToAnyone mirrors the contained model, which has no API key at all:
	// it is not on a network, so the socket's permissions are its access
	// control and there is no file to read.
	openToAnyone bool
}

func newFakeModel(t *testing.T, tokens ...string) *fakeModel {
	t.Helper()
	f := &fakeModel{tokens: tokens, finish: "stop"}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if !f.openToAnyone && r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"/srv/qwen-lab/models/Qwen3.8-4B-Q4_K_M.gguf"}]}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.lastBody = body
		status, hold := f.status, f.hold
		f.mu.Unlock()

		if status != 0 {
			w.WriteHeader(status)
			return
		}
		if hold != nil {
			<-hold
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i, token := range f.tokens {
			reason := "null"
			if i == len(f.tokens)-1 && f.finish != "" {
				reason = `"` + f.finish + `"`
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":%s}]}\n\n",
				token, reason)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// withAssistant wires a harness to a fake model and a readable key file.
func withAssistant(t *testing.T, h *harness, f *fakeModel) {
	t.Helper()
	keyFile := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(keyFile, []byte("test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	url := ""
	if f != nil {
		url = f.server.URL + "/v1"
	}
	h.server.WithAssistant(url, keyFile)
	h.handler = h.server.Handler()
}

func auth1(token string) map[string]string {
	return map[string]string{"Cookie": SessionCookie + "=" + token}
}

// --- Availability ------------------------------------------------------------

func TestAssistantIsOffUnlessConfigured(t *testing.T) {
	h := newHarness(t)
	token := h.signIn(t)

	rec := h.do(http.MethodGet, "/api/v1/assistant", "", auth1(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var status AssistantStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Available {
		t.Fatal("a server with no model configured reported an available assistant")
	}
	// The reason is what the dashboard shows instead of a blank tab.
	if status.Reason == "" {
		t.Fatal("unavailable without saying why")
	}
}

func TestAssistantReportsTheModelItIsServing(t *testing.T) {
	h := newHarness(t)
	withAssistant(t, h, newFakeModel(t, "hi"))
	token := h.signIn(t)

	rec := h.do(http.MethodGet, "/api/v1/assistant", "", auth1(token))
	var status AssistantStatus
	json.Unmarshal(rec.Body.Bytes(), &status)

	if !status.Available {
		t.Fatalf("not available: %s", status.Reason)
	}
	// The path and extension are noise; the name is the part a person reads.
	if status.Model != "Qwen3.8-4B-Q4_K_M" {
		t.Fatalf("model = %q, want the bare name", status.Model)
	}
}

func TestAssistantSaysWhenItsKeyCannotBeRead(t *testing.T) {
	h := newHarness(t)
	f := newFakeModel(t, "hi")
	h.server.WithAssistant(f.server.URL+"/v1", filepath.Join(t.TempDir(), "absent"))
	h.handler = h.server.Handler()
	token := h.signIn(t)

	rec := h.do(http.MethodGet, "/api/v1/assistant", "", auth1(token))
	var status AssistantStatus
	json.Unmarshal(rec.Body.Bytes(), &status)

	if status.Available {
		t.Fatal("reported available with an unreadable key")
	}
	// "not created yet" and "cannot read it" have different fixes, so the
	// message has to distinguish them.
	if !strings.Contains(status.Reason, "not been created") {
		t.Fatalf("reason = %q, want it to say the key does not exist", status.Reason)
	}
}

// --- Permission --------------------------------------------------------------

func TestAssistantRequiresItsOwnPermission(t *testing.T) {
	h := newHarness(t)
	withAssistant(t, h, newFakeModel(t, "hi"))
	token := h.signIn(t)

	// An account with everything except assistant.use.
	user, err := h.auth.CreateUser(t.Context(), "guest", goodPassword,
		[]string{"system.read", "system.manage", "apps.read"})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := h.auth.CreateSession(t.Context(), user.ID, "test")
	if err != nil {
		t.Fatal(err)
	}

	rec := h.do(http.MethodPost, "/api/v1/assistant/chat",
		`{"messages":[{"role":"user","content":"hello"}]}`, auth1(session))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — asking the model is not implied by "+
			"being able to manage the machine", rec.Code)
	}
	_ = token
}

// --- Answering ---------------------------------------------------------------

func TestAssistantStreamsAnAnswer(t *testing.T) {
	h := newHarness(t)
	f := newFakeModel(t, "A ", "home ", "server.")
	withAssistant(t, h, f)
	token := h.signIn(t)

	rec := h.do(http.MethodPost, "/api/v1/assistant/chat",
		`{"messages":[{"role":"user","content":"what is it?"}]}`, auth1(token))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q", got)
	}

	body := rec.Body.String()
	for _, want := range []string{`"text":"A "`, `"text":"home "`, `"text":"server."`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream is missing %s:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "event: done") {
		t.Fatalf("stream never ended:\n%s", body)
	}
	// Buffering is what turns a live answer into a two-minute wait followed by
	// everything at once.
	if rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatal("the stream does not ask proxies to stop buffering")
	}
}

func TestAssistantReportsAnAnswerCutShort(t *testing.T) {
	h := newHarness(t)
	f := newFakeModel(t, "It was going to say")
	f.finish = "length"
	withAssistant(t, h, f)
	token := h.signIn(t)

	rec := h.do(http.MethodPost, "/api/v1/assistant/chat",
		`{"messages":[{"role":"user","content":"hi"}]}`, auth1(token))

	// Without this the browser shows a sentence that simply stops, which reads
	// as the model being broken rather than as the token limit being reached.
	if !strings.Contains(rec.Body.String(), `"reason":"length"`) {
		t.Fatalf("a truncated answer was not reported as truncated:\n%s", rec.Body)
	}
}

func TestAssistantOnlySendsThinkingWhenAsked(t *testing.T) {
	h := newHarness(t)
	f := newFakeModel(t, "ok")
	withAssistant(t, h, f)
	token := h.signIn(t)

	h.do(http.MethodPost, "/api/v1/assistant/chat",
		`{"messages":[{"role":"user","content":"hi"}]}`, auth1(token))
	f.mu.Lock()
	_, present := f.lastBody["chat_template_kwargs"]
	f.mu.Unlock()
	if present {
		t.Fatal("thinking was sent without being asked for; the service decides the default")
	}

	h.do(http.MethodPost, "/api/v1/assistant/chat",
		`{"messages":[{"role":"user","content":"hi"}],"think":true}`, auth1(token))
	f.mu.Lock()
	kwargs, _ := f.lastBody["chat_template_kwargs"].(map[string]any)
	f.mu.Unlock()
	if kwargs["enable_thinking"] != true {
		t.Fatalf("--think did not reach the model: %v", kwargs)
	}
}

// --- One at a time -----------------------------------------------------------

func TestAssistantRefusesASecondQuestionWhileBusy(t *testing.T) {
	h := newHarness(t)
	f := newFakeModel(t, "slow")
	f.hold = make(chan struct{})
	withAssistant(t, h, f)
	token := h.signIn(t)

	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		h.do(http.MethodPost, "/api/v1/assistant/chat",
			`{"messages":[{"role":"user","content":"one"}]}`, auth1(token))
		close(done)
	}()

	<-started
	// Wait until the first request is actually inside the model call, so this
	// tests the lock rather than the scheduler.
	deadline := time.After(2 * time.Second)
	for {
		f.mu.Lock()
		inFlight := f.requests > 0
		f.mu.Unlock()
		if inFlight {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the first request never reached the model")
		case <-time.After(5 * time.Millisecond):
		}
	}

	rec := h.do(http.MethodPost, "/api/v1/assistant/chat",
		`{"messages":[{"role":"user","content":"two"}]}`, auth1(token))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — the model has one slot and a queued "+
			"caller cannot tell waiting from hanging", rec.Code)
	}
	if code := decodeError(t, rec).Code; code != "assistant.busy" {
		t.Fatalf("code = %q", code)
	}

	close(f.hold)
	<-done

	// And the slot is given back, or the assistant works exactly once.
	rec = h.do(http.MethodPost, "/api/v1/assistant/chat",
		`{"messages":[{"role":"user","content":"three"}]}`, auth1(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("the slot was not released: status = %d", rec.Code)
	}
}

// --- Refusals ----------------------------------------------------------------

func TestAssistantRejectsAnEmptyOrOversizedConversation(t *testing.T) {
	h := newHarness(t)
	withAssistant(t, h, newFakeModel(t, "ok"))
	token := h.signIn(t)

	cases := []struct {
		name string
		body string
		code string
	}{
		{"nothing to ask", `{"messages":[]}`, "assistant.empty"},
		{"a role we do not send", `{"messages":[{"role":"system","content":"x"}]}`, "assistant.bad_role"},
		{"a pasted logfile", `{"messages":[{"role":"user","content":"` +
			strings.Repeat("x", assistantMaxPromptBytes+1) + `"}]}`, "assistant.message_too_long"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := h.do(http.MethodPost, "/api/v1/assistant/chat", c.body, auth1(token))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if got := decodeError(t, rec).Code; got != c.code {
				t.Fatalf("code = %q, want %q", got, c.code)
			}
		})
	}
}

func TestAssistantReportsAModelThatIsDown(t *testing.T) {
	h := newHarness(t)
	f := newFakeModel(t, "ok")
	f.status = http.StatusInternalServerError
	withAssistant(t, h, f)
	token := h.signIn(t)

	rec := h.do(http.MethodPost, "/api/v1/assistant/chat",
		`{"messages":[{"role":"user","content":"hi"}]}`, auth1(token))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	e := decodeError(t, rec)
	if !e.Recoverable || e.Recovery == "" {
		t.Fatal("a model that is down is recoverable and should say how")
	}
}

// --- The contained model -----------------------------------------------------

// withUnrestricted adds a fake sandboxed model on a Unix socket.
func withUnrestricted(t *testing.T, h *harness, f *fakeModel) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "api.sock")
	if f != nil {
		f.openToAnyone = true
		// Re-serve the same handler over a Unix socket, which is how the real
		// contained model is reached: it has no network at all.
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: f.server.Config.Handler}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })
	}
	h.server.WithUnrestrictedAssistant(socket)
	h.handler = h.server.Handler()
	return socket
}

func permitted(t *testing.T, h *harness, permissions []string) string {
	t.Helper()
	user, err := h.auth.CreateUser(t.Context(), "researcher", goodPassword, permissions)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := h.auth.CreateSession(t.Context(), user.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func modelsFor(t *testing.T, h *harness, token string) []AssistantModel {
	t.Helper()
	rec := h.do(http.MethodGet, "/api/v1/assistant", "", auth1(token))
	var status AssistantStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	return status.Models
}

// Not merely disabled for them — absent. A model somebody may not use is not
// one they should learn exists from a greyed-out entry.
func TestTheUnrestrictedModelIsNotOfferedWithoutThePermission(t *testing.T) {
	h := newHarness(t)
	withAssistant(t, h, newFakeModel(t, "hi"))
	withUnrestricted(t, h, newFakeModel(t, "hi"))
	token := permitted(t, h, []string{auth.PermAssistantUse})

	for _, model := range modelsFor(t, h, token) {
		if model.Unrestricted {
			t.Fatalf("offered %q to an account without assistant.unrestricted", model.ID)
		}
	}
}

func TestTheUnrestrictedModelIsOfferedWithThePermission(t *testing.T) {
	h := newHarness(t)
	withAssistant(t, h, newFakeModel(t, "hi"))
	withUnrestricted(t, h, newFakeModel(t, "hi"))
	token := permitted(t, h,
		[]string{auth.PermAssistantUse, auth.PermAssistantUnrestricted})

	var found *AssistantModel
	for _, model := range modelsFor(t, h, token) {
		if model.Unrestricted {
			found = &model
		}
	}
	if found == nil {
		t.Fatal("not offered to an account that holds the permission")
	}
	if !found.Available {
		t.Fatalf("offered but unavailable: %q", found.Reason)
	}
}

// The list is a convenience; the check on the request is the control.
func TestNamingTheUnrestrictedModelDirectlyIsStillRefused(t *testing.T) {
	h := newHarness(t)
	withAssistant(t, h, newFakeModel(t, "hi"))
	withUnrestricted(t, h, newFakeModel(t, "hi"))
	token := permitted(t, h, []string{auth.PermAssistantUse})

	rec := h.do(http.MethodPost, "/api/v1/assistant/chat",
		`{"model":"unrestricted","messages":[{"role":"user","content":"hi"}]}`,
		auth1(token))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — a client that was never offered the "+
			"model can still name it", rec.Code)
	}
	if code := decodeError(t, rec).Code; code != "assistant.not_permitted" {
		t.Fatalf("code = %q", code)
	}
}

// Selecting it is always deliberate: naming nothing must never reach it.
func TestTheDefaultModelIsNeverTheUnrestrictedOne(t *testing.T) {
	h := newHarness(t)
	safe := newFakeModel(t, "safe answer")
	withAssistant(t, h, safe)
	unsafe := newFakeModel(t, "unrestricted answer")
	withUnrestricted(t, h, unsafe)
	token := permitted(t, h,
		[]string{auth.PermAssistantUse, auth.PermAssistantUnrestricted})

	rec := h.do(http.MethodPost, "/api/v1/assistant/chat",
		`{"messages":[{"role":"user","content":"hi"}]}`, auth1(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "safe answer") {
		t.Fatalf("a request naming no model did not reach the primary one:\n%s", rec.Body)
	}
}

// It is stopped most of the time, and that is not a fault.
func TestAStoppedContainedModelSaysSoRatherThanErroring(t *testing.T) {
	h := newHarness(t)
	withAssistant(t, h, newFakeModel(t, "hi"))
	withUnrestricted(t, h, nil) // a socket path with nothing listening
	token := permitted(t, h,
		[]string{auth.PermAssistantUse, auth.PermAssistantUnrestricted})

	for _, model := range modelsFor(t, h, token) {
		if !model.Unrestricted {
			continue
		}
		if model.Available {
			t.Fatal("reported available with nothing listening")
		}
		if !strings.Contains(model.Reason, "not running") {
			t.Fatalf("reason = %q, want it to say it is not running", model.Reason)
		}
	}
}

// Homebase must have no way to start it. The volume it lives in stays locked
// unless somebody opened it at the machine, and nothing here may change that.
func TestNothingInTheAPICanStartTheContainedModel(t *testing.T) {
	h := newHarness(t)
	withAssistant(t, h, newFakeModel(t, "hi"))
	withUnrestricted(t, h, nil)
	token := permitted(t, h,
		[]string{auth.PermAssistantUse, auth.PermAssistantUnrestricted})

	// Every shape of request that might plausibly be wired to a start.
	for _, attempt := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/assistant/start", ""},
		{http.MethodPost, "/api/v1/assistant/unrestricted/start", ""},
		{http.MethodPost, "/api/v1/assistant", `{"model":"unrestricted","start":true}`},
	} {
		rec := h.do(attempt.method, attempt.path, attempt.body, auth1(token))
		if rec.Code == http.StatusOK || rec.Code == http.StatusAccepted ||
			rec.Code == http.StatusCreated {
			t.Fatalf("%s %s answered %d; there must be no route that starts it",
				attempt.method, attempt.path, rec.Code)
		}
	}
}
