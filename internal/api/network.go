package api

import (
	"context"
	"net/http"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
)

// The network, as the dashboard sees it.
//
// One endpoint, read-only. What it is for is telling three faults apart that
// are indistinguishable from a browser that will not load — no address, no
// internet, and nothing wrong here — because a server that cannot tell them
// apart sends its owner to restart a router over a problem with their phone.

func (s *Server) registerNetworkRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/network", s.require(auth.PermNetworkDiag, s.handleNetwork))
}

func (s *Server) handleNetwork(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	// Longer than most reads: deciding whether the internet is reachable means
	// waiting for something not to answer.
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	status, err := s.host.NetworkStatus(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, status)
}
