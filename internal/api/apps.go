package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
	"github.com/HusnuOkanCakir/homebase/internal/hostclient"
	"github.com/HusnuOkanCakir/homebase/internal/jobs"
)

// Application endpoints.
//
// Everything here sends an application id to hostd and nothing more. core never
// describes a container — see ADR-0012 — so there is no request body on these
// endpoints beyond a confirmation, and no way for a client to influence what
// gets run. A client that wants a different image wants a different manifest,
// which is a package.
//
// The mutating operations return 202 with a job, because installing an
// application means downloading several hundred megabytes on a home connection.
// A request that blocks for four minutes is a request that has already timed out
// somewhere in between.

func (s *Server) registerAppRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/apps", s.require(auth.PermAppsRead, s.handleListApps))
	mux.Handle("GET /api/v1/apps/{id}", s.require(auth.PermAppsRead, s.handleGetApp))
	mux.Handle("GET /api/v1/apps/{id}/logs", s.require(auth.PermAppsRead, s.handleAppLogs))

	mux.Handle("POST /api/v1/apps/{id}/install", s.require(auth.PermAppsManage, s.handleInstallApp))
	mux.Handle("POST /api/v1/apps/{id}/start", s.require(auth.PermAppsManage, s.handleStartApp))
	mux.Handle("POST /api/v1/apps/{id}/stop", s.require(auth.PermAppsManage, s.handleStopApp))
	mux.Handle("POST /api/v1/apps/{id}/restart", s.require(auth.PermAppsManage, s.handleRestartApp))
	mux.Handle("POST /api/v1/apps/{id}/uninstall", s.require(auth.PermAppsManage, s.handleUninstallApp))

	// Separate from uninstall, and it stays separate. Uninstalling is how a user
	// frees space or stops using something; deleting their photographs is a
	// different intention that happens to be implemented nearby.
	mux.Handle("POST /api/v1/apps/{id}/data/remove", s.require(auth.PermAppsManage, s.handleRemoveAppData))
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	list, err := s.host.Apps(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	// Rejected manifests are reported, not hidden. If an application a user
	// expects to see is absent, the reason it is absent is the only useful thing
	// anybody can be told.
	if len(list.Rejected) > 0 {
		s.log.Warn("some application manifests did not load", "count", len(list.Rejected))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":            list.Applications,
		"total":            len(list.Applications),
		"docker_available": list.DockerAvailable,
		"unavailable":      list.Rejected,
	})
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	app, err := s.host.App(ctx, r.PathValue("id"))
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (s *Server) handleAppLogs(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	logs, err := s.host.AppLogs(ctx, r.PathValue("id"), lines)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

// --- Mutating operations ------------------------------------------------------

func (s *Server) handleInstallApp(w http.ResponseWriter, r *http.Request, user *auth.User) {
	if !s.expectNoBody(w, r) {
		return
	}
	app, ok := s.appFor(w, r)
	if !ok {
		return
	}

	s.submitAppJob(w, r, user, appJob{
		operation: "app.install",
		app:       app,
		recorded:  app.Name + " was installed.",
		// Cancelling an install mid-pull would leave a partial image and a
		// half-created container, and hostd has no operation to tidy that up
		// yet. Offering a cancel button that does not work is worse than not
		// offering one.
		cancellable: false,
		event:       "application_installed",
		failedEvent: "application_install_failed",
		stages: []appStage{
			{"downloading", 10, "Downloading " + app.Name + ". This can take several minutes."},
		},
		run: func(ctx context.Context) error { return s.host.InstallApp(ctx, app.ID) },
		done: func(report *jobs.Reporter) {
			report.Progress("running", percent(100), app.Name+" is installed and running.")
		},
	})
}

func (s *Server) handleStartApp(w http.ResponseWriter, r *http.Request, user *auth.User) {
	if !s.expectNoBody(w, r) {
		return
	}
	app, ok := s.appFor(w, r)
	if !ok {
		return
	}
	s.submitAppJob(w, r, user, appJob{
		operation:   "app.start",
		app:         app,
		recorded:    app.Name + " was started.",
		event:       "application_started",
		failedEvent: "application_start_failed",
		stages:      []appStage{{"starting", 50, "Starting " + app.Name + "…"}},
		run:         func(ctx context.Context) error { return s.host.StartApp(ctx, app.ID) },
	})
}

func (s *Server) handleStopApp(w http.ResponseWriter, r *http.Request, user *auth.User) {
	app, ok := s.appFor(w, r)
	if !ok {
		return
	}
	// Stopping takes a service away from whoever is using it, possibly somebody
	// else in the house. hostd requires the confirmation; core is where it is
	// obtained.
	if !s.confirmedByName(w, r, app) {
		return
	}
	s.submitAppJob(w, r, user, appJob{
		operation:   "app.stop",
		app:         app,
		recorded:    app.Name + " was stopped.",
		event:       "application_stopped",
		failedEvent: "application_stop_failed",
		stages:      []appStage{{"stopping", 50, "Stopping " + app.Name + "…"}},
		run:         func(ctx context.Context) error { return s.host.StopApp(ctx, app.ID) },
	})
}

func (s *Server) handleRestartApp(w http.ResponseWriter, r *http.Request, user *auth.User) {
	app, ok := s.appFor(w, r)
	if !ok {
		return
	}
	if !s.confirmedByName(w, r, app) {
		return
	}
	s.submitAppJob(w, r, user, appJob{
		operation:   "app.restart",
		app:         app,
		recorded:    app.Name + " was restarted.",
		event:       "application_restarted",
		failedEvent: "application_restart_failed",
		stages:      []appStage{{"restarting", 50, "Restarting " + app.Name + "…"}},
		run:         func(ctx context.Context) error { return s.host.RestartApp(ctx, app.ID) },
	})
}

func (s *Server) handleUninstallApp(w http.ResponseWriter, r *http.Request, user *auth.User) {
	app, ok := s.appFor(w, r)
	if !ok {
		return
	}
	if !s.confirmedByName(w, r, app) {
		return
	}
	s.submitAppJob(w, r, user, appJob{
		operation:   "app.uninstall",
		app:         app,
		recorded:    app.Name + " was removed. Its data was kept.",
		event:       "application_uninstalled",
		failedEvent: "application_uninstall_failed",
		stages:      []appStage{{"removing", 50, "Removing " + app.Name + "…"}},
		run:         func(ctx context.Context) error { return s.host.UninstallApp(ctx, app.ID) },
		done: func(report *jobs.Reporter) {
			// Said explicitly, because the whole point of keeping the data is
			// that the user knows it was kept.
			report.Progress("removed", percent(100),
				app.Name+" has been removed. Its data is still on the server.")
		},
	})
}

// handleRemoveAppData deletes an application's data. There is no undo.
func (s *Server) handleRemoveAppData(w http.ResponseWriter, r *http.Request, user *auth.User) {
	app, ok := s.appFor(w, r)
	if !ok {
		return
	}

	var body struct {
		Confirm string `json:"confirm"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	// Typing the id, not clicking a second button. The confirmation for an
	// irreversible deletion has to be something a user cannot do by reflex.
	if body.Confirm != app.ID {
		s.writeError(w, r, http.StatusPreconditionRequired, apiError{
			Code:        "apps.confirmation_required",
			Message:     "Please confirm you want to delete " + app.Name + "'s data.",
			Detail:      "confirm must be " + app.ID,
			Recoverable: true,
			Recovery: "Type " + app.ID + " to confirm. This cannot be undone — " +
				"if you want to keep the data, remove the application instead.",
		})
		return
	}

	s.submitAppJob(w, r, user, appJob{
		operation:   "app.remove_data",
		app:         app,
		recorded:    app.Name + "'s data was deleted permanently.",
		event:       "application_data_removed",
		failedEvent: "application_data_removal_failed",
		severity:    events.SeverityWarning,
		stages:      []appStage{{"deleting", 50, "Deleting " + app.Name + "'s data…"}},
		run: func(ctx context.Context) error {
			return s.host.RemoveAppData(ctx, app.ID, body.Confirm)
		},
		done: func(report *jobs.Reporter) {
			report.Progress("deleted", percent(100), app.Name+"'s data has been deleted.")
		},
	})
}

// --- Shared plumbing ----------------------------------------------------------

type appStage struct {
	name     string
	progress int
	message  string
}

// percent exists because a job's progress is a *int: nil means "no idea how far
// along this is", which is a different statement from 0 %.
func percent(n int) *int { return &n }

type appJob struct {
	operation   string
	app         *hostclient.App
	cancellable bool
	event       string
	failedEvent string
	// recorded is what the event says happened, as a sentence. An event is part
	// of the API and somebody reads it in a history list weeks later; "app.stop"
	// is a function name, not an account of what became of their media server.
	recorded string
	severity events.Severity
	stages   []appStage
	run      func(context.Context) error
	done     func(*jobs.Reporter)
}

func (s *Server) submitAppJob(w http.ResponseWriter, r *http.Request, user *auth.User, spec appJob) {
	app := spec.app

	job, err := s.jobs.Submit(r.Context(), jobs.Definition{
		Operation:      spec.operation,
		Cancellable:    spec.cancellable,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		CreatedBy:      user.ID,
		Run: func(ctx context.Context, report *jobs.Reporter) error {
			for _, stage := range spec.stages {
				report.Progress(stage.name, percent(stage.progress), stage.message)
			}

			if err := spec.run(ctx); err != nil {
				severity := spec.severity
				if severity == "" {
					severity = events.SeverityError
				}
				s.recordAppEvent(ctx, spec.failedEvent, severity, app.ID, hostErrorCode(err),
					s.appFailureMessage(app, err))
				return hostErrorToJobError(err)
			}

			if spec.done != nil {
				spec.done(report)
			}

			severity := spec.severity
			if severity == "" {
				severity = events.SeverityInfo
			}
			s.recordAppEvent(ctx, spec.event, severity, app.ID, "", spec.recorded)
			return nil
		},
	})
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}

	writeJSON(w, http.StatusAccepted, job)
}

// appFor resolves the id in the path to an application, so that an unknown one
// fails before a job exists.
//
// A job that starts and immediately fails with "no such application" is a job in
// the user's history and an entry in the audit log for something that was never
// going to happen. A 404 is the honest answer.
func (s *Server) appFor(w http.ResponseWriter, r *http.Request) (*hostclient.App, bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	app, err := s.host.App(ctx, r.PathValue("id"))
	if err != nil {
		s.writeHostError(w, r, err)
		return nil, false
	}
	return app, true
}

// confirmedByName requires the request to name the application it is acting on.
//
// The name rather than a boolean, for the same reason the reboot confirmation is
// the hostname: a `{"confirm": true}` a client can send by default is not a
// confirmation, and a confirmation that names its target cannot be replayed
// against a different application.
func (s *Server) confirmedByName(w http.ResponseWriter, r *http.Request, app *hostclient.App) bool {
	var body struct {
		Confirm string `json:"confirm"`
	}
	if !s.decode(w, r, &body) {
		return false
	}
	if body.Confirm != app.ID {
		s.writeError(w, r, http.StatusPreconditionRequired, apiError{
			Code:        "apps.confirmation_required",
			Message:     "Please confirm you want to do that to " + app.Name + ".",
			Detail:      "confirm must be " + app.ID,
			Recoverable: true,
			Recovery:    "Confirm the action, naming the application.",
		})
		return false
	}
	return true
}

func (s *Server) recordAppEvent(ctx context.Context, eventType string, severity events.Severity, subject, reason, message string) {
	if s.events == nil {
		return
	}
	event := events.Event{
		Type:     eventType,
		Severity: severity,
		Subject:  &subject,
		Message:  &message,
	}
	if reason != "" {
		event.Reason = &reason
	}
	// A detached context: the job's context may already be cancelled by the time
	// a failure is being recorded, and losing the record of why something failed
	// is exactly the wrong thing to lose.
	s.events.Record(context.WithoutCancel(ctx), event)
}

func (s *Server) appFailureMessage(app *hostclient.App, err error) string {
	var hostErr *hostclient.Error
	if errors.As(err, &hostErr) && hostErr.Message != "" {
		return hostErr.Message
	}
	return "Something went wrong with " + app.Name + "."
}

func hostErrorCode(err error) string {
	var hostErr *hostclient.Error
	if errors.As(err, &hostErr) {
		return hostErr.Code
	}
	if errors.Is(err, hostclient.ErrUnavailable) {
		return "hostd.unavailable"
	}
	return "unknown"
}
