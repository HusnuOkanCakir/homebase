package hostd

import (
	"bufio"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
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
}

// Reachable reports whether this interface is carrying an address.
func (n NetworkInterface) Reachable() bool { return n.Up && len(n.Addresses) > 0 }

const (
	sysClassNet    = "/sys/class/net"
	procNetRoute   = "/proc/net/route"
	resolvConfPath = "/etc/resolv.conf"
)

// netScanner is where the network state is read from. Fields rather than
// constants so tests can point it at a tree they built — the alternative is
// testing against whatever network the test happens to run on, which tests
// nothing repeatable.
type netScanner struct {
	classNet   string
	routes     string
	resolvConf string
	hostname   func() (string, error)
	interfaces func() ([]net.Interface, error)
	addrsOf    func(net.Interface) ([]net.Addr, error)
}

func systemNetScanner() netScanner {
	return netScanner{
		classNet:   sysClassNet,
		routes:     procNetRoute,
		resolvConf: resolvConfPath,
		hostname:   os.Hostname,
		interfaces: net.Interfaces,
		addrsOf:    func(i net.Interface) ([]net.Addr, error) { return i.Addrs() },
	}
}

// ReadNetworkStatus reports how this machine is connected.
func ReadNetworkStatus() NetworkStatus { return systemNetScanner().status() }

func (s netScanner) status() NetworkStatus {
	status := NetworkStatus{}

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

	described.WakeOnLAN = wakeOnLANEnabled(s.classNet, iface.Name)

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

// wakeOnLANEnabled reports whether a card will start the machine when a magic
// packet arrives.
//
// Read from sysfs rather than by running `ethtool`, the same rule as everything
// else in this file: parsing a tool's output means depending on its formatting
// and its presence, and the kernel answers directly.
//
// `device/power/wakeup` is "enabled" or "disabled". It is the device's wakeup
// capability rather than specifically the magic-packet flag — the distinction
// matters to a kernel developer and not to somebody deciding whether their
// server can be woken, and a card with wakeup disabled certainly cannot be.
func wakeOnLANEnabled(classNet, name string) bool {
	raw, err := os.ReadFile(filepath.Join(classNet, name, "device", "power", "wakeup"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(raw)) == "enabled"
}
