package hostd

import (
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
