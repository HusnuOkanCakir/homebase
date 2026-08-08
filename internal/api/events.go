package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
)

func (s *Server) registerEventRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/events", s.require(auth.PermSystemRead, s.handleListEvents))
	mux.Handle("GET /api/v1/events/stream", s.require(auth.PermSystemRead, s.handleStreamEvents))
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	if s.events == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "total": 0})
		return
	}

	query := events.Query{Severity: events.Severity(r.URL.Query().Get("severity"))}
	query.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))

	if since := r.URL.Query().Get("since"); since != "" {
		parsed, err := time.Parse(time.RFC3339, since)
		if err != nil {
			s.writeError(w, r, http.StatusBadRequest, apiError{
				Code:        "request.invalid_parameter",
				Message:     "That is not a time Homebase understands.",
				Detail:      "since must be RFC 3339, for example 2026-08-08T12:00:00Z",
				Recoverable: true,
				Recovery:    "Use an RFC 3339 timestamp.",
			})
			return
		}
		query.Since = parsed
	}

	list, err := s.events.List(r.Context(), query)
	if errors.Is(err, events.ErrInvalidQuery) {
		// 400 rather than 500: the caller asked for something impossible, which
		// is not a bug in Homebase.
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.invalid_parameter",
			Message:     "Homebase does not know that kind of event.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Ask for info, warning, error or critical.",
		})
		return
	}
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": list, "total": len(list)})
}

// handleStreamEvents streams events as they happen, as server-sent events.
//
// SSE rather than a WebSocket: this is one-way, it is plain HTTP so it inherits
// the session cookie and every proxy in between already understands it, and the
// browser reconnects on its own. A WebSocket would buy bidirectionality that
// nothing here needs and a second protocol to secure.
//
// The stream is a convenience, never the source of truth. A client that misses
// events while disconnected re-reads GET /events, which is why that endpoint
// takes `since`. Anything that only ever arrives over the live stream is
// something a user who closed their laptop lid will never learn.
func (s *Server) handleStreamEvents(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Every server this runs behind can flush. If one cannot, say so rather
		// than opening a stream that silently never delivers anything.
		s.writeError(w, r, http.StatusInternalServerError, apiError{
			Code:        "events.streaming_unsupported",
			Message:     "This server cannot stream live updates.",
			Recoverable: false,
		})
		return
	}
	if s.events == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, apiError{
			Code:        "events.unavailable",
			Message:     "Live updates are not available on this server.",
			Recoverable: false,
		})
		return
	}

	stream, unsubscribe := s.events.Subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	// Named for nginx, which buffers proxied responses by default and would
	// hold each event until the buffer filled — turning a live stream into a
	// batch delivery that arrives minutes late.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Told up front, so a client knows the stream is open rather than waiting
	// for the first thing to happen on a quiet machine.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	// A heartbeat, because a home network puts a router between the browser and
	// this server, and an idle TCP connection is something routers close without
	// telling either end. Without this a stream looks alive for hours and
	// delivers nothing.
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case event, open := <-stream:
			if !open {
				return
			}
			payload, err := json.Marshal(event)
			if err != nil {
				s.log.Error("could not encode an event for the stream",
					"type", event.Type, "error", err)
				continue
			}
			// id: lets the browser send Last-Event-ID when it reconnects.
			fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.Type, payload)
			flusher.Flush()

		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}
