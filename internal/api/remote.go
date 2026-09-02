package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
)

// Folders on other people's computers.
//
// The scenario this exists for, in the words it was asked in: a disk is in a
// drawer, somebody at home plugs it into their own laptop, and somebody in
// another country needs a file off it. The server has no such disk and nobody
// knows in advance which files are wanted, so copying it across first does not
// answer the question.
//
// What makes it small is that the laptop needs no Homebase software. Windows
// has shared folders for thirty years; what was missing was this server being
// able to open one. So somebody at home shares the drive the way they already
// know how, Homebase mounts it read-only, and it appears in the Files screen
// like anything else — which means the person abroad reaches it over Tailscale
// in a browser, with nothing installed at their end either.

func (s *Server) registerRemoteRoutes(mux *http.ServeMux) {
	// files.read to see them: they are folders, and being told what you can
	// open is the same question as being told what is shared.
	mux.Handle("GET /api/v1/remote-folders",
		s.require(auth.PermFilesRead, s.handleRemoteFolders))

	// storage.modify to connect one, which is a deliberately higher bar than
	// using it. Connecting hands this server a credential for a computer it
	// does not administer and puts the contents of somebody's disk in front of
	// the household; that is an administrator's decision even though the disk
	// belongs to whoever plugged it in.
	mux.Handle("POST /api/v1/remote-folders",
		s.require(auth.PermStorageModify, s.handleConnectRemoteFolder))
	mux.Handle("POST /api/v1/remote-folders/remove",
		s.require(auth.PermStorageModify, s.handleDisconnectRemoteFolder))
	mux.Handle("POST /api/v1/remote-folders/reconnect",
		s.require(auth.PermStorageModify, s.handleReconnectRemoteFolder))
}

func (s *Server) handleRemoteFolders(w http.ResponseWriter, r *http.Request, user *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	status, err := s.host.RemoteFolders(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	// Filtered to what this person may open, for the same reason the Files
	// areas are: a folder kept for somebody else should not be listed to
	// everybody with its name on it.
	folders := make([]any, 0, len(status.Folders))
	for _, folder := range status.Folders {
		if !mayOpenShare(folder.Access, user.Username) {
			continue
		}
		folders = append(folders, map[string]any{
			"name": folder.Name, "host": folder.Host, "share": folder.Share,
			"username": folder.Username, "added_by": folder.AddedBy,
			"access": folder.Access, "added_at": folder.AddedAt,
			"connected": folder.Connected,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"installed": status.Installed,
		"folders":   folders,
	})
}

func (s *Server) handleConnectRemoteFolder(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Name     string   `json:"name"`
		Host     string   `json:"host"`
		Share    string   `json:"share"`
		Username string   `json:"username"`
		Password string   `json:"password"`
		Access   []string `json:"access"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Host) == "" ||
		strings.TrimSpace(body.Share) == "" {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.missing_field",
			Message:     "Homebase needs a name for the folder, the computer, and what it is shared as.",
			Detail:      "name, host and share are all required",
			Recoverable: true,
			Recovery:    "Fill in all three and try again.",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	if unknown := s.unknownAccounts(ctx, body.Access); len(unknown) > 0 {
		s.writeError(w, r, http.StatusUnprocessableEntity, apiError{
			Code:        "share.no_such_account",
			Message:     "Nobody on this server has that name.",
			Detail:      strings.Join(unknown, ", "),
			Recoverable: true,
			Recovery:    "Check the spelling, or add them under People first.",
		})
		return
	}

	result, err := s.host.ConnectRemoteFolder(ctx, body.Name, body.Host, body.Share,
		body.Username, body.Password, user.Username, body.Access)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	// The name of the computer and who connected it, never the password —
	// nothing here writes the request body to a log, and hostd declares the
	// field a secret so its own audit record is safe too.
	s.events.Warn(r.Context(), "remote.connected", body.Name, body.Host,
		body.Name+" was connected from "+body.Host+" and can be opened on this server.")
	s.log.Info("remote folder connected",
		"name", body.Name, "host", body.Host, "by", user.Username)

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleDisconnectRemoteFolder(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Name string `json:"name"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if !s.confirmedByName(w, r, body.Name, body.Name) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	result, err := s.host.DisconnectRemoteFolder(ctx, body.Name)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	s.events.Info(r.Context(), "remote.disconnected", body.Name,
		body.Name+" is no longer open on this server.")
	s.log.Info("remote folder disconnected", "name", body.Name, "by", user.Username)

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleReconnectRemoteFolder(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Name string `json:"name"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	result, err := s.host.ReconnectRemoteFolder(ctx, body.Name)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	s.log.Info("remote folder reconnected", "name", body.Name, "by", user.Username)
	writeJSON(w, http.StatusOK, result)
}
