package hostd

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
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
func TestAServerWithAnAddressButNoInternetSaysSo(t *testing.T) {
	services := &NetworkServices{
		dial: func(context.Context, string) error { return errors.New("no route") },
	}

	result, err := services.status(context.Background(), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	status, ok := result.(networkStatusResult)
	if !ok {
		t.Fatalf("unexpected result type %T", result)
	}
	if status.Online {
		t.Error("reported the internet as reachable when every attempt failed")
	}
}

func TestOnlineWhenSomethingAnswers(t *testing.T) {
	services := &NetworkServices{
		dial: func(context.Context, string) error { return nil },
	}

	result, _ := services.status(context.Background(), struct{}{})
	status := result.(networkStatusResult)

	// Only meaningful when there is a route to try over; the scanner reads the
	// real machine here, so this asserts the pairing rather than the value.
	if status.Gateway != "" && !status.Online {
		t.Error("something answered but the result says offline")
	}
}

// The internet check must not call a working connection broken.
//
// It dialled TCP/53 at the public resolvers, and said "the internet is not
// reachable" on the first real network Homebase met — a machine that was
// downloading Ubuntu updates while it said so. Plenty of networks block
// outbound TCP/53 to public resolvers; some ISPs do it as policy.
func TestABlockedResolverPortIsNotAnOfflineMachine(t *testing.T) {
	tried := []string{}
	services := &NetworkServices{
		dial: func(_ context.Context, address string) error {
			tried = append(tried, address)
			// Exactly the network that broke this: 53 refused, 443 fine.
			if strings.HasSuffix(address, ":53") {
				return errors.New("connection refused")
			}
			return nil
		},
	}

	if !services.reachesTheInternet(t.Context()) {
		t.Errorf("a machine that can reach 443 was called offline; tried %v", tried)
	}

	// And 443 has to be tried first, or the answer costs two timeouts on every
	// such network.
	if len(tried) == 0 || !strings.HasSuffix(tried[0], ":443") {
		t.Errorf("the first thing tried was %v, want a 443 address", tried)
	}
}

// A genuinely offline machine still says so.
func TestAnOfflineMachineIsStillOffline(t *testing.T) {
	services := &NetworkServices{
		dial: func(_ context.Context, _ string) error {
			return errors.New("network is unreachable")
		},
	}
	if services.reachesTheInternet(t.Context()) {
		t.Error("a machine that could reach nothing was called online")
	}
}

// Two organisations, because one being down is not evidence about the internet.
func TestTheCheckAsksMoreThanOneOrganisation(t *testing.T) {
	tried := map[string]bool{}
	services := &NetworkServices{
		dial: func(_ context.Context, address string) error {
			tried[strings.Split(address, ":")[0]] = true
			return errors.New("no")
		},
	}
	services.reachesTheInternet(t.Context())

	if len(tried) < 2 {
		t.Errorf("only asked %v — one host being down is not evidence about "+
			"the internet", tried)
	}
}
