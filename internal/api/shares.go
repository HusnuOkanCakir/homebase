package api

import (
	"context"
	"net/http"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
)

// File sharing.
//
// The one part of Homebase that puts somebody's files where every device in the
// house can open them, so every route here takes the permission that changes the
// network rather than the one that reads it.
//
// core adds no policy of its own. What may be shared, who may open it and what
// the configuration says are all decided in hostd, which is where the file is
// written — see internal/hostd/shares.go.

func (s *Server) registerShareRoutes(mux *http.ServeMux) {
	// files.read, not network.diagnose.
	//
	// Seeing how to reach your own files is a files question. It was a network
	// one because everybody was an administrator and held both; the first
	// account that held neither could not open the only screen its role was for.
	mux.Handle("GET /api/v1/shares", s.require(auth.PermFilesRead, s.handleShares))
	mux.Handle("POST /api/v1/shares", s.require(auth.PermNetworkModify, s.handleAddShare))
	mux.Handle("POST /api/v1/shares/remove", s.require(auth.PermNetworkModify, s.handleRemoveShare))
	mux.Handle("POST /api/v1/shares/users", s.require(auth.PermNetworkModify, s.handleSetSharePassword))
	mux.Handle("POST /api/v1/shares/users/remove", s.require(auth.PermNetworkModify, s.handleRemoveShareUser))
}

func (s *Server) handleShares(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	status, err := s.host.Shares(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleAddShare(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Name     string `json:"name"`
		Location string `json:"location"`
		ReadOnly bool   `json:"read_only"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Name == "" || body.Location == "" {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.missing_field",
			Message:     "Homebase needs a name for the folder and a disk to put it on.",
			Detail:      "name and location are both required",
			Recoverable: true,
			Recovery:    "Choose a name and a disk.",
		})
		return
	}

	// Long, because this installs the file server the first time it is called
	// and that is an apt download on a domestic connection.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	result, err := s.host.AddShare(ctx, body.Name, body.Location, body.ReadOnly)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	s.events.Warn(r.Context(), "share.added", body.Name,
		"a folder was shared onto the local network",
		body.Name+" can now be opened from other computers in the house.")
	s.log.Info("folder shared", "name", body.Name, "disk", body.Location,
		"by", user.Username)

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRemoveShare(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Name string `json:"name"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	result, err := s.host.RemoveShare(ctx, body.Name)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	s.events.Info(r.Context(), "share.removed", body.Name,
		body.Name+" is no longer shared. The files are still on the server.")
	s.log.Info("folder unshared", "name", body.Name, "by", user.Username)

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSetSharePassword(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Username == "" || body.Password == "" {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.missing_field",
			Message:     "Homebase needs a name and a password.",
			Detail:      "username and password are both required",
			Recoverable: true,
			Recovery:    "Choose a name and a password for it.",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	result, err := s.host.SetSharePassword(ctx, body.Username, body.Password)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	// The username, never the password — and nothing here writes the request
	// body to a log. hostd declares the field a secret so its audit record is
	// safe too; this is the same rule applied on the way in.
	s.events.Info(r.Context(), "share.user_changed", body.Username,
		"a file-sharing password was set for "+body.Username+".")
	s.log.Info("file-sharing password set", "username", body.Username,
		"by", user.Username)

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRemoveShareUser(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Username string `json:"username"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 1*time.Minute)
	defer cancel()

	result, err := s.host.RemoveShareUser(ctx, body.Username)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	s.events.Info(r.Context(), "share.user_removed", body.Username,
		body.Username+" can no longer open the shared folders.")
	s.log.Info("file-sharing account removed", "username", body.Username,
		"by", user.Username)

	writeJSON(w, http.StatusOK, result)
}
