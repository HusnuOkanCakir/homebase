package hostd

import (
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Naming a network card by something that survives a reboot.
//
// A card's name is not a property of the card. `enp4s0` describes where the
// kernel found it this time, and on this hardware it moves: the same laptop has
// enumerated its wired card as both enp4s0 and enp5s0 across reboots, with
// nothing logged and nothing to notice.
//
// That cost real breakage twice. Wake-on-LAN was configured for a name the card
// no longer had, so the machine could not be woken and the only sign was a line
// in the journal. avahi was told to publish on a name that matched nothing, so
// `homebase.local` stopped resolving and the server appeared to have vanished
// from the network while answering perfectly well on its address.
//
// The hardware address does not move. It is what the router's DHCP reservation
// is keyed on, which is exactly why the machine kept its address through both
// renames while everything keyed on the name broke. So Homebase keys on it too,
// and resolves it to whatever the card is called at the moment it needs a name.

// hardwareAddress reads a card's MAC, normalised.
func hardwareAddress(name, classNet string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(classNet, name, "address"))
	if err != nil {
		return "", err
	}
	return normaliseHardwareAddress(string(raw)), nil
}

// normaliseHardwareAddress puts an address in one form so two spellings of the
// same card compare equal.
func normaliseHardwareAddress(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// looksLikeHardwareAddress distinguishes a MAC from an interface name.
//
// Both appear in configuration written by different versions of Homebase, and a
// file written before this change names cards the old way. Rather than migrate
// on upgrade — which runs once, and fails silently if it fails — both forms are
// understood forever, and a name is simply resolved as a name.
func looksLikeHardwareAddress(value string) bool {
	_, err := net.ParseMAC(strings.TrimSpace(value))
	return err == nil
}

// interfaceNameFor finds the card with this hardware address.
//
// Returns the name it currently has. An address belonging to no card present is
// not an error worth failing on: cards get removed, and a machine whose USB
// adapter is unplugged should still boot.
func interfaceNameFor(address, classNet string) (string, bool) {
	wanted := normaliseHardwareAddress(address)
	entries, err := os.ReadDir(classNet)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		got, err := hardwareAddress(entry.Name(), classNet)
		if err != nil || got != wanted {
			continue
		}
		return entry.Name(), true
	}
	return "", false
}

// resolveInterface turns a configured value — a name or a hardware address —
// into the name the card has right now.
func resolveInterface(value, classNet string) (string, bool) {
	if !looksLikeHardwareAddress(value) {
		// A name, from a configuration written before this change. Honoured if
		// the card still has it, and reported missing if it does not — which is
		// the case this whole file exists to stop happening quietly.
		if _, err := os.Stat(filepath.Join(classNet, value)); err != nil {
			return "", false
		}
		return value, true
	}
	return interfaceNameFor(value, classNet)
}

// realInterfaces lists the machine's actual network cards, by current name.
//
// "Actual" means a card with a device behind it: not loopback, not a Docker
// bridge, not a veth, not a tunnel. On a server running applications these
// outnumber the real cards ten to one, and telling a service to publish on all
// of them is how `homebase.local` came to advertise ten addresses that only the
// server itself could reach.
func realInterfaces(classNet string) []string {
	entries, err := os.ReadDir(classNet)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if !countableInterface(entry.Name(), classNet) {
			continue
		}
		names = append(names, entry.Name())
	}
	return names
}
