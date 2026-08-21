package hostd

import (
	"strconv"
	"strings"
	"testing"
)

// The server's key survives being set up again.
//
// Regenerating it would silently invalidate every device that had been handed
// out — which somebody discovers on holiday, with no way to fix it from where
// they are. Changing the hostname is the ordinary reason to run setup twice.
func TestSettingUpAgainKeepsTheServerKeyAndTheDevices(t *testing.T) {
	first := renderServerConfig("cHJpdmF0ZS1rZXk=", "old.example.org", []VPNDevice{
		{Name: "phone", Address: "10.71.0.2", PublicKey: "cGhvbmUta2V5"},
		{Name: "laptop", Address: "10.71.0.3", PublicKey: "bGFwdG9wLWtleQ=="},
	})

	key := serverKeyFrom(first)
	if key != "cHJpdmF0ZS1rZXk=" {
		t.Fatalf("the server key was read as %q", key)
	}

	// What setUpVPN does with an existing file: keep the key, keep the peers,
	// change the name.
	second := renderServerConfig(key, "new.example.org", devicesFromConfig(first))

	if serverKeyFrom(second) != key {
		t.Error("the server key changed; every device handed out would stop working")
	}
	if got := configuredHostname(second); got != "new.example.org" {
		t.Errorf("hostname = %q, want the new one", got)
	}

	devices := devicesFromConfig(second)
	if len(devices) != 2 {
		t.Fatalf("%d devices survived, want 2", len(devices))
	}
	for i, want := range []string{"phone", "laptop"} {
		if devices[i].Name != want {
			t.Errorf("device %d is %q, want %q", i, devices[i].Name, want)
		}
	}
}

// The server's private key must never be in what is handed to a device.
func TestTheClientConfigCarriesOnlyItsOwnKey(t *testing.T) {
	const serverPrivate = "c2VydmVyLXByaXZhdGU="
	const devicePrivate = "ZGV2aWNlLXByaXZhdGU="

	client := renderClientConfig(devicePrivate, "c2VydmVyLXB1YmxpYw==",
		"home.example.org", "10.71.0.2", true)

	if strings.Contains(client, serverPrivate) {
		t.Fatal("the server's private key is in a device's configuration")
	}
	if !strings.Contains(client, devicePrivate) {
		t.Error("the device's own key is missing; it could not connect")
	}
	if !strings.Contains(client, "home.example.org:51820") {
		t.Error("the configuration does not say where to connect")
	}
}

// A full tunnel would route a phone's entire internet through a domestic upload
// link and a machine in a cupboard. Only the house's traffic goes over the VPN.
func TestOnlyTheHouseGoesOverTheVPN(t *testing.T) {
	client := renderClientConfig("a2V5", "cHVi", "home.example.org", "10.71.0.2", true)

	for _, line := range strings.Split(client, "\n") {
		if !strings.HasPrefix(line, "AllowedIPs") {
			continue
		}
		if strings.Contains(line, "0.0.0.0/0") {
			t.Errorf("everything is routed over the VPN: %q", line)
		}
		if !strings.Contains(line, vpnNetwork) {
			t.Errorf("the VPN's own range is not routed: %q", line)
		}
		return
	}
	t.Error("no AllowedIPs line at all; the device would route nothing")
}

// Addresses are handed out without collisions, and reuse a gap left by a removed
// device rather than walking off the end.
func TestAddressesAreHandedOutWithoutCollisions(t *testing.T) {
	existing := []VPNDevice{
		{Name: "a", Address: "10.71.0.2"},
		{Name: "c", Address: "10.71.0.4"},
	}
	next, err := nextAddress(existing)
	if err != nil {
		t.Fatal(err)
	}
	if next != "10.71.0.3" {
		t.Errorf("next address = %q, want the gap at .3", next)
	}

	full := make([]VPNDevice, 0, maxDevices)
	for i := 2; i < 2+maxDevices; i++ {
		full = append(full, VPNDevice{Address: "10.71.0." + strconv.Itoa(i)})
	}
	if _, err := nextAddress(full); err == nil {
		t.Error("a full network handed out an address anyway")
	}
}

// A device name reaches a configuration file as a comment. The characters that
// are refused are the ones that would let it end that line and start a directive.
func TestADeviceNameCannotBecomeADirective(t *testing.T) {
	for _, name := range []string{
		"", " ", "phone\nPublicKey = attacker",
		"phone\r\n[Peer]", strings.Repeat("x", 32),
		"[Peer]", "#comment", "phone/../..",
	} {
		if validDeviceName.MatchString(name) {
			t.Errorf("%q was accepted as a device name", name)
		}
	}

	// An iPhone is called "Alex's iPhone" out of the box. A naming rule that
	// rejects what the device calls itself is one people work around.
	for _, name := range []string{
		"phone", "work laptop", "Alex's iPad", "tv-2", "Alex's iPhone",
	} {
		if !validDeviceName.MatchString(name) {
			t.Errorf("%q was refused, and is an ordinary name", name)
		}
	}
}

func TestAnEndpointHasToLookLikeOne(t *testing.T) {
	for _, endpoint := range []string{
		"", "home.example.org\nListenPort = 1",
		"home example.org", "-leading.dash", "trailing.dash-",
		strings.Repeat("x", 254),
	} {
		if validEndpoint.MatchString(endpoint) {
			t.Errorf("%q was accepted as an endpoint", endpoint)
		}
	}

	for _, endpoint := range []string{
		"home.duckdns.org", "203.0.113.4", "my-server.example.co.uk", "homebase",
	} {
		if !validEndpoint.MatchString(endpoint) {
			t.Errorf("%q was refused, and is a reasonable endpoint", endpoint)
		}
	}
}

// A machine with nothing set up says so, and says what to do.
func TestAServerWithNoRemoteAccessSaysSo(t *testing.T) {
	status := readVPNStatus(t.Context())

	if status.Configured {
		t.Skip("this machine has Wireguard configured; not the case under test")
	}
	if status.Message == "" {
		t.Error("a server with no remote access says nothing about it")
	}
	if !strings.Contains(status.Message, "vpn setup") {
		t.Errorf("the message does not say how to switch it on: %q", status.Message)
	}
}

// A device is told to use this server's resolver only when there is one.
//
// Wireguard clients do not treat the DNS line as advisory: setting it replaces
// the device's resolver for as long as the tunnel is up. Naming a port nothing
// listens on therefore does not mean "the house is unreachable by name", it
// means *nothing resolves* — which reads, on a phone in a café, as the VPN
// having broken the internet.
func TestNoResolverMeansNoDNSLine(t *testing.T) {
	withResolver := renderClientConfig("a2V5", "cHVi", "home.example.org", "10.71.0.2", true)
	if !strings.Contains(withResolver, "DNS = "+vpnServer) {
		t.Error("a server that answers DNS did not offer itself as the resolver")
	}

	without := renderClientConfig("a2V5", "cHVi", "home.example.org", "10.71.0.2", false)
	if strings.Contains(without, "DNS =") {
		t.Errorf("a device was pointed at a resolver that does not exist:\n%s", without)
	}
	// Still a usable tunnel — the device keeps its own resolver and reaches the
	// house by address.
	if !strings.Contains(without, "Endpoint = home.example.org:51820") {
		t.Error("dropping the DNS line broke the rest of the configuration")
	}
	if !strings.Contains(without, "[Peer]") {
		t.Error("the peer section is missing")
	}
}

// What the client is told it can reach must be reachable.
//
// The server used to write no forwarding rules at all, so a connected device
// reached this machine and nothing else: every packet for the rest of the house
// arrived and was dropped, because Ubuntu forwards nothing by default and
// because a printer replying to 10.71.0.2 does not know where that is.
//
// It looked like it worked, which is why this is asserted rather than left to
// the next person to connect from somewhere far away.
func TestTheServerActuallyRoutesTheHouse(t *testing.T) {
	config := renderServerConfig("cHJpdmF0ZQ==", "home.example.org", nil)

	for _, needed := range []string{
		"net.ipv4.ip_forward=1",
		"PostUp = iptables -A FORWARD -i %i -j ACCEPT",
		"MASQUERADE",
	} {
		if !strings.Contains(config, needed) {
			t.Errorf("the server config does not %q; a device would reach this "+
				"machine and nothing else on the network:\n%s", needed, config)
		}
	}

	// Taken down again with the interface. Rules left behind on a machine whose
	// remote access is switched off are rules nobody knows are there.
	ups := strings.Count(config, "PostUp = iptables")
	downs := strings.Count(config, "PostDown = iptables")
	if ups != downs {
		t.Errorf("%d rules are added and %d removed; the difference is left "+
			"behind when the tunnel goes down", ups, downs)
	}

	// The card is not named. It has been renamed on the machine this was written
	// for — a wireless card that failed to appear on one boot renumbered the
	// ethernet from enp5s0 to enp4s0 — and a NAT rule naming it would have
	// stopped working that morning.
	for _, name := range []string{"enp5s0", "eth0", "-o enp", "-o eth"} {
		if strings.Contains(config, name) {
			t.Errorf("the forwarding rules name a network card (%q), which is a "+
				"fact about one boot", name)
		}
	}
}
