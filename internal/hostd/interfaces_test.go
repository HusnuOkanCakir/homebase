package hostd

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeNet builds a /sys/class/net standing in for a machine's cards.
//
// Built from the two states this laptop has actually been observed in, a week
// apart, with the same card under two names — which is the entire reason this
// file exists.
func fakeClassNet(t *testing.T, cards map[string]string, virtual ...string) string {
	t.Helper()
	root := t.TempDir()
	for name, mac := range cards {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(dir, "device"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "address"), []byte(mac+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Bridges and tunnels have no device behind them, which is how they are told
	// apart from real cards.
	for _, name := range virtual {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "address"),
			[]byte("02:42:ac:11:00:01\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const wiredMAC = "40:16:7e:01:f3:f5"

// The failure this fixes: the same card, two names, a week apart.
func TestAHardwareAddressFindsTheCardUnderEitherName(t *testing.T) {
	for _, name := range []string{"enp4s0", "enp5s0"} {
		t.Run(name, func(t *testing.T) {
			classNet := fakeClassNet(t, map[string]string{name: wiredMAC})
			got, found := resolveInterface(wiredMAC, classNet)
			if !found {
				t.Fatal("did not find the card by its hardware address")
			}
			if got != name {
				t.Fatalf("resolved to %q, want %q", got, name)
			}
		})
	}
}

// Configuration written before this change names cards the old way, and must
// keep working rather than being migrated once and silently.
func TestAnInterfaceNameStillResolvesWhenTheCardHasIt(t *testing.T) {
	classNet := fakeClassNet(t, map[string]string{"enp5s0": wiredMAC})

	if got, found := resolveInterface("enp5s0", classNet); !found || got != "enp5s0" {
		t.Fatalf("resolveInterface(name) = %q, %v", got, found)
	}
	// And a name the card no longer has is reported missing rather than used —
	// which is the case that was failing silently.
	if _, found := resolveInterface("enp4s0", classNet); found {
		t.Fatal("resolved a name no card has; that is the bug this fixes")
	}
}

func TestAnAddressWithNoCardIsNotAnError(t *testing.T) {
	classNet := fakeClassNet(t, map[string]string{"enp5s0": wiredMAC})
	if _, found := resolveInterface("aa:bb:cc:dd:ee:ff", classNet); found {
		t.Fatal("found a card that is not there")
	}
}

func TestCaseAndSpacingDoNotMatter(t *testing.T) {
	classNet := fakeClassNet(t, map[string]string{"enp5s0": wiredMAC})
	for _, spelling := range []string{
		"40:16:7E:01:F3:F5", " 40:16:7e:01:f3:f5 ", "40:16:7e:01:f3:f5",
	} {
		if _, found := resolveInterface(spelling, classNet); !found {
			t.Fatalf("did not resolve %q", spelling)
		}
	}
}

// The bug that made homebase.local advertise ten unreachable addresses.
func TestRealInterfacesExcludesEveryContainerBridge(t *testing.T) {
	classNet := fakeClassNet(t,
		map[string]string{"enp5s0": wiredMAC, "wlp4s0": "aa:bb:cc:dd:ee:01"},
		"lo", "docker0", "br-33411a35575b", "br-472abc6d618c", "veth1a2b", "tailscale0")

	got := realInterfaces(classNet)
	if len(got) != 2 {
		t.Fatalf("real cards = %v, want the two with hardware behind them", got)
	}
	for _, name := range got {
		if name == "docker0" || name == "lo" || name == "tailscale0" {
			t.Fatalf("%q counted as a real card", name)
		}
	}
}

func TestTellingAnAddressFromAName(t *testing.T) {
	for _, value := range []string{"40:16:7e:01:f3:f5", "AA-BB-CC-DD-EE-FF"} {
		if !looksLikeHardwareAddress(value) {
			t.Errorf("%q not recognised as a hardware address", value)
		}
	}
	for _, value := range []string{"enp5s0", "eth0", "", "br-33411a"} {
		if looksLikeHardwareAddress(value) {
			t.Errorf("%q mistaken for a hardware address", value)
		}
	}
}

// --- avahi -------------------------------------------------------------------

func TestTheShippedDenyLineIsReplacedEntirely(t *testing.T) {
	// What packaging actually wrote, and why it never worked: avahi matches
	// whole names, so "br-" excluded nothing.
	config := "[server]\ndeny-interfaces=docker0,br-\nuse-ipv4=yes\n"

	updated, changed := replaceAvahiInterfaces(config, "allow-interfaces=enp5s0")
	if !changed {
		t.Fatal("left the broken deny line in place")
	}
	if contains := "deny-interfaces"; len(updated) > 0 && stringsContains(updated, contains) {
		t.Fatalf("the deny line survived:\n%s", updated)
	}
	if !stringsContains(updated, "allow-interfaces=enp5s0") {
		t.Fatalf("did not write the allow line:\n%s", updated)
	}
	// Everything else in the file is left alone.
	if !stringsContains(updated, "use-ipv4=yes") {
		t.Fatalf("clobbered unrelated settings:\n%s", updated)
	}
}

func TestAnUnchangedConfigIsNotRewritten(t *testing.T) {
	config := "[server]\nallow-interfaces=enp5s0\n"
	if _, changed := replaceAvahiInterfaces(config, "allow-interfaces=enp5s0"); changed {
		t.Fatal("reported a change; avahi would be restarted on every boot")
	}
}

func TestTheLineIsAddedWhenThereIsNone(t *testing.T) {
	updated, changed := replaceAvahiInterfaces(
		"[server]\nuse-ipv4=yes\n", "allow-interfaces=enp5s0")
	if !changed || !stringsContains(updated, "allow-interfaces=enp5s0") {
		t.Fatalf("did not add the line:\n%s", updated)
	}
}

func stringsContains(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle || len(needle) == 0 ||
			indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
