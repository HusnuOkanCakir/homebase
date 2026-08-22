package hostd

import (
	"context"
	"os"
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
// called "Alex's iPhone" out of the box, and a device-naming rule that rejects
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
		Name:    "vpn.disable",
		Summary: "Switch remote access off, keeping the keys.",
		// Medium. It closes a way in rather than opening one, and it is
		// reversible without reissuing anything — but it disconnects whoever is
		// using it, possibly somebody away from home relying on it.
		Risk:        RiskMedium,
		Permissions: []string{"network.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     1 * time.Minute,
		Rollback:    "vpn.setup, with the same name",
		Handler:     Typed(services.vpnDisable),
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
		Name:    "vpn.set_dns",
		Summary: "Keep a dynamic DNS name pointing at this house.",
		// Medium. It changes nothing about the machine and everything about
		// whether it can be found — a wrong name means a server nobody can
		// reach, which looks exactly like a server that is switched off.
		Risk:        RiskMedium,
		Permissions: []string{"network.modify"},
		Confirm:     ConfirmNone,
		Timeout:     2 * time.Minute,
		// The token is a credential and the audit log is kept for ever.
		Secret:   []string{"token"},
		Rollback: "vpn.set_dns, with the previous name, or vpn.clear_dns",
		Handler:  Typed(services.vpnSetDNS),
	})

	r.MustRegister(Operation{
		Name:        "vpn.clear_dns",
		Summary:     "Stop keeping a dynamic DNS name up to date.",
		Risk:        RiskMedium,
		Permissions: []string{"network.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     60 * time.Second,
		Rollback:    "vpn.set_dns",
		Handler:     Typed(services.vpnClearDNS),
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

// vpnDisable stops remote access and closes the port behind it.
//
// The keys stay. "Switch this off" and "forget every device I have set up" are
// different intentions, and collapsing them would mean that turning the VPN off
// for an afternoon costs re-issuing a configuration to every phone in the house.
//
// This existed as a promise before it existed as an operation: `vpn.setup`
// named `vpn.disable` as its rollback and nothing implemented it, so there was
// no way to close a port that setup opens to the whole internet.
func (s *NetworkServices) vpnDisable(ctx context.Context, _ struct{}) (any, error) {
	if err := disableVPN(ctx); err != nil {
		return nil, &Error{
			Code:        "vpn.disable_failed",
			Message:     "Homebase could not switch remote access off.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery: "Check `systemctl status wg-quick@wg0`. The port is closed " +
				"either way, so nothing can reach it from outside.",
			Status: 500,
		}
	}
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

// --- Dynamic DNS --------------------------------------------------------------------

type vpnDNSRequest struct {
	// Provider is a word from a fixed table — never a URL, which would be a way
	// to make this machine fetch an arbitrary address as root.
	Provider string `json:"provider"`

	// Name is the name being kept up to date.
	Name string `json:"name"`

	// Token authenticates the update. Declared Secret on the operation, so it is
	// redacted from the audit log.
	Token string `json:"token,omitempty"`
}

func (s *NetworkServices) vpnSetDNS(ctx context.Context, req vpnDNSRequest) (any, error) {
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if _, known := ddnsProviders[provider]; !known {
		known := make([]string, 0, len(ddnsProviders))
		for name := range ddnsProviders {
			known = append(known, name)
		}
		return nil, &Error{
			Code:        "vpn.unknown_dns_provider",
			Message:     "Homebase cannot keep a name up to date with that provider.",
			Detail:      "asked for " + short(req.Provider) + "; it knows " + strings.Join(known, ", "),
			Recoverable: true,
			Recovery:    "Use one Homebase knows, or give this server a fixed address.",
			Status:      400,
		}
	}

	name := strings.TrimSpace(req.Name)
	if !validEndpoint.MatchString(name) {
		return nil, &Error{
			Code:        "vpn.invalid_hostname",
			Message:     "That is not a name a provider could keep up to date.",
			Detail:      "expected a hostname, got " + short(name),
			Recoverable: true,
			Recovery:    "Use the name you registered — for DuckDNS, just the first part.",
			Status:      400,
		}
	}

	// A token is checked for shape but never for content: what a provider
	// accepts is theirs to decide, and guessing here would refuse tokens that
	// work.
	if strings.ContainsAny(req.Token, " \t\n\r&?#") {
		return nil, &Error{
			Code:        "vpn.invalid_token",
			Message:     "That does not look like a token.",
			Detail:      "a token cannot contain spaces or URL punctuation",
			Recoverable: true,
			Recovery:    "Copy it again from the provider's page.",
			Status:      400,
		}
	}

	if err := configureDDNS(ctx, provider, name, req.Token); err != nil {
		return nil, &Error{
			Code:        "vpn.dns_failed",
			Message:     "Homebase could not set up the name.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Check the name and the token, and try again.",
			Status:      500,
		}
	}
	return readDDNSStatus(ctx), nil
}

func (s *NetworkServices) vpnClearDNS(ctx context.Context, _ struct{}) (any, error) {
	if err := disableDDNS(ctx); err != nil && !os.IsNotExist(err) {
		return nil, &Error{
			Code:        "vpn.dns_failed",
			Message:     "Homebase could not stop updating the name.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Try again.",
			Status:      500,
		}
	}
	return readDDNSStatus(ctx), nil
}
