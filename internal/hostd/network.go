package hostd

import (
	"bufio"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// What the network actually looks like, read from the kernel.
//
// Same reasoning as everywhere else in hostd: parsing `ip` or `nmcli` means
// depending on their formatting and their presence, and it means a root process
// spawning shells. /proc and /sys answer these questions directly.
//
// The point of this is not the numbers. It is being able to say which of three
// different things is wrong when somebody cannot reach their server, because
// they look identical from a browser that will not load:
//
//	no cable            — the machine has no address at all
//	no internet         — the machine is on the network, the world is not there
//	nothing wrong here  — the machine is fine and the problem is at the other end
//
// A server that cannot tell those apart leaves its owner power-cycling a router
// for an hour over a problem with their phone's Wi-Fi.

// NetworkStatus is everything Homebase knows about how it is connected.
type NetworkStatus struct {
	// Hostname and MDNSName are how the machine can be reached by name.
	Hostname  string `json:"hostname"`
	MDNSName  string `json:"mdns_name"`
	MDNSWorks bool   `json:"mdns_works"`

	Interfaces []NetworkInterface `json:"interfaces"`

	// Gateway is the router this machine sends everything else through. Empty
	// means there is no route off this machine, which is a different fault from
	// having a route that does not work.
	Gateway string `json:"gateway,omitempty"`

	// Nameservers are what it asks to turn names into addresses.
	Nameservers []string `json:"nameservers,omitempty"`

	// MissingInterfaces are named by the network configuration and are not on
	// this machine.
	//
	// Almost always a renamed card rather than a removed one: the name comes
	// from the PCI slot, and a slot number moves when the enumeration does. It
	// is reported because the symptom otherwise is a server that boots
	// perfectly and cannot be reached, with nothing anywhere to say why.
	MissingInterfaces []string `json:"missing_interfaces,omitempty"`
}

// NetworkInterface is one way this machine is attached to a network.
type NetworkInterface struct {
	Name string `json:"name"`

	// Kind is what a person would call it: "ethernet", "wireless", or the
	// loopback the machine talks to itself on.
	Kind string `json:"kind"`

	// Up is the kernel's operational state, which is the honest answer to "is
	// there a cable in it": a socket with nothing plugged in reports down.
	Up bool `json:"up"`

	Addresses []string `json:"addresses,omitempty"`

	// MAC identifies the hardware. Shown because it is what a router's list of
	// connected devices matches against, and finding the server in that list is
	// a real thing people have to do — and because it is what a wake-up packet
	// is addressed to.
	MAC string `json:"mac,omitempty"`

	// WakeOnLAN is whether this card will start the machine when a magic packet
	// arrives.
	//
	// Reported because nothing on a sleeping machine can run a command, so this
	// is the one thing about waking the server that has to be known *before* it
	// goes to sleep. A laptop in a cupboard that cannot be woken is one somebody
	// has to walk to.
	WakeOnLAN bool `json:"wake_on_lan"`

	// WakeOnLANSupported is whether the card *could* be woken, whether or not it
	// currently is.
	//
	// Three states rather than two, and the middle one is the only actionable
	// one. The first real laptop reported `Supports Wake-on: pumbg` and
	// `Wake-on: d` — the hardware does magic packets and the setting is off. To
	// say "cannot be woken" there is accurate and useless: what somebody needs
	// to know is that it could be, and that switching it on is a thing Homebase
	// can do for them.
	WakeOnLANSupported bool `json:"wake_on_lan_supported"`

	// WakeOnLANKnown is whether Homebase could find out at all.
	//
	// A third state rather than folding failure into "not supported", which is
	// what the first implementation did — and it did it on every machine, since
	// it read the setting through an ioctl on an AF_INET socket that this
	// process is forbidden to open. It is answered over netlink now, so on a
	// real installation this is true; a container, a kernel older than 5.6 or an
	// interface with no driver behind it can still leave it false, and there the
	// only honest report is that Homebase does not know.
	WakeOnLANKnown bool `json:"wake_on_lan_known"`
}

// Reachable reports whether this interface is carrying an address.
func (n NetworkInterface) Reachable() bool { return n.Up && len(n.Addresses) > 0 }

// configuredButAbsent names interfaces the network configuration asks for and
// this machine does not have.
//
// The failure it exists for took a working server off the network for an evening.
// A wireless card was not detected on one boot, which moved the ethernet from
// PCI slot 5 to slot 4 and renamed it — and the configuration named the old name,
// so nothing was brought up, no address was obtained, and the machine could not
// be reached at all. It booted perfectly. The card was fine. The only way to
// find out was a keyboard, a screen, and knowing to compare two names.
//
// Homebase writes a configuration that matches on the kind of device rather than
// the name, so it cannot happen through anything Homebase installed. This is for
// the other cases: a machine somebody configured by hand, an upgrade from before
// that fix, or a distribution that put its own file back.
func configuredButAbsent(netplanDir string, present []NetworkInterface) []string {
	files, err := filepath.Glob(filepath.Join(netplanDir, "*.yaml"))
	if err != nil {
		return nil
	}
	have := map[string]bool{}
	for _, iface := range present {
		have[iface.Name] = true
	}

	var missing []string
	seen := map[string]bool{}
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, name := range namedInterfaces(string(content)) {
			if have[name] || seen[name] {
				continue
			}
			seen[name] = true
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// namedInterfaces pulls interface names out of a netplan file.
//
// Deliberately crude — a key at any depth whose name looks like an interface and
// which is not inside a `match:` block. A YAML parser would be more correct and
// would need a dependency hostd is not allowed (ADR-0002), and being wrong here
// costs a diagnostic message rather than a decision: nothing acts on this.
func namedInterfaces(content string) []string {
	var found []string
	inMatch := false
	matchIndent := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))

		// A name under `match:` is a pattern, not a device — `name: "en*"` is
		// the fix for this whole problem and must not be reported as missing.
		if inMatch && indent <= matchIndent {
			inMatch = false
		}
		if strings.HasPrefix(trimmed, "match:") {
			inMatch, matchIndent = true, indent
			continue
		}
		if inMatch {
			continue
		}

		key, _, isKey := strings.Cut(trimmed, ":")
		if !isKey {
			continue
		}
		if looksLikeInterface(strings.TrimSpace(key)) {
			found = append(found, strings.TrimSpace(key))
		}
	}
	return found
}

// looksLikeInterface recognises the shapes the kernel produces — enp5s0, eth0,
// wlp4s0, eno1 — without matching every other key in a netplan file.
func looksLikeInterface(key string) bool {
	if len(key) < 3 || strings.ContainsAny(key, " \"'#") {
		return false
	}
	for _, prefix := range []string{"enp", "eno", "ens", "eth", "wlp", "wlan", "wls"} {
		if strings.HasPrefix(key, prefix) && len(key) > len(prefix) {
			// A digit somewhere after the prefix, which every kernel name has
			// and words like "ethernets" do not.
			return strings.ContainsAny(key[len(prefix):], "0123456789")
		}
	}
	return false
}

const (
	sysClassNet    = "/sys/class/net"
	procNetRoute   = "/proc/net/route"
	resolvConfPath = "/etc/resolv.conf"
	// netplanConfigDir is where the network configuration lives. Read only to
	// notice a name in it that this machine does not have.
	netplanConfigDir = "/etc/netplan"
)

// netScanner is where the network state is read from. Fields rather than
// constants so tests can point it at a tree they built — the alternative is
// testing against whatever network the test happens to run on, which tests
// nothing repeatable.
type netScanner struct {
	classNet   string
	routes     string
	resolvConf string
	// netplanDir is where the network configuration lives, so that a test can
	// point it at a tree it wrote rather than at the machine's own.
	netplanDir string
	hostname   func() (string, error)
	interfaces func() ([]net.Interface, error)
	addrsOf    func(net.Interface) ([]net.Addr, error)
}

func systemNetScanner() netScanner {
	return netScanner{
		classNet:   sysClassNet,
		routes:     procNetRoute,
		resolvConf: resolvConfPath,
		netplanDir: netplanConfigDir,
		hostname:   os.Hostname,
		interfaces: net.Interfaces,
		addrsOf:    func(i net.Interface) ([]net.Addr, error) { return i.Addrs() },
	}
}

// ReadNetworkStatus reports how this machine is connected.
func ReadNetworkStatus() NetworkStatus { return systemNetScanner().status() }

func (s netScanner) status() NetworkStatus {
	// An empty list, never nil — a nil slice encodes as JSON `null` and breaks
	// the first client that indexes into it. See readVPNStatus.
	status := NetworkStatus{Interfaces: []NetworkInterface{}}

	if name, err := s.hostname(); err == nil {
		status.Hostname = strings.TrimSuffix(strings.ToLower(name), ".local")
		status.MDNSName = status.Hostname + ".local"
	}

	interfaces, err := s.interfaces()
	if err == nil {
		for _, iface := range interfaces {
			status.Interfaces = append(status.Interfaces, s.describe(iface))
		}
	}

	status.Gateway = s.defaultGateway()
	status.Nameservers = s.nameservers()

	// A name in the configuration that this machine does not have. Reported
	// last, because it is only interesting once everything real has been listed.
	status.MissingInterfaces = configuredButAbsent(s.netplanDir, status.Interfaces)
	return status
}

func (s netScanner) describe(iface net.Interface) NetworkInterface {
	described := NetworkInterface{
		Name: iface.Name,
		Kind: s.kindOf(iface),
		// The kernel's operational state rather than the administrative one:
		// an interface can be "up" in the sense of being configured while
		// nothing is plugged into it.
		Up:  iface.Flags&net.FlagRunning != 0,
		MAC: iface.HardwareAddr.String(),
	}

	described.WakeOnLAN, described.WakeOnLANSupported, described.WakeOnLANKnown =
		readWakeOnLAN(iface.Name)

	addrs, err := s.addrsOf(iface)
	if err != nil {
		return described
	}
	for _, addr := range addrs {
		network, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		// Link-local addresses are left out. A machine that has only one has
		// not been given an address by anybody, and listing it would let
		// "169.254.x.x" read as a working network to somebody who has no way to
		// know otherwise.
		if network.IP.IsLinkLocalUnicast() {
			continue
		}
		described.Addresses = append(described.Addresses, network.IP.String())
	}
	return described
}

// kindOf says what sort of connection this is, in the words a person uses.
func (s netScanner) kindOf(iface net.Interface) string {
	if iface.Flags&net.FlagLoopback != 0 {
		return "loopback"
	}
	// A wireless interface has a `wireless` directory in sysfs. This is what
	// every tool that reports "Wi-Fi" is actually looking at.
	if _, err := os.Stat(filepath.Join(s.classNet, iface.Name, "wireless")); err == nil {
		return "wireless"
	}
	if strings.HasPrefix(iface.Name, "docker") || strings.HasPrefix(iface.Name, "br-") ||
		strings.HasPrefix(iface.Name, "veth") {
		return "container"
	}
	return "ethernet"
}

// defaultGateway reads the route everything else goes through.
func (s netScanner) defaultGateway() string {
	file, err := os.Open(s.routes)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Scan() // the header

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		// A destination of 0.0.0.0 is the default route. The addresses are
		// little-endian hexadecimal, which is why this is parsed rather than
		// read.
		if fields[1] != "00000000" {
			continue
		}
		value, err := strconv.ParseUint(fields[2], 16, 32)
		if err != nil {
			continue
		}
		address := make(net.IP, 4)
		binary.LittleEndian.PutUint32(address, uint32(value))
		return address.String()
	}
	return ""
}

func (s netScanner) nameservers() []string {
	file, err := os.Open(s.resolvConf)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()

	var servers []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" {
			servers = append(servers, fields[1])
		}
	}
	return servers
}
