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
		"home.example.org", "10.71.0.2")

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
	client := renderClientConfig("a2V5", "cHVi", "home.example.org", "10.71.0.2")

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

	// An iPhone is called "Okan's iPhone" out of the box. A naming rule that
	// rejects what the device calls itself is one people work around.
	for _, name := range []string{
		"phone", "work laptop", "Okan's iPad", "tv-2", "Okan's iPhone",
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
