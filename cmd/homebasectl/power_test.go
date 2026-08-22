package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The exact answer a real Homebase gave, on a machine where waking over the
// network demonstrably worked.
//
// Copied rather than composed, with the hardware addresses swapped for the range
// RFC 7042 reserves for documentation. A MAC identifies one specific machine
// anywhere in the world, and "a real answer from a real server" should not mean
// a real server anybody can point at. The bug this pins was a vocabulary error — the
// filter asked for kind "wired" and hostd says "ethernet" — and it survived
// because the fixtures were written by whoever wrote the filter, so both were
// wrong in the same way. A recorded answer cannot agree with the code out of
// politeness.
const realNetworkReply = `{
  "hostname": "homebase",
  "interfaces": [
    {"addresses":["127.0.0.1","::1"],"kind":"loopback","name":"lo","up":true,
     "wake_on_lan":false,"wake_on_lan_known":true,"wake_on_lan_supported":false},
    {"addresses":["192.168.1.177"],"kind":"ethernet","mac":"00:00:5e:00:53:01",
     "name":"enp5s0","up":true,
     "wake_on_lan":true,"wake_on_lan_known":true,"wake_on_lan_supported":true},
    {"kind":"wireless","mac":"00:00:5e:00:53:02","name":"wlp4s0","up":false,
     "wake_on_lan":false,"wake_on_lan_known":true,"wake_on_lan_supported":false},
    {"addresses":["10.71.0.1"],"kind":"ethernet","name":"wg0","up":true,
     "wake_on_lan":false,"wake_on_lan_known":true,"wake_on_lan_supported":false},
    {"addresses":["172.17.0.1"],"kind":"container","mac":"00:00:5e:00:53:03",
     "name":"docker0","up":true,
     "wake_on_lan":false,"wake_on_lan_known":true,"wake_on_lan_supported":false}
  ]
}`

func TestWakeableAddressReadsARealServer(t *testing.T) {
	var status networkReply
	if err := json.Unmarshal([]byte(realNetworkReply), &status); err != nil {
		t.Fatal(err)
	}

	got := wakeableAddress(status)
	if got == "" {
		t.Fatal("a machine whose network card is set to wake was reported as " +
			"impossible to switch on again; somebody would have walked to it")
	}
	if got != "00:00:5e:00:53:01" {
		t.Errorf("wakeableAddress = %q, want the cable's address", got)
	}
}

// The three things that must not be mistaken for a machine that can be woken.
func TestWakeableAddressRefusesWhatCannotBeWoken(t *testing.T) {
	cases := map[string]string{
		"a card that says no": `{"interfaces":[{"kind":"ethernet","mac":"aa:bb:cc:dd:ee:ff",
			"wake_on_lan":false,"wake_on_lan_known":true}]}`,
		"a card that will not say": `{"interfaces":[{"kind":"ethernet","mac":"aa:bb:cc:dd:ee:ff",
			"wake_on_lan":true,"wake_on_lan_known":false}]}`,
		"wireless, whatever it claims": `{"interfaces":[{"kind":"wireless","mac":"aa:bb:cc:dd:ee:ff",
			"wake_on_lan":true,"wake_on_lan_known":true}]}`,
		"a tunnel with no hardware address": `{"interfaces":[{"kind":"ethernet","name":"wg0",
			"wake_on_lan":true,"wake_on_lan_known":true}]}`,
		"nothing at all": `{"interfaces":[]}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var status networkReply
			if err := json.Unmarshal([]byte(body), &status); err != nil {
				t.Fatal(err)
			}
			if mac := wakeableAddress(status); mac != "" {
				t.Errorf("promised %s could be woken, at %s", name, mac)
			}
		})
	}
}

// The dashboard makes the same decision in TypeScript and must make it the same
// way. Checked by reading it, which is crude and is still the only thing that
// would notice one of the two being changed alone.
func TestTheDashboardFiltersOnTheSameWord(t *testing.T) {
	// Relative to this package, which is where `go test` runs from.
	source, err := os.ReadFile("../../dashboard/src/views/Overview.tsx")
	if err != nil {
		t.Skip("the dashboard source is not here:", err)
	}
	if !strings.Contains(string(source), `iface.kind !== "ethernet"`) {
		t.Error(`Overview.tsx no longer filters on kind "ethernet"; if hostd's ` +
			`vocabulary changed, this test and wakeableAddress need changing too`)
	}
}
