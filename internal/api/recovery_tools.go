package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
)

// Putting a broken server back together, from the browser.
//
// The order of the screen is the order of escalation: collect what is wrong,
// try to fix it, and only then start again. Each of these is more drastic than
// the one before, and the API mirrors that — diagnostics changes nothing, repair
// changes only things that should already have been true, and the reset takes
// the machine's own name typed by hand.
//
// The download endpoint is separate from the operation that produces the file
// on purpose. The bundle is written on the machine, by hostd, and read back
// through core — so nothing about it travels through hostd's JSON envelope, and
// a caller cannot ask core to read a path of their choosing.

func (s *Server) registerRecoveryToolRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/v1/system/diagnostics", s.require(auth.PermSystemManage, s.handleDiagnostics))
	mux.Handle("GET /api/v1/system/diagnostics/download", s.require(auth.PermSystemManage, s.handleDownloadDiagnostics))
	mux.Handle("POST /api/v1/system/repair", s.require(auth.PermSystemManage, s.handleRepair))
	mux.Handle("POST /api/v1/system/factory-reset", s.require(auth.PermSystemManage, s.handleFactoryReset))
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request, user *auth.User) {
	// Generous: it reads a day of the journal, and on a machine that is having
	// trouble that is exactly when the journal is longest.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	result, err := s.host.CollectDiagnostics(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	// Recorded because the file is meant to leave the machine, and a record that
	// one was made is what lets somebody notice one they did not ask for.
	s.recordAppEvent(r.Context(), "system.diagnostics_collected", events.SeverityInfo,
		"", "", "A diagnostic file was made, to send to somebody helping.")
	s.log.Info("diagnostics collected", "bytes", result.Bytes, "by", user.Username)

	writeJSON(w, http.StatusOK, result)
}

// diagnosticsDir is where hostd writes bundles. core reads them from here and
// nowhere else — the path never comes from the request.
const diagnosticsDir = "/var/lib/homebase/diagnostics"

// handleDownloadDiagnostics serves the most recent bundle.
//
// The newest one, rather than one named by the caller. A filename in a request
// is a path to be validated, and the validation is the part that gets subtly
// wrong; there is nothing to get wrong if there is no filename. A browser that
// just made a bundle wants that bundle, which is the newest one.
func (s *Server) handleDownloadDiagnostics(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	newest, err := newestDiagnostics()
	if err != nil {
		s.writeError(w, r, http.StatusNotFound, apiError{
			Code:        "diagnostics.none",
			Message:     "There is no diagnostic file to download yet.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Make one first.",
		})
		return
	}

	contents, err := os.ReadFile(newest)
	if err != nil {
		s.writeInternal(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+filepath.Base(newest)+`"`)
	// It names disks, applications and file paths. Nothing should cache it.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(contents)
}

func newestDiagnostics() (string, error) {
	entries, err := os.ReadDir(diagnosticsDir)
	if err != nil {
		return "", err
	}

	newest, latest := "", time.Time{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "homebase-diagnostics-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latest) {
			newest, latest = filepath.Join(diagnosticsDir, entry.Name()), info.ModTime()
		}
	}
	if newest == "" {
		return "", os.ErrNotExist
	}
	return newest, nil
}

func (s *Server) handleRepair(w http.ResponseWriter, r *http.Request, user *auth.User) {
	// Long: finishing an interrupted package transaction is dpkg running
	// maintainer scripts, which on an old laptop is minutes.
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Minute)
	defer cancel()

	result, err := s.host.Repair(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	// Only when it did something. A repair that found nothing wrong is not worth
	// a line in a history somebody reads to find out what changed.
	if result.Changed > 0 {
		s.events.Warn(r.Context(), "system.repaired", "",
			"a repair changed something",
			"Homebase repaired something that was wrong with this server.")
	}
	s.log.Info("repair run", "changed", result.Changed, "healthy", result.Healthy,
		"by", user.Username)

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleFactoryReset(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Confirm string `json:"confirm"`

		// A pointer, so "absent" and "false" are different. A caller that
		// forgets the field keeps the data — the default has to be the safe one,
		// and a plain bool would make forgetting it mean "delete everything".
		KeepData *bool `json:"keep_data,omitempty"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	keepData := true
	if body.KeepData != nil {
		keepData = *body.KeepData
	}

	// Recorded *before* it happens. The database holding this history is one of
	// the things about to be deleted, so this line exists for the journal rather
	// than for the event list — which is exactly why it has to be written while
	// there is still somewhere to write it.
	s.log.Warn("factory reset requested", "keep_data", keepData, "by", user.Username)

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
	defer cancel()

	result, err := s.host.FactoryReset(ctx, strings.TrimSpace(body.Confirm), keepData)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	s.log.Warn("factory reset completed", "kept", result.Kept, "by", user.Username)
	writeJSON(w, http.StatusOK, result)
}
