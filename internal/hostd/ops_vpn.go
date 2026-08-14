package hostd

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// The remote-access operations.
//
// Graded the way the wireless ones are, and for a related reason: this is the
// surface that decides who can reach the machine at all. Adding a device hands
// somebody a key to the house's network, and removing one takes it away — which
// is how a lost phone is dealt with, and therefore has to work.

// A device name is a label. It goes into the configuration file as a comment and
// into a filename nowhere, so what it must not contain is a newline — that is
// what would end the comment and start a directive.
//
// An allowlist rather than a check for newlines, because an allowlist fails
// closed. It includes the apostrophe, which is not decoration: an iPhone is
// called "Okan's iPhone" out of the box, and a device-naming rule that rejects
// what the device calls itself is a rule people work around by typing something
// worse.
var validDeviceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 '._-]{0,30}$`)

// A hostname is what every client is told to connect to. It reaches a
// configuration file, so it is checked against the shape of a name rather than
// trusted — and an address is allowed, because a household with a static one
// needs no dynamic DNS at all.
var validEndpoint = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// RegisterVPNOperations adds remote access to a registry.
func RegisterVPNOperations(r *Registry, services *NetworkServices) {
	r.MustRegister(Operation{
		Name:    "vpn.status",
		Summary: "Report whether this server can be reached from outside the house.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 30 * time.Second,
		Handler: Typed(services.vpnStatus),
	})

	r.MustRegister(Operation{
		Name:    "vpn.setup",
		Summary: "Switch on remote access, and say what name devices connect to.",
		// High. It opens a way into the house's network from the internet — one
		// that answers nothing without a key, but a way in nonetheless.
		Risk:        RiskHigh,
		Permissions: []string{"network.modify"},
		Confirm:     ConfirmExplicit,
		Timeout:     2 * time.Minute,
		Rollback:    "vpn.disable",
		Handler:     Typed(services.vpnSetup),
	})

	r.MustRegister(Operation{
		Name:    "vpn.add_device",
		Summary: "Let one more device connect from outside, and hand it its key.",
		// High, and the grade is about what it returns rather than what it
		// changes: a key to the network, shown once, which whoever holds can use
		// from anywhere in the world.
		Risk:        RiskHigh,
		Permissions: []string{"network.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     60 * time.Second,
		Rollback:    "vpn.remove_device",
		Handler:     Typed(services.vpnAddDevice),
	})

	r.MustRegister(Operation{
		Name:    "vpn.remove_device",
		Summary: "Stop a device connecting from outside.",
		// Medium: nothing is destroyed and it is the remedy for a lost phone, so
		// it must not be hard to reach in a hurry.
		Risk:        RiskMedium,
		Permissions: []string{"network.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     60 * time.Second,
		Rollback:    "vpn.add_device, which issues a new key — the old one cannot be brought back",
		Handler:     Typed(services.vpnRemoveDevice),
	})
}

func (s *NetworkServices) vpnStatus(ctx context.Context, _ struct{}) (any, error) {
	return readVPNStatus(ctx), nil
}

type vpnSetupRequest struct {
	// Hostname is what devices connect to: a dynamic DNS name, or an address for
	// a household with a static one.
	Hostname string `json:"hostname"`
}

func (s *NetworkServices) vpnSetup(ctx context.Context, req vpnSetupRequest) (any, error) {
	hostname := strings.TrimSpace(req.Hostname)
	if !validEndpoint.MatchString(hostname) {
		return nil, &Error{
			Code:        "vpn.invalid_hostname",
			Message:     "That is not a name devices could connect to.",
			Detail:      "expected a hostname or an address, got " + short(hostname),
			Recoverable: true,
			Recovery: "Use the name your home connection answers to — a dynamic DNS " +
				"name like yours.duckdns.org, or a fixed address if you have one.",
			Status: 400,
		}
	}

	if err := setUpVPN(ctx, hostname); err != nil {
		return nil, &Error{
			Code:        "vpn.setup_failed",
			Message:     "Homebase could not switch on remote access.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery: "Check that wireguard-tools is installed. If it is, " +
				"`homebasectl diagnostics` will say more.",
			Status: 500,
		}
	}
	return readVPNStatus(ctx), nil
}

type vpnDeviceRequest struct {
	// Name is what to call the device — "phone", "work laptop". A label, for
	// somebody deciding later which one to remove.
	Name string `json:"name"`
}

func (s *NetworkServices) vpnAddDevice(ctx context.Context, req vpnDeviceRequest) (any, error) {
	name := strings.TrimSpace(req.Name)
	if !validDeviceName.MatchString(name) {
		return nil, &Error{
			Code:    "vpn.invalid_name",
			Message: "That is not a name Homebase can give a device.",
			Detail: "letters, numbers, spaces, dots, dashes and underscores, " +
				"up to 31 characters",
			Recoverable: true,
			Recovery:    "Try something like \"phone\" or \"work laptop\".",
			Status:      400,
		}
	}

	device, err := addDevice(ctx, name)
	if err != nil {
		return nil, &Error{
			Code:        "vpn.add_failed",
			Message:     "Homebase could not add that device.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "If remote access is not set up yet, do that first.",
			Status:      409,
		}
	}
	return device, nil
}

func (s *NetworkServices) vpnRemoveDevice(ctx context.Context, req vpnDeviceRequest) (any, error) {
	name := strings.TrimSpace(req.Name)
	if err := removeDevice(ctx, name); err != nil {
		return nil, &Error{
			Code:        "vpn.remove_failed",
			Message:     "Homebase could not remove that device.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Check the name with `homebasectl vpn list`.",
			Status:      404,
		}
	}
	return readVPNStatus(ctx), nil
}

func short(value string) string {
	if len(value) > 60 {
		return value[:60] + "…"
	}
	return value
}
