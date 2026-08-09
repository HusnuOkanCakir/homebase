package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/hostclient"
	"github.com/HusnuOkanCakir/homebase/internal/jobs"
)

// apiError is the envelope from schemas/error.schema.json.
//
// `Code` is the contract: clients switch on it and it never changes meaning once
// shipped. `Message` is not, and may be reworded or translated freely.
type apiError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Detail      string `json:"detail,omitempty"`
	Recoverable bool   `json:"recoverable"`
	Recovery    string `json:"recovery,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
}

const maxRequestBytes = 1 << 20

func (s *Server) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes))
	// Strict: a caller who misspells a field is a caller whose intent we do not
	// know, and quietly proceeding without the value they thought they set is
	// the wrong way to resolve that.
	dec.DisallowUnknownFields()

	if err := dec.Decode(target); err != nil {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.invalid_body",
			Message:     "The request was not in the expected format.",
			Detail:      err.Error(),
			Recoverable: false,
		})
		return false
	}
	return true
}

// expectNoBody rejects a request that carries fields the endpoint does not read.
//
// An endpoint taking no parameters is not the same as an endpoint that ignores
// them. A client sending {"image": "..."} to an install endpoint believes it is
// choosing an image; accepting the request and quietly installing something else
// is the worst of the three possible answers. An absent or empty body is fine —
// that is what a caller with nothing to say sends.
func (s *Server) expectNoBody(w http.ResponseWriter, r *http.Request) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.invalid_body",
			Message:     "The request could not be read.",
			Detail:      err.Error(),
			Recoverable: false,
		})
		return false
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return true
	}

	s.writeError(w, r, http.StatusBadRequest, apiError{
		Code:        "request.unexpected_body",
		Message:     "The request contained settings this operation does not accept.",
		Detail:      "this endpoint takes no parameters beyond the application in its path",
		Recoverable: false,
	})
	return false
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, e apiError) {
	if id, ok := r.Context().Value(requestIDKey).(string); ok {
		e.RequestID = id
	}
	writeJSON(w, status, map[string]any{"error": e})
}

// writeInternal reports an unexpected failure without leaking its internals.
//
// The detail goes to the log with the request id; the caller gets the id to
// quote. A stack trace in an API response tells an attacker about the
// implementation and tells the user nothing they can act on.
func (s *Server) writeInternal(w http.ResponseWriter, r *http.Request, err error) {
	id, _ := r.Context().Value(requestIDKey).(string)

	// A client that went away is not a fault. It happens constantly and by
	// design: the dashboard polls, and every navigation and every reboot
	// cancels whatever was in flight. Logging those at error level fills the
	// journal with entries nobody can act on, which is how people learn to
	// scroll past the ones they can.
	level := slog.LevelError
	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		level = slog.LevelDebug
	}
	s.log.Log(r.Context(), level, "request failed",
		"error", err, "request_id", id, "path", r.URL.Path, "method", r.Method)

	s.writeError(w, r, http.StatusInternalServerError, apiError{
		Code:        "internal_error",
		Message:     "Something went wrong inside Homebase.",
		Recoverable: true,
		Recovery:    "Try again. If it keeps happening, export a diagnostic bundle.",
	})
}

func (s *Server) writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrSessionExpired):
		s.writeError(w, r, http.StatusUnauthorized, apiError{
			Code:        "auth.session_expired",
			Message:     "You have been signed out.",
			Recoverable: true,
			Recovery:    "Sign in again.",
		})
	case errors.Is(err, auth.ErrInvalidCredential):
		s.writeError(w, r, http.StatusUnauthorized, apiError{
			Code:        "auth.not_authenticated",
			Message:     "Please sign in.",
			Recoverable: true,
			Recovery:    "Sign in and try again.",
		})
	default:
		s.writeInternal(w, r, err)
	}
}

// writeHostError passes a failure from hostd through unchanged.
//
// The code and the user-facing message survive the journey. A disk that is not
// connected should say so to the person who unplugged it, rather than becoming
// "internal error" because it crossed a process boundary on the way out.
func (s *Server) writeHostError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, hostclient.ErrUnavailable) {
		s.writeError(w, r, http.StatusServiceUnavailable, apiError{
			Code:        "hostd.unavailable",
			Message:     "Homebase cannot reach the part of itself that manages this server.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "This usually clears in a moment while the server starts up. If it persists, restart the server.",
		})
		return
	}

	var hostErr *hostclient.Error
	if errors.As(err, &hostErr) {
		status := hostErr.Status
		if status == 0 || status < 400 {
			status = http.StatusInternalServerError
		}
		s.writeError(w, r, status, apiError{
			Code:        hostErr.Code,
			Message:     hostErr.Message,
			Detail:      hostErr.Detail,
			Recoverable: hostErr.Recoverable,
			Recovery:    hostErr.Recovery,
		})
		return
	}

	s.writeInternal(w, r, err)
}

// hostErrorToJobError converts a hostd failure for storage on a job, keeping
// the code and the explanation the user can act on.
func hostErrorToJobError(err error) error {
	if errors.Is(err, hostclient.ErrUnavailable) {
		return &jobs.Error{
			Code:        "hostd.unavailable",
			Message:     "Homebase could not reach the part of itself that manages this server.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Try again in a moment.",
		}
	}

	var hostErr *hostclient.Error
	if errors.As(err, &hostErr) {
		return &jobs.Error{
			Code:        hostErr.Code,
			Message:     hostErr.Message,
			Detail:      hostErr.Detail,
			Recoverable: hostErr.Recoverable,
			Recovery:    hostErr.Recovery,
		}
	}
	return err
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func newRequestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "req_unknown"
	}
	return "req_" + hex.EncodeToString(b)
}
