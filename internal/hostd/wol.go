package hostd

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
)

// Making wake-on-LAN survive a reboot.
//
// The setting lives in the card and the driver puts it back the way it likes on
// every boot, so switching it on is two separate jobs: change it now, and
// arrange for it to be changed again next time.
//
// The usual answer to the second is a systemd .link file, and that is what was
// written by hand on the first real laptop. It works, and it has a trap: a
// .link file that matches a device and does not set NamePolicy= takes over
// naming for it, so a file written to configure one thing can rename an
// interface and leave a server unreachable. Getting that right means restating
// the whole of 99-default.link in a file about something else.
//
// hostd already starts on every boot, already runs as root, and already owns a
// configuration directory. So it reapplies the setting itself. No .link file, no
// naming policy to restate, no dependency on ethtool being installed — and the
// record of what the machine is supposed to do is one line in /etc/homebase,
// where the rest of Homebase's intentions are kept.

const wakeOnLANFile = "/etc/homebase/wake-on-lan.conf"

// readWakeOnLANConfig lists the interfaces that are meant to wake this machine.
func readWakeOnLANConfig() []string {
	value := readResultFile(wakeOnLANFile)["interfaces"]
	if value == "" {
		return nil
	}
	var wanted []string
	for _, name := range strings.Split(value, ",") {
		if name = strings.TrimSpace(name); name != "" {
			wanted = append(wanted, name)
		}
	}
	return wanted
}

func writeWakeOnLANConfig(interfaces []string) error {
	sort.Strings(interfaces)
	return writeRootFile(wakeOnLANFile, fmt.Sprintf(
		"# Written by Homebase. Cards that may start this machine when a magic\n"+
			"# packet arrives — see `homebasectl network wake-on-lan`.\n"+
			"#\n"+
			"# Recorded by hardware address, not by name. A card's name describes\n"+
			"# where the kernel found it this boot and it moves: this machine has\n"+
			"# enumerated the same wired card as both enp4s0 and enp5s0, which left\n"+
			"# wake-on-LAN configured for a card that no longer had that name and no\n"+
			"# way to notice but a line in the journal.\n"+
			"interfaces=%s\n", strings.Join(interfaces, ",")), 0o644)
}

// ApplyWakeOnLAN puts the configured setting back into the cards, and is called
// once at startup.
//
// Failures are logged and not returned. A card that has been removed, renamed or
// replaced must not stop hostd from starting: the entire point of this service
// is that it is what remains when other things are broken, and refusing to run
// because a network card is missing would take the diagnostic tool away at
// exactly the moment somebody needs it.
func ApplyWakeOnLAN(log *slog.Logger) {
	for _, configured := range readWakeOnLANConfig() {
		name, found := resolveInterface(configured, sysClassNet)
		if !found {
			log.Warn("a card configured to wake this machine is not present",
				"configured", configured)
			continue
		}
		if err := setWakeOnLANMagic(name, true); err != nil {
			log.Warn("could not switch wake-on-LAN back on",
				"interface", name, "configured", configured, "error", err)
			continue
		}
		log.Info("wake-on-LAN is on", "interface", name, "configured", configured)
	}
}

// configureWakeOnLAN changes the setting now and records it for next time.
//
// Applied first. Recording an intention the hardware rejected would produce a
// server that claims to be wakeable, is not, and says so again after every
// reboot — and the only way anybody finds out is by walking to the machine.
func configureWakeOnLAN(name string, on bool) error {
	if !interfaceExists(name) {
		return &Error{
			Code:        "wol.no_such_interface",
			Message:     "This server has no network card by that name.",
			Detail:      "no " + name + " under " + sysClassNet,
			Recoverable: true,
			Recovery:    "Run `homebasectl network` to see what this server's cards are called.",
			Status:      404,
		}
	}
	if _, supported, known := readWakeOnLAN(name); on && known && !supported {
		return &Error{
			Code:        "wol.unsupported",
			Message:     "This network card cannot be woken by a network packet.",
			Detail:      name + ": the driver reports no wake-on-LAN modes",
			Recoverable: false,
			// The honest answer, and the one people most need, is that wireless
			// almost never does this. Somebody who has read that a home server
			// can be woken over the network will otherwise spend an evening in
			// a BIOS looking for a setting that would not have helped.
			Recovery: "Wired cards can usually be woken and wireless ones usually " +
				"cannot. If this server has a network socket, plug a cable into it " +
				"and try that card instead.",
			Status: 400,
		}
	}
	if err := setWakeOnLANMagic(name, on); err != nil {
		return &Error{
			Code:        "wol.could_not_change",
			Message:     "The network card would not accept the change.",
			Detail:      name + ": " + err.Error(),
			Recoverable: false,
			Status:      500,
		}
	}

	// Recorded by hardware address, so the setting survives the card being
	// renamed. Falls back to the name only if the address cannot be read, which
	// would mean the card vanished between being changed and being saved.
	identifier := name
	if address, err := hardwareAddress(name, sysClassNet); err == nil && address != "" {
		identifier = address
	}

	wanted := readWakeOnLANConfig()
	kept := wanted[:0:0]
	for _, existing := range wanted {
		// Dropped if it names this card by either spelling, so turning it off
		// clears an entry written by an older version — and so an entry for a
		// card that no longer exists can be removed at all, which it could not
		// before: `wake-on-lan <gone> off` refused, leaving a stale name warning
		// on every boot with no supported way to clear it.
		if existing == identifier || existing == name {
			continue
		}
		if resolved, found := resolveInterface(existing, sysClassNet); found && resolved == name {
			continue
		}
		kept = append(kept, existing)
	}
	if on {
		kept = append(kept, identifier)
	}
	if err := writeWakeOnLANConfig(kept); err != nil {
		// The card has already been changed, so this is not a failed operation —
		// it is one that will not survive a reboot. Said plainly, because "it
		// worked" and "it worked until you restart" need different words.
		return &Error{
			Code: "wol.not_saved",
			Message: "The card was changed, but the change could not be saved, so " +
				"it will be undone by the next restart.",
			Detail:      wakeOnLANFile + ": " + err.Error(),
			Recoverable: true,
			Recovery: "Check that the system disk is not full or read-only, then " +
				"run this again.",
			Status: 500,
		}
	}
	return nil
}

// interfaceExists reports whether the kernel has a card by this name, so that a
// typo is refused with the right sentence rather than an errno from netlink.
func interfaceExists(name string) bool {
	if name == "" || strings.ContainsAny(name, "/.") {
		return false
	}
	info, err := os.Stat(sysClassNet + "/" + name)
	return err == nil && info.IsDir()
}
