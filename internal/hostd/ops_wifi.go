package hostd

import (
	"context"
	"errors"
	"strings"
	"time"
)

// The wireless operations.
//
// Registered separately from the read-only network ones because they are the
// first thing in Homebase that can change how the machine is reached. The
// grading reflects that rather than the amount of work done: joining a network
// is `RiskHigh` even though it writes one file, because the cost of getting it
// wrong is an appliance nobody can reach to put right.

// RegisterWifiOperations adds the wireless domain to a registry.
func RegisterWifiOperations(r *Registry, services *NetworkServices) {
	r.MustRegister(Operation{
		Name:    "network.wifi_status",
		Summary: "Report whether this server has wireless, and what it has joined.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 30 * time.Second,
		Handler: Typed(services.wifiStatus),
	})

	r.MustRegister(Operation{
		Name:    "network.wifi_scan",
		Summary: "List the wireless networks this server can see.",
		// A read, and it is worth noting what it is not: scanning does not join
		// anything and does not disturb an existing connection.
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 60 * time.Second,
		Handler: Typed(services.wifiScan),
	})

	r.MustRegister(Operation{
		Name:    "network.wifi_connect",
		Summary: "Join a wireless network.",
		// High. Nothing is destroyed, and that is not the measure — this is the
		// operation that can leave a server unreachable from the browser that
		// asked for it, on a machine with no keyboard and no screen.
		Risk:        RiskHigh,
		Permissions: []string{"network.modify"},
		// Explicit, because the sentence somebody needs to have read is about
		// what happens if the password is wrong, and a client that could send a
		// bare `true` would not have shown it to them.
		Confirm:  ConfirmExplicit,
		Timeout:  3 * time.Minute,
		Rollback: "network.wifi_connect, with the previous network — done automatically if the new one does not come up",
		// The first operation in Homebase that takes a secret by value. The
		// audit log records what was asked for and is kept for ever, so the
		// passphrase is declared here and never reaches it.
		Secret:  []string{"passphrase"},
		Handler: Typed(services.wifiConnect),
	})

	r.MustRegister(Operation{
		Name:    "network.wifi_forget",
		Summary: "Stop using wireless on this server.",
		// Medium: it can disconnect a machine that is only on Wi-Fi, and it is
		// reversible only by somebody who can still reach it.
		Risk:        RiskMedium,
		Permissions: []string{"network.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     2 * time.Minute,
		Rollback:    "network.wifi_connect, with the network that was forgotten",
		Handler:     Typed(services.wifiForget),
	})
}

func (s *NetworkServices) wifiStatus(ctx context.Context, _ struct{}) (any, error) {
	return readWifiStatus(ctx), nil
}

type wifiScanResult struct {
	Networks []WifiNetwork `json:"networks"`

	// Message is what to say when the list is empty, which is a different thing
	// from an error and needs different words.
	Message string `json:"message,omitempty"`
}

func (s *NetworkServices) wifiScan(ctx context.Context, _ struct{}) (any, error) {
	status := readWifiStatus(ctx)
	if !status.Available {
		return nil, noWirelessCard()
	}

	networks, err := scanForNetworks(ctx, status.Interface)
	if err != nil {
		return nil, &Error{
			Code:        "wifi.scan_failed",
			Message:     "Homebase could not look for wireless networks.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Try again. If it keeps failing, this server's wireless card may not be working.",
			Status:      500,
		}
	}

	result := wifiScanResult{Networks: networks}
	if len(networks) == 0 {
		result.Message = "No wireless networks are in range. Move the server closer " +
			"to your router, or use a network cable."
	}
	return result, nil
}

type wifiConnectRequest struct {
	SSID string `json:"ssid"`

	// Passphrase is empty for an open network. It is never read back: no
	// operation returns it, and it is written to a root-only file.
	Passphrase string `json:"passphrase,omitempty"`
}

func (s *NetworkServices) wifiConnect(ctx context.Context, req wifiConnectRequest) (any, error) {
	status := readWifiStatus(ctx)
	if !status.Available {
		return nil, noWirelessCard()
	}

	ssid := strings.TrimSpace(req.SSID)
	if err := validateWifiRequest(ssid, req.Passphrase); err != nil {
		return nil, &Error{
			Code:        "wifi.invalid_request",
			Message:     "Homebase cannot join that network.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Check the network name and password and try again.",
			Status:      400,
		}
	}

	joined, err := joinNetwork(ctx, status.Interface, ssid, req.Passphrase)
	if err != nil {
		// Two faults, two messages. A settings file that could not be written is
		// a broken installation; a network that would not let the machine in is
		// almost always a wrong password. Telling somebody to check their
		// password when the real problem is a read-only filesystem sends them
		// round a loop that cannot end — which is exactly what the first version
		// of this did, and what let the wireless test pass its wrong-password
		// case without ever exercising a password.
		var configuration errCannotConfigure
		if errors.As(err, &configuration) {
			return nil, &Error{
				Code:        "wifi.cannot_configure",
				Message:     "Homebase could not change this server's network settings.",
				Detail:      err.Error(),
				Recoverable: true,
				Recovery: "This is a problem with the server rather than with the " +
					"network or the password. Nothing has changed. Try 'Check and " +
					"repair' under Something's wrong.",
				Status: 500,
			}
		}

		// The previous configuration is already back by the time this returns.
		// Saying so is the difference between a person trying again and a person
		// going to look for a monitor.
		return nil, &Error{
			Code:        "wifi.did_not_join",
			Message:     "This server could not join " + ssid + ".",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery: "The most likely reason is the password. Nothing has changed — " +
				"the server is connected exactly as it was before. Check the " +
				"password and try again.",
			Status: 409,
		}
	}
	return joined, nil
}

func (s *NetworkServices) wifiForget(ctx context.Context, _ struct{}) (any, error) {
	status := readWifiStatus(ctx)
	if !status.Configured {
		return readWifiStatus(ctx), nil
	}

	if err := forgetNetwork(ctx); err != nil {
		return nil, &Error{
			Code:        "wifi.forget_failed",
			Message:     "Homebase could not stop using wireless.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Try again.",
			Status:      500,
		}
	}
	return readWifiStatus(ctx), nil
}

// noWirelessCard is the answer on most of the hardware Homebase runs on.
//
// Its own error rather than an empty list, because "there are no networks" and
// "this machine cannot see networks at all" send somebody to entirely different
// places — one to move the router, one to buy an adapter or use a cable.
func noWirelessCard() *Error {
	return &Error{
		Code:        "wifi.no_adapter",
		Message:     "This server does not have wireless.",
		Detail:      "no interface on this machine reports as wireless",
		Recoverable: false,
		Recovery: "Connect it to your router with a network cable. Most old laptops " +
			"do have wireless, so if this one should, its card may need a driver " +
			"Ubuntu does not include.",
		Status: 404,
	}
}
