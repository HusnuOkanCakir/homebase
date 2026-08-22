package api

import (
	"context"
	"net"
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

	mux.Handle("POST /api/v1/network/wake-on-lan", s.require(auth.PermNetworkModify, s.handleWakeOnLAN))

	mux.Handle("GET /api/v1/network/wifi", s.require(auth.PermNetworkDiag, s.handleWifiStatus))
	mux.Handle("POST /api/v1/network/wifi/scan", s.require(auth.PermNetworkDiag, s.handleWifiScan))
	mux.Handle("POST /api/v1/network/wifi", s.require(auth.PermNetworkModify, s.handleWifiConnect))
	mux.Handle("POST /api/v1/network/wifi/forget", s.require(auth.PermNetworkModify, s.handleWifiForget))

	mux.Handle("GET /api/v1/network/vpn", s.require(auth.PermNetworkDiag, s.handleVPNStatus))
	mux.Handle("POST /api/v1/network/vpn", s.require(auth.PermNetworkModify, s.handleVPNSetup))
	mux.Handle("POST /api/v1/network/vpn/disable", s.require(auth.PermNetworkModify, s.handleVPNDisable))
	mux.Handle("POST /api/v1/network/vpn/devices", s.require(auth.PermNetworkModify, s.handleAddVPNDevice))
	mux.Handle("POST /api/v1/network/vpn/devices/remove", s.require(auth.PermNetworkModify, s.handleRemoveVPNDevice))
	mux.Handle("POST /api/v1/network/vpn/dns", s.require(auth.PermNetworkModify, s.handleSetDNS))
	mux.Handle("POST /api/v1/network/vpn/dns/clear", s.require(auth.PermNetworkModify, s.handleClearDNS))
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

	// Whether anything outside this network answers.
	//
	// Asked here rather than in hostd, and that is structural rather than
	// tidiness: hostd's unit sets RestrictAddressFamilies=AF_UNIX AF_NETLINK, so
	// it cannot open an internet socket and the check returned false on every
	// machine that ever ran it. Reaching 1.1.1.1 needs no privilege at all, so
	// it belongs on this side of the boundary.
	//
	// Only asked when there is a route to ask over: without a gateway the answer
	// is already known and the attempt would cost the caller a timeout.
	if status.Gateway != "" {
		status.Online = reachesTheInternet(ctx)
	}

	writeJSON(w, http.StatusOK, status)
}

// reachesTheInternet answers whether anything outside this network responded.
//
// A TCP connection rather than a ping: ICMP is blocked on plenty of networks,
// and "the internet is down" is the wrong conclusion to draw from a firewall.
//
// Port 443 rather than 53. It dialled the public resolvers on 53 and reported a
// working connection as broken on the first real network Homebase met — plenty
// of networks block outbound TCP/53 to public resolvers, and some ISPs do it as
// policy. Almost nothing blocks 443, because blocking it breaks the web.
//
// Two organisations, because one being down is not evidence about the internet.
// Reached by address rather than by name: this is a question about connectivity,
// and resolving a name first would make a broken resolver look like a broken
// connection.
func reachesTheInternet(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	for _, address := range []string{
		"1.1.1.1:443", "8.8.8.8:443",
		"1.1.1.1:53", "8.8.8.8:53",
	} {
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

// --- Wireless ------------------------------------------------------------------

// handleWakeOnLAN switches magic-packet waking on or off for one card.
func (s *Server) handleWakeOnLAN(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var request struct {
		Interface string `json:"interface"`
		Enabled   *bool  `json:"enabled"`
	}
	if !s.decode(w, r, &request) {
		return
	}
	// A pointer, so that a body omitting the field is refused rather than read
	// as "switch it off". This endpoint is reached by a person who has just been
	// told their server can be woken; silently doing the opposite of what they
	// asked, because they left out a field, is the kind of thing nobody finds
	// out about until the machine will not start.
	if request.Enabled == nil {
		s.writeError(w, r, http.StatusBadRequest, apiError{
			Code:        "request.missing_field",
			Message:     "Homebase needs to know whether to switch this on or off.",
			Detail:      "enabled is required",
			Recoverable: true,
			Recovery:    "Say whether the server should be startable over the network.",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := s.host.SetWakeOnLAN(ctx, request.Interface, *request.Enabled)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	message := "This server can now be started over the network."
	if !*request.Enabled {
		message = "This server can no longer be started over the network."
	}
	s.events.Info(r.Context(), "network.wake_on_lan_changed", request.Interface, message)
	s.log.Info("wake-on-LAN changed", "interface", request.Interface,
		"enabled", *request.Enabled, "by", user.Username)

	writeJSON(w, http.StatusOK, result)
}

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

// --- Remote access ----------------------------------------------------------------

func (s *Server) handleVPNStatus(w http.ResponseWriter, r *http.Request, _ *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	status, err := s.host.VPNStatus(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleVPNSetup(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Hostname string `json:"hostname"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	status, err := s.host.SetUpVPN(ctx, strings.TrimSpace(body.Hostname))
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	// Worth an event above 'info'. This opens a way into the house's network
	// from the internet, and somebody reading the history later needs to find
	// the day it was switched on.
	s.events.Warn(r.Context(), "vpn.enabled", status.Hostname,
		"remote access was switched on",
		"This server can now be reached from outside the house, at "+
			status.Hostname+".")
	s.log.Info("remote access configured", "hostname", status.Hostname,
		"by", user.Username)

	writeJSON(w, http.StatusOK, status)
}

// handleAddVPNDevice issues a key, once.
//
// The response carries a private key — the only response in the API that does,
// apart from the recovery code at setup. It is stored nowhere and cannot be
// asked for again.
// handleVPNDisable closes the way in from outside.
func (s *Server) handleVPNDisable(w http.ResponseWriter, r *http.Request, user *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 1*time.Minute)
	defer cancel()

	status, err := s.host.DisableVPN(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	// A warning rather than an ordinary note. Somebody away from home who
	// relies on this has just been disconnected, and the event log is where
	// they will look to find out why.
	s.events.Warn(r.Context(), "network.vpn_disabled", "",
		"remote access was switched off",
		"This server can no longer be reached from outside the house. The "+
			"devices already set up keep their keys and will work again when "+
			"it is switched back on.")
	s.log.Info("remote access disabled", "by", user.Username)

	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleAddVPNDevice(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Name string `json:"name"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	device, err := s.host.AddVPNDevice(ctx, strings.TrimSpace(body.Name))
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	// The event records that a key was issued and to what it was called. It does
	// not record the key, and the log line below does not either — this is the
	// one place in core where that would be easy to do by accident.
	s.events.Warn(r.Context(), "vpn.device_added", device.Name,
		"a device was given remote access",
		"The device \""+device.Name+"\" can now reach this server from anywhere.")
	s.log.Info("remote access device added", "device", device.Name,
		"address", device.Address, "by", user.Username)

	writeJSON(w, http.StatusCreated, device)
}

func (s *Server) handleRemoveVPNDevice(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Name string `json:"name"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	status, err := s.host.RemoveVPNDevice(ctx, strings.TrimSpace(body.Name))
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	s.events.Warn(r.Context(), "vpn.device_removed", body.Name,
		"a device lost remote access",
		"The device \""+body.Name+"\" can no longer reach this server from outside.")
	s.log.Info("remote access device removed", "device", body.Name, "by", user.Username)

	writeJSON(w, http.StatusOK, status)
}

// handleSetDNS records the name that has to keep pointing at the house.
//
// The token is a credential. It is not logged here, and hostd declares it as a
// secret so it is redacted from the audit log too.
func (s *Server) handleSetDNS(w http.ResponseWriter, r *http.Request, user *auth.User) {
	var body struct {
		Provider string `json:"provider"`
		Name     string `json:"name"`
		Token    string `json:"token,omitempty"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	status, err := s.host.SetDNS(ctx, strings.TrimSpace(body.Provider),
		strings.TrimSpace(body.Name), body.Token)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}

	s.log.Info("dynamic DNS configured", "provider", status.Provider,
		"name", status.Name, "by", user.Username)
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleClearDNS(w http.ResponseWriter, r *http.Request, user *auth.User) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	status, err := s.host.ClearDNS(ctx)
	if err != nil {
		s.writeHostError(w, r, err)
		return
	}
	s.log.Info("dynamic DNS switched off", "by", user.Username)
	writeJSON(w, http.StatusOK, status)
}
