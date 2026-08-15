package hostd

import (
	"context"
	"net"
	"os/exec"
	"time"
)

// Network operations.
//
// Read-only, all of them. Milestone 7 is about a server being reachable and
// being honest when it is not; changing the network from the dashboard is a
// larger surface and arrives with Wi-Fi.

// NetworkServices is what the network operations need.
type NetworkServices struct {
	// dial is how reachability is tested. Replaced in tests, because a test
	// whose result depends on whether the machine running it has internet is a
	// test that fails on a train.
	dial func(ctx context.Context, address string) error
}

func NewNetworkServices() *NetworkServices {
	return &NetworkServices{dial: dialTCP}
}

// RegisterNetworkOperations adds the network domain to a registry.
func RegisterNetworkOperations(r *Registry, services *NetworkServices) {
	r.MustRegister(Operation{
		Name: "network.status",
		Summary: "Report how this server is connected: addresses, router, " +
			"name resolution, and whether the internet is reachable.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 30 * time.Second,
		Handler: Typed(services.status),
	})
}

type networkStatusResult struct {
	NetworkStatus

	// Online is whether anything outside this network answered.
	//
	// Deliberately separate from having an address: a machine with a perfectly
	// good address on a network whose broadband is down is a different problem
	// from a machine with no address, and telling somebody "not connected" when
	// their server is fine and their internet is not sends them to the wrong
	// place entirely.
	Online bool `json:"online"`

	// Reachable is whether this machine can be reached at all — it has an
	// address on something other than loopback. This is what determines whether
	// the dashboard is usable from another room.
	Reachable bool `json:"reachable"`
}

func (s *NetworkServices) status(ctx context.Context, _ struct{}) (any, error) {
	status := ReadNetworkStatus()

	result := networkStatusResult{NetworkStatus: status}
	for _, iface := range status.Interfaces {
		if iface.Kind == "loopback" || iface.Kind == "container" {
			continue
		}
		if iface.Reachable() {
			result.Reachable = true
		}
	}

	// mDNS is only claimed to work if something is actually answering for it.
	// Reporting a name the network cannot resolve is worse than reporting none:
	// it sends somebody to type an address that will never load.
	result.MDNSWorks = mdnsResponderRunning()

	// Only asked if there is a route to ask over. Without a gateway the answer
	// is already known, and the attempt would cost the caller a timeout.
	if status.Gateway != "" {
		result.Online = s.reachesTheInternet(ctx)
	}

	return result, nil
}

// reachesTheInternet answers whether anything outside this network responded.
//
// A TCP connection rather than a ping: ICMP is blocked on plenty of networks,
// and "the internet is down" is the wrong conclusion to draw from a firewall.
// Two addresses, because one being down is not evidence about the internet.
func (s *NetworkServices) reachesTheInternet(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Port 443, not 53.
	//
	// This dialled TCP/53 at the public resolvers, and reported "the internet is
	// not reachable" on the first real network Homebase was installed on — a
	// machine that was downloading Ubuntu updates at the time. Plenty of
	// networks block outbound TCP/53 to public resolvers; some ISPs do it as
	// policy. Almost nothing blocks 443, because blocking it breaks the web.
	//
	// Reached by address rather than by name, still: this is a question about
	// connectivity, and resolving a name first would make a broken resolver look
	// like a broken connection.
	//
	// Two addresses at two organisations, because one being down is not evidence
	// about the internet. 53 is kept as a last resort for the rarer network that
	// allows it and not 443.
	for _, address := range []string{
		"1.1.1.1:443", "8.8.8.8:443",
		"1.1.1.1:53", "8.8.8.8:53",
	} {
		if err := s.dial(ctx, address); err == nil {
			return true
		}
	}
	return false
}

func dialTCP(ctx context.Context, address string) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return conn.Close()
}

// mdnsResponderRunning reports whether something is publishing this machine's
// name on the local network.
//
// Checked by asking the daemon rather than by assuming the package is
// installed: a responder that is installed and not running publishes nothing,
// and the difference is invisible from here otherwise.
func mdnsResponderRunning() bool {
	binary, err := exec.LookPath("systemctl")
	if err != nil {
		return false
	}
	out, err := exec.Command(binary, "is-active", "avahi-daemon.service").Output()
	if err != nil {
		return false
	}
	return string(out) == "active\n"
}
