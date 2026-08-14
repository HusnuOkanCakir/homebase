package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
	"github.com/HusnuOkanCakir/homebase/internal/hostclient"
	"github.com/HusnuOkanCakir/homebase/internal/jobs"
)

// Backup endpoints.
//
// The milestone's exit condition is about restoring, and the shape of this file
// follows: previewing is a first-class operation with its own endpoint, and
// restoring cannot be reached without an id typed by hand.
//
// One rule from ADR-0014 shows up here rather than in hostd, because it is about
// what the user is told: **a failed backup is loud.** It is recorded as an error
// event and stays visible until one succeeds. The failure this exists for is
// backups stopping, nobody noticing for eight months, and the first anyone knows
// being when they need one.

func (s *Server) registerBackupRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/backups", s.require(auth.PermBackupRead, s.handleListBackups))
	mux.Handle("POST /api/v1/backups", s.require(auth.PermBackupRun, s.handleCreateBackup))

	mux.Handle("GET /api/v1/backups/schedule", s.require(auth.PermBackupRead, s.handleBackupSchedule))
	mux.Handle("POST /api/v1/backups/schedule", s.require(auth.PermBackupRun, s.handleSetBackupSchedule))

	mux.Handle("GET /api/v1/backups/{id}/preview", s.require(auth.PermBackupRead, s.handlePreviewRestore))
	mux.Handle("POST /api/v1/backups/{id}/verify", s.require(auth.PermBackupRead, s.handleVerifyBackup))
	mux.Handle("POST /api/v1/backups/{id}/restore", s.require(auth.PermBackupRun, s.handleRestoreBackup))
	mux.Handle("POST /api/v1/backups/{id}/delete", s.require(auth.PermBackupRun, s.handleDeleteBackup))
}

// location is required on every backup endpoint: a backup lives on a disk, and
// which disk is not something Homebase should guess.
func (s *Server) backupLocation(w http.ResponseWriter, r *http.Request) (string, bool) {
	location := r.URL.Query().Get("location")
	if location == "" {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.missing_field",
			Message:     "Homebase needs to know which disk the backups are on.",
			Detail:      "the location query parameter is required",
			Recoverable: true,
			Recovery:    "Choose a disk.",
		})
		return "", false
	}
	return location, true
}

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	location, ok := s.backupLocation(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	backups, err := s.host.Backups(ctx, location)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": backups, "total": len(backups), "location": location,
	})
}

func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Location    string `json:"location"`
		IncludeData bool   `json:"include_data,omitempty"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Location == "" {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.missing_field",
			Message:     "Homebase needs to know which disk to back up to.",
			Detail:      "location is required",
			Recoverable: true,
			Recovery:    "Choose a disk to keep the backup on.",
		})
		return
	}

	what := "Saving your settings…"
	if body.IncludeData {
		what = "Copying your files. On a large collection this can take a long time."
	}

	s.submitBackupJob(w, r, user, backupJob{
		operation:   "backup.create",
		subject:     body.Location,
		event:       "backup_completed",
		failedEvent: "backup_failed",
		recorded:    "A backup was made.",
		// A failed backup is an error the user has to see, not a warning that
		// scrolls past.
		failureSeverity: events.SeverityError,
		stage:           backupStage{"copying", 20, what},
		// Cancelling mid-copy would leave a directory that looks like a backup
		// and is not. It can be deleted afterwards instead.
		cancellable: false,
		run: func(ctx context.Context) error {
			result, err := s.host.CreateBackup(ctx, body.Location, body.IncludeData)
			if err != nil {
				return err
			}
			s.lastBackup.record(body.Location, result)
			return nil
		},
		done: func(report *jobs.Reporter) {
			report.Progress("done", percent(100), "The backup is finished.")
		},
	})
}

// --- Backups that happen without anybody pressing anything -------------------------

// handleBackupSchedule reports when backups run, and how the last one went.
//
// The last result is on the same response as the schedule on purpose. A
// schedule is a promise made once and kept nightly, and the way it fails is
// silently: the disk is unplugged in March, and nobody finds out until the
// machine dies in November. Anything that shows the promise has to show whether
// it is being kept.
func (s *Server) handleBackupSchedule(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	schedule, err := s.host.BackupSchedule(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, schedule)
}

func (s *Server) handleSetBackupSchedule(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Every    string `json:"every"`
		Location string `json:"location,omitempty"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	every := strings.ToLower(strings.TrimSpace(body.Every))
	if every == "" {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.missing_field",
			Message:     "Homebase needs to know how often to back up.",
			Detail:      "every is required",
			Recoverable: true,
			Recovery:    "Choose every night, every week, or off.",
		})
		return
	}

	// Long enough for hostd to check the destination is a disk that can actually
	// hold a backup, which is a mount and a statfs rather than a guess.
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	schedule, err := s.host.SetBackupSchedule(ctx, every, strings.TrimSpace(body.Location))
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	// Turning backups off is recorded above 'info'. It is one click, it is
	// invisible afterwards, and it is the change somebody will want to find in
	// the history on the day they discover there is nothing to restore.
	if schedule.Every == "off" {
		s.events.Warn(r.Context(), "backup.schedule_disabled", "",
			"scheduled backups were turned off",
			"Backups will now only happen when you ask for one.")
	} else {
		s.recordAppEvent(r.Context(), "backup.schedule_changed", events.SeverityInfo,
			schedule.Location, "", "Backups will now run "+schedule.Description+".")
	}
	s.log.Info("backup schedule changed", "every", schedule.Every,
		"location", schedule.Location, "enabled", schedule.Enabled, "by", user.Username)

	writeJSON(w, http.StatusOK, schedule)
}

func (s *Server) handleVerifyBackup(w http.ResponseWriter, r *http.Request, user *auth.User) {
	if !s.expectNoBody(w, r) {
		return
	}
	location, ok := s.backupLocation(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")

	s.submitBackupJob(w, r, user, backupJob{
		operation:   "backup.verify",
		subject:     id,
		event:       "backup_verified",
		failedEvent: "backup_verification_failed",
		recorded:    "A backup was checked and everything in it is intact.",
		// A backup that fails verification is the thing this whole milestone
		// exists to catch before somebody needs it.
		failureSeverity: events.SeverityError,
		stage:           backupStage{"checking", 20, "Reading every file and checking it…"},
		run: func(ctx context.Context) error {
			result, err := s.host.VerifyBackup(ctx, location, id)
			if err != nil {
				return err
			}
			if !result.Valid {
				// A damaged backup is a failed job, not a successful one with a
				// footnote. Somebody skimming the job list must not read it as
				// fine.
				return &jobs.Error{
					Code:        "backup.damaged",
					Message:     result.Message,
					Detail:      damageDetail(result),
					Recoverable: false,
					Recovery: "Make a new backup. If this keeps happening, the disk " +
						"is probably failing and should be replaced.",
				}
			}
			return nil
		},
		done: func(report *jobs.Reporter) {
			report.Progress("checked", percent(100),
				"Every file in this backup is present and unchanged.")
		},
	})
}

func damageDetail(result *hostclient.VerificationResult) string {
	detail := ""
	if len(result.Missing) > 0 {
		detail += "missing: " + joinUpTo(result.Missing, 5)
	}
	if len(result.Corrupt) > 0 {
		if detail != "" {
			detail += "; "
		}
		detail += "damaged: " + joinUpTo(result.Corrupt, 5)
	}
	return detail
}

func joinUpTo(items []string, limit int) string {
	if len(items) <= limit {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:limit], ", ") +
		", and " + strconv.Itoa(len(items)-limit) + " more"
}

// --- Preview and restore ---------------------------------------------------------

// handlePreviewRestore answers "what would this do?" and changes nothing.
//
// Its own endpoint rather than a flag on restore, because a preview a client can
// forget to ask for is a preview nobody sees. Restoring is the operation with
// the highest ratio of irreversible to understood: the user is anxious, often in
// a hurry, and may be about to overwrite something they still needed.
func (s *Server) handlePreviewRestore(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	location, ok := s.backupLocation(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	preview, err := s.host.PreviewRestore(ctx, location, r.PathValue("id"))
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request, user *auth.User) {
	location, ok := s.backupLocation(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")

	var body struct {
		Confirm string `json:"confirm"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	// The backup's own id, typed. Not a word like "yes": this overwrites what
	// somebody is usually trying to save.
	if body.Confirm != id {
		s.writeError(w, r, http.StatusPreconditionRequired, apiError{
			Code:        "backup.confirmation_required",
			Message:     "Please confirm which backup you want to restore.",
			Detail:      "confirm must be " + id,
			Recoverable: true,
			Recovery: "Restoring replaces files on this server with the ones in the " +
				"backup. Anything added since is left alone. Confirm by naming " +
				"the backup exactly.",
		})
		return
	}

	s.submitBackupJob(w, r, user, backupJob{
		operation:   "backup.restore",
		subject:     id,
		event:       "backup_restored",
		failedEvent: "backup_restore_failed",
		recorded:    "A backup was restored onto this server.",
		// Restoring is recorded above 'info' even when it works: it is a large,
		// irreversible change, and somebody reading the history months later
		// needs to find it.
		severity:        events.SeverityWarning,
		failureSeverity: events.SeverityCritical,
		stage:           backupStage{"restoring", 20, "Putting your files back…"},
		cancellable:     false,
		run: func(ctx context.Context) error {
			_, err := s.host.RestoreBackup(ctx, location, id, body.Confirm)
			return err
		},
		done: func(report *jobs.Reporter) {
			report.Progress("restored", percent(100),
				"Everything in the backup has been put back. Nothing that was "+
					"already on this server was deleted.")
		},
	})
}

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request, user *auth.User) {
	location, ok := s.backupLocation(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")

	if !s.confirmedByName(w, r, id, "that backup") {
		return
	}

	s.submitBackupJob(w, r, user, backupJob{
		operation:   "backup.delete",
		subject:     id,
		event:       "backup_deleted",
		failedEvent: "backup_delete_failed",
		recorded:    "A backup was deleted.",
		severity:    events.SeverityWarning,
		stage:       backupStage{"deleting", 50, "Deleting that backup…"},
		run: func(ctx context.Context) error {
			return s.host.DeleteBackup(ctx, location, id, id)
		},
		done: func(report *jobs.Reporter) {
			report.Progress("deleted", percent(100),
				"That backup has been deleted. Your other backups are untouched.")
		},
	})
}

// --- Remembering how the last backup went ------------------------------------------

// lastBackupState records the most recent outcome per destination, so the
// dashboard can say "your last backup failed" rather than requiring somebody to
// go looking through the job history.
type lastBackupState struct {
	mu      sync.Mutex
	byPlace map[string]*hostclient.CreateBackupResult
}

func newLastBackupState() *lastBackupState {
	return &lastBackupState{byPlace: map[string]*hostclient.CreateBackupResult{}}
}

func (l *lastBackupState) record(location string, result *hostclient.CreateBackupResult) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byPlace[location] = result
}

// --- Shared plumbing -----------------------------------------------------------------

type backupStage struct {
	name     string
	progress int
	message  string
}

type backupJob struct {
	operation   string
	subject     string
	event       string
	failedEvent string
	recorded    string
	severity    events.Severity
	// failureSeverity is separate because a failed backup matters more than a
	// successful one is worth mentioning.
	failureSeverity events.Severity
	stage           backupStage
	cancellable     bool
	run             func(context.Context) error
	done            func(*jobs.Reporter)
}

func (s *Server) submitBackupJob(w http.ResponseWriter, r *http.Request, user *auth.User, spec backupJob) {
	job, err := s.jobs.Submit(r.Context(), jobs.Definition{
		Operation:      spec.operation,
		Cancellable:    spec.cancellable,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		CreatedBy:      user.ID,
		Run: func(ctx context.Context, report *jobs.Reporter) error {
			report.Progress(spec.stage.name, percent(spec.stage.progress), spec.stage.message)

			if err := spec.run(ctx); err != nil {
				severity := spec.failureSeverity
				if severity == "" {
					severity = events.SeverityError
				}
				s.recordAppEvent(ctx, spec.failedEvent, severity, spec.subject,
					hostErrorCode(err), storageFailureMessage(err))
				return hostErrorToJobError(err)
			}

			if spec.done != nil {
				spec.done(report)
			}

			severity := spec.severity
			if severity == "" {
				severity = events.SeverityInfo
			}
			s.recordAppEvent(ctx, spec.event, severity, spec.subject, "", spec.recorded)
			return nil
		},
	})
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}

	writeJSON(w, http.StatusAccepted, job)
}
