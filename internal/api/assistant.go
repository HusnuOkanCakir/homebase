package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
)

// The local assistant.
//
// A language model running on this machine, reached over loopback. Homebase
// proxies it rather than letting the browser talk to it directly, for the same
// reason the dashboard has no other side channels: the browser holds a Homebase
// session, not a model API key, and the model's own key never leaves the host.
//
// **Nothing here leaves the network.** The model is a file on this disk and the
// process serving it listens on 127.0.0.1. There is no upstream, no telemetry,
// and no account. That is the whole point of running a 2.6 GiB model on a
// laptop instead of calling something better over the internet, and it is worth
// stating in the interface rather than in a footnote.
//
// **Conversations are not stored.** The browser holds the transcript and sends
// it back with each turn; core keeps nothing once the response is written. A
// record of everything anybody ever asked their house server would be a new and
// fairly personal database, and this feature does not need one to work.
//
// Off unless configured. `--assistant-url` is empty by default, and with no URL
// the endpoints report unavailable and the dashboard hides the tab — an
// installation that never set this up should show no sign of it.

const (
	// assistantMaxTurns bounds the transcript a client may send back.
	//
	// The model's context is 2048 tokens on this hardware. Rather than let a
	// long conversation silently push the earliest turns out of the window —
	// which reads as the assistant developing amnesia — the limit is explicit
	// and the client trims to it knowingly.
	assistantMaxTurns = 24

	// assistantMaxPromptBytes bounds one message.
	//
	// Generous next to 2048 tokens, and small next to what a paste can be. It
	// exists so a pasted logfile fails immediately with a sentence about why
	// rather than after ninety seconds of the model reading it.
	assistantMaxPromptBytes = 24 << 10

	// assistantMaxTokens is the ceiling on one answer.
	//
	// Measured decode on this machine is about 6.5 tokens/second, so 1024
	// tokens is around two and a half minutes. That is the longest wait worth
	// designing for; past it, somebody has stopped reading.
	assistantMaxTokens     = 1024
	assistantDefaultTokens = 512

	// assistantTimeout has to cover a full-length answer at measured speed,
	// with room for the prompt to be read first.
	assistantTimeout = 5 * time.Minute

	// assistantProbeTimeout is for the status check only, which must stay fast
	// enough to run on every dashboard load.
	assistantProbeTimeout = 3 * time.Second
)

// assistantBackend is one model server core can talk to.
type assistantBackend struct {
	// id is what a client selects. Stable; the label is not.
	id      string
	label   string
	url     string
	keyFile string

	// socket is set instead of a network address for a model that has none.
	//
	// The contained model is deliberately unreachable over IP — see the lab's
	// sandbox/THREAT-MODEL.md — so it listens on a Unix socket. Homebase can
	// therefore *use* it, and has no way whatsoever to *start* it, which is the
	// property being preserved: opening the encrypted volume it lives in stays
	// a deliberate act at the machine.
	socket string

	// unrestricted marks a model whose refusal behaviour a third party removed.
	// Selecting it requires its own permission, and it is never the default.
	unrestricted bool

	// busy serialises requests.
	//
	// Each server is started with --parallel 1: it has one slot, and a second
	// question does not run alongside the first, it waits behind it. Left to
	// itself that produces two people watching a stalled cursor with no
	// indication that anything is wrong. One at a time, and the second caller
	// is told. Held per backend, because they are separate processes.
	held bool
	mu   sync.Mutex
}

// assistantConfig is the set of models this machine offers.
type assistantConfig struct {
	backends []*assistantBackend
}

// WithAssistant points core at a local model server.
//
// An empty URL leaves the assistant off, which is the default.
func (s *Server) WithAssistant(url, keyFile string) *Server {
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	if url == "" {
		return s
	}
	if s.assistant == nil {
		s.assistant = &assistantConfig{}
	}
	s.assistant.backends = append(s.assistant.backends, &assistantBackend{
		id: "local", label: "Local model", url: url, keyFile: keyFile,
	})
	return s
}

// WithUnrestrictedAssistant offers a contained model that is already running.
//
// Homebase can use it and cannot start it. There is no operation anywhere that
// unlocks its volume or launches its unit, and adding one would mean a
// privileged operation whose purpose is to make a model with its refusals
// removed easier to reach — which is the thing ADR-0006 exists to prevent. If
// it is not running, it is not offered, and the way to change that is a command
// typed at the machine.
func (s *Server) WithUnrestrictedAssistant(socket string) *Server {
	socket = strings.TrimSpace(socket)
	if socket == "" {
		return s
	}
	if s.assistant == nil {
		s.assistant = &assistantConfig{}
	}
	s.assistant.backends = append(s.assistant.backends, &assistantBackend{
		id: "unrestricted", label: "Unrestricted model",
		socket: socket, unrestricted: true,
	})
	return s
}

// client dials this backend, over a socket or over loopback.
func (b *assistantBackend) client(timeout time.Duration) *http.Client {
	if b.socket == "" {
		return &http.Client{Timeout: timeout}
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", b.socket)
			},
		},
	}
}

// endpoint is the base URL to use with client().
func (b *assistantBackend) endpoint() string {
	if b.socket == "" {
		return b.url
	}
	// The host is ignored by the Unix transport, but net/http insists on one.
	return "http://localhost/v1"
}

// key reads this backend's API key, if it has one.
//
// The contained model has none: it is not on a network, so the socket's
// permissions are its access control, and there is no file to read.
func (b *assistantBackend) key() (string, error) {
	if b.keyFile == "" {
		return "", nil
	}
	raw, err := os.ReadFile(b.keyFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// find returns the backend with this id.
func (a *assistantConfig) find(id string) *assistantBackend {
	for _, backend := range a.backends {
		if backend.id == id {
			return backend
		}
	}
	return nil
}

// primary is the model used when a client names none: the first that is not
// unrestricted. Selecting the contained one is always deliberate.
func (a *assistantConfig) primary() *assistantBackend {
	for _, backend := range a.backends {
		if !backend.unrestricted {
			return backend
		}
	}
	return nil
}

// tryHold takes the single slot, reporting whether it got it.
func (b *assistantBackend) tryHold() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.held {
		return false
	}
	b.held = true
	return true
}

func (b *assistantBackend) release() {
	b.mu.Lock()
	b.held = false
	b.mu.Unlock()
}

// AssistantStatus is what the dashboard asks before showing anything.
type AssistantStatus struct {
	Available bool   `json:"available"`
	Model     string `json:"model,omitempty"`
	// Reason says why it is not available, in a sentence meant for a person.
	Reason   string `json:"reason,omitempty"`
	MaxTurns int    `json:"max_turns"`
	MaxChars int    `json:"max_chars"`

	// Models is every model this person may select, in offer order.
	//
	// Filtered by permission before it is written, not after: a model somebody
	// may not use is not one they should learn exists from a greyed-out entry.
	Models []AssistantModel `json:"models"`
}

// AssistantModel is one selectable model.
type AssistantModel struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Name is what the server underneath reports, e.g. Qwen3.8-4B-Q4_K_M.
	Name      string `json:"name,omitempty"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Unrestricted marks a model whose refusal behaviour a third party removed.
	// The dashboard says so on screen for as long as it is selected.
	Unrestricted bool `json:"unrestricted"`
}

func (s *Server) handleAssistantStatus(w http.ResponseWriter, r *http.Request, user *auth.User) {
	status := AssistantStatus{
		MaxTurns: assistantMaxTurns,
		MaxChars: assistantMaxPromptBytes,
		Models:   []AssistantModel{},
	}

	if s.assistant == nil {
		status.Reason = "No local model is configured on this server."
		writeJSON(w, http.StatusOK, status)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), assistantProbeTimeout)
	defer cancel()

	for _, backend := range s.assistant.backends {
		// Never offered to somebody without the permission, and never merely
		// disabled for them either.
		if backend.unrestricted && !user.Can(auth.PermAssistantUnrestricted) {
			continue
		}

		entry := AssistantModel{
			ID: backend.id, Label: backend.label, Unrestricted: backend.unrestricted,
		}

		// Checked before probing, and reported separately. "Never created",
		// "created but core cannot read it" and "the model is not answering"
		// look identical from the outside and have completely different fixes;
		// collapsing them into one sentence sends somebody to restart a service
		// when the problem is a file mode.
		if _, err := backend.key(); err != nil {
			if os.IsNotExist(err) {
				entry.Reason = "The local model's API key has not been created yet."
			} else {
				entry.Reason = "Homebase cannot read the local model's API key."
			}
			s.log.Warn("assistant key unreadable",
				"model", backend.id, "path", backend.keyFile, "error", err)
			status.Models = append(status.Models, entry)
			continue
		}

		name, err := s.assistantModel(ctx, backend)
		switch {
		case err == nil:
			entry.Available = true
			entry.Name = name
		case backend.unrestricted:
			// Expected, and not a fault: this one is started by hand and is
			// stopped most of the time.
			entry.Reason = "It is not running. Start it on the server."
		default:
			entry.Reason = "The local model is not responding."
			s.log.Debug("assistant probe failed", "model", backend.id, "error", err)
		}
		status.Models = append(status.Models, entry)
	}

	// The top-level fields describe the model a client gets if it names none,
	// so that a caller written before models existed keeps working unchanged.
	for _, entry := range status.Models {
		if !entry.Unrestricted {
			status.Available = entry.Available
			status.Model = entry.Name
			status.Reason = entry.Reason
			break
		}
	}
	if len(status.Models) == 0 {
		status.Reason = "No local model is configured on this server."
	}
	writeJSON(w, http.StatusOK, status)
}

// assistantModel asks the model server what it is serving.
func (s *Server) assistantModel(ctx context.Context, backend *assistantBackend) (string, error) {
	key, err := backend.key()
	if err != nil {
		// Distinguished because the fixes differ: never created, versus created
		// and unreadable by core.
		if os.IsNotExist(err) {
			return "", fmt.Errorf("its API key has not been created yet")
		}
		return "", fmt.Errorf("its API key cannot be read: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		backend.endpoint()+"/models", nil)
	if err != nil {
		return "", err
	}
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}

	response, err := backend.client(assistantProbeTimeout).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("model server answered %d", response.StatusCode)
	}

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return "", err
	}
	if len(body.Data) == 0 {
		return "", fmt.Errorf("model server named no model")
	}
	// The id is the file path it was started with. The name is the useful part.
	name := body.Data[0].ID
	if index := strings.LastIndex(name, "/"); index >= 0 {
		name = name[index+1:]
	}
	return strings.TrimSuffix(name, ".gguf"), nil
}

type assistantMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type assistantRequest struct {
	Messages []assistantMessage `json:"messages"`
	Think    bool               `json:"think"`

	// Model selects which of the offered models answers. Empty means the
	// primary one — selecting the contained model is always deliberate.
	Model string `json:"model,omitempty"`
}

func (s *Server) handleAssistantChat(w http.ResponseWriter, r *http.Request, user *auth.User) {
	if s.assistant == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, apiError{
			Code:        "assistant.unavailable",
			Message:     "No local model is configured on this server.",
			Recoverable: false,
		})
		return
	}

	var body assistantRequest
	if !s.decode(w, r, &body) {
		return
	}
	if !s.validAssistantMessages(w, r, body.Messages) {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, r, http.StatusInternalServerError, apiError{
			Code:        "assistant.streaming_unsupported",
			Message:     "This server cannot stream the answer.",
			Recoverable: false,
		})
		return
	}

	backend := s.assistant.primary()
	if body.Model != "" {
		backend = s.assistant.find(body.Model)
	}
	if backend == nil {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "assistant.unknown_model",
			Message:     "This server does not have that model.",
			Recoverable: true,
			Recovery:    "Choose one of the models the assistant offers.",
		})
		return
	}

	// The permission is checked here as well as when the list is built. The
	// list is a convenience; this is the control. A client that names the
	// contained model directly, having never been offered it, is refused.
	if backend.unrestricted && !user.Can(auth.PermAssistantUnrestricted) {
		s.log.Warn("refused an unrestricted assistant request",
			"user", user.Username, "model", backend.id)
		s.writeError(w, r, http.StatusForbidden, apiError{
			Code:        "assistant.not_permitted",
			Message:     "You are not permitted to use that model.",
			Recoverable: false,
		})
		return
	}

	key, err := backend.key()
	if err != nil {
		s.log.Warn("assistant key unreadable", "path", backend.keyFile, "error", err)
		s.writeError(w, r, http.StatusServiceUnavailable, apiError{
			Code:        "assistant.unavailable",
			Message:     "Homebase cannot reach the local model.",
			Detail:      "Its API key could not be read.",
			Recoverable: false,
			Recovery:    "Check that /etc/qwen-lab/api-key exists and Homebase can read it.",
		})
		return
	}

	// One question at a time. Refused rather than queued: a caller who waits
	// behind somebody else's two-minute answer cannot tell that from a server
	// that has hung.
	if !backend.tryHold() {
		s.writeError(w, r, http.StatusConflict, apiError{
			Code:        "assistant.busy",
			Message:     "The assistant is already answering a question.",
			Detail:      "This server runs one question at a time.",
			Recoverable: true,
			Recovery:    "Wait for the current answer to finish, then ask again.",
		})
		return
	}
	defer backend.release()

	ctx, cancel := context.WithTimeout(r.Context(), assistantTimeout)
	defer cancel()

	upstream, err := s.assistantStream(ctx, backend, key, body)
	if err != nil {
		s.writeError(w, r, http.StatusServiceUnavailable, apiError{
			Code:        "assistant.unavailable",
			Message:     "The local model is not responding.",
			Recoverable: true,
			Recovery:    "Check that the model service is running, then try again.",
		})
		s.log.Warn("assistant upstream failed", "error", err)
		return
	}
	defer upstream.Close()

	// As in handleStreamEvents: core's WriteTimeout would kill a long answer
	// partway through, which looks from the browser like the model stopping
	// mid-sentence.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		s.log.Warn("could not clear the write deadline for an answer", "error", err)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	started := time.Now()
	tokens := s.relayAssistantStream(ctx, w, flusher, upstream)

	s.log.Info("assistant answered",
		"user", user.Username, "model", backend.id, "tokens", tokens,
		"seconds", time.Since(started).Round(time.Second).Seconds())
}

// validAssistantMessages rejects a transcript before any of it reaches the model.
func (s *Server) validAssistantMessages(w http.ResponseWriter, r *http.Request, messages []assistantMessage) bool {
	if len(messages) == 0 {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "assistant.empty",
			Message:     "There is no question here.",
			Recoverable: true,
			Recovery:    "Type something to ask.",
		})
		return false
	}
	if len(messages) > assistantMaxTurns {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:    "assistant.too_long",
			Message: "This conversation is too long for the assistant to hold.",
			Detail: fmt.Sprintf("It keeps the last %d messages; this one has %d.",
				assistantMaxTurns, len(messages)),
			Recoverable: true,
			Recovery:    "Start a new conversation.",
		})
		return false
	}
	for _, message := range messages {
		if message.Role != "user" && message.Role != "assistant" {
			s.writeError(w, r, http.StatusBadRequest, apiError{
				Code:        "assistant.bad_role",
				Message:     "That conversation is not in a shape the assistant understands.",
				Recoverable: true,
				Recovery:    "Start a new conversation.",
			})
			return false
		}
		if len(message.Content) > assistantMaxPromptBytes {
			s.writeError(w, r, http.StatusBadRequest, apiError{
				Code:    "assistant.message_too_long",
				Message: "That message is too long for the assistant.",
				Detail: fmt.Sprintf("The limit is about %d characters.",
					assistantMaxPromptBytes),
				Recoverable: true,
				Recovery:    "Shorten it, or paste a smaller part of it.",
			})
			return false
		}
	}
	return true
}

// assistantStream opens the streaming completion on the model server.
func (s *Server) assistantStream(ctx context.Context, backend *assistantBackend,
	key string, body assistantRequest) (interface {
	Read([]byte) (int, error)
	Close() error
}, error) {
	payload := map[string]any{
		"messages":    body.Messages,
		"max_tokens":  assistantDefaultTokens,
		"temperature": 0.7,
		"stream":      true,
	}
	if body.Think {
		// Only sent when overriding the service default. Thinking measured at
		// 4.4x time-to-answer on this machine, so it stays something asked for.
		payload["chat_template_kwargs"] = map[string]any{"enable_thinking": true}
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		backend.endpoint()+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}

	response, err := backend.client(assistantTimeout).Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("model server answered %d", response.StatusCode)
	}
	return response.Body, nil
}

// relayAssistantStream turns the model's SSE into Homebase's own, returning how
// many pieces of text it forwarded.
//
// The wire format is re-emitted rather than piped through, so the browser is
// coupled to this API and not to whichever inference server happens to be
// behind it.
func (s *Server) relayAssistantStream(ctx context.Context, w http.ResponseWriter,
	flusher http.Flusher, upstream interface{ Read([]byte) (int, error) }) int {

	scanner := bufio.NewScanner(upstream)
	// A single token is tiny, but a reasoning model's first chunk can carry a
	// lot at once. The default 64 KiB line limit would end the stream with a
	// scanner error rather than an answer.
	scanner.Buffer(make([]byte, 0, 8<<10), 1<<20)

	forwarded := 0
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return forwarded
		default:
		}

		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		if text := chunk.Choices[0].Delta.Content; text != "" {
			payload, err := json.Marshal(map[string]string{"text": text})
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: token\ndata: %s\n\n", payload)
			flusher.Flush()
			forwarded++
		}

		if reason := chunk.Choices[0].FinishReason; reason != nil && *reason != "" {
			// "length" means the answer was cut off at the token ceiling. The
			// browser says so rather than leaving a sentence that just stops.
			payload, _ := json.Marshal(map[string]string{"reason": *reason})
			fmt.Fprintf(w, "event: done\ndata: %s\n\n", payload)
			flusher.Flush()
			return forwarded
		}
	}

	if err := scanner.Err(); err != nil {
		s.log.Warn("assistant stream ended early", "error", err)
		fmt.Fprint(w, "event: failed\ndata: {\"reason\":\"interrupted\"}\n\n")
		flusher.Flush()
		return forwarded
	}

	fmt.Fprint(w, "event: done\ndata: {\"reason\":\"stop\"}\n\n")
	flusher.Flush()
	return forwarded
}
