package main

import (
	"context"
	"fmt"
	"io"
)

// Switching the server off, and restarting it, from a terminal.
//
//	homebasectl shutdown
//	homebasectl restart
//
// These exist mostly for the machine that is being worked on over SSH, where
// `sudo poweroff` would do the same thing. The difference is what Homebase knows
// and the shell does not: whether this machine can be switched on again without
// somebody walking to it.
//
// That is the whole reason `shutdown` is not just an alias for poweroff. A
// server in a cupboard, switched off from a laptop in another room, is a mistake
// that costs a trip up a ladder — and it is entirely avoidable if the one screen
// that can still say "waking it over the network is switched on" says so before
// the confirmation rather than after.

func shutdownCommand(args []string, stdout io.Writer) error {
	return withClient("shutdown", args, stdout, power("shutdown"))
}

func restartCommand(args []string, stdout io.Writer) error {
	return withClient("restart", args, stdout, power("restart"))
}

func power(kind string) func(context.Context, *Client, *options, []string, io.Writer) error {
	return func(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
		var system struct {
			Hostname string `json:"hostname"`
		}
		if err := c.Get(ctx, "/system", &system); err != nil {
			return err
		}

		off := kind == "shutdown"
		if off {
			fmt.Fprintf(w, "This is %s.\n\n", system.Hostname)
			fmt.Fprintln(w, "Switching it off stops everything on it — anything being")
			fmt.Fprintln(w, "watched, any download, any backup that is running.")
			describeWaking(ctx, c, w)
		} else {
			fmt.Fprintf(w, "This is %s.\n\n", system.Hostname)
			fmt.Fprintln(w, "Restarting takes a minute or two. Everything on it stops")
			fmt.Fprintln(w, "until it is back, and then starts again by itself.")
		}

		prompt := "It will stay off until somebody switches it on."
		if !off {
			prompt = "Everything on it stops until it comes back."
		}
		confirmed, err := confirmDestruction(w, system.Hostname, prompt, o.confirm)
		if err != nil || !confirmed {
			return err
		}

		path := "/system/shutdown"
		if !off {
			path = "/system/reboot"
		}

		var job struct {
			JobID   string `json:"job_id"`
			Message string `json:"message"`
		}
		// A connection that dies here is the operation succeeding, not failing:
		// the machine went away before it could answer. Anything else is
		// reported.
		err = c.Post(ctx, path, map[string]any{
			"confirm": system.Hostname,
			"reason":  "Asked for from a terminal",
		}, &job)
		if err != nil && !connectionLost(err) {
			return err
		}
		if o.asJSON {
			return printResponse(w, c, job)
		}

		if off {
			fmt.Fprintf(w, "\n%s is switching off.\n", system.Hostname)
			fmt.Fprintln(w, "\nTo switch it on again: press its power button, or run")
			fmt.Fprintln(w, "`homebasectl wake` from another computer on this network.")
		} else {
			fmt.Fprintf(w, "\n%s is restarting. It will be back in a minute or two.\n",
				system.Hostname)
		}
		return nil
	}
}

// describeWaking says whether this machine can be switched on again remotely.
//
// Printed before the confirmation, because afterwards there is no terminal
// session left to print it into — the machine that was answering is the machine
// going away.
//
// Silent about the interfaces it cannot get an answer for. A card that will not
// report its wake setting is not a card that said no, and neither "it can be
// woken" nor "it cannot" is a thing to tell somebody on that evidence.
func describeWaking(ctx context.Context, c *Client, w io.Writer) {
	var status networkReply
	if err := c.Get(ctx, "/network", &status); err != nil {
		return
	}

	if mac := wakeableAddress(status); mac != "" {
		fmt.Fprintf(w, "\nIt can be switched on again from here:\n")
		fmt.Fprintf(w, "    homebasectl wake %s\n", mac)
		fmt.Fprintln(w, "\nThat needs it to stay plugged in — a laptop running on its")
		fmt.Fprintln(w, "battery has nothing listening once it is off.")
		return
	}

	fmt.Fprintln(w, "\nNothing here can switch it on again. Waking it over the network")
	fmt.Fprintln(w, "is not enabled, so somebody has to press its power button.")
}

// networkReply is the part of GET /network this needs. The field names are the
// contract; see the test, which feeds it a real answer from a real server.
type networkReply struct {
	Interfaces []struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		MAC       string `json:"mac"`
		WakeOnLAN bool   `json:"wake_on_lan"`
		Known     bool   `json:"wake_on_lan_known"`
	} `json:"interfaces"`
}

// wakeableAddress is the hardware address a magic packet would reach this
// machine on, or empty if there is none.
//
// Separated from the printing so that it can be tested against what a server
// actually sends, which is how the vocabulary error in it was found: this asked
// for kind "wired" and hostd says "ethernet", so on a machine where waking
// worked perfectly the answer was "nothing here can switch it on again". Every
// hand-written fixture had agreed with the code, because the same person wrote
// both.
func wakeableAddress(status networkReply) string {
	for _, iface := range status.Interfaces {
		// Cables only. Waking over Wi-Fi needs the access point, the card's
		// firmware and the BIOS all to agree, and on the hardware Homebase is
		// meant for it essentially never works.
		if iface.Kind != "ethernet" {
			continue
		}
		// Three states, not two: a card that would not say is not a card that
		// said no, and neither answer should be given on that evidence.
		if !iface.Known || !iface.WakeOnLAN {
			continue
		}
		// A tunnel is reported as ethernet too and has no hardware address.
		if iface.MAC == "" {
			continue
		}
		return iface.MAC
	}
	return ""
}
