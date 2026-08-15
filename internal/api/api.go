// Package api implements the core HTTP API.
//
// This is Homebase's only supported interface. The dashboard uses it, the CLI
// uses it, and from Stage 2 the local AI operator will use it. Nothing bypasses
// it, and nothing here holds privilege of its own — every privileged action
// travels to hostd as a named, typed, audited operation.
//
// Conventions are in docs/architecture/api-conventions.md. The two that shape
// most of this file: every mutating endpoint returns 202 with a job, and the
// error `code` is the contract while the `message` is not.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
	"github.com/HusnuOkanCakir/homebase/internal/hostclient"
	"github.com/HusnuOkanCakir/homebase/internal/jobs"
)

const SessionCookie = "homebase_session"

type Server struct {
	auth   *auth.Service
	jobs   *jobs.Manager
	host   *hostclient.Client
	events *events.Recorder
	// lastBackup remembers how the most recent backup to each disk went, so the
	// dashboard can say "your last backup failed" without anybody going looking
	// through the job history.
	lastBackup *lastBackupState
	// authLimit rations the unauthenticated endpoints that verify a password
	// hash, which is the only expensive thing on this server anybody can reach
	// without credentials.
	authLimit *rateLimiter
	log       *slog.Logger
	version   string
	started   time.Time
	static    http.Handler

	// thermalPath overrides where the temperature record is read from, so tests
	// do not need a directory only root can create.
	thermalPath string
}

// WithThermalLog points the history endpoint at a particular file.
func (s *Server) WithThermalLog(path string) *Server {
	s.thermalPath = path
	return s
}

func NewServer(a *auth.Service, j *jobs.Manager, h *hostclient.Client, e *events.Recorder, log *slog.Logger, version string) *Server {
	return &Server{
		auth: a, jobs: j, host: h, events: e,
		lastBackup: newLastBackupState(),
		authLimit:  newRateLimiter(authBurst, authRefill),
		log:        log, version: version, started: time.Now(),
	}
}

// SetStatic makes core serve the built dashboard alongside the API.
//
// Optional: core runs perfectly well without it, which is how it behaves
// during development when the dashboard is served by Vite instead. A missing
// dashboard is not a reason for the API to refuse to start.
func (s *Server) SetStatic(h http.Handler) { s.static = h }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated. /health so a machine that cannot authenticate anyone can
	// still be diagnosed; /setup so the first administrator can be created.
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/setup", s.handleSetupStatus)

	// Rationed, because each of these verifies an argon2id hash and none of
	// them needs a credential to reach. See ratelimit.go.
	mux.HandleFunc("POST /api/v1/setup", s.limited(s.handleSetup))
	mux.HandleFunc("POST /api/v1/auth/login", s.limited(s.handleLogin))
	mux.HandleFunc("POST /api/v1/auth/recover", s.limited(s.handleRecover))

	// Authenticated.
	mux.Handle("POST /api/v1/auth/logout", s.authenticated(s.handleLogout))
	mux.Handle("GET /api/v1/auth/me", s.authenticated(s.handleMe))
	mux.Handle("GET /api/v1/auth/recovery-code", s.authenticated(s.handleRecoveryStatus))
	mux.Handle("POST /api/v1/auth/recovery-code", s.authenticated(s.handleReissueRecoveryCode))

	mux.Handle("GET /api/v1/system", s.require(auth.PermSystemRead, s.handleSystem))
	mux.Handle("GET /api/v1/system/history", s.require(auth.PermSystemRead, s.handleSystemHistory))
	mux.Handle("POST /api/v1/system/reboot", s.require(auth.PermSystemManage, s.handleReboot))
	mux.Handle("POST /api/v1/system/name", s.require(auth.PermSystemManage, s.handleRename))

	mux.Handle("GET /api/v1/jobs", s.require(auth.PermSystemRead, s.handleListJobs))
	mux.Handle("GET /api/v1/jobs/{id}", s.require(auth.PermSystemRead, s.handleGetJob))
	mux.Handle("POST /api/v1/jobs/{id}/cancel", s.require(auth.PermSystemManage, s.handleCancelJob))

	s.registerAppRoutes(mux)
	s.registerStorageRoutes(mux)
	s.registerBackupRoutes(mux)
	s.registerNetworkRoutes(mux)
	s.registerShareRoutes(mux)
	s.registerUpdateRoutes(mux)
	s.registerRecoveryToolRoutes(mux)
	s.registerEventRoutes(mux)

	// The dashboard, when it is present. Registered last and at the root, so
	// every API route above takes precedence.
	if s.static != nil {
		mux.Handle("/", s.static)
	}

	return s.withRequestID(s.withSecurityHeaders(mux))
}

// --- Middleware --------------------------------------------------------------

type contextKey string

const (
	userKey      contextKey = "user"
	requestIDKey contextKey = "request_id"
)

func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// The dashboard is served from the same origin and loads nothing
		// external. A home server must work with the internet unplugged, so a
		// policy that forbids external sources costs nothing and removes a
		// class of injection.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'")

		// API responses are never cacheable. They describe the machine right
		// now, and a client polling a cached endpoint would watch a value that
		// stopped changing — which looks identical to a value that is not
		// changing. Static assets are handled separately and are cached hard.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}

		next.ServeHTTP(w, r)
	})
}

// authenticated rejects anyone without a valid session.
func (s *Server) authenticated(next func(http.ResponseWriter, *http.Request, *auth.User)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := s.userFrom(r)
		if err != nil {
			s.writeAuthError(w, r, err)
			return
		}
		next(w, r, user)
	})
}

// require additionally checks a permission.
//
// Authorisation happens here, before anything reaches hostd. hostd checks again
// on its own account — that is defence in depth, not a shared responsibility.
func (s *Server) require(permission string, next func(http.ResponseWriter, *http.Request, *auth.User)) http.Handler {
	return s.authenticated(func(w http.ResponseWriter, r *http.Request, user *auth.User) {
		if !user.Can(permission) {
			s.writeError(w, r, http.StatusForbidden, apiError{
				Code:        "auth.permission_denied",
				Message:     "You do not have permission to do that.",
				Detail:      "requires " + permission,
				Recoverable: false,
			})
			return
		}
		next(w, r, user)
	})
}

func (s *Server) userFrom(r *http.Request) (*auth.User, error) {
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		return s.auth.UserForSession(r.Context(), strings.TrimPrefix(header, "Bearer "))
	}
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil, auth.ErrInvalidCredential
	}
	return s.auth.UserForSession(r.Context(), cookie.Value)
}

// --- Handlers ----------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	hostdUp := s.host.Healthy(ctx)
	status := "ok"
	code := http.StatusOK
	if !hostdUp {
		// Running but not ready. Saying "ok" here would mean the dashboard shows
		// a healthy server while every operation fails.
		status = "degraded"
		code = http.StatusServiceUnavailable
	}

	writeJSON(w, code, map[string]any{
		"status":          status,
		"version":         s.version,
		"hostd_reachable": hostdUp,
		"uptime_seconds":  int64(time.Since(s.started).Seconds()),
	})
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	needs, err := s.auth.NeedsSetup(r.Context())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"needs_setup": needs})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	user, err := s.auth.CreateAdministrator(r.Context(), body.Username, body.Password)
	switch {
	case errors.Is(err, auth.ErrAlreadySetUp):
		s.writeError(w, r, http.StatusConflict, apiError{
			Code:        "setup.already_complete",
			Message:     "This server already has an administrator.",
			Recoverable: false,
		})
		return
	case errors.Is(err, auth.ErrWeakPassword):
		s.writeError(w, r, http.StatusUnprocessableEntity, apiError{
			Code:    "setup.password_too_short",
			Message: "Please choose a longer password.",
			Detail: "at least " + strconv.Itoa(auth.MinPasswordLen) +
				" characters",
			Recoverable: true,
			Recovery:    "Choose a password of at least " + strconv.Itoa(auth.MinPasswordLen) + " characters.",
		})
		return
	case err != nil:
		s.writeInternal(w, r, err)
		return
	}

	// The recovery code is created here and shown exactly once, because this is
	// the only moment we can be sure somebody is watching. A server whose first
	// screen does not produce one is a server that will be lost the first time
	// its owner forgets a password — which is what happened for five milestones.
	code, err := s.auth.IssueRecoveryCode(r.Context(), user.ID)
	if err != nil {
		// Setup itself succeeded, and refusing to sign the user in now would
		// leave a claimed server nobody can enter. Report the account, say the
		// code is missing, and let the security screen offer another.
		s.log.Error("could not issue the first recovery code", "error", err)
		s.issueSession(w, r, user, http.StatusCreated)
		return
	}

	s.issueSessionWithExtra(w, r, user, http.StatusCreated, map[string]any{
		"recovery_code": code,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	user, err := s.auth.Authenticate(r.Context(), body.Username, body.Password)
	if err != nil {
		// One message for both "no such user" and "wrong password". Telling them
		// apart is a username oracle.
		s.writeError(w, r, http.StatusUnauthorized, apiError{
			Code:        "auth.invalid_credentials",
			Message:     "That username or password is not right.",
			Recoverable: true,
			Recovery:    "Check them and try again.",
		})
		return
	}

	s.issueSession(w, r, user, http.StatusOK)
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, user *auth.User, status int) {
	s.issueSessionWithExtra(w, r, user, status, nil)
}

// issueSessionWithExtra signs the user in and returns additional fields
// alongside the account — the recovery code, on the two occasions one is shown.
func (s *Server) issueSessionWithExtra(w http.ResponseWriter, r *http.Request, user *auth.User, status int, extra map[string]any) {
	token, expires, err := s.auth.CreateSession(r.Context(), user.ID, r.UserAgent())
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		// Secure follows the connection this request actually arrived on.
		//
		// It used to be hard-coded true, on the reasoning that a cookie which
		// works without TLS is one that keeps working after somebody removes
		// the TLS by accident. That reasoning was sound and the consequence was
		// not: browsers refuse a Secure cookie from a non-secure origin, so on
		// a real installation reached at http://192.168.1.50:8080 the browser
		// silently discarded the session and `/auth/me` answered 401 straight
		// after a correct password. Every test missed it, because they all
		// reach the server over loopback — the one origin browsers exempt.
		//
		// Set from the connection, this is both correct and self-enforcing:
		// over TLS the flag is on, and over plain HTTP the cookie works rather
		// than the product not working at all.
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
	})

	payload := map[string]any{
		"user":       user,
		"expires_at": expires.Format(time.RFC3339),
	}
	for key, value := range extra {
		payload[key] = value
	}
	writeJSON(w, status, payload)
}

// isSecureRequest reports whether this request arrived over TLS.
//
// Read from the connection rather than from a header. `X-Forwarded-Proto` is
// client-supplied, and a request that can claim to be secure is one an attacker
// uses to have a Secure cookie issued over plain HTTP — which the browser would
// then refuse to send back, locking the real user out. Homebase is reached
// directly on the local network, so there is no proxy whose word would be worth
// trusting anyway.
func isSecureRequest(r *http.Request) bool {
	return r.TLS != nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	if cookie, err := r.Cookie(SessionCookie); err == nil {
		_ = s.auth.DeleteSession(r.Context(), cookie.Value)
	}
	// The clearing cookie has to match the one being cleared: a browser will
	// not replace a non-Secure cookie with a Secure one, so signing out over
	// plain HTTP would leave the old cookie in place.
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: isSecureRequest(r), SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, user *auth.User) {
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Read live from hostd on every request. A remembered disk-usage figure is a
	// wrong disk-usage figure, and the Stage 2 operator must never reason about
	// the machine from stale values.
	info, err := s.host.SystemInfo(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	resources, err := s.host.SystemResources(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"hostname":       info.Hostname,
		"os":             info.OS,
		"kernel":         info.Kernel,
		"architecture":   info.Architecture,
		"virtualised":    info.Virtualised,
		"uptime_seconds": resources.UptimeSeconds,
		"cpu": map[string]any{
			"model":   info.CPU.Model,
			"cores":   info.CPU.Cores,
			"threads": info.CPU.Threads,
		},
		"memory":       resources.Memory,
		"load_average": resources.LoadAverage,
		"power":        resources.Power,
		"temperature":  resources.Temperature,
		// Beside the temperature and never without it. A fan speed on its own
		// says nothing: loud and cool is a fan fault, loud and hot is a
		// heatsink full of dust, and they sound the same from across a room.
		"fan": resources.Fan,
	})
}

// handleSystemHistory returns how hot the machine has been.
//
// Read from the file rather than from a table, because the file is the record —
// see internal/api/thermallog.go. It means this endpoint keeps working when the
// database is being restored, which is one of the times somebody most wants to
// know what the machine was doing.
func (s *Server) handleSystemHistory(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	// Days rather than an arbitrary count, because the question is always about
	// a period: "the last week" is answerable and "the last two hundred
	// readings" is not.
	days := 7
	if raw := r.URL.Query().Get("days"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}

	// A cap on points rather than on time. A month at five-minute intervals is
	// nine thousand readings, which is more than any chart can draw and more
	// than any browser should be asked to parse — and thinning keeps the shape,
	// which is the whole of what a chart is for.
	points := 500
	if raw := r.URL.Query().Get("points"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 5000 {
			points = parsed
		}
	}

	history := readThermalHistory(s.thermalLogPath(), time.Duration(days)*24*time.Hour, points)
	writeJSON(w, http.StatusOK, history)
}

// thermalLogPath is where the record is, overridable so tests do not need a
// directory only root can create.
func (s *Server) thermalLogPath() string {
	if s.thermalPath != "" {
		return s.thermalPath
	}
	return DefaultThermalLogPath
}

// handleRename changes what the machine calls itself.
//
// Not a job, and deliberately so. Every mutating endpoint in Homebase returns a
// job because a client that has to know which operations are fast is one that
// breaks the first time a "fast" one is not — but this is the exception the
// convention allows for: renaming is three file writes and a syscall, it cannot
// be interrupted usefully, and the first thing somebody does after it is read
// the name back. A job here would be a job whose result is checked immediately
// and never again.
func (s *Server) handleRename(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Name string `json:"name"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	result, err := s.host.Rename(ctx, strings.TrimSpace(body.Name))
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	if result.Previous != result.Name {
		// Worth an event: the name is how the machine is found, and somebody
		// who cannot reach it afterwards should be able to see why.
		s.events.Info(r.Context(), "system.renamed", result.Name,
			"This server was renamed from "+result.Previous+" to "+result.Name+".")
		s.log.Info("server renamed", "from", result.Previous, "to", result.Name,
			"by", user.Username)
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleReboot(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Reason  string `json:"reason,omitempty"`
		Confirm string `json:"confirm"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	// Ask hostd for the hostname rather than trusting the client's idea of it:
	// the confirmation has to name the machine actually being restarted.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	info, err := s.host.SystemInfo(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	if body.Confirm != info.Hostname {
		s.writeError(w, r, http.StatusPreconditionRequired, apiError{
			Code:        "system.confirmation_required",
			Message:     "Please confirm you want to restart this server.",
			Detail:      "confirm must be the server's name",
			Recoverable: true,
			Recovery:    "Confirm the restart, naming this server.",
		})
		return
	}

	job, err := s.jobs.Submit(r.Context(), jobs.Definition{
		Operation: "system.reboot",
		// Nothing can observe this finishing — the connection dies with the
		// machine. The job resolves itself on the next start by comparing the
		// kernel's boot id. See internal/jobs.
		InterruptsHost: true,
		Cancellable:    false,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		CreatedBy:      user.ID,
		Run: func(ctx context.Context, report *jobs.Reporter) error {
			report.Progress("requesting_restart", nil, "Asking the server to restart…")
			if err := s.host.Reboot(ctx, info.Hostname, body.Reason); err != nil {
				return hostErrorToJobError(err)
			}
			report.Progress("restarting", nil, "The server is restarting.")
			// Deliberately does not return success. If this process is still
			// alive in a moment the reboot did not happen, and the next start
			// resolves the job either way.
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}

	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.jobs.List(r.Context(), r.URL.Query().Get("state"), limit)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list, "total": len(list)})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	job, err := s.jobs.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, jobs.ErrNotFound) {
		s.writeError(w, r, http.StatusNotFound, apiError{
			Code: "jobs.not_found", Message: "No such job.", Recoverable: false,
		})
		return
	}
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	err := s.jobs.Cancel(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, apiError{
			Code: "jobs.not_found", Message: "No such job.", Recoverable: false,
		})
	case errors.Is(err, jobs.ErrConflict):
		s.writeError(w, r, http.StatusConflict, apiError{
			Code:        "jobs.conflict",
			Message:     "That job cannot be cancelled.",
			Detail:      err.Error(),
			Recoverable: false,
		})
	case err != nil:
		s.writeInternal(w, r, err)
	default:
		job, _ := s.jobs.Get(r.Context(), r.PathValue("id"))
		writeJSON(w, http.StatusAccepted, job)
	}
}
