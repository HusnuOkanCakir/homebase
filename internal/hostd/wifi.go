package hostd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Joining a wireless network.
//
// This was deferred out of Milestone 7 for one reason, and the shape of the file
// follows from it: **the failure mode is a server that can no longer be reached
// to fix it.** Every other operation in Homebase fails by not working. This one
// can fail by making the machine unreachable from the browser configuring it, on
// an appliance whose owner has no terminal and no monitor.
//
// Four decisions come out of that.
//
// **The Ethernet configuration is never touched.** Homebase writes exactly one
// file, and reads none of the others. If somebody is setting up Wi-Fi over a
// cable — the ordinary case — the cable is still there afterwards no matter how
// badly the wireless goes.
//
// **Wireless gets a worse route metric than the cable.** Both can be connected
// at once, and the cable should win: it is faster, and it is the one that was
// already working.
//
// **Applying is rolled back if it does not work.** The previous file is kept,
// the new one applied, and the interface given a bounded time to come up with an
// address. If it does not, the previous state is put back and netplan applied
// again. The same shape as the update path, for the same reason.
//
// **The passphrase is never read back.** It goes into a root-only file and is
// returned by no operation, including the one that reports the status.
//
// netplan rather than NetworkManager, because netplan is what an Ubuntu Server
// install already uses. Running two things that both believe they own the
// network is a well-known way to lose it.

const (
	wifiNetplanFile = "/etc/netplan/90-homebase-wifi.yaml"

	// The cable's metric is left as whatever configured it; wireless is pushed
	// well below, so a machine with both prefers the cable.
	wirelessRouteMetric = 600

	// WPA2 personal. Fixed by the standard rather than chosen here.
	minPassphrase = 8
	maxPassphrase = 63

	// An SSID is at most 32 bytes. Longer is not a long name; it is a mistake,
	// or an attempt at one.
	maxSSID = 32
)

// WifiNetwork is one network the machine can see.
type WifiNetwork struct {
	SSID string `json:"ssid"`

	// Signal is the strength in dBm — negative, closer to zero is better.
	Signal int `json:"signal"`

	// Bars is that turned into something a person can act on, 0 to 4. The dBm
	// figure is reported too, because it is what somebody moving the server
	// around the house actually watches.
	Bars int `json:"bars"`

	// Security is "open", "wep", "wpa" or "wpa3" — in the words needed to warn
	// somebody, not to configure anything.
	Security string `json:"security"`

	// Current is whether this is the one the machine is on.
	Current bool `json:"current"`
}

// WifiStatus is what this machine's wireless is doing.
type WifiStatus struct {
	// Available is whether there is a wireless card at all. False on much of the
	// hardware Homebase runs on, and the first thing a screen needs to know.
	Available bool `json:"available"`

	Interface string `json:"interface,omitempty"`

	Connected bool   `json:"connected"`
	SSID      string `json:"ssid,omitempty"`

	Addresses []string `json:"addresses,omitempty"`
	Signal    int      `json:"signal,omitempty"`
	Bars      int      `json:"bars,omitempty"`

	// Configured is whether Homebase has written a wireless configuration.
	// Separate from Connected: a configured network that is not joined is a
	// state somebody needs to see, and it looks like nothing at all otherwise.
	Configured bool `json:"configured"`

	// HasWiredConnection is whether a cable is also carrying an address.
	//
	// The field the screen uses to decide how frightening to be. Setting up
	// Wi-Fi over a cable is safe, because the cable survives a failure. Changing
	// it while wireless is the only connection is the case that can strand the
	// machine, and somebody should be told which one they are in.
	HasWiredConnection bool `json:"has_wired_connection"`
}

func readWifiStatus(ctx context.Context) WifiStatus {
	status := WifiStatus{}

	network := ReadNetworkStatus()
	for _, iface := range network.Interfaces {
		switch iface.Kind {
		case "wireless":
			// One card is assumed. A home server with two is not a case worth
			// guessing about, and picking the wrong one silently would be worse
			// than not supporting it.
			if status.Interface == "" {
				status.Available = true
				status.Interface = iface.Name
				status.Addresses = iface.Addresses
			}
		case "ethernet":
			if iface.Reachable() {
				status.HasWiredConnection = true
			}
		}
	}

	if _, err := os.Stat(wifiNetplanFile); err == nil {
		status.Configured = true
	}

	if !status.Available {
		return status
	}

	// Association is asked of the kernel through `iw`, not inferred from having
	// an address. An interface can hold a stale address after the network has
	// gone, and reporting that as connected is how somebody spends an evening
	// wondering why nothing loads.
	if link := iwLink(ctx, status.Interface); link.ssid != "" {
		status.Connected = true
		status.SSID = link.ssid
		status.Signal = link.signal
		status.Bars = bars(link.signal)
	}
	return status
}

type wifiLink struct {
	ssid   string
	signal int
}

func iwLink(ctx context.Context, iface string) wifiLink {
	out, err := runIw(ctx, "dev", iface, "link")
	if err != nil {
		return wifiLink{}
	}

	link := wifiLink{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "SSID: "):
			link.ssid = strings.TrimPrefix(line, "SSID: ")
		case strings.HasPrefix(line, "signal: "):
			fields := strings.Fields(strings.TrimPrefix(line, "signal: "))
			if len(fields) > 0 {
				if value, err := strconv.Atoi(fields[0]); err == nil {
					link.signal = value
				}
			}
		}
	}
	return link
}

// scanForNetworks lists what the card can hear.
func scanForNetworks(ctx context.Context, iface string) ([]WifiNetwork, error) {
	// The interface has to be up to scan, and it may not be if no network has
	// ever been configured. Bringing it up changes nothing about what it joins.
	_ = runQuietly(ctx, "ip", "link", "set", iface, "up")

	out, err := runIw(ctx, "dev", iface, "scan")
	if err != nil {
		return nil, err
	}

	byName := map[string]WifiNetwork{}
	current := WifiNetwork{Signal: -100}
	flush := func() {
		if current.SSID == "" {
			return
		}
		if current.Security == "" {
			current.Security = "open"
		}
		current.Bars = bars(current.Signal)
		// The strongest sighting wins. A network with three access points
		// appears three times, and showing it three times is a list nobody can
		// use.
		if existing, seen := byName[current.SSID]; !seen || current.Signal > existing.Signal {
			byName[current.SSID] = current
		}
	}

	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "BSS "):
			flush()
			current = WifiNetwork{Signal: -100}
		case strings.HasPrefix(line, "SSID: "):
			current.SSID = strings.TrimPrefix(line, "SSID: ")
		case strings.HasPrefix(line, "signal: "):
			fields := strings.Fields(strings.TrimPrefix(line, "signal: "))
			if len(fields) > 0 {
				if value, err := strconv.ParseFloat(fields[0], 64); err == nil {
					current.Signal = int(value)
				}
			}
		case strings.Contains(line, "Authentication suites: SAE"):
			current.Security = "wpa3"
		case strings.HasPrefix(line, "RSN:"), strings.HasPrefix(line, "WPA:"):
			if current.Security != "wpa3" {
				current.Security = "wpa"
			}
		case strings.HasPrefix(line, "capability:") && strings.Contains(line, "Privacy"):
			if current.Security == "" {
				current.Security = "wep"
			}
		}
	}
	flush()

	// A hidden network broadcasts an empty SSID, and is left out rather than
	// listed as a blank row: it cannot be joined by picking it, and a row that
	// does nothing is worse than no row.
	networks := make([]WifiNetwork, 0, len(byName))
	for _, network := range byName {
		networks = append(networks, network)
	}

	joined := readWifiStatus(ctx).SSID
	for i := range networks {
		networks[i].Current = networks[i].SSID == joined
	}

	// Strongest first: the order somebody scans with their eyes, and their own
	// network is almost always at the top of it.
	sort.Slice(networks, func(a, b int) bool {
		return networks[a].Signal > networks[b].Signal
	})
	return networks, nil
}

// bars turns dBm into something a person can act on.
//
// The conventional thresholds. Precision here would be false: the number moves
// several dBm as somebody walks past the machine.
func bars(signal int) int {
	switch {
	case signal == 0:
		return 0
	case signal >= -55:
		return 4
	case signal >= -67:
		return 3
	case signal >= -75:
		return 2
	default:
		return 1
	}
}

// --- Joining ---------------------------------------------------------------------

// The netplan document, as structs.
//
// Written with encoding/json rather than assembled as text, and that is a
// security property rather than a convenience. A netplan file is YAML, JSON is
// valid YAML, and the encoder escapes an SSID or a passphrase containing quotes,
// newlines or colons by construction. Hand-formatting somebody's Wi-Fi password
// into a configuration file is the kind of quoting that is right until the day
// it is not.
type wifiConfig struct {
	Network wifiNetworkSection `json:"network"`
}

type wifiNetworkSection struct {
	Version int                   `json:"version"`
	Wifis   map[string]wifiDevice `json:"wifis"`
}

type wifiDevice struct {
	DHCP4 bool `json:"dhcp4"`
	DHCP6 bool `json:"dhcp6"`

	// The metric is why this override exists: a machine with a cable and Wi-Fi
	// should send everything down the cable.
	DHCP4Overrides wifiRouteOverrides `json:"dhcp4-overrides"`

	// Optional, so a boot does not wait for a network that may not be in range.
	// Without it, a server carried to a different room adds a two-minute pause
	// to every start-up.
	Optional bool `json:"optional"`

	AccessPoints map[string]wifiAccessPoint `json:"access-points"`
}

type wifiRouteOverrides struct {
	RouteMetric int `json:"route-metric"`
}

type wifiAccessPoint struct {
	Password string `json:"password,omitempty"`
}

func renderWifiConfig(iface, ssid, passphrase string) ([]byte, error) {
	device := wifiDevice{
		DHCP4:          true,
		DHCP6:          false,
		DHCP4Overrides: wifiRouteOverrides{RouteMetric: wirelessRouteMetric},
		Optional:       true,
		AccessPoints:   map[string]wifiAccessPoint{ssid: {Password: passphrase}},
	}

	encoded, err := json.MarshalIndent(wifiConfig{
		Network: wifiNetworkSection{
			Version: 2,
			Wifis:   map[string]wifiDevice{iface: device},
		},
	}, "", "  ")
	if err != nil {
		return nil, err
	}

	// A comment cannot be expressed in JSON, and somebody will find this file
	// eventually — so it is prepended. YAML ignores it, and everything after it
	// is still machine-generated.
	header := "# Written by Homebase. Change the wireless network from the\n" +
		"# dashboard rather than by editing this file.\n" +
		"#\n" +
		"# This is JSON, which is valid YAML. It is produced by an encoder so that\n" +
		"# a network name or password containing quotes or newlines cannot change\n" +
		"# the shape of the document.\n"
	return append([]byte(header), encoded...), nil
}

// validateWifiRequest checks what a caller sent before any of it reaches a file.
func validateWifiRequest(ssid, passphrase string) error {
	switch {
	case ssid == "":
		return fmt.Errorf("no network name was given")
	case len(ssid) > maxSSID:
		return fmt.Errorf("a network name is at most %d characters; that one is %d",
			maxSSID, len(ssid))
	case strings.ContainsAny(ssid, "\x00\n\r"):
		return fmt.Errorf("that network name contains characters a name cannot contain")
	}

	// An open network is allowed — plenty exist — but a passphrase, if there is
	// one, has to be one WPA2 can accept. netplan writes a shorter one happily,
	// and wpa_supplicant then fails in a way nobody can read.
	if passphrase == "" {
		return nil
	}
	if len(passphrase) < minPassphrase || len(passphrase) > maxPassphrase {
		return fmt.Errorf("a Wi-Fi password is between %d and %d characters; that one is %d",
			minPassphrase, maxPassphrase, len(passphrase))
	}
	if strings.ContainsAny(passphrase, "\x00\n\r") {
		return fmt.Errorf("that password contains characters a password cannot contain")
	}
	return nil
}

// joinNetwork writes the configuration, applies it, and puts the previous state
// back if the machine does not come up on the new network.
//
// The rollback is the point. Everything else here is a file being written.
func joinNetwork(ctx context.Context, iface, ssid, passphrase string) (WifiStatus, error) {
	rendered, err := renderWifiConfig(iface, ssid, passphrase)
	if err != nil {
		return WifiStatus{}, err
	}

	// What to go back to. A machine with no wireless configured goes back to
	// having none; one that was already on a network goes back to that network,
	// byte for byte, rather than to something regenerated from what was read.
	previous, notConfiguredYet := os.ReadFile(wifiNetplanFile)
	restore := func() {
		if notConfiguredYet != nil {
			_ = os.Remove(wifiNetplanFile)
		} else {
			_ = writeWifiFile(previous)
		}
		_ = netplanApply(ctx)
	}

	if err := writeWifiFile(rendered); err != nil {
		return WifiStatus{}, errCannotConfigure{err}
	}

	if err := netplanApply(ctx); err != nil {
		restore()
		return WifiStatus{}, errCannotConfigure{err}
	}

	// Association and DHCP, with a bound. Wireless takes seconds rather than
	// milliseconds, and a machine that has not joined in this long is not going
	// to.
	if status, ok := waitForWireless(ctx, ssid, 45*time.Second); ok {
		return status, nil
	}

	restore()
	return WifiStatus{}, fmt.Errorf("the server did not join %q", ssid)
}

// errCannotConfigure separates a settings file that could not be written from a
// network that would not let the machine in.
//
// They are entirely different faults with entirely different remedies, and the
// first version of this returned both as "the most likely reason is the
// password". The wireless test then passed its wrong-password case for the wrong
// reason: the write was failing on a read-only /etc/netplan, so nothing about
// passwords was ever exercised while every assertion held.
type errCannotConfigure struct{ err error }

func (e errCannotConfigure) Error() string { return e.err.Error() }
func (e errCannotConfigure) Unwrap() error { return e.err }

// waitForWireless polls until the card has joined the named network and been
// given an address.
//
// Both conditions, because they fail separately and mean different things: a
// wrong password fails to associate, and a network with no DHCP server
// associates and then has no address. Either leaves a machine that cannot be
// reached over that network, which is what the caller is about to be told.
func waitForWireless(ctx context.Context, ssid string, within time.Duration) (WifiStatus, bool) {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		status := readWifiStatus(ctx)
		if status.SSID == ssid && len(status.Addresses) > 0 {
			return status, true
		}
		select {
		case <-ctx.Done():
			return status, false
		case <-time.After(2 * time.Second):
		}
	}
	return readWifiStatus(ctx), false
}

// writeWifiFile writes the netplan file root-only.
//
// 0600, because it holds somebody's Wi-Fi password in plain text. netplan itself
// warns about permissions on files under /etc/netplan for this reason, and a
// warning in a log nobody reads is not the mechanism worth relying on.
func writeWifiFile(content []byte) error {
	temporary := wifiNetplanFile + ".new"
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		return fmt.Errorf("writing the network settings: %w", err)
	}
	if err := os.Chown(temporary, 0, 0); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("writing the network settings: %w", err)
	}
	return os.Rename(temporary, wifiNetplanFile)
}

func forgetNetwork(ctx context.Context) error {
	if err := os.Remove(wifiNetplanFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return netplanApply(ctx)
}

func netplanApply(ctx context.Context) error {
	binary, err := exec.LookPath("netplan")
	if err != nil {
		return fmt.Errorf("netplan is not on this machine")
	}
	limited, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(limited, binary, "apply")
	cmd.Env = aptEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

func runIw(ctx context.Context, args ...string) (string, error) {
	binary, err := exec.LookPath("iw")
	if err != nil {
		return "", fmt.Errorf("iw is not on this machine")
	}
	// Scanning takes a few seconds on a quiet band and longer on a busy one.
	limited, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(limited, binary, args...)
	cmd.Env = aptEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func runQuietly(ctx context.Context, name string, args ...string) error {
	binary, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	limited, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(limited, binary, args...)
	cmd.Env = aptEnv()
	return cmd.Run()
}
