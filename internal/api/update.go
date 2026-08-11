package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
)

// What version this server is running, and moving it to a newer one.
//
// Applying is the only operation in the API that outlives the request. It
// replaces `hostd` and restarts it, so there is no arrangement in which a
// response could describe the outcome — the caller polls `progress` instead,
// which is also how a browser whose connection died during the restart finds
// out what happened.

func (s *Server) registerUpdateRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/system/update", s.require(auth.PermUpdateRead, s.handleUpdateStatus))
	mux.Handle("GET /api/v1/system/update/progress", s.require(auth.PermUpdateRead, s.handleUpdateProgress))
	mux.Handle("POST /api/v1/system/update/check", s.require(auth.PermUpdateRead, s.handleUpdateCheck))
	mux.Handle("POST /api/v1/system/update/channel", s.require(auth.PermUpdateManage, s.handleUpdateChannel))
	mux.Handle("POST /api/v1/system/update/apply", s.require(auth.PermUpdateManage, s.handleUpdateApply))
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

func (s *Server) handleUpdateProgress(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	progress, err := s.host.UpdateProgress(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, progress)
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	// Longer than most: this reaches the network, and deciding a repository is
	// unreachable means waiting for something not to answer.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	result, err := s.host.UpdateCheck(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Channel string `json:"channel"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	result, err := s.host.ConfigureUpdates(ctx, strings.TrimSpace(body.Channel))
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	// Worth an event. Which channel a machine is on decides how tested the code
	// it runs has been, and somebody looking at a server behaving oddly should
	// be able to see that it was moved to development three weeks ago.
	s.events.Warn(r.Context(), "update.channel_changed", result.Channel,
		"the update channel was changed",
		"This server now takes updates from the "+result.Channel+" channel.")
	s.log.Info("update channel changed", "channel", result.Channel,
		"origin", result.Origin, "reachable", result.Reachable, "by", user.Username)

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request, user *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	started, err := s.host.ApplyUpdate(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	// Recorded before it happens rather than afterwards, because there may be no
	// afterwards from this process's point of view: the update restarts core.
	s.events.Warn(r.Context(), "update.started", "",
		"an update was started",
		"An update was started. This server will restart its services.")
	s.log.Info("update started", "by", user.Username)

	// 202: accepted, not finished. The outcome arrives at `progress`, and by the
	// time it exists this process will have been replaced.
	writeJSON(w, http.StatusAccepted, started)
}
