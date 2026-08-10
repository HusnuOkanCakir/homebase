package hostd

import (
	"os/exec"
	"strings"
	"testing"
)

// The unit name has to be exactly what systemd would derive from the path.
//
// Getting it wrong does not produce an error. systemd loads the unit, never
// associates it with the directory, and the location silently never mounts —
// which looks to a user like a disk that does not work.
func TestUnitNamesMatchSystemd(t *testing.T) {
	paths := []string{
		"/srv/homebase/storage/media",
		"/srv/homebase/storage/films2024",
		"/srv/homebase/storage/My Films",  // a space
		"/srv/homebase/storage/a-b_c.d",   // a hyphen, which systemd escapes
		"/srv/homebase/storage/naïve",     // non-ASCII, escaped per byte
		"/srv/homebase/storage/back.up",   // a dot that is not leading
		"/srv/homebase/storage/x/y/z",     // nested
		"/srv/homebase/storage/trailing/", // a trailing slash to clean off
	}

	expected := map[string]string{
		"/srv/homebase/storage/media":     "srv-homebase-storage-media",
		"/srv/homebase/storage/films2024": "srv-homebase-storage-films2024",
		"/srv/homebase/storage/My Films":  `srv-homebase-storage-My\x20Films`,
		"/srv/homebase/storage/a-b_c.d":   `srv-homebase-storage-a\x2db_c.d`,
		"/srv/homebase/storage/naïve":     `srv-homebase-storage-na\xc3\xafve`,
		"/srv/homebase/storage/back.up":   "srv-homebase-storage-back.up",
		"/srv/homebase/storage/x/y/z":     "srv-homebase-storage-x-y-z",
		"/srv/homebase/storage/trailing/": "srv-homebase-storage-trailing",
	}

	for _, path := range paths {
		got := systemdEscapePath(path)
		if want := expected[path]; got != want {
			t.Errorf("systemdEscapePath(%q) = %q, want %q", path, got, want)
		}
	}

	// And against systemd itself, which is the actual authority. Skipped where
	// it is not installed, so the fixed expectations above still stand alone —
	// but where it is, this is what catches a rule that changed.
	binary, err := exec.LookPath("systemd-escape")
	if err != nil {
		t.Skip("systemd-escape is not installed; checked against fixed expectations only")
	}

	for _, path := range paths {
		out, err := exec.Command(binary, "--path", path).Output()
		if err != nil {
			t.Fatalf("systemd-escape --path %q: %v", path, err)
		}
		want := strings.TrimSpace(string(out))
		if got := systemdEscapePath(path); got != want {
			t.Errorf("systemdEscapePath(%q) = %q, systemd says %q", path, got, want)
		}
	}
}

// Every generated unit carries nofail. A disk that is not connected must never
// stop the machine booting — that is the whole reason these are units rather
// than fstab entries, and a unit missing it would put the failure back.
func TestEveryUnitRefusesToBlockTheBoot(t *testing.T) {
	unit := managedMountUnit("abcd-1234", "/srv/homebase/storage/media", "ext4", "Media")

	if !strings.Contains(unit, "nofail") {
		t.Error("the unit does not carry nofail")
	}
	if !strings.Contains(unit, "x-systemd.device-timeout=") {
		t.Error("the unit has no device timeout, so an absent disk delays the boot")
	}
}

// The disk is named by UUID, never by device path. This is ADR-0013 in one
// assertion: a unit naming /dev/sdb points at whatever is sdb today.
func TestUnitsNameTheDiskByUUID(t *testing.T) {
	unit := managedMountUnit("abcd-1234", "/srv/homebase/storage/media", "ext4", "Media")

	if !strings.Contains(unit, "What=/dev/disk/by-uuid/abcd-1234") {
		t.Errorf("the unit does not name the disk by UUID:\n%s", unit)
	}
	for _, forbidden := range []string{"What=/dev/sd", "What=/dev/nvme", "What=/dev/vd"} {
		if strings.Contains(unit, forbidden) {
			t.Errorf("the unit names a device path (%s)", forbidden)
		}
	}
}

// A removable disk is untrusted input: it can be prepared elsewhere and posted.
func TestUnitsMountRemovableDisksWithoutTrustingThem(t *testing.T) {
	unit := managedMountUnit("abcd-1234", "/srv/homebase/storage/media", "ext4", "Media")

	for _, option := range []string{"nosuid", "nodev", "noexec"} {
		if !strings.Contains(unit, option) {
			t.Errorf("the unit does not mount with %s", option)
		}
	}
}

func TestUnitsWithoutAKnownFilesystemFallBackToAuto(t *testing.T) {
	unit := managedMountUnit("abcd-1234", "/srv/homebase/storage/media", "", "Media")
	if !strings.Contains(unit, "Type=auto") {
		t.Error("a location with no detected filesystem did not fall back to auto")
	}
}

// Nothing outside the storage root may become a unit. A mount point of "/"
// escapes to -.mount, which is the unit that mounts the root filesystem.
func TestUnitsAreRefusedOutsideTheStorageRoot(t *testing.T) {
	root := "/srv/homebase/storage"

	for _, path := range []string{
		"/",
		"/etc",
		"/srv/homebase",
		"/srv/homebase/storage", // the root itself is not a location
		"/srv/homebase/storage/../../etc",
		"/srv/homebase/storage-elsewhere", // a prefix match that is not a child
		"/var/lib/homebase",
	} {
		if err := underStorageRoot(root, path); err == nil {
			t.Errorf("%s was accepted as a managed location", path)
		}
	}

	for _, path := range []string{
		"/srv/homebase/storage/media",
		"/srv/homebase/storage/a/b",
	} {
		if err := underStorageRoot(root, path); err != nil {
			t.Errorf("%s was refused: %v", path, err)
		}
	}
}

// The generated unit says who wrote it and that edits are lost. Somebody will
// find this file while trying to fix something at 11pm.
func TestUnitsSayTheyAreGenerated(t *testing.T) {
	unit := managedMountUnit("abcd-1234", "/srv/homebase/storage/media", "ext4", "Media")
	if !strings.Contains(unit, "Written by Homebase") {
		t.Error("the unit does not say what wrote it")
	}
	if !strings.Contains(unit, "Do not edit") {
		t.Error("the unit does not warn that edits are lost")
	}
}

// A generated unit must not be able to run anything.
//
// hostd is granted write access to /etc/systemd/system so that managed mounts
// survive a reboot, which is the widest grant in its unit file. What keeps that
// from being a generic execution path is that the only units it writes are
// .mount units, and a .mount unit has no Exec directives.
//
// If this ever fails, the sandbox grant has become a way to run code as root at
// boot, which is precisely what ADR-0006 exists to prevent.
func TestGeneratedUnitsCannotRunAnything(t *testing.T) {
	// Every value that could plausibly carry an injection: the description comes
	// from a user-supplied location name.
	unit := managedMountUnit(
		"abcd-1234",
		"/srv/homebase/storage/media",
		"ext4",
		"Homebase storage: Films",
	)

	for _, directive := range []string{
		"ExecStart", "ExecStop", "ExecReload", "ExecStartPre", "ExecStartPost",
		"ExecCondition", "ExecStopPost",
	} {
		if strings.Contains(unit, directive) {
			t.Errorf("the generated unit contains %s, so it can run code as root", directive)
		}
	}

	// And it is a mount unit, not a service. Only [Unit], [Mount] and [Install]
	// sections; a [Service] section would be a different kind of unit entirely.
	if strings.Contains(unit, "[Service]") {
		t.Error("the generated unit has a [Service] section")
	}
	if !strings.Contains(unit, "[Mount]") {
		t.Error("the generated unit is not a mount unit")
	}
}

// A location name is typed by a user and lands in the unit's Description. A
// newline in it would let the rest of the line become a new directive.
func TestALocationNameCannotInjectADirective(t *testing.T) {
	unit := managedMountUnit(
		"abcd-1234",
		"/srv/homebase/storage/media",
		"ext4",
		"Films\nExecStartPre=/bin/sh -c id",
	)

	// The generator refuses to emit a line break, so the injected text stays on
	// the Description line where it means nothing.
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ExecStartPre=") {
			t.Errorf("a location name became a directive:\n%s", unit)
		}
	}

	// And the ids and labels that reach the rest of the system are validated
	// separately, so this is not the only thing standing in the way.
	if validFilesystemLabel("Films\nExecStartPre=/bin/sh -c id") {
		t.Error("a label containing a newline was accepted")
	}
	if validLocationID.MatchString("films\nExecStartPre=x") {
		t.Error("a location id containing a newline was accepted")
	}
}

func TestControlCharactersNeverReachAUnitFile(t *testing.T) {
	names := []string{
		"Films\nExecStart=/bin/sh",
		"Films\r\n[Service]",
		"Films\x00hidden",
		"Films\tand\ttabs",
	}

	for _, name := range names {
		unit := managedMountUnit("abcd", "/srv/homebase/storage/media", "ext4", name)

		// Exactly the lines the template defines, and no more. A name that could
		// add one is a name that could add a directive.
		descriptions := 0
		for _, line := range strings.Split(unit, "\n") {
			if strings.HasPrefix(line, "Description=") {
				descriptions++
			}
			if strings.HasPrefix(line, "ExecStart") || line == "[Service]" {
				t.Errorf("%q produced the line %q", name, line)
			}
		}
		if descriptions != 1 {
			t.Errorf("%q produced %d Description lines", name, descriptions)
		}
	}
}
