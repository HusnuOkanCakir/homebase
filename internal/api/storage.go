package api

import (
	"context"
	"net/http"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
	"github.com/HusnuOkanCakir/homebase/internal/hostclient"
	"github.com/HusnuOkanCakir/homebase/internal/jobs"
)

// Storage endpoints.
//
// The rule from ADR-0013 shows up here as an absence: no endpoint accepts a
// device path or a mount point. A client names a disk by filesystem UUID or a
// location by its id, and hostd resolves it. `/dev/sdb` is not a stable name for
// anything, and an API that accepted one would be inviting a client to act on
// whichever disk happens to hold that name today.
//
// The confirmations are stricter here than anywhere else in Homebase. Formatting
// is the only operation that destroys data Homebase never created — somebody's
// photographs, put there before Homebase existed — and it is the one place where
// getting a confirmation wrong cannot be walked back.

func (s *Server) registerStorageRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/storage/disks", s.require(auth.PermStorageRead, s.handleListDisks))
	mux.Handle("GET /api/v1/storage/locations", s.require(auth.PermStorageRead, s.handleListLocations))

	mux.Handle("POST /api/v1/storage/locations", s.require(auth.PermStorageModify, s.handleAddLocation))
	mux.Handle("POST /api/v1/storage/locations/{id}/remove", s.require(auth.PermStorageModify, s.handleRemoveLocation))
	mux.Handle("POST /api/v1/storage/locations/{id}/mount", s.require(auth.PermStorageModify, s.handleMountLocation))
	mux.Handle("POST /api/v1/storage/locations/{id}/unmount", s.require(auth.PermStorageModify, s.handleUnmountLocation))

	// Deliberately not under /locations: this acts on a disk that is not a
	// location yet, and usually cannot become one until it has been done.
	mux.Handle("POST /api/v1/storage/format", s.require(auth.PermStorageModify, s.handleFormatDisk))

	mux.Handle("GET /api/v1/apps/{id}/storage", s.require(auth.PermAppsRead, s.handleAppStorage))
	mux.Handle("POST /api/v1/apps/{id}/storage", s.require(auth.PermAppsManage, s.handleAssignStorage))
}

func (s *Server) handleListDisks(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	disks, err := s.host.Disks(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": disks, "total": len(disks)})
}

func (s *Server) handleListLocations(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	locations, err := s.host.Locations(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": locations, "total": len(locations)})
}

// --- Adding and removing ------------------------------------------------------

func (s *Server) handleAddLocation(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		UUID string `json:"uuid"`
		ID   string `json:"id"`
		Name string `json:"name,omitempty"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	if body.UUID == "" || body.ID == "" {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.missing_field",
			Message:     "Homebase needs to know which disk to set up, and what to call it.",
			Detail:      "uuid and id are both required",
			Recoverable: true,
			Recovery:    "Choose a disk and give it a name.",
		})
		return
	}

	name := body.Name
	if name == "" {
		name = body.ID
	}

	s.submitStorageJob(w, r, user, storageJob{
		operation:   "storage.add_location",
		subject:     body.ID,
		event:       "storage_location_added",
		failedEvent: "storage_location_add_failed",
		recorded:    name + " was set up as a storage location.",
		stage:       storageStage{"preparing", 40, "Setting up " + name + "…"},
		run: func(ctx context.Context) error {
			return s.host.AddLocation(ctx, body.UUID, body.ID, body.Name)
		},
		done: func(report *jobs.Reporter) {
			report.Progress("ready", percent(100), name+" is ready to use.")
		},
	})
}

func (s *Server) handleRemoveLocation(w http.ResponseWriter, r *http.Request, user *auth.User) {
	location, ok := s.locationFor(w, r)
	if !ok {
		return
	}
	if !s.confirmedByName(w, r, location.ID, location.Name) {
		return
	}

	s.submitStorageJob(w, r, user, storageJob{
		operation:   "storage.remove_location",
		subject:     location.ID,
		event:       "storage_location_removed",
		failedEvent: "storage_location_remove_failed",
		recorded:    location.Name + " is no longer managed. Nothing on it was changed.",
		stage:       storageStage{"removing", 50, "Disconnecting " + location.Name + "…"},
		run:         func(ctx context.Context) error { return s.host.RemoveLocation(ctx, location.ID) },
		done: func(report *jobs.Reporter) {
			// Said plainly, because the whole point is that nothing was deleted.
			report.Progress("removed", percent(100),
				location.Name+" is no longer used by Homebase. Everything on the disk is still there.")
		},
	})
}

func (s *Server) handleMountLocation(w http.ResponseWriter, r *http.Request, user *auth.User) {
	if !s.expectNoBody(w, r) {
		return
	}
	location, ok := s.locationFor(w, r)
	if !ok {
		return
	}

	s.submitStorageJob(w, r, user, storageJob{
		operation:   "storage.mount",
		subject:     location.ID,
		event:       "storage_location_mounted",
		failedEvent: "storage_location_mount_failed",
		recorded:    location.Name + " was connected.",
		stage:       storageStage{"mounting", 50, "Opening " + location.Name + "…"},
		run:         func(ctx context.Context) error { return s.host.MountLocation(ctx, location.ID) },
	})
}

func (s *Server) handleUnmountLocation(w http.ResponseWriter, r *http.Request, user *auth.User) {
	location, ok := s.locationFor(w, r)
	if !ok {
		return
	}
	if !s.confirmedByName(w, r, location.ID, location.Name) {
		return
	}

	s.submitStorageJob(w, r, user, storageJob{
		operation:   "storage.unmount",
		subject:     location.ID,
		event:       "storage_location_unmounted",
		failedEvent: "storage_location_unmount_failed",
		recorded:    location.Name + " was disconnected safely.",
		stage:       storageStage{"unmounting", 50, "Finishing writes to " + location.Name + "…"},
		run:         func(ctx context.Context) error { return s.host.UnmountLocation(ctx, location.ID) },
		done: func(report *jobs.Reporter) {
			report.Progress("safe", percent(100),
				location.Name+" can now be unplugged safely.")
		},
	})
}

// --- Formatting ---------------------------------------------------------------

// handleFormatDisk erases a disk. There is no undo and no backup taken first.
func (s *Server) handleFormatDisk(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		UUID    string `json:"uuid,omitempty"`
		Device  string `json:"device,omitempty"`
		Label   string `json:"label,omitempty"`
		Confirm string `json:"confirm"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	// A disk is named by UUID where it has one. The device path is accepted only
	// for a disk with no filesystem, because there is then nothing else to name
	// it by — and hostd checks that path against a volume it discovered itself.
	expected := body.UUID
	if expected == "" {
		expected = body.Device
	}
	if expected == "" {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.missing_field",
			Message:     "Homebase needs to know which disk to prepare.",
			Detail:      "uuid or device is required",
			Recoverable: true,
		})
		return
	}

	// Typed, not clicked, and typed as the disk's own identifier rather than as
	// a word like "yes". A confirmation somebody can satisfy by reflex is not a
	// confirmation, and this is the one operation in Homebase that can destroy
	// data it never created.
	if body.Confirm != expected {
		s.writeError(w, r, http.StatusPreconditionRequired, apiError{
			Code:        "storage.confirmation_required",
			Message:     "Please confirm which disk you want to erase.",
			Detail:      "confirm must be " + expected,
			Recoverable: true,
			Recovery: "Everything on that disk will be permanently deleted, " +
				"including anything that was on it before you had Homebase. " +
				"Confirm by naming it exactly.",
		})
		return
	}

	s.submitStorageJob(w, r, user, storageJob{
		operation:   "storage.format",
		subject:     expected,
		event:       "storage_disk_formatted",
		failedEvent: "storage_disk_format_failed",
		recorded:    "A disk was erased and prepared for use.",
		severity:    events.SeverityWarning,
		stage:       storageStage{"formatting", 30, "Preparing the disk. This can take a few minutes."},
		// Cancelling half-way through mkfs leaves a disk in a state that is
		// neither the old filesystem nor a new one. Offering a button that
		// cannot do what it says is worse than not offering one.
		cancellable: false,
		run: func(ctx context.Context) error {
			_, err := s.host.FormatDisk(ctx, body.UUID, body.Device, body.Label, body.Confirm)
			return err
		},
		done: func(report *jobs.Reporter) {
			report.Progress("ready", percent(100), "The disk is ready to use.")
		},
	})
}

// --- An application's storage -------------------------------------------------

func (s *Server) handleAppStorage(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	storage, err := s.host.AppStorage(ctx, r.PathValue("id"))
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, storage)
}

func (s *Server) handleAssignStorage(w http.ResponseWriter, r *http.Request, user *auth.User) {
	appID := r.PathValue("id")

	var body struct {
		StorageID string `json:"storage_id"`
		Location  string `json:"location"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.StorageID == "" || body.Location == "" {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.missing_field",
			Message:     "Homebase needs to know which disk to use, and what for.",
			Detail:      "storage_id and location are both required",
			Recoverable: true,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	// Resolved before a job exists, so an unknown application or an unknown slot
	// is a 404 rather than a job in the user's history that was never going to
	// work.
	app, err := s.host.App(ctx, appID)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	s.submitStorageJob(w, r, user, storageJob{
		operation:   "app.assign_storage",
		subject:     appID,
		event:       "application_storage_assigned",
		failedEvent: "application_storage_assign_failed",
		recorded:    app.Name + " was given a disk to keep its files on.",
		stage:       storageStage{"assigning", 50, "Setting up storage for " + app.Name + "…"},
		run: func(ctx context.Context) error {
			return s.host.AssignStorage(ctx, appID, body.StorageID, body.Location)
		},
		done: func(report *jobs.Reporter) {
			// Said explicitly: nothing already written moves, and the change
			// only takes effect on the next start.
			report.Progress("assigned", percent(100),
				app.Name+" will use that disk the next time it starts. "+
					"Anything it has already saved stays where it is.")
		},
	})
}

// --- Shared plumbing ----------------------------------------------------------

type storageStage struct {
	name     string
	progress int
	message  string
}

type storageJob struct {
	operation   string
	subject     string
	event       string
	failedEvent string
	recorded    string
	severity    events.Severity
	stage       storageStage
	cancellable bool
	run         func(context.Context) error
	done        func(*jobs.Reporter)
}

func (s *Server) submitStorageJob(w http.ResponseWriter, r *http.Request, user *auth.User, spec storageJob) {
	job, err := s.jobs.Submit(r.Context(), jobs.Definition{
		Operation:      spec.operation,
		Cancellable:    spec.cancellable,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		CreatedBy:      user.ID,
		Run: func(ctx context.Context, report *jobs.Reporter) error {
			report.Progress(spec.stage.name, percent(spec.stage.progress), spec.stage.message)

			if err := spec.run(ctx); err != nil {
				severity := spec.severity
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

// locationFor resolves the id in the path to a managed location.
func (s *Server) locationFor(w http.ResponseWriter, r *http.Request) (hostclient.Location, bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	id := r.PathValue("id")

	locations, err := s.host.Locations(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return hostclient.Location{}, false
	}

	for _, location := range locations {
		if location.ID == id {
			return location, true
		}
	}

	s.writeError(w, r, http.StatusNotFound, apiError{
		Code:        "storage.unknown_location",
		Message:     "This server has no storage location by that name.",
		Detail:      id,
		Recoverable: false,
	})
	return hostclient.Location{}, false
}

func storageFailureMessage(err error) string {
	var hostErr *hostclient.Error
	if asAPIHostError(err, &hostErr) && hostErr.Message != "" {
		return hostErr.Message
	}
	return "Something went wrong with that disk."
}
