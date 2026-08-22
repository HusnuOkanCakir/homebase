package hostd

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// The point of network status is telling three faults apart that look identical
// from a browser that will not load: no address, no internet, and nothing wrong
// here. Each of these tests is one of those distinctions.

func fakeNet(t *testing.T) netScanner {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sys/class/net"), 0o755); err != nil {
		t.Fatal(err)
	}
	return netScanner{
		classNet:   filepath.Join(root, "sys/class/net"),
		routes:     filepath.Join(root, "route"),
		resolvConf: filepath.Join(root, "resolv.conf"),
		hostname:   func() (string, error) { return "attic", nil },
		interfaces: func() ([]net.Interface, error) { return nil, nil },
		addrsOf:    func(net.Interface) ([]net.Addr, error) { return nil, nil },
	}
}

func TestTheMachineIsNamedForMDNS(t *testing.T) {
	s := fakeNet(t)
	status := s.status()

	if status.Hostname != "attic" {
		t.Errorf("hostname = %q", status.Hostname)
	}
	if status.MDNSName != "attic.local" {
		t.Errorf("mdns name = %q, want attic.local — this is what somebody types", status.MDNSName)
	}
}

// A hostname that already ends in .local must not become attic.local.local.
func TestTheNameIsNotDoubledUp(t *testing.T) {
	s := fakeNet(t)
	s.hostname = func() (string, error) { return "attic.local", nil }

	if got := s.status().MDNSName; got != "attic.local" {
		t.Errorf("mdns name = %q", got)
	}
}

func TestTheDefaultRouteIsRead(t *testing.T) {
	s := fakeNet(t)
	// /proc/net/route, little-endian hex. 0100A8C0 is 192.168.0.1.
	write(t, s.routes, "Iface\tDestination\tGateway\tFlags\n"+
		"enp0s3\t00000000\t0100A8C0\t0003\n"+
		"enp0s3\t0000A8C0\t00000000\t0001\n")

	if got := s.status().Gateway; got != "192.168.0.1" {
		t.Errorf("gateway = %q, want 192.168.0.1", got)
	}
}

// No gateway is a fact worth having: it is the difference between "your
// broadband is down" and "this machine is not on a network at all".
func TestNoDefaultRouteIsReportedAsNone(t *testing.T) {
	s := fakeNet(t)
	write(t, s.routes, "Iface\tDestination\tGateway\tFlags\n"+
		"enp0s3\t0000A8C0\t00000000\t0001\n")

	if got := s.status().Gateway; got != "" {
		t.Errorf("gateway = %q, want none", got)
	}
}

func TestNameserversAreRead(t *testing.T) {
	s := fakeNet(t)
	write(t, s.resolvConf, "# a comment\nnameserver 192.168.0.1\nsearch home\nnameserver 1.1.1.1\n")

	got := s.status().Nameservers
	if len(got) != 2 || got[0] != "192.168.0.1" || got[1] != "1.1.1.1" {
		t.Errorf("nameservers = %v", got)
	}
}

// A machine that has only handed itself a link-local address has not been given
// one by anybody. Reporting 169.254.x.x as an address would read, to somebody
// with no way to know better, as a working network.
func TestLinkLocalAddressesAreNotReportedAsBeingOnANetwork(t *testing.T) {
	s := fakeNet(t)
	s.interfaces = func() ([]net.Interface, error) {
		return []net.Interface{{Index: 1, Name: "enp0s3", Flags: net.FlagUp | net.FlagRunning}}, nil
	}
	s.addrsOf = func(net.Interface) ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("169.254.3.4"), Mask: net.CIDRMask(16, 32)},
		}, nil
	}

	status := s.status()
	if len(status.Interfaces) != 1 {
		t.Fatalf("got %d interfaces", len(status.Interfaces))
	}
	if addrs := status.Interfaces[0].Addresses; len(addrs) != 0 {
		t.Errorf("addresses = %v, want none: a link-local address is not being on a network", addrs)
	}
	if status.Interfaces[0].Reachable() {
		t.Error("an interface with only a link-local address reports itself reachable")
	}
}

// "The machine is fine, the internet is not" has to be expressible.
// The internet check moved to core.
//
// It lived here and could never have worked: homebase-hostd.service sets
// RestrictAddressFamilies=AF_UNIX AF_NETLINK, so this process cannot open an
// internet socket at all. It returned false on every machine that ever ran it,
// including one downloading Ubuntu updates while it said so.
//
// The tests that were here injected a fake dialler, so they exercised the logic
// while never asking whether hostd could execute it — and passed for four
// milestones. They are in internal/api now, next to the code, along with one
// that dials for real.

// A list field is an array or it is absent — never `null`.
//
// A nil slice in Go encodes as JSON `null`, so a field that is an array while
// there is something in it and null when there is not is a trap laid for every
// client. It went off exactly once and took a whole screen with it: removing
// the last remote-access device made the network page unreachable, because the
// page did `devices.length` on what had become null. The page was not the bug.
func TestListFieldsAreNeverNull(t *testing.T) {
	// A machine with nothing at all — no interfaces, no devices, no shares.
	for name, encode := range map[string]func() ([]byte, error){
		"network": func() ([]byte, error) {
			// A machine with no interfaces at all — which is a VM with its
			// network removed, and is the state that produced the bug.
			return json.Marshal(netScanner{
				classNet:   t.TempDir(),
				routes:     "/nonexistent",
				resolvConf: "/nonexistent",
				hostname:   func() (string, error) { return "x", nil },
				interfaces: func() ([]net.Interface, error) { return nil, nil },
				addrsOf:    func(net.Interface) ([]net.Addr, error) { return nil, nil },
			}.status())
		},
		"vpn": func() ([]byte, error) {
			return json.Marshal(VPNStatus{Port: 51820, Devices: []VPNDevice{}})
		},
		"shares": func() ([]byte, error) {
			return json.Marshal(ShareStatus{Users: []string{}, Shares: []ShareState{}})
		},
	} {
		body, err := encode()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, field := range []string{"interfaces", "devices", "shares", "users"} {
			value, present := decoded[field]
			if present && value == nil {
				t.Errorf("%s.%s is null; a client that indexes into it crashes", name, field)
			}
		}
	}
}

// --- A configuration naming a card that is not there ------------------------------

// The failure this catches took a working server off the network for an evening.
//
// A wireless card was not detected on one boot, which moved the ethernet from PCI
// slot 5 to slot 4 and renamed it. The configuration named the old name, so
// nothing was brought up and no address was obtained. The machine booted
// perfectly, the card was fine, and the only way to find out was a keyboard, a
// screen, and knowing to compare two names.
func TestAConfiguredInterfaceThatIsNotThereIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "50-cloud-init.yaml"), []byte(
		"network:\n  version: 2\n  ethernets:\n    enp5s0:\n      dhcp4: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	missing := configuredButAbsent(dir, []NetworkInterface{{Name: "enp4s0"}, {Name: "lo"}})
	if len(missing) != 1 || missing[0] != "enp5s0" {
		t.Fatalf("got %v, want [enp5s0]", missing)
	}

	// And says nothing once the card is there under that name.
	if got := configuredButAbsent(dir, []NetworkInterface{{Name: "enp5s0"}}); len(got) != 0 {
		t.Errorf("reported %v about an interface that exists", got)
	}
}

// A name inside `match:` is a pattern, not a device. `name: "en*"` is the fix for
// this whole class of problem, and reporting it as a missing card would turn the
// remedy into a permanent warning.
func TestAMatchPatternIsNotReportedAsMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "50-homebase.yaml"), []byte(
		"network:\n  version: 2\n  ethernets:\n    wired:\n      match:\n"+
			"        name: \"en*\"\n      dhcp4: true\n      optional: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := configuredButAbsent(dir, []NetworkInterface{{Name: "enp4s0"}}); len(got) != 0 {
		t.Errorf("the configuration Homebase writes reported %v as missing", got)
	}
}

// Ordinary netplan keys are not interface names. "ethernets" is the one that
// would otherwise be reported on every machine for ever.
func TestNetplanKeysAreNotMistakenForInterfaces(t *testing.T) {
	for _, key := range []string{
		"network", "version", "ethernets", "wifis", "dhcp4", "match", "name",
		"optional", "renderer", "wired", "en", "eth",
	} {
		if looksLikeInterface(key) {
			t.Errorf("%q was taken for an interface name", key)
		}
	}
	for _, key := range []string{"enp5s0", "enp4s0", "eth0", "eno1", "ens18", "wlp4s0"} {
		if !looksLikeInterface(key) {
			t.Errorf("%q was not recognised as an interface name", key)
		}
	}
}
