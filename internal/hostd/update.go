package hostd

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// What version this machine is running, and whether it is running one version.
//
// Homebase is four packages that depend on each other with `(= version)`, so in
// a healthy installation they all agree. When they do not, something stopped
// half way — which is the exact state Milestone 8's exit condition is about, and
// the reason this reports a set rather than a single string.
//
// Unlike the system information in ops_system.go, this does shell out. That is
// deliberate and the distinction is worth stating: `/proc` and `/sys` are kernel
// interfaces with stable documented formats, whereas `/var/lib/dpkg/status` is
// dpkg's private database and `dpkg-query` is the supported way to ask about it.
// Reading the database directly would be depending on an implementation detail
// of the component whose correctness this whole milestone rests on.

// homebasePackages is everything an installation is made of, in dependency
// order. Fixed rather than discovered: a machine missing one of these is
// broken in a way worth reporting, and a glob would report it as fine.
var homebasePackages = []string{
	"homebase-hostd",
	"homebase-core",
	"homebase-apps",
	"homebase-dashboard",
}

// Component is one installed package.
type Component struct {
	Package string `json:"package"`

	// Version is empty when the package is not installed.
	Version string `json:"version"`

	// State is dpkg's own word for it: `installed`, `not-installed`,
	// `config-files`, `half-configured`, `unpacked`. Passed through rather than
	// simplified to a boolean, because the difference between "absent" and
	// "half-installed" is the difference between a machine that needs an install
	// and one that needs finishing.
	State string `json:"state"`
}

// installed reports whether this component is present and fully configured.
func (c Component) installed() bool { return c.State == "installed" }

// interrupted reports whether dpkg stopped part way through this package.
//
// Anything that is neither fully installed nor fully absent. `config-files` is
// excluded on purpose: that is a package the user removed without purging,
// which is a finished state and not a failure.
func (c Component) interrupted() bool {
	switch c.State {
	case "", "installed", "not-installed", "config-files":
		return false
	default:
		return true
	}
}

// UpdateStatus is what this machine is running and where it gets updates from.
type UpdateStatus struct {
	// Version is the version of Homebase as a whole, taken from homebase-core
	// because that is the component a user is actually running. Empty if it is
	// not installed.
	Version string `json:"version"`

	// Consistent is whether every installed component reports the same version.
	//
	// False means an update did not finish. The packages depend on each other
	// with `(= version)`, so apt would never produce this deliberately — only an
	// interruption or a manual dpkg can.
	Consistent bool `json:"consistent"`

	// Interrupted is whether dpkg has a transaction it did not finish. A machine
	// in this state usually still works; it needs `dpkg --configure -a` before
	// anything else can be installed.
	Interrupted bool `json:"interrupted"`

	Components []Component `json:"components"`

	// Channel is the suite this machine subscribes to — development, alpha, beta
	// or stable. Empty when no update source is configured, which is the state
	// every machine installed from media starts in.
	Channel string `json:"channel"`

	// Origin is where updates would come from. Reported so that a machine
	// pointed at the wrong repository is visible rather than merely quiet.
	Origin string `json:"origin"`
}

// readComponents asks dpkg what is installed.
func readComponents(ctx context.Context) ([]Component, error) {
	binary, err := exec.LookPath("dpkg-query")
	if err != nil {
		// Not a Debian machine, or a broken one. Either way there is nothing to
		// report rather than something to fail over: this is a read.
		return nil, nil
	}

	args := append([]string{
		"--show",
		"--showformat=${Package}\t${Version}\t${db:Status-Status}\n",
	}, homebasePackages...)

	// Output is used whatever the exit status. dpkg-query exits non-zero when
	// *any* named package is unknown, while still printing the ones it found —
	// so on a machine where homebase-apps was never installed, treating exit 1
	// as failure would discard a perfectly good answer about the other three.
	out, _ := exec.CommandContext(ctx, binary, args...).Output()

	return parseComponents(string(out)), nil
}

func parseComponents(out string) []Component {
	found := map[string]Component{}

	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 3 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		if name == "" {
			continue
		}
		found[name] = Component{
			Package: name,
			Version: strings.TrimSpace(fields[1]),
			State:   strings.TrimSpace(fields[2]),
		}
	}

	// Reported in the fixed order above, including the ones dpkg said nothing
	// about. A component missing from the output is missing from the machine,
	// and silently omitting it would make a broken installation look complete.
	components := make([]Component, 0, len(homebasePackages))
	for _, name := range homebasePackages {
		if component, ok := found[name]; ok {
			components = append(components, component)
			continue
		}
		components = append(components, Component{Package: name, State: "not-installed"})
	}
	return components
}

// consistent reports whether every installed component agrees on a version.
func consistent(components []Component) bool {
	var seen string
	for _, component := range components {
		if !component.installed() {
			continue
		}
		if seen == "" {
			seen = component.Version
			continue
		}
		if component.Version != seen {
			return false
		}
	}
	return true
}

// dpkgInterrupted reports whether dpkg has an unfinished transaction.
//
// Two signals, because they catch different halves of the same failure. A
// package in a partial state is what a machine looks like after dpkg was killed
// between unpacking and configuring. Files in dpkg's journal directory are what
// it looks like when dpkg died before it could even update its own database —
// the state dpkg replays on its next run.
func dpkgInterrupted(components []Component, dpkgUpdates string) bool {
	for _, component := range components {
		if component.interrupted() {
			return true
		}
	}

	entries, err := os.ReadDir(dpkgUpdates)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return true
		}
	}
	return false
}

// readChannel works out which suite this machine subscribes to.
//
// Read from apt's own source file rather than from a Homebase setting, because
// apt's file is what actually decides where packages come from. A setting that
// says `stable` on a machine whose source says `development` is a lie in the
// place it matters most, and keeping one copy makes that impossible.
//
// deb822 format (`Types:`/`URIs:`/`Suites:`) rather than the one-line form: it
// is unambiguous to parse, which for the file that determines what code this
// machine will execute as root is worth more than brevity.
func readChannel(path string) (channel, origin string) {
	file, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "suites":
			// One suite per machine. A source listing several would make
			// "which channel is this?" unanswerable, so only the first is
			// reported and configuring them is Homebase's job, not a user's.
			channel = firstField(value)
		case "uris":
			origin = firstField(value)
		}
	}
	return channel, origin
}

func firstField(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// ReadUpdateStatus reports what this machine is running.
func ReadUpdateStatus(ctx context.Context, aptSource, dpkgUpdates string) UpdateStatus {
	components, _ := readComponents(ctx)

	status := UpdateStatus{
		Components:  components,
		Consistent:  consistent(components),
		Interrupted: dpkgInterrupted(components, dpkgUpdates),
	}

	for _, component := range components {
		if component.Package == "homebase-core" && component.installed() {
			status.Version = component.Version
		}
	}

	status.Channel, status.Origin = readChannel(aptSource)
	return status
}

// defaultAptSource is where the update source lives on a real installation.
func defaultAptSource() string {
	return filepath.Join("/etc/apt/sources.list.d", "homebase.sources")
}
