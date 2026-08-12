package hostd

import (
	"encoding/json"
	"strings"
	"testing"
)

// The netplan file is produced by an encoder, not by formatting, and this is the
// test that says why.
//
// A network name is somebody else's string. Pasted into YAML it could close the
// value and open a new key — in a file that decides what this machine connects
// to and holds the password it connects with. Going through encoding/json means
// the escaping is the standard library's rather than a format string nobody
// re-reads.
func TestAnAwkwardNetworkNameCannotChangeTheDocument(t *testing.T) {
	hostile := []string{
		`a" b`,
		`x: y`,
		"tab\there",
		`"`,
		`\`,
		`{}`,
		`- item`,
		`#comment`,
		`Café ☕`,
	}

	for _, ssid := range hostile {
		rendered, err := renderWifiConfig("wlan0", ssid, "a-good-passphrase")
		if err != nil {
			t.Fatalf("%q: %v", ssid, err)
		}

		// Everything after the comment header has to parse back to exactly what
		// went in. If the name had escaped its string, this is where it shows.
		body := rendered[strings.Index(string(rendered), "{"):]
		var parsed wifiConfig
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("%q produced a document that does not parse: %v", ssid, err)
		}

		device, ok := parsed.Network.Wifis["wlan0"]
		if !ok {
			t.Fatalf("%q: the interface is gone from the document", ssid)
		}
		if len(device.AccessPoints) != 1 {
			t.Errorf("%q produced %d access points, want 1", ssid, len(device.AccessPoints))
		}
		if _, ok := device.AccessPoints[ssid]; !ok {
			t.Errorf("%q did not survive the round trip: %v", ssid, device.AccessPoints)
		}
	}
}

// A newline in an SSID or a passphrase is refused before anything is written.
//
// The encoder would escape it correctly, so this is belt and braces — but a
// newline is not a character either can contain, and rejecting it early means
// the failure names the field rather than arriving from wpa_supplicant later.
func TestRequestsThatCannotBeWifiAreRefused(t *testing.T) {
	cases := []struct {
		name, ssid, passphrase string
	}{
		{"no name", "", "a-good-passphrase"},
		{"a name too long for the standard", strings.Repeat("x", 33), "a-good-passphrase"},
		{"a newline in the name", "home\nnetwork", "a-good-passphrase"},
		{"a null in the name", "home\x00network", "a-good-passphrase"},
		{"a password WPA2 cannot take", "home", "short"},
		{"a password too long for WPA2", "home", strings.Repeat("x", 64)},
		{"a newline in the password", "home", "abcdefgh\nij"},
	}

	for _, c := range cases {
		if err := validateWifiRequest(c.ssid, c.passphrase); err == nil {
			t.Errorf("%s was accepted", c.name)
		}
	}
}

func TestOrdinaryRequestsAreAccepted(t *testing.T) {
	cases := []struct {
		name, ssid, passphrase string
	}{
		{"a household network", "BT-HUB-3F2A", "correct-horse-battery"},
		{"an open network", "Library Wi-Fi", ""},
		{"the shortest password WPA2 allows", "home", "12345678"},
		{"the longest", "home", strings.Repeat("x", 63)},
		{"a name with an emoji in it", "☕ Café", "correct-horse-battery"},
		{"the longest name the standard allows", strings.Repeat("x", 32), "correct-horse-battery"},
	}

	for _, c := range cases {
		if err := validateWifiRequest(c.ssid, c.passphrase); err != nil {
			t.Errorf("%s was refused: %v", c.name, err)
		}
	}
}

// The cable wins. A machine with both should send everything down the wire: it
// is faster, and it is the one that was already working.
func TestWirelessGetsAWorseRouteThanTheCable(t *testing.T) {
	rendered, err := renderWifiConfig("wlan0", "home", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}

	body := rendered[strings.Index(string(rendered), "{"):]
	var parsed wifiConfig
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}

	device := parsed.Network.Wifis["wlan0"]
	if device.DHCP4Overrides.RouteMetric <= 100 {
		t.Errorf("route metric is %d; a low one would make wireless beat the cable",
			device.DHCP4Overrides.RouteMetric)
	}

	// `optional` is what stops a boot hanging for two minutes on a network that
	// is not in range — which is every time somebody carries the server to a
	// different room.
	if !device.Optional {
		t.Error("the wireless interface is not optional; a server out of range " +
			"would add minutes to every start-up")
	}
}

// The file is a YAML file that happens to be JSON, and it says so — because
// somebody will open it eventually and wonder.
func TestTheFileExplainsItself(t *testing.T) {
	rendered, err := renderWifiConfig("wlan0", "home", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	text := string(rendered)

	if !strings.HasPrefix(text, "#") {
		t.Error("the file does not begin with a comment saying where it came from")
	}
	if !strings.Contains(text, "dashboard") {
		t.Error("the comment does not say where to change it instead")
	}
	// The header must be comments only, or netplan reads it as content.
	for _, line := range strings.Split(text[:strings.Index(text, "{")], "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			t.Errorf("a non-comment line before the document: %q", line)
		}
	}
}

// dBm is a number somebody moving a server around the house watches; bars is the
// number they act on. The thresholds matter less than the ordering being right.
func TestSignalBarsAreMonotonic(t *testing.T) {
	previous := -1
	for _, signal := range []int{-90, -80, -70, -60, -40} {
		got := bars(signal)
		if got < previous {
			t.Errorf("signal %d gave %d bars, worse than the weaker one before it", signal, got)
		}
		if got < 1 || got > 4 {
			t.Errorf("signal %d gave %d bars, outside 1..4", signal, got)
		}
		previous = got
	}

	// Zero means "no reading", which is not the same as a very weak one.
	if bars(0) != 0 {
		t.Errorf("no reading gave %d bars, want 0", bars(0))
	}
}

// The audit log is append-only and kept for ever, so anything written into it is
// written for good. That is the wrong place for somebody's Wi-Fi password, and
// it went there in the first version of this — caught by the VM test looking for
// it in the file.
func TestASecretNeverReachesTheAuditLog(t *testing.T) {
	const secret = "correct-horse-battery"
	body := []byte(`{"ssid":"Home","passphrase":"` + secret + `"}`)

	recorded := string(redactSecrets(body, []string{"passphrase"}))
	if strings.Contains(recorded, secret) {
		t.Fatalf("the passphrase survived redaction: %s", recorded)
	}

	// The network name has to survive. Redaction that removed everything would
	// make the log useless, which is how it ends up switched off.
	if !strings.Contains(recorded, "Home") {
		t.Errorf("the network name was lost: %s", recorded)
	}

	// "There was a passphrase and it is not recorded" and "there was no
	// passphrase" are different facts, and somebody reconstructing an incident
	// needs the first.
	if !strings.Contains(recorded, "redacted") {
		t.Errorf("the log does not say a value was removed: %s", recorded)
	}
}

func TestRedactionLeavesOrdinaryRequestsAlone(t *testing.T) {
	body := []byte(`{"id":"jellyfin","confirm":"jellyfin"}`)
	if got := string(redactSecrets(body, nil)); got != string(body) {
		t.Errorf("an operation with no secrets had its parameters rewritten: %s", got)
	}
}

// A body that is not what it should be is dropped rather than recorded. If the
// shape is unexpected, nothing here knows what is in it.
func TestAnUnreadableBodyIsNotRecorded(t *testing.T) {
	const secret = "correct-horse-battery"
	for _, body := range []string{
		`not json at all ` + secret,
		`["` + secret + `"]`,
		`"` + secret + `"`,
	} {
		recorded := string(redactSecrets([]byte(body), []string{"passphrase"}))
		if strings.Contains(recorded, secret) {
			t.Errorf("%q was recorded verbatim: %s", body, recorded)
		}
	}
}

// Every operation that takes a secret has to declare it, and the way to keep
// that true is to check the registry rather than to remember.
func TestOperationsTakingSecretsDeclareThem(t *testing.T) {
	r := NewRegistry()
	RegisterWifiOperations(r, NewNetworkServices())

	connect, ok := r.Lookup("network.wifi_connect")
	if !ok {
		t.Fatal("network.wifi_connect is not registered")
	}
	found := false
	for _, name := range connect.Secret {
		if name == "passphrase" {
			found = true
		}
	}
	if !found {
		t.Error("network.wifi_connect does not declare its passphrase as a secret; " +
			"it would be written to the audit log in plain text")
	}
}
