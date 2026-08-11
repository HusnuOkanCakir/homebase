package hostd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Talking to apt.
//
// ADR-0018: apt does the verification, not code written here. Every signature
// check, every checksum and every refusal below is apt's, and the job of this
// file is to ask the right questions and to translate the answers into
// something a person can act on.
//
// The commands are fixed with fixed arguments — no shell, and nothing from a
// caller reaches an argument list without being validated first. This is a root
// process, and `update.configure` writes the file that decides which code this
// machine will execute as root.

const (
	// Where the source lives, and where the key it is allowed to be signed by
	// lives. The keyring is installed by homebase-hostd's package.
	aptSourceFile = "/etc/apt/sources.list.d/homebase.sources"
	aptPrefsFile  = "/etc/apt/preferences.d/homebase"
	aptKeyring    = "/usr/share/keyrings/homebase-archive-keyring.gpg"

	// The unit that does the part hostd cannot, and where it leaves its answer.
	updateCheckUnit = "homebase-update-check.service"
	updateResultDir = "/var/lib/homebase"
)

// aptEnv is the environment apt runs in.
//
// Non-interactive, because there is no terminal and a maintainer script that
// stops to ask a question would hang a root service until its timeout. The C
// locale so that messages parsed below do not change with the machine's
// language.
func aptEnv() []string {
	return []string{
		"DEBIAN_FRONTEND=noninteractive",
		"LC_ALL=C",
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
	}
}

// runUpdateUnit asks systemd to run one of the update units, and waits.
//
// This is the whole reason those units exist. `homebase-hostd.service` sets
//
//	RestrictAddressFamilies=AF_UNIX AF_NETLINK
//
// so this process cannot open a network socket — deliberately, because a root
// service that cannot reach the internet is a much smaller thing to get wrong.
// Anything that must talk to the network therefore cannot happen here.
//
// `systemctl start` is a message to PID 1 over a Unix socket, which is
// permitted, and the unit name is a constant chosen from a fixed set. Nothing
// from a request reaches it.
func runUpdateUnit(ctx context.Context, unit string) (string, error) {
	binary, err := exec.LookPath("systemctl")
	if err != nil {
		return "", fmt.Errorf("systemctl is not on this machine")
	}

	cmd := exec.CommandContext(ctx, binary, "start", "--wait", unit)
	cmd.Env = aptEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// readResultFile parses the key=value lines an update unit leaves behind.
//
// Written by a shell script, so key=value rather than JSON: emitting correct
// JSON from shell means escaping by hand in the one place a hostile value could
// arrive, which is not a trade worth making. Values may contain `=`, so only
// the first one separates.
func readResultFile(path string) map[string]string {
	values := map[string]string{}

	content, err := os.ReadFile(path)
	if err != nil {
		return values
	}
	for _, line := range strings.Split(string(content), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}

// newerVersion reports whether a is a higher version than b.
//
// dpkg does the comparison. Debian version ordering has epochs, tildes that
// sort *before* the empty string, and rules that a plain string compare gets
// wrong — and getting it wrong here means either refusing a real update or
// accepting a downgrade, which is the thing downgrade protection exists to
// prevent.
func newerVersion(ctx context.Context, a, b string) bool {
	if a == "" {
		return false
	}
	if b == "" {
		return true
	}
	binary, err := exec.LookPath("dpkg")
	if err != nil {
		return false
	}
	cmd := exec.CommandContext(ctx, binary, "--compare-versions", a, "gt", b)
	cmd.Env = aptEnv()
	return cmd.Run() == nil
}

// writeAptSource points this machine at a channel.
//
// deb822 rather than the one-line format: it is unambiguous to parse and to
// write, and `Signed-By` naming an explicit keyring is what stops this key
// being trusted for every other package on the machine.
func writeAptSource(origin, channel string) error {
	source := "" +
		"# Written by Homebase. Change the channel from the dashboard rather than\n" +
		"# by editing this file, so that what the machine reports matches what it does.\n" +
		"Types: deb\n" +
		"URIs: " + origin + "\n" +
		"Suites: " + channel + "\n" +
		"Components: main\n" +
		"Signed-By: " + aptKeyring + "\n"

	if err := writeRootFile(aptSourceFile, source, 0o644); err != nil {
		return err
	}

	// The pin, and it is load-bearing rather than tidy.
	//
	// `Signed-By` binds a key to a source; it does not bind it to package
	// names. Without this, a repository Homebase controls could offer a package
	// called `openssh-server` and win on version number — so compromising the
	// signing key would mean replacing anything on the machine rather than only
	// Homebase's own packages. This restricts the origin to what it is for.
	prefs := "" +
		"# Homebase's repository may provide Homebase's packages, and nothing else.\n" +
		"# Removing this widens a compromise of the signing key from four packages\n" +
		"# to every package on this machine.\n" +
		"#\n" +
		"# The negative priority is what does the work: it does not merely lose a\n" +
		"# version comparison, it tells apt never to install from that origin.\n" +
		"Package: *\n" +
		"Pin: release o=Homebase\n" +
		"Pin-Priority: -1\n" +
		"\n" +
		"Package: homebase-hostd homebase-core homebase-apps homebase-dashboard\n" +
		"Pin: release o=Homebase\n" +
		"Pin-Priority: 900\n"

	return writeRootFile(aptPrefsFile, prefs, 0o644)
}

// writeRootFile writes a configuration file atomically.
//
// Written and renamed so that a machine losing power here comes back with
// either the old file or the new one. Half of an apt source is a machine that
// cannot install anything, including the update that would fix it.
func writeRootFile(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	temporary := path + ".new"
	if err := os.WriteFile(temporary, []byte(content), mode); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
