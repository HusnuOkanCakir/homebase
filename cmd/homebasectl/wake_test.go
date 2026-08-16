package main

import (
	"strings"
	"testing"
)

// Go's flag package stops at the first argument that is not a flag, so a flag
// written after the address was silently dropped: the packet went to the default
// broadcast and nothing said otherwise.
//
// Invisible in this command in particular, because nothing acknowledges a magic
// packet — the only symptom is a machine that does not start, which is also what
// every other wake-on-LAN failure looks like.
func TestAFlagAfterTheAddressIsStillSeen(t *testing.T) {
	for _, args := range [][]string{
		{"--broadcast", "192.168.1.255", "40:16:7E:01:F3:F5"},
		{"40:16:7E:01:F3:F5", "--broadcast", "192.168.1.255"},
		{"40:16:7E:01:F3:F5", "--broadcast=192.168.1.255"},
	} {
		reordered := reorderFlags(args)
		if reordered[len(reordered)-1] != "40:16:7E:01:F3:F5" {
			t.Errorf("%v reordered to %v, which loses the address", args, reordered)
		}
		if !strings.Contains(strings.Join(reordered, " "), "192.168.1.255") {
			t.Errorf("%v reordered to %v, which loses the broadcast address", args, reordered)
		}
	}
}

// Nothing may be dropped. Reordering that quietly loses an argument would turn
// one silent failure into another.
func TestReorderingLosesNothing(t *testing.T) {
	args := []string{"one", "--json", "two", "--address=x", "three"}
	got := reorderFlags(args)
	if len(got) != len(args) {
		t.Fatalf("got %d arguments from %d: %v", len(got), len(args), got)
	}
	for _, want := range args {
		if !strings.Contains(strings.Join(got, " "), want) {
			t.Errorf("%v lost %q", got, want)
		}
	}
}

// A lone dash is a positional argument by convention, not a flag.
func TestALoneDashIsNotAFlag(t *testing.T) {
	got := reorderFlags([]string{"-", "40:16:7E:01:F3:F5"})
	if got[0] != "-" {
		t.Errorf("a lone dash was reordered as a flag: %v", got)
	}
}
