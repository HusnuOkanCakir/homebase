package hostd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Reaching the server from outside the house.
//
// ADR-0019: self-hosted Wireguard, no coordination service, nobody who can
// switch it off. The shape follows the Wi-Fi work — hostd writes the
// configuration and systemd brings the interface up, because
// `homebase-hostd.service` sets RestrictAddressFamilies=AF_UNIX AF_NETLINK and
// keeping that true is worth more than a smaller diff.
//
// Two properties are load-bearing and neither is obvious from the code alone.
//
// **A device's private key is shown once and then discarded.** The server
// generates both halves — it has to, because a phone joins by scanning a QR code
// and that code must contain the key — and then stores only the public half. A
// configuration that is lost cannot be re-shown; the device is removed and added
// again. Every comparable tool keeps the configurations, which means one
// compromise of the server yields every device's identity.
//
// **Reachability is a completed handshake, not a probe.** "Is my router
// forwarding the port?" cannot be answered from inside the house without asking
// somebody else's service. So it is not asked: `wg` already knows whether any
// device has ever handshaked, and a handshake proves the whole path with
// evidence rather than with a guess.

const (
	wireguardDir  = "/etc/wireguard"
	wireguardConf = wireguardDir + "/wg0.conf"
	wireguardUnit = "wg-quick@wg0"

	// The interface's own network. A range from the private space that home
	// routers do not hand out — 192.168.0.x and 192.168.1.x are the two every
	// router in the world uses, and a VPN that collides with the network at the
	// far end is a VPN that works everywhere except the office.
	vpnNetwork = "10.71.0"
	vpnServer  = vpnNetwork + ".1"

	// The port Wireguard listens on. Fixed rather than chosen, because it is the
	// number the user has to type into their router and a number that changes is
	// a support problem for ever.
	vpnPort = 51820

	// Devices, at 10.71.0.2 upwards. A household has phones and laptops, not two
	// hundred of them; the limit exists so a bug cannot walk off the end of the
	// range rather than because anybody would reach it.
	maxDevices = 100
)

// VPNStatus is what remote access is doing.
type VPNStatus struct {
	// Configured is whether Homebase has set Wireguard up at all.
	Configured bool `json:"configured"`

	// Running is whether the interface is actually up. Separate from Configured
	// for the same reason the backup schedule's Enabled is: a configuration that
	// systemd is not acting on is the failure worth seeing.
	Running bool `json:"running"`

	// Hostname is the name devices connect to, and Port the UDP port that has to
	// be forwarded on the router.
	Hostname string `json:"hostname,omitempty"`
	Port     int    `json:"port"`

	Devices []VPNDevice `json:"devices"`

	// EverConnected is whether any device has completed a handshake, ever.
	//
	// This is the reachability check, and it is evidence rather than a probe: a
	// handshake means the name resolved, the router forwarded, and the key was
	// accepted. Until one happens the honest answer is that nothing has
	// connected, which is what the message says.
	EverConnected bool `json:"ever_connected"`

	// DNS is the name that has to keep pointing here. Reported alongside,
	// because they fail together: a name that stopped updating is a VPN nobody
	// can reach, and from outside the two look identical.
	DNS DDNSStatus `json:"dns"`

	Message string `json:"message,omitempty"`
}

// VPNDevice is one thing that can connect.
type VPNDevice struct {
	Name string `json:"name"`

	// Address is its address inside the VPN.
	Address string `json:"address"`

	// PublicKey identifies it. The private half was shown once and is not here,
	// because it is not anywhere.
	PublicKey string `json:"public_key"`

	// LastHandshake is when it last connected, or empty if it never has.
	LastHandshake string `json:"last_handshake,omitempty"`

	// TransferRx and TransferTx are bytes, so somebody can tell a device that
	// connects from a device that connects and does something.
	TransferRx uint64 `json:"transfer_rx,omitempty"`
	TransferTx uint64 `json:"transfer_tx,omitempty"`
}

// NewDevice is a device that has just been created, with the half that is only
// ever returned once.
type NewDevice struct {
	VPNDevice

	// Config is the whole client configuration, including the private key. It is
	// returned exactly once, by the call that created it, and stored nowhere.
	Config string `json:"config"`

	// QRCode is the same configuration as a QR code a phone can scan, drawn with
	// terminal block characters.
	QRCode string `json:"qr_code,omitempty"`

	// QRImage is the same code as a PNG data URI, for a browser. Separate from
	// the terminal drawing because neither can be shown where the other belongs,
	// and a page that renders block characters as text is not a QR code.
	QRImage string `json:"qr_image,omitempty"`

	Message string `json:"message"`
}

// --- Reading -----------------------------------------------------------------------

func readVPNStatus(ctx context.Context) VPNStatus {
	status := VPNStatus{Port: vpnPort}

	raw, err := os.ReadFile(wireguardConf)
	if err != nil {
		status.Message = "Remote access is not set up. Run `homebasectl vpn setup` " +
			"to switch it on."
		return status
	}
	status.Configured = true
	status.Hostname = configuredHostname(string(raw))
	status.Running = unitIsActive(ctx, wireguardUnit)

	status.Devices = devicesFromConfig(string(raw))
	applyLiveState(ctx, status.Devices)
	status.DNS = readDDNSStatus(ctx)

	for _, device := range status.Devices {
		if device.LastHandshake != "" {
			status.EverConnected = true
		}
	}

	switch {
	case !status.Running:
		status.Message = "Remote access is set up but not running. Try " +
			"`homebasectl repair`."
	case status.DNS.Configured && !status.DNS.Working:
		// Before the port message, because a name that is not being updated is
		// the more specific fault and the one that is actually known.
		status.Message = "The name " + status.DNS.Name + " is not being kept up " +
			"to date, so devices may be trying to reach an address this house " +
			"no longer has. " + status.DNS.Detail
	case len(status.Devices) == 0:
		status.Message = "No devices yet. Add one with `homebasectl vpn add-device NAME`."
	case !status.EverConnected:
		status.Message = "No device has connected yet. If one has tried, the most " +
			"likely reason is that UDP port " + strconv.Itoa(vpnPort) +
			" is not forwarded to this server on your router."
	}
	return status
}

// configuredHostname reads back the name devices were told to connect to.
//
// Kept in a comment in the file rather than anywhere else: the server's own
// configuration has no field for it — the endpoint lives in each *client's*
// config — and a second file holding one string is a second file to keep in step.
func configuredHostname(config string) string {
	for _, line := range strings.Split(config, "\n") {
		if rest, found := strings.CutPrefix(strings.TrimSpace(line), "# Endpoint: "); found {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// devicesFromConfig reads the peers out of the server's configuration.
func devicesFromConfig(config string) []VPNDevice {
	var devices []VPNDevice
	var current *VPNDevice

	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.EqualFold(line, "[Peer]"):
			devices = append(devices, VPNDevice{})
			current = &devices[len(devices)-1]
		case current == nil:
			continue
		case strings.HasPrefix(line, "# Name: "):
			current.Name = strings.TrimPrefix(line, "# Name: ")
		case strings.HasPrefix(line, "PublicKey"):
			current.PublicKey = valueOf(line)
		case strings.HasPrefix(line, "AllowedIPs"):
			current.Address = strings.TrimSuffix(valueOf(line), "/32")
		}
	}
	return devices
}

func valueOf(line string) string {
	_, value, _ := strings.Cut(line, "=")
	return strings.TrimSpace(value)
}

// applyLiveState fills in what only the running interface knows.
func applyLiveState(ctx context.Context, devices []VPNDevice) {
	out, err := runWG(ctx, "show", "wg0", "dump")
	if err != nil {
		return
	}

	// `wg show dump` is tab-separated, one line per peer after the first, which
	// describes the interface itself.
	byKey := map[string][]string{}
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if i == 0 {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) >= 7 {
			byKey[fields[0]] = fields
		}
	}

	for i := range devices {
		fields, found := byKey[devices[i].PublicKey]
		if !found {
			continue
		}
		if seconds, err := strconv.ParseInt(fields[4], 10, 64); err == nil && seconds > 0 {
			devices[i].LastHandshake = time.Unix(seconds, 0).UTC().Format(time.RFC3339)
		}
		devices[i].TransferRx, _ = strconv.ParseUint(fields[5], 10, 64)
		devices[i].TransferTx, _ = strconv.ParseUint(fields[6], 10, 64)
	}
}

// --- Setting up --------------------------------------------------------------------

// setUpVPN writes the server configuration and starts the interface.
//
// Idempotent in the way that matters: called again on a machine that already has
// devices, it changes the hostname and leaves every peer alone. Regenerating the
// server key would silently invalidate every device that had been handed out,
// which is the sort of thing somebody discovers on holiday.
func setUpVPN(ctx context.Context, hostname string) error {
	if err := os.MkdirAll(wireguardDir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", wireguardDir, err)
	}

	existing, _ := os.ReadFile(wireguardConf)
	private := serverKeyFrom(string(existing))
	if private == "" {
		generated, err := generateKey(ctx)
		if err != nil {
			return err
		}
		private = generated
	}

	config := renderServerConfig(private, hostname, devicesFromConfig(string(existing)))
	if err := writeWireguardFile(wireguardConf, config); err != nil {
		return err
	}

	// The kernel has to forward between the VPN and the rest of the house, or a
	// connected phone reaches the server and nothing else on the network.
	if err := writeWireguardFile("/etc/sysctl.d/99-homebase-vpn.conf",
		"# Written by Homebase. Remote access needs the kernel to forward\n"+
			"# packets between the VPN and this network.\nnet.ipv4.ip_forward=1\n"); err != nil {
		return err
	}
	_ = runQuietly(ctx, "sysctl", "-p", "/etc/sysctl.d/99-homebase-vpn.conf")

	// The one port in Homebase opened to the whole internet, and it is opened
	// deliberately rather than as a side effect.
	//
	// Everything else that listens is offered to private address ranges only.
	// This is remote access: a service reachable from outside the house is the
	// entire point of it, and a Wireguard port that is closed is a VPN that is
	// configured, running, and impossible to connect to — which looks from a
	// phone exactly like a wrong password.
	//
	// What makes it defensible is what Wireguard does with an unrecognised
	// packet, which is nothing at all: no reply, no banner, no way to tell the
	// port from a closed one without a key.
	openPort(ctx, vpnPort, "udp", "any", "Homebase remote access")

	if err := runSystemctl(ctx, "enable", "--now", wireguardUnit); err != nil {
		return err
	}
	// Restart rather than start: enable --now does nothing if it was already
	// running, and the configuration has just changed.
	return runSystemctl(ctx, "restart", wireguardUnit)
}

// disableVPN stops the tunnel and closes the port.
//
// The port is closed first and unconditionally. If stopping the service then
// fails, what is left is a Wireguard nothing on the internet can reach, which is
// the safe half of the pair — the reverse order would leave the door open after
// somebody was told it had been shut.
func disableVPN(ctx context.Context) error {
	closePort(ctx, vpnPort, "udp", "any", "Homebase remote access")

	if err := runSystemctl(ctx, "disable", "--now", wireguardUnit); err != nil {
		return err
	}
	return nil
}

func serverKeyFrom(config string) string {
	for _, line := range strings.Split(config, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "PrivateKey") {
			return valueOf(line)
		}
		if strings.EqualFold(strings.TrimSpace(line), "[Peer]") {
			break
		}
	}
	return ""
}

func renderServerConfig(private, hostname string, peers []VPNDevice) string {
	var out strings.Builder
	out.WriteString("# Written by Homebase. Add and remove devices with\n")
	out.WriteString("# `homebasectl vpn`, rather than by editing this file.\n")
	out.WriteString("# Endpoint: " + hostname + "\n\n")
	out.WriteString("[Interface]\n")
	out.WriteString("Address = " + vpnServer + "/24\n")
	out.WriteString("ListenPort = " + strconv.Itoa(vpnPort) + "\n")
	out.WriteString("PrivateKey = " + private + "\n")

	for _, peer := range peers {
		out.WriteString("\n[Peer]\n")
		out.WriteString("# Name: " + peer.Name + "\n")
		out.WriteString("PublicKey = " + peer.PublicKey + "\n")
		out.WriteString("AllowedIPs = " + peer.Address + "/32\n")
	}
	return out.String()
}

// --- Devices -----------------------------------------------------------------------

// addDevice creates a keypair, adds the public half to the server, and returns
// the client configuration once.
func addDevice(ctx context.Context, name string) (*NewDevice, error) {
	raw, err := os.ReadFile(wireguardConf)
	if err != nil {
		return nil, fmt.Errorf("remote access is not set up yet")
	}

	existing := devicesFromConfig(string(raw))
	for _, device := range existing {
		if strings.EqualFold(device.Name, name) {
			return nil, fmt.Errorf("there is already a device called %q", device.Name)
		}
	}

	address, err := nextAddress(existing)
	if err != nil {
		return nil, err
	}

	private, err := generateKey(ctx)
	if err != nil {
		return nil, err
	}
	public, err := publicKeyOf(ctx, private)
	if err != nil {
		return nil, err
	}

	device := VPNDevice{Name: name, Address: address, PublicKey: public}
	config := renderServerConfig(serverKeyFrom(string(raw)),
		configuredHostname(string(raw)), append(existing, device))
	if err := writeWireguardFile(wireguardConf, config); err != nil {
		return nil, err
	}
	if err := runSystemctl(ctx, "restart", wireguardUnit); err != nil {
		return nil, err
	}

	client := renderClientConfig(private, serverPublicKey(ctx),
		configuredHostname(string(raw)), address)

	return &NewDevice{
		VPNDevice: device,
		Config:    client,
		QRCode:    qrCode(client),
		QRImage:   qrImage(client),
		// "Scan the code" is not enough, and the first person to use this found
		// out how: a phone's camera decodes a QR code to text, so pointing it at
		// this one displays the private key on screen and does nothing else.
		// The scanner that matters is inside the Wireguard app, and saying which
		// app costs one sentence.
		Message: "Scan this from inside the Wireguard app — Add tunnel, then " +
			"Scan from QR code. A phone's own camera will only show you the text. " +
			"This is the only time the configuration can be shown; if it is lost, " +
			"remove the device and add it again.",
	}, nil
}

func nextAddress(existing []VPNDevice) (string, error) {
	used := map[int]bool{}
	for _, device := range existing {
		parts := strings.Split(device.Address, ".")
		if len(parts) == 4 {
			if last, err := strconv.Atoi(parts[3]); err == nil {
				used[last] = true
			}
		}
	}
	for i := 2; i < 2+maxDevices; i++ {
		if !used[i] {
			return fmt.Sprintf("%s.%d", vpnNetwork, i), nil
		}
	}
	return "", fmt.Errorf("there is no room for another device")
}

func removeDevice(ctx context.Context, name string) error {
	raw, err := os.ReadFile(wireguardConf)
	if err != nil {
		return fmt.Errorf("remote access is not set up yet")
	}

	existing := devicesFromConfig(string(raw))
	kept := make([]VPNDevice, 0, len(existing))
	found := false
	for _, device := range existing {
		if strings.EqualFold(device.Name, name) {
			found = true
			continue
		}
		kept = append(kept, device)
	}
	if !found {
		return fmt.Errorf("there is no device called %q", name)
	}

	config := renderServerConfig(serverKeyFrom(string(raw)),
		configuredHostname(string(raw)), kept)
	if err := writeWireguardFile(wireguardConf, config); err != nil {
		return err
	}
	return runSystemctl(ctx, "restart", wireguardUnit)
}

// renderClientConfig is what goes on the phone.
func renderClientConfig(private, serverPublic, hostname, address string) string {
	var out strings.Builder
	out.WriteString("[Interface]\n")
	out.WriteString("PrivateKey = " + private + "\n")
	out.WriteString("Address = " + address + "/32\n")
	// The server's own resolver, so names inside the house work from outside it.
	out.WriteString("DNS = " + vpnServer + "\n\n")
	out.WriteString("[Peer]\n")
	out.WriteString("PublicKey = " + serverPublic + "\n")
	out.WriteString("Endpoint = " + hostname + ":" + strconv.Itoa(vpnPort) + "\n")
	// Only the house's traffic, not everything.
	//
	// A full tunnel would route the phone's entire internet through a domestic
	// upload link, which is slow, and through a machine in a cupboard, which
	// nobody asked for. Somebody who wants that can edit the line; somebody who
	// does not would never have found it.
	out.WriteString("AllowedIPs = " + vpnNetwork + ".0/24, " + localNetworks() + "\n")
	out.WriteString("PersistentKeepalive = 25\n")
	return out.String()
}

// localNetworks is the house's own address range, so a connected device can
// reach the other things on it and not only the server.
func localNetworks() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "192.168.0.0/16"
	}
	var ranges []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagRunning == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			network, ok := addr.(*net.IPNet)
			if !ok || network.IP.To4() == nil || !network.IP.IsPrivate() {
				continue
			}
			if strings.HasPrefix(network.IP.String(), vpnNetwork) {
				continue
			}
			ranges = append(ranges, network.IP.Mask(network.Mask).String()+
				"/"+strconv.Itoa(maskBits(network)))
		}
	}
	if len(ranges) == 0 {
		return "192.168.0.0/16"
	}
	sort.Strings(ranges)
	return strings.Join(unique(ranges), ", ")
}

func maskBits(network *net.IPNet) int {
	ones, _ := network.Mask.Size()
	return ones
}

func unique(values []string) []string {
	seen := map[string]bool{}
	out := values[:0]
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

// --- Keys and files ----------------------------------------------------------------

func generateKey(ctx context.Context) (string, error) {
	if out, err := runWG(ctx, "genkey"); err == nil {
		return strings.TrimSpace(out), nil
	}
	// `wg` does nothing here that crypto/rand cannot: a Curve25519 private key is
	// 32 random bytes with three bits cleared and one set. Falling back means a
	// machine without wireguard-tools fails at the point it tries to *use* the
	// key, with a message about the missing package, rather than here.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	raw[0] &= 248
	raw[31] &= 127
	raw[31] |= 64
	return base64.StdEncoding.EncodeToString(raw), nil
}

func publicKeyOf(ctx context.Context, private string) (string, error) {
	binary, err := exec.LookPath("wg")
	if err != nil {
		return "", fmt.Errorf("wireguard-tools is not installed on this machine")
	}
	limited, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(limited, binary, "pubkey")
	cmd.Stdin = strings.NewReader(private + "\n")
	cmd.Env = aptEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("deriving the public key: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func serverPublicKey(ctx context.Context) string {
	raw, err := os.ReadFile(wireguardConf)
	if err != nil {
		return ""
	}
	public, err := publicKeyOf(ctx, serverKeyFrom(string(raw)))
	if err != nil {
		return ""
	}
	return public
}

// writeWireguardFile writes root-only.
//
// 0600, because these hold private keys. `wg-quick` refuses to use a
// configuration that is group- or world-readable, which is a good check and not
// the one being relied on here.
func writeWireguardFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".new"
	if err := os.WriteFile(temporary, []byte(content), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Chown(temporary, 0, 0); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return os.Rename(temporary, path)
}

func runWG(ctx context.Context, args ...string) (string, error) {
	binary, err := exec.LookPath("wg")
	if err != nil {
		return "", fmt.Errorf("wireguard-tools is not installed on this machine")
	}
	limited, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(limited, binary, args...)
	cmd.Env = aptEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// qrCode draws the configuration as something a phone can scan.
//
// `qrencode`, from Ubuntu, rather than a QR encoder written here: encoding is
// Reed-Solomon over GF(256) with a mask-selection pass, and hostd carries no
// third-party Go dependencies (ADR-0002), so the alternative is several hundred
// lines of subtle arithmetic whose failure mode is a code that scans to the
// wrong thing.
//
// **The configuration goes in on standard input, never as an argument.** It
// contains the device's private key, and an argument is visible in `ps` output
// to every user on the machine for as long as the process runs.
//
// A machine without qrencode gets no code and the text configuration, which is
// what a laptop wants anyway.
func qrCode(config string) string {
	// ANSIUTF8 draws with half-block characters, so the code is square in a
	// terminal rather than twice as tall as it is wide — which matters, because
	// a stretched code will not scan.
	out, ok := runQrencode(config, "ANSIUTF8")
	if !ok {
		return ""
	}
	return string(out)
}

// qrImage renders the same code as a PNG, for a browser.
//
// A PNG rather than the SVG qrencode can also produce, and the difference is
// not aesthetic: an SVG has to be put into the page as markup, and this one is
// generated from a configuration containing a hostname somebody typed. A PNG
// arrives as a data URI in an `img` tag, where there is nothing to inject into.
// The code is a few kilobytes either way.
func qrImage(config string) string {
	// -s 6 gives a code large enough to scan from a laptop screen at arm's
	// length, which is where this is read from; -m 2 is the quiet border a
	// scanner needs to find the edges at all.
	out, ok := runQrencode(config, "PNG", "-s", "6", "-m", "2")
	if !ok {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(out)
}

// runQrencode encodes a configuration, with it arriving on standard input and
// never as an argument — the configuration contains the device's private key,
// and an argument is readable in /proc by every process on the machine for as
// long as the command runs.
func runQrencode(config, format string, extra ...string) ([]byte, bool) {
	binary, err := exec.LookPath("qrencode")
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	args := append([]string{"-t", format, "-o", "-"}, extra...)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = strings.NewReader(config)
	cmd.Env = aptEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

// --- Dynamic DNS --------------------------------------------------------------------

// A home connection's address changes, and the name has to follow it. Something
// has to be told when it moves, which is inherently a service somebody else runs
// — the one outside dependency in ADR-0019, kept as small as it can be.
const (
	ddnsConfigFile = "/etc/homebase/ddns.conf"
	ddnsResultFile = "/var/lib/homebase/ddns"
	ddnsUnit       = "homebase-ddns.timer"
)

// ddnsProviders is the set of services Homebase can update.
//
// A fixed table, and that is the load-bearing part. The alternative — a URL from
// the caller — would be a way to make the machine fetch an arbitrary address as
// root, which is the generic execution path ADR-0006 exists to prevent wearing a
// different hat. Adding a provider is a change to this table and to
// `packaging/ddns-run`, reviewable in a diff.
var ddnsProviders = map[string]string{
	"duckdns": "DuckDNS",
}

// DDNSStatus is what the name is doing.
type DDNSStatus struct {
	Configured bool   `json:"configured"`
	Provider   string `json:"provider,omitempty"`
	Name       string `json:"name,omitempty"`

	// Enabled is whether systemd is actually keeping it up to date. Read from
	// systemd, the same rule as the backup schedule.
	Enabled bool `json:"enabled"`

	// Working is whether the last update succeeded, and LastChecked when it ran.
	// A name that stopped updating three weeks ago is a server nobody can reach,
	// and it looks identical to one that is fine.
	Working     bool   `json:"working"`
	LastChecked string `json:"last_checked,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

func readDDNSStatus(ctx context.Context) DDNSStatus {
	status := DDNSStatus{}

	values := readResultFile(ddnsConfigFile)
	if values["name"] == "" {
		return status
	}
	status.Configured = true
	status.Provider = values["provider"]
	status.Name = values["name"]
	status.Enabled = unitIsActive(ctx, ddnsUnit)

	result := readResultFile(ddnsResultFile)
	status.Working = result["ok"] == "true"
	status.Detail = result["detail"]
	if seconds, err := strconv.ParseInt(result["checked"], 10, 64); err == nil && seconds > 0 {
		status.LastChecked = time.Unix(seconds, 0).UTC().Format(time.RFC3339)
	}
	return status
}

// configureDDNS records the name and starts keeping it up to date.
func configureDDNS(ctx context.Context, provider, name, token string) error {
	if _, known := ddnsProviders[provider]; !known {
		return fmt.Errorf("Homebase cannot update %q", provider)
	}

	// The token is a credential. Root-only, and not group-readable like the
	// backup schedule — the account that reads this one is root, because the
	// unit that uses it runs as root so the token is not in every user's `ps`.
	config := "# Written by Homebase. Change this with `homebasectl vpn dns`\n" +
		"# rather than by editing the file.\n" +
		"provider=" + provider + "\nname=" + name + "\ntoken=" + token + "\n"
	if err := writeWireguardFile(ddnsConfigFile, config); err != nil {
		return err
	}

	if err := runSystemctl(ctx, "enable", "--now", ddnsUnit); err != nil {
		return err
	}
	// Once now, rather than waiting up to five minutes to find out whether the
	// token was even right.
	if _, err := runUpdateUnit(ctx, "homebase-ddns.service"); err != nil {
		// Not fatal: the configuration is recorded and the timer will try again.
		// What matters is that the status reports it, which it does.
		return nil
	}
	return nil
}

func disableDDNS(ctx context.Context) error {
	if err := runSystemctl(ctx, "disable", "--now", ddnsUnit); err != nil {
		return err
	}
	return os.Remove(ddnsConfigFile)
}
