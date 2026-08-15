package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
)

// Waking a machine that has gone to sleep.
//
// This is the one command in `homebasectl` that talks to nothing — not core, not
// hostd, not the database. A magic packet is a UDP broadcast that any process can
// send, so making it a privileged operation would add a boundary crossing, an
// audit record and a permission check to something with no privilege in it at
// all.
//
// It is here rather than nowhere because of where it is useful: away from home,
// connected over the VPN, wanting the desktop in the study to come on. The
// server is awake — it is the thing serving the VPN — and it is on the same
// network as the machine that is not.
//
// Waking *the server itself* is a different problem and cannot be solved from
// here, because nothing on a sleeping machine can run a command. That is why
// `homebasectl network` reports the server's own MAC address and whether its
// card is set to accept a magic packet: so somebody can send one from their
// phone.

var macAddress = regexp.MustCompile(`^([0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}$`)

func wakeCommand(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("wake", flag.ContinueOnError)
	flags.SetOutput(stdout)
	// The broadcast address of the network the machine is on. Configurable
	// because a house with more than one segment needs it, and defaulted because
	// almost nobody does.
	broadcast := flags.String("broadcast", "255.255.255.255", "where to send it")
	if err := flags.Parse(args); err != nil {
		return usageError{err}
	}

	if flags.NArg() != 1 {
		return usageError{errors.New(
			"which machine? — homebasectl wake AA:BB:CC:DD:EE:FF\n" +
				"The hardware address of the machine to wake. Its own operating\n" +
				"system will tell you, and so will your router's list of devices.")}
	}

	address := flags.Arg(0)
	if !macAddress.MatchString(address) {
		return usageError{fmt.Errorf(
			"%q is not a hardware address — six pairs of hex digits, like "+
				"AA:BB:CC:DD:EE:FF", address)}
	}

	if err := sendMagicPacket(address, *broadcast); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Sent. %s should be starting.\n\n", address)
	// Nothing acknowledges a magic packet — it is fire and forget by design, so
	// there is no success to report beyond having sent it. Saying so is better
	// than implying the machine woke up.
	fmt.Fprintln(stdout, "Nothing answers a wake-up packet, so there is no way to")
	fmt.Fprintln(stdout, "confirm it arrived. Give it a minute and try to reach the")
	fmt.Fprintln(stdout, "machine.")
	fmt.Fprintln(stdout)
	// Named settings rather than "check your BIOS", because the first real
	// laptop had wake-on-LAN switched on in both the card and the firmware and
	// still would not start. The setting that stopped it was a power-saving one
	// with a different name, three menus away, whose own help text admitted it
	// breaks waking up — and finding that took an evening.
	fmt.Fprintln(stdout, "If nothing happens, the setting is usually in the sleeping")
	fmt.Fprintln(stdout, "machine's firmware. Restart it, open the BIOS, and:")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "  - turn ON  \"Wake on LAN\" or \"Power On By PCI-E\"")
	fmt.Fprintln(stdout, "  - turn OFF \"ERP\", \"EuP\", \"Deep Sleep\", or")
	fmt.Fprintln(stdout, "             \"Power Off Energy Saving\"")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "The second group cuts power to the network card while the")
	fmt.Fprintln(stdout, "machine is off, so it never hears the packet. Leave the")
	fmt.Fprintln(stdout, "machine plugged in to the mains as well — many laptops will")
	fmt.Fprintln(stdout, "not wake on battery whatever the settings say.")
	return nil
}

// sendMagicPacket sends the packet that wakes a machine.
//
// Six bytes of 0xFF followed by the target's hardware address sixteen times.
// That is the whole format — there is no library worth the dependency, and
// writing it here means `homebasectl` gains nothing to keep up to date.
func sendMagicPacket(address, broadcast string) error {
	hardware, err := net.ParseMAC(address)
	if err != nil {
		return fmt.Errorf("%q is not a hardware address: %w", address, err)
	}

	packet := make([]byte, 0, 102)
	for range 6 {
		packet = append(packet, 0xFF)
	}
	for range 16 {
		packet = append(packet, hardware...)
	}

	// Port 9, the discard port, which is the convention. Nothing listens on it;
	// the network card is woken by the packet arriving, not by anything reading
	// it.
	conn, err := net.Dial("udp", net.JoinHostPort(broadcast, "9"))
	if err != nil {
		return fmt.Errorf("could not send the wake-up packet: %w\n\n"+
			"Sending to a broadcast address needs a network that allows it.",
			err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write(packet); err != nil {
		return fmt.Errorf("could not send the wake-up packet: %w", err)
	}
	return nil
}

// normaliseMAC is used when reporting the server's own address, so it is written
// the way somebody would type it back.
func normaliseMAC(address string) string {
	return strings.ToUpper(strings.ReplaceAll(address, "-", ":"))
}
