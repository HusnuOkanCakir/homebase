package hostd

import (
	"context"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

// Keeping `<hostname>.local` answerable.
//
// avahi publishes the machine's name on every interface it can see. On a server
// running applications that is ten or eleven Docker bridges, so a laptop asking
// for the server gets back a list of 172.x addresses that only the server can
// reach, and the name becomes intermittently useless with nothing to point at.
//
// The packaging tried to prevent this with `deny-interfaces=docker0,br-`, which
// does not work: avahi matches whole interface names and `br-` is not a prefix.
// It stopped docker0 and nothing else, so the bug arrived the moment anybody
// installed an application made of more than one container — which is most of
// them.
//
// Naming the real card instead is correct and, on its own, fragile: this
// machine has enumerated the same wired card as both enp4s0 and enp5s0 across
// reboots, and a configuration naming one of them published nothing at all the
// morning it came back as the other. The server was fine and appeared to have
// left the network.
//
// So the list is not written once and trusted. hostd already starts on every
// boot and already owns configuration; it works out the real cards each time and
// writes them, which is the same approach wake-on-LAN takes and for the same
// reason.

const (
	avahiConfig = "/etc/avahi/avahi-daemon.conf"
	avahiUnit   = "avahi-daemon"
)

var avahiInterfaceLine = regexp.MustCompile(`(?m)^\s*(allow|deny)-interfaces\s*=.*$`)

// PublishOnRealInterfacesOnly points avahi at this machine's actual cards.
//
// Failures are logged, never returned. A name that does not resolve is a
// nuisance; hostd refusing to start is the tool that diagnoses it being absent.
func PublishOnRealInterfacesOnly(ctx context.Context, log *slog.Logger) {
	raw, err := os.ReadFile(avahiConfig)
	if err != nil {
		// No avahi on this machine, which is legitimate.
		if !os.IsNotExist(err) {
			log.Warn("could not read the discovery configuration",
				"path", avahiConfig, "error", err)
		}
		return
	}

	cards := realInterfaces(sysClassNet)
	if len(cards) == 0 {
		// Writing an empty allow list would publish on nothing at all, which is
		// worse than publishing on too much.
		log.Warn("found no real network cards; leaving discovery configuration alone")
		return
	}
	wanted := "allow-interfaces=" + strings.Join(cards, ",")

	updated, changed := replaceAvahiInterfaces(string(raw), wanted)
	if !changed {
		return
	}
	if err := writeRootFile(avahiConfig, updated, 0o644); err != nil {
		log.Warn("could not update the discovery configuration",
			"path", avahiConfig, "error", err)
		return
	}
	log.Info("discovery restricted to this machine's real cards", "interfaces", wanted)
	if err := runSystemctl(ctx, "restart", avahiUnit); err != nil {
		log.Warn("could not restart discovery", "unit", avahiUnit, "error", err)
	}
}

// replaceAvahiInterfaces sets the interface line, reporting whether anything
// changed so an unchanged file is not rewritten and the daemon not restarted on
// every boot.
func replaceAvahiInterfaces(config, wanted string) (string, bool) {
	if avahiInterfaceLine.MatchString(config) {
		updated := avahiInterfaceLine.ReplaceAllString(config, wanted)
		return updated, updated != config
	}
	// No line at all: add one under [server], which is where it belongs and the
	// only section avahi reads it from.
	server := regexp.MustCompile(`(?m)^\[server\]$`)
	if server.MatchString(config) {
		return server.ReplaceAllString(config, "[server]\n"+wanted), true
	}
	return config, false
}
