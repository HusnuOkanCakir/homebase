package hostd

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every remote folder reported as disconnected while it was mounted, readable
// and plainly there in `findmnt`. The Files screen would never have offered one.
//
// The cause: the mount table reader this used was the disk-shaped one, which
// keeps only entries whose source begins with /dev/. A folder mounted from
// //dads-laptop/sandisk is not in that list at all.
func TestAMountFromAnotherComputerCountsAsMounted(t *testing.T) {
	table := filepath.Join(t.TempDir(), "mountinfo")
	contents := "" +
		"25 1 8:2 / / rw,relatime shared:1 - ext4 /dev/sda2 rw\n" +
		"62 31 0:51 / /srv/homebase/storage/remote/dads-disk ro,nosuid shared:369 - cifs //dads-laptop/sandisk rw,vers=3.0\n" +
		"36 25 0:31 / /mnt/with\\040space rw shared:11 - btrfs /dev/sdc1 rw\n"
	if err := os.WriteFile(table, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	if !isMountPoint("/srv/homebase/storage/remote/dads-disk", table) {
		t.Fatal("a folder mounted from another computer is reported as absent")
	}
	// A space in a mount point is escaped in the table, and a reader that does
	// not undo that answers about a path nobody has.
	if !isMountPoint("/mnt/with space", table) {
		t.Fatal("a mount point containing a space is not recognised")
	}
	if isMountPoint("/srv/homebase/storage/remote/somebody-else", table) {
		t.Fatal("a folder that is not mounted is reported as connected")
	}
	// The prefix of a real mount point is a different directory.
	if isMountPoint("/srv/homebase/storage/remote", table) {
		t.Fatal("the parent of a mount point is reported as mounted")
	}
}

// The unit is what enforces read-only, and it is not a policy that could be
// relaxed by a caller: it mounts a disk belonging to somebody standing next to
// it, and nothing at this end has any business writing to it.
func TestAFolderOnAnotherComputerIsMountedReadOnly(t *testing.T) {
	unit := remoteMountUnit(RemoteFolder{
		Name: "dads-disk", Host: "dads-laptop", Share: "sandisk",
	}, "/srv/homebase/storage/remote/dads-disk", 111, 113)

	for _, option := range []string{",ro,", "vers=3.0", "soft", "nosuid", "nodev", "noexec"} {
		if !strings.Contains(unit, option) {
			t.Errorf("the mount is missing %q", option)
		}
	}
	// The password is not in the unit, which is world-readable in
	// /etc/systemd/system. It goes in a root-only credentials file.
	if !strings.Contains(unit, "credentials=/etc/homebase/remote-dads-disk.cred") {
		t.Error("the credentials are not read from a file")
	}
	if strings.Contains(unit, "password=") {
		t.Error("a password would be written into a unit file everybody can read")
	}
	// No [Install]: a laptop in the next room is expected to be absent most of
	// the time, and a unit that tried at boot would delay the machine starting
	// or fill the journal with failures nobody can act on.
	if strings.Contains(unit, "[Install]") {
		t.Error("this would be mounted at boot, when the other computer is off")
	}
}

// The first household hit this on their first attempt. They typed the computer
// name their own PC reports — `whoami` says `ozan\fozan` — and got "Homebase
// could not open that folder", with a recovery line about checking the password.
// The journal said `could not resolve address for ozan`. Nothing about the
// password was wrong.
func TestWhatMountSaidBecomesSomethingToActOn(t *testing.T) {
	folder := RemoteFolder{Name: "dads-disk", Host: "ozan", Share: "slayer"}

	cases := []struct {
		said     string
		reason   mountFailure
		mentions string
	}{
		{"mount error: could not resolve address for ozan: Unknown error",
			mountUnresolved, "could not find a computer called ozan"},
		{"mount error(13): Permission denied", mountRefused, "refused that name and password"},
		{"mount error(2): No such file or directory", mountNoSuchShare, "nothing shared called slayer"},
		{"mount error(112): Host is down", mountUnreachable, "did not answer"},
		{"something nobody has seen before", mountUnknown, "switched on and awake"},
	}
	for _, c := range cases {
		if got := classifyMountFailure(c.said); got != c.reason {
			t.Errorf("%q was read as %v, not %v", c.said, got, c.reason)
		}
		problem := c.reason.asError(folder, c.said)
		var typed *Error
		if !errors.As(problem, &typed) {
			t.Fatalf("%q did not produce a typed error", c.said)
		}
		whole := typed.Message + " " + typed.Recovery
		if !strings.Contains(whole, c.mentions) {
			t.Errorf("%q produced %q, which does not mention %q", c.said, whole, c.mentions)
		}
		// What mount actually said is kept, so somebody who does read logs can
		// see it without going to the journal.
		if typed.Detail != c.said {
			t.Errorf("the underlying message was lost: %q", typed.Detail)
		}
	}
}

// Only a bare name is worth retrying as mDNS. An address that does not answer
// is not going to answer as `192.168.1.42.local`, and a name that already has a
// dot in it has been spelled out on purpose.
func TestOnlyABareNameIsWorthRetryingAsMDNS(t *testing.T) {
	worthIt := func(host string) bool {
		return !strings.Contains(host, ".") && net.ParseIP(host) == nil
	}
	for _, host := range []string{"ozan", "dads-laptop", "OZAN"} {
		if !worthIt(host) {
			t.Errorf("%q would not be retried as an mDNS name", host)
		}
	}
	for _, host := range []string{"192.168.1.42", "ozan.local", "nas.home.arpa", "::1"} {
		if worthIt(host) {
			t.Errorf("%q would be retried as %s.local, which is nonsense", host, host)
		}
	}
}
