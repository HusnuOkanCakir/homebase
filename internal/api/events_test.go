package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/events"
)

func TestListEventsRequiresAuthentication(t *testing.T) {
	h, _ := newAppHarness(t)

	for _, path := range []string{"/api/v1/events", "/api/v1/events/stream"} {
		if rec := h.do("GET", path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s returned %d without a session; want 401", path, rec.Code)
		}
	}
}

func TestListEventsRejectsAnUnparseableSince(t *testing.T) {
	h, _ := newAppHarness(t)
	headers := h.signedIn(t)

	// 400, not 500. A caller sending a bad timestamp is not a bug in Homebase,
	// and 500 means "we broke".
	rec := h.do("GET", "/api/v1/events?since=last%20tuesday", "", headers)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestListEventsRejectsAnUnknownSeverity(t *testing.T) {
	h, _ := newAppHarness(t)
	headers := h.signedIn(t)

	rec := h.do("GET", "/api/v1/events?severity=catastrophic", "", headers)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// A quiet machine returns an empty list, not null. A client doing
// `data.items.map(...)` should not have to special-case a server where nothing
// has happened yet.
func TestListEventsOnAQuietMachineReturnsAnEmptyArray(t *testing.T) {
	h, _ := newAppHarness(t)
	headers := h.signedIn(t)

	rec := h.do("GET", "/api/v1/events", "", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("body = %s", rec.Body.String())
	}
}

// The stream needs a real server: a ResponseRecorder never flushes and the
// handler would sit in its loop until the test timed out.
func TestEventStreamDeliversAnEventAsItHappens(t *testing.T) {
	h, _ := newAppHarness(t)
	headers := h.signedIn(t)

	server := httptest.NewServer(h.handler)
	defer server.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		server.URL+"/api/v1/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Errorf("Content-Type = %q", contentType)
	}
	// Without this nginx buffers the response and the stream arrives in batches
	// minutes late, which is indistinguishable from a stream that is not working.
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Error("X-Accel-Buffering is not disabled; a proxy would buffer this stream")
	}

	reader := bufio.NewReader(resp.Body)

	// The stream says it is open before anything happens, so a client is not left
	// wondering whether it connected.
	if line, err := reader.ReadString('\n'); err != nil || !strings.HasPrefix(line, ":") {
		t.Fatalf("first line = %q, err = %v", line, err)
	}

	// Give the handler a moment to register its subscription before producing an
	// event it would otherwise miss.
	deadline := time.Now().Add(2 * time.Second)
	for h.events.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.events.Subscribers() == 0 {
		t.Fatal("the stream never subscribed")
	}

	h.events.Warn(t.Context(), "application_unhealthy", "jellyfin",
		"health_check_failed", "Jellyfin is not responding.")

	payload := readSSEData(t, reader)

	var event events.Event
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		t.Fatalf("decoding %q: %v", payload, err)
	}
	if event.Type != "application_unhealthy" {
		t.Errorf("type = %q", event.Type)
	}
	if event.Reason == nil || *event.Reason != "health_check_failed" {
		t.Errorf("reason = %v", event.Reason)
	}
}

// A closed client must not leave a subscriber behind. Every dashboard reload
// opens a new stream, so a leak here is unbounded on a machine left running for
// months.
func TestClosingTheStreamReleasesItsSubscription(t *testing.T) {
	h, _ := newAppHarness(t)
	headers := h.signedIn(t)

	server := httptest.NewServer(h.handler)
	defer server.Close()

	ctx, cancel := context.WithCancel(t.Context())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/api/v1/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for h.events.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.events.Subscribers() != 1 {
		t.Fatalf("subscribers = %d", h.events.Subscribers())
	}

	cancel()
	resp.Body.Close()

	deadline = time.Now().Add(5 * time.Second)
	for h.events.Subscribers() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.events.Subscribers() != 0 {
		t.Errorf("subscribers = %d after the client went away", h.events.Subscribers())
	}
}

// readSSEData reads until the next `data:` line, skipping comments and fields.
func readSSEData(t *testing.T, reader *bufio.Reader) string {
	t.Helper()

	for i := 0; i < 50; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		if payload, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data: "); ok {
			return payload
		}
	}
	t.Fatal("no data line in the stream")
	return ""
}
