package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
)

// The network, as the dashboard sees it.
//
// The read half tells three faults apart that are indistinguishable from a
// browser that will not load — no address, no internet, and nothing wrong here —
// because a server that cannot tell them apart sends its owner to restart a
// router over a problem with their phone.
//
// The write half is Wi-Fi, and it is the only thing in the API that can change
// how the server is reached. Joining takes `network.modify` rather than the
// diagnostic permission the reads take, and it is the one route where a failure
// has to say *what did not change* — because the person reading it is wondering
// whether their server is still there.

func (s *Server) registerNetworkRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/network", s.require(auth.PermNetworkDiag, s.handleNetwork))

	mux.Handle("GET /api/v1/network/wifi", s.require(auth.PermNetworkDiag, s.handleWifiStatus))
	mux.Handle("POST /api/v1/network/wifi/scan", s.require(auth.PermNetworkDiag, s.handleWifiScan))
	mux.Handle("POST /api/v1/network/wifi", s.require(auth.PermNetworkModify, s.handleWifiConnect))
	mux.Handle("POST /api/v1/network/wifi/forget", s.require(auth.PermNetworkModify, s.handleWifiForget))
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

// --- Wireless ------------------------------------------------------------------

func (s *Server) handleWifiStatus(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	status, err := s.host.WifiStatus(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handleWifiScan is a POST despite changing nothing.
//
// Scanning takes seconds, tunes the radio away from its channel while it runs,
// and is something a person asks for by pressing a button. A GET that a browser
// may retry, prefetch or cache is the wrong shape for it.
func (s *Server) handleWifiScan(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	if !s.expectNoBody(w, r) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	scan, err := s.host.ScanWifi(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scan)
}

func (s *Server) handleWifiConnect(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		SSID       string `json:"ssid"`
		Passphrase string `json:"passphrase,omitempty"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ssid := strings.TrimSpace(body.SSID)
	if ssid == "" {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.missing_field",
			Message:     "Homebase needs to know which network to join.",
			Detail:      "ssid is required",
			Recoverable: true,
			Recovery:    "Choose a network.",
		})
		return
	}

	// Longer than any other request in the API. Associating and getting an
	// address takes seconds, and if it fails hostd puts the previous
	// configuration back and applies it again before answering — so the caller
	// waits for the rollback too, which is the right thing to wait for.
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()

	status, err := s.host.JoinWifi(ctx, ssid, body.Passphrase)
	if err != nil {
		// Recorded even when it fails. A server that fell off the network is
		// diagnosed afterwards, and "somebody tried to join a Wi-Fi network at
		// 21:04" is the line that explains it.
		s.events.Warn(r.Context(), "network.wifi_failed", ssid,
			"joining a wireless network did not work",
			"This server tried to join "+ssid+" and could not. Nothing was changed.")
		s.writeHostError(w, r, err)
		return
	}

	s.events.Warn(r.Context(), "network.wifi_joined", ssid,
		"the wireless network was changed",
		"This server joined the wireless network "+ssid+".")
	// The passphrase is never logged, and this is the one place it would be easy
	// to include by accident.
	s.log.Info("wireless network joined", "ssid", ssid,
		"addresses", len(status.Addresses), "by", user.Username)

	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleWifiForget(w http.ResponseWriter, r *http.Request, user *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	status, err := s.host.ForgetWifi(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	s.events.Warn(r.Context(), "network.wifi_forgotten", "",
		"wireless was turned off",
		"This server is no longer set up to use wireless.")
	s.log.Info("wireless forgotten", "by", user.Username)

	writeJSON(w, http.StatusOK, status)
}
