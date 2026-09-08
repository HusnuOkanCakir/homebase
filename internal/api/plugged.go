package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
)

// Disks people plug into the server.
//
// The flow this exists for, in the words it was asked in: "the only thing my
// father does is turn on the server and plug in a hard disk". Everything else
// is Homebase's problem — there is no account to make on anybody's computer, no
// sharing dialog, no computer name to resolve, and no laptop that has to stay
// awake while somebody in another country reads from it.
//
// So there is no endpoint here for *connecting* a disk. hostd notices it and
// mounts it read-only, and it appears in Files. The only thing anybody presses
// is Finish, and only when they want to take the disk away again.

func (s *Server) registerPluggedRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/plugged-disks",
		s.require(auth.PermFilesRead, s.handlePluggedDisks))

	// files.read to eject, which is the same right as opening it.
	//
	// Unplugging a disk safely is not a privilege: it is the polite end of
	// reading from it, and the person who wants to walk away with the disk is
	// usually the person standing next to the server rather than the
	// administrator in another country. Nothing on the disk can be changed by
	// this — or by anything else here, since it is mounted read-only.
	mux.Handle("POST /api/v1/plugged-disks/eject",
		s.require(auth.PermFilesRead, s.handleEjectPluggedDisk))
}

func (s *Server) handlePluggedDisks(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	disks, err := s.host.PluggedDisks(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	// Everybody with an account can read a disk somebody plugged in, so there
	// is no per-person filtering here — unlike a shared folder, which can be
	// kept for particular people. A disk carried to the server by hand is
	// offered to the household by the act of plugging it in.
	listed := make([]any, 0, len(disks))
	for _, disk := range disks {
		listed = append(listed, map[string]any{
			"name": disk.Name, "label": disk.Label,
			"filesystem": disk.Filesystem, "size_bytes": disk.SizeBytes,
			"connected": disk.Connected,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"disks": listed})
}

func (s *Server) handleEjectPluggedDisk(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Name string `json:"name"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.missing_field",
			Message:     "Homebase needs to know which disk.",
			Detail:      "name is required",
			Recoverable: true,
			Recovery:    "Choose a disk.",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 1*time.Minute)
	defer cancel()

	result, err := s.host.EjectPluggedDisk(ctx, body.Name)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	s.events.Info(r.Context(), "plugged.ejected", body.Name,
		body.Name+" was finished with and can be unplugged.")
	s.log.Info("plugged disk ejected", "disk", body.Name, "by", user.Username)

	writeJSON(w, http.StatusOK, result)
}
