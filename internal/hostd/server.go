package hostd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// opPrefix is the only routable path. Everything else is a 404.
const opPrefix = "/v1/op/"

// maxRequestBytes caps a request body. Operation parameters are small — a
// hostname, a device name, a reason string — so anything approaching this is
// either a bug or an attempt to exhaust memory in a root process.
const maxRequestBytes = 1 << 20 // 1 MiB

type peerKey struct{}

// Peer is the kernel's view of who is connected, obtained via SO_PEERCRED.
// It cannot be forged by the caller, which is the entire reason it is used
// instead of anything in the request.
type Peer struct {
	UID uint32
	GID uint32
	PID int32
}

// Server exposes the registry over a Unix socket.
type Server struct {
	registry *Registry
	auditor  *Auditor
	log      *slog.Logger

	// allowedUID is the uid of the unprivileged core service. Socket
	// permissions (0660 root:homebase) are the primary control; this is the
	// second, because the socket mode lives in packaging and a packaging change
	// could widen it without touching any Go code.
	allowedUID uint32

	// allowAnyPeer disables the uid check. Tests only — there is no
	// configuration path that reaches it.
	allowAnyPeer bool
}

func NewServer(registry *Registry, auditor *Auditor, log *slog.Logger, allowedUID uint32) *Server {
	return &Server{
		registry:   registry,
		auditor:    auditor,
		log:        log,
		allowedUID: allowedUID,
	}
}

// Listen creates the HTTP server bound to a Unix listener.
func (s *Server) HTTPServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/operations", s.handleDescribe)
	mux.HandleFunc("/v1/health", s.handleHealth)
	mux.HandleFunc(opPrefix, s.handleOperation)

	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          nil,

		// Capture the peer's credentials as the connection is accepted. This is
		// the only point at which the underlying net.Conn is available.
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			peer, err := peerCredentials(c)
			if err != nil {
				s.log.Warn("could not read peer credentials", "error", err)
				return ctx
			}
			return context.WithValue(ctx, peerKey{}, peer)
		},
	}
}

// peerCredentials asks the kernel who is on the other end of a Unix socket.
func peerCredentials(c net.Conn) (Peer, error) {
	unixConn, ok := c.(*net.UnixConn)
	if !ok {
		return Peer{}, fmt.Errorf("connection is not a unix socket")
	}

	raw, err := unixConn.SyscallConn()
	if err != nil {
		return Peer{}, err
	}

	var cred *syscall.Ucred
	var credErr error
	err = raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil {
		return Peer{}, err
	}
	if credErr != nil {
		return Peer{}, credErr
	}

	return Peer{UID: cred.Uid, GID: cred.Gid, PID: cred.Pid}, nil
}

func peerFrom(ctx context.Context) (Peer, bool) {
	peer, ok := ctx.Value(peerKey{}).(Peer)
	return peer, ok
}

func requestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "req_unknown"
	}
	return "req_" + hex.EncodeToString(b)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"operations": len(s.registry.Names()),
	})
}

// handleDescribe returns the machine-readable operation list.
//
// Read-only and harmless in itself, but it does map the privileged surface — so
// it is behind the same peer check as everything else.
func (s *Server) handleDescribe(w http.ResponseWriter, r *http.Request) {
	if err := s.checkPeer(r.Context()); err != nil {
		s.writeError(w, requestID(), err)
		return
	}
	body, err := s.registry.Describe()
	if err != nil {
		s.writeError(w, requestID(), internalError(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) checkPeer(ctx context.Context) *Error {
	if s.allowAnyPeer {
		return nil
	}
	peer, ok := peerFrom(ctx)
	if !ok {
		return ErrPeerRejected
	}
	// root is allowed so that recovery tooling can talk to hostd directly when
	// core will not start — which is exactly the situation where it is needed.
	if peer.UID != s.allowedUID && peer.UID != 0 {
		return ErrPeerRejected
	}
	return nil
}

// handleOperation is the whole privileged surface.
//
// Order matters and is deliberate: identify the caller by the peer's user id,
// resolve the operation against the compiled-in registry, check the request's
// shape, check confirmation, audit the attempt, and only then run anything.
//
// Note what is *not* in that list. The signed-in person's permissions are not
// checked here and cannot be: hostd is never told who they are, and a core that
// claimed to know would be a core that could claim anything. The permission a
// an operation declares is metadata for core to enforce and for the catalogue to
// publish. This comment used to say "check permission", and so did
// docs/security/privilege-boundaries.md — a description of two independent
// gates where there is one. What hostd guarantees is that only core can ask,
// that only compiled-in operations exist to be asked for, and that every
// attempt is recorded.
func (s *Server) handleOperation(w http.ResponseWriter, r *http.Request) {
	reqID := requestID()

	if r.Method != http.MethodPost {
		s.writeError(w, reqID, &Error{
			Code:        "hostd.method_not_allowed",
			Message:     "Operations must be requested with POST.",
			Recoverable: false,
			Status:      http.StatusMethodNotAllowed,
		})
		return
	}

	if err := s.checkPeer(r.Context()); err != nil {
		peer, _ := peerFrom(r.Context())
		s.log.Warn("rejected peer", "uid", peer.UID, "pid", peer.PID, "request_id", reqID)
		s.writeError(w, reqID, err)
		return
	}
	peer, _ := peerFrom(r.Context())

	name := strings.TrimPrefix(r.URL.Path, opPrefix)
	op, found := s.registry.Lookup(name)
	if !found {
		// Audited: an attempt to invoke something that does not exist is
		// exactly what you want a record of.
		_ = s.auditor.Write(AuditEvent{
			RequestID: reqID,
			Operation: name,
			PeerUID:   peer.UID,
			PeerPID:   peer.PID,
			Phase:     "result",
			Outcome:   "rejected",
			ErrorCode: ErrUnknownOperation.Code,
		})
		s.writeError(w, reqID, ErrUnknownOperation)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		s.writeError(w, reqID, &Error{
			Code:        "request.body_too_large",
			Message:     "The request was too large.",
			Recoverable: false,
			Status:      http.StatusRequestEntityTooLarge,
		})
		return
	}

	if op.Confirm == ConfirmRequired || op.Confirm == ConfirmExplicit {
		if r.Header.Get("X-Homebase-Confirmed") != "true" {
			_ = s.auditor.Write(AuditEvent{
				RequestID: reqID,
				Operation: op.Name,
				Risk:      op.Risk,
				PeerUID:   peer.UID,
				PeerPID:   peer.PID,
				Phase:     "result",
				Outcome:   "rejected",
				ErrorCode: ErrConfirmationRequired.Code,
				Params:    redactSecrets(body, op.Secret),
			})
			s.writeError(w, reqID, ErrConfirmationRequired)
			return
		}
	}

	// Written before the operation runs. If this fails we do not proceed: an
	// unauditable privileged action is not one we are willing to perform.
	attempt := AuditEvent{
		RequestID: reqID,
		Operation: op.Name,
		Risk:      op.Risk,
		PeerUID:   peer.UID,
		PeerPID:   peer.PID,
		Phase:     "attempt",
		Params:    redactSecrets(body, op.Secret),
	}
	if err := s.auditor.Write(attempt); err != nil {
		s.log.Error("audit write failed; refusing to proceed", "error", err, "request_id", reqID)
		s.writeError(w, reqID, internalError("the operation was not attempted because it could not be audited"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), op.Timeout)
	defer cancel()

	started := time.Now()
	result, opErr := op.Handler(ctx, json.RawMessage(body))
	elapsed := time.Since(started)

	outcome := "succeeded"
	errCode := ""
	if opErr != nil {
		outcome = "failed"
		var e *Error
		if errors.As(opErr, &e) {
			errCode = e.Code
		} else {
			errCode = "hostd.internal_error"
		}
	}

	_ = s.auditor.Write(AuditEvent{
		RequestID:  reqID,
		Operation:  op.Name,
		Risk:       op.Risk,
		PeerUID:    peer.UID,
		PeerPID:    peer.PID,
		Phase:      "result",
		Outcome:    outcome,
		ErrorCode:  errCode,
		DurationMS: float64(elapsed.Microseconds()) / 1000,
	})

	if opErr != nil {
		var e *Error
		if errors.As(opErr, &e) {
			s.writeError(w, reqID, e)
			return
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			s.writeError(w, reqID, ErrTimeout)
			return
		}
		s.log.Error("operation failed", "operation", op.Name, "error", opErr, "request_id", reqID)
		s.writeError(w, reqID, internalError(opErr.Error()))
		return
	}

	w.Header().Set("X-Request-Id", reqID)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) writeError(w http.ResponseWriter, reqID string, e *Error) {
	status := e.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}

	// Copy so that adding the request id does not mutate a shared sentinel.
	out := *e
	w.Header().Set("X-Request-Id", reqID)
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":        out.Code,
			"message":     out.Message,
			"detail":      out.Detail,
			"recoverable": out.Recoverable,
			"recovery":    out.Recovery,
			"request_id":  reqID,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
