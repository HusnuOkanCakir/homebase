package api

import (
	"context"
	"net/http"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
)

// What version this server is running.
//
// Read-only for now. Applying an update is the rest of Milestone 8, and this is
// deliberately in front of it: the interruption tests assert against this, and
// an update that goes wrong is diagnosed with it. A machine that cannot say what
// it is running cannot be helped by anybody.

func (s *Server) registerUpdateRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/system/update", s.require(auth.PermUpdateRead, s.handleUpdateStatus))
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	status, err := s.host.UpdateStatus(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, status)
}
