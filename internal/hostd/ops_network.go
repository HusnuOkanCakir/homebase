package hostd

import (
	"context"
	"os/exec"
	"time"
)

// Network operations.
//
// Read-only, all of them. Milestone 7 is about a server being reachable and
// being honest when it is not; changing the network from the dashboard is a
// larger surface and arrives with Wi-Fi.

// NetworkServices is what the network operations need.
//
// Empty, since the one thing it used to hold was a dialler for an internet check
// this process is forbidden from performing. See networkStatusResult.Online.
type NetworkServices struct{}

func NewNetworkServices() *NetworkServices {
	return &NetworkServices{}
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

	r.MustRegister(Operation{
		Name: "network.wake_on_lan",
		Summary: "Let this server be started by a magic packet, or stop it " +
			"being started by one.",
		// Medium rather than low. It does not change how the machine is
		// reached, so it cannot strand anybody — but a server that can be
		// started remotely is one anybody on the network can start, and that is
		// a decision about the machine rather than a display preference.
		Risk:        RiskMedium,
		Permissions: []string{"network.modify"},
		Confirm:     ConfirmNone,
		Timeout:     30 * time.Second,
		Rollback:    "network.wake_on_lan, with enabled reversed",
		Handler:     Typed(services.wakeOnLAN),
	})
}

type wakeOnLANRequest struct {
	Interface string `json:"interface"`
	Enabled   bool   `json:"enabled"`
}

type wakeOnLANResult struct {
	Interface string `json:"interface"`
	Enabled   bool   `json:"enabled"`

	// Note is what is left to do, and there is always something: Linux arming
	// the card is only half of it. On the first real laptop everything here
	// reported correctly while the machine stayed dark, because the firmware
	// cuts power to the card when the machine is off — a setting ASUS calls
	// "Power Off Energy Saving", whose own help text admits it stops wake-up
	// working. Nothing on a running machine can read that setting, so the only
	// thing Homebase can do is say where to look.
	Note string `json:"note,omitempty"`
}

func (s *NetworkServices) wakeOnLAN(_ context.Context, request wakeOnLANRequest) (any, error) {
	if err := configureWakeOnLAN(request.Interface, request.Enabled); err != nil {
		return nil, err
	}
	result := wakeOnLANResult{Interface: request.Interface, Enabled: request.Enabled}
	if request.Enabled {
		result.Note = "If the server still does not start, the setting is in its " +
			"firmware: restart it, open the BIOS, and look for \"Wake on LAN\" or " +
			"\"Power On By PCI-E\". Also switch off anything called \"ERP\", " +
			"\"EuP\", \"Deep Sleep\" or \"Power Off Energy Saving\" — those cut " +
			"power to the network card while the machine is off, which stops it " +
			"hearing anything."
	}
	return result, nil
}

type networkStatusResult struct {
	NetworkStatus

	// Online is no longer answered here, and the reason is structural.
	//
	// `homebase-hostd.service` sets RestrictAddressFamilies=AF_UNIX AF_NETLINK,
	// so this process cannot open an internet socket at all. The check that used
	// to live here therefore returned false on every machine that ever ran it,
	// including one downloading Ubuntu updates while it said so.
	//
	// Nothing caught it. The unit tests injected a fake dialler, so they
	// exercised the logic without ever asking whether hostd could execute it,
	// and the VM suite only ever asserted `online is False` — which passed for
	// the wrong reason for four milestones.
	//
	// Reaching 1.1.1.1 needs no privilege whatsoever, so the check belongs in
	// core, which is allowed to open sockets. It is set there.
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

	// Online is left false here and filled in by core. See the field's comment.
	return result, nil
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
