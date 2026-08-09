package hostd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mount units, written by Homebase and enabled through systemd.
//
// Never /etc/fstab. A malformed or unsatisfiable fstab entry stops the boot and
// drops the machine to an emergency shell — on a laptop in a cupboard belonging
// to somebody who was promised they would never need a terminal, that is a
// brick, and the thing that caused it is a disk they unplugged. See ADR-0013.
//
// Every unit written here carries nofail. There is no setting to turn that off.

const (
	// systemdUnitDir is where Homebase's own units live. Only files matching
	// unitPrefix are ever touched.
	systemdUnitDir = "/etc/systemd/system"

	// DefaultStorageRoot is where managed locations are mounted.
	//
	// It is also what namespaces the units. A mount unit's name is derived from
	// its path by systemd's rules and cannot be prefixed, so the guarantee that
	// Homebase only ever writes or deletes its own units comes from every
	// managed mount point being under this root — checked before any unit file
	// is touched.
	DefaultStorageRoot = "/srv/homebase/storage"

	// mountOptions are applied to every managed location.
	//
	// nosuid and nodev are not defensive decoration. A removable disk is
	// untrusted input: it can be prepared on another machine and posted, and a
	// setuid root binary or a device node on it is a local root exploit that
	// arrives by hand. See ADR-0013.
	mountOptions = "nosuid,nodev,noexec"

	// deviceTimeout is how long systemd waits for an absent disk at boot before
	// giving up on that one unit. Short: a disk that is not there is not going
	// to appear, and a user watching a machine start should not wait 90 seconds
	// for systemd's default to expire.
	deviceTimeout = "5s"
)

// mountUnitName is the escaped unit name systemd requires for a mount point.
//
// systemd derives it from the path: /srv/homebase/storage/media becomes
// srv-homebase-storage-media.mount. Getting this wrong produces a unit systemd
// loads and never associates with the directory, which then silently never
// mounts.
func mountUnitName(mountPoint string) string {
	return systemdEscapePath(mountPoint) + ".mount"
}

// systemdEscapePath implements `systemd-escape --path`.
//
// Written out rather than shelled out to, for the same reason the rest of this
// package reads /proc directly: a root process should not need a subprocess to
// answer a question about a string.
func systemdEscapePath(path string) string {
	path = filepath.Clean(path)
	path = strings.Trim(path, "/")

	if path == "" {
		return "-"
	}

	var out strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c == '/':
			out.WriteByte('-')
		case c == '.' && i == 0:
			// A leading dot is escaped; a dot elsewhere is not.
			fmt.Fprintf(&out, `\x%02x`, c)
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == ':', c == '_', c == '.':
			out.WriteByte(c)
		default:
			fmt.Fprintf(&out, `\x%02x`, c)
		}
	}
	return out.String()
}

// managedMountUnit renders the unit for one location.
func managedMountUnit(uuid, mountPoint, filesystem, description string) string {
	fstype := filesystem
	if fstype == "" {
		// systemd works it out. Homebase knows the type from the superblock, but
		// if it somehow did not, auto is right rather than refusing to mount.
		fstype = "auto"
	}

	return fmt.Sprintf(`# Written by Homebase. Do not edit.
#
# This file is regenerated whenever the location it describes changes, and
# deleted when the location is removed. Edits will be lost.
#
# The disk is named by filesystem UUID rather than by device path: /dev/sdb
# becomes /dev/sdc when something else is plugged in first, and a mount pointed
# at a device path would then be pointed at a different disk. See ADR-0013.

[Unit]
Description=%s
Documentation=https://github.com/HusnuOkanCakir/homebase/blob/main/docs/decisions/0013-storage-identity-and-mounting.md
DefaultDependencies=no
Conflicts=umount.target
Before=umount.target

[Mount]
What=/dev/disk/by-uuid/%s
Where=%s
Type=%s
# nofail: a disk that is not connected must never stop this machine booting.
Options=%s,nofail,x-systemd.device-timeout=%s

[Install]
WantedBy=multi-user.target
`, description, uuid, mountPoint, fstype, mountOptions, deviceTimeout)
}

// unitPath returns where a mount unit for this mount point lives.
func unitPath(mountPoint string) string {
	return filepath.Join(systemdUnitDir, mountUnitName(mountPoint))
}

// underStorageRoot refuses a mount point outside the root Homebase manages.
//
// Every path that reaches a unit file goes through here first. Without it, a
// mount point of "/" would produce -.mount and Homebase would be rewriting the
// unit that mounts the root filesystem.
func underStorageRoot(root, mountPoint string) error {
	root = filepath.Clean(root)
	clean := filepath.Clean(mountPoint)

	if clean == root || !strings.HasPrefix(clean, root+"/") || strings.Contains(mountPoint, "..") {
		return fmt.Errorf("%s is not a managed location under %s", mountPoint, root)
	}
	return nil
}

// writeMountUnit creates or replaces the unit for a location.
func writeMountUnit(root, uuid, mountPoint, filesystem, description string) error {
	if err := underStorageRoot(root, mountPoint); err != nil {
		return err
	}

	path := unitPath(mountPoint)

	// Written to a temporary file and renamed, so a unit is never half-written.
	// systemd may be reading this directory at any moment, and a truncated unit
	// is one that fails to parse for reasons nobody will connect to a power cut.
	temporary := path + ".new"
	body := managedMountUnit(uuid, mountPoint, filesystem, description)

	if err := os.WriteFile(temporary, []byte(body), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", temporary, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("installing %s: %w", path, err)
	}
	return nil
}

// removeMountUnit deletes the unit for a location, if Homebase wrote it.
func removeMountUnit(root, mountPoint string) error {
	if err := underStorageRoot(root, mountPoint); err != nil {
		return err
	}
	if err := os.Remove(unitPath(mountPoint)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
