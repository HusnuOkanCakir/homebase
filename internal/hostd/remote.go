package hostd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// A folder on somebody else's computer.
//
// The problem this solves, in the words it was asked in: a disk is in a drawer,
// somebody at home plugs it into *their own* laptop, and somebody in another
// country needs a file off it. Nothing about that involves the server's own
// disks, and the obvious answer — copy it to the server first — requires
// knowing in advance which files are wanted.
//
// The unobvious part is that this needs no software on the laptop. Windows has
// shared a folder since Windows for Workgroups; what was missing was this
// server being able to *open* one. It is a file server and was not a file
// client. So the person at home shares the drive the way they already know how,
// and Homebase mounts it and offers it in the Files screen like anything else —
// which means the person in another country reaches it over Tailscale in a
// browser, with no Homebase software on their machine either.
//
// # Read-only, always
//
// Nothing here can write to somebody's disk, and that is not a limitation
// waiting to be lifted. The whole point is somebody fetching files off a disk
// that belongs to a person who is standing next to it; the ability to delete
// them is not part of that, and its absence means a mistake at this end — or a
// compromised server — cannot reach into a laptop in another room and destroy
// what is on a disk that was plugged in to help.
//
// # Not a storage location
//
// Deliberately a separate concept from the disks Homebase manages. A location
// is something the backup copies, applications keep data on, and Homebase will
// offer to format. None of that is true of a folder on a laptop that will be
// closed in an hour, and letting the two share a type would mean every piece of
// code that handles a location learning about one that can vanish mid-read.
const remoteDirName = "remote"

// remoteConfigDir is where the credentials files live: root-only, and the same
// directory the firewall and account requests use.
const remoteConfigDir = "/etc/homebase"

// RemoteFolder is one folder on another computer that this server can open.
type RemoteFolder struct {
	// Name is what the household calls it, and the directory it appears under.
	Name string `json:"name"`

	// Host is the computer, as typed: a name on the local network or an
	// address. Kept as typed rather than resolved — an address recorded today
	// is a different computer next week, and a name is what the person knows.
	Host string `json:"host"`

	// Share is the name the folder is shared under on that computer. Windows
	// calls this the "share name"; it is not the drive letter.
	Share string `json:"share"`

	// Username is the account on *that computer*, not on Homebase. The password
	// is not here and never is: it goes into a root-only credentials file and
	// is never returned by any operation.
	Username string `json:"username"`

	// AddedBy is the Homebase account that connected it. Whose credentials the
	// server is holding is worth being able to answer, and it decides who may
	// disconnect it again.
	AddedBy string `json:"added_by,omitempty"`

	// Access is who may open it, by Homebase username. Empty means everybody
	// with an account, the same rule shared folders use.
	Access []string `json:"access,omitempty"`

	AddedAt string `json:"added_at"`
}

// RemoteFolderState is a folder plus whether it is actually there.
type RemoteFolderState struct {
	RemoteFolder

	// Path is where it is mounted on the server.
	Path string `json:"path"`

	// Connected is whether the other computer is answering. This is the field
	// that matters: a laptop that has gone to sleep leaves a folder that is
	// configured, listed, and empty — which looks exactly like a folder whose
	// files have been deleted.
	Connected bool `json:"connected"`
}

// remoteMountPoint is where a remote folder appears on the server.
func remoteMountPoint(root, name string) string {
	return filepath.Join(root, remoteDirName, name)
}

// remoteCredentialsPath is the root-only file holding one folder's password.
func remoteCredentialsPath(name string) string {
	return filepath.Join(remoteConfigDir, "remote-"+name+".cred")
}

// writeRemoteCredentials stores the password where only root can read it.
//
// A file rather than a mount option, and this is the whole reason the file
// exists: mount options are visible in /proc/mounts, in `mount` output, and in
// the process list while the mount runs. A password in there is a password
// every account on this machine can read.
func writeRemoteCredentials(name, username, password string) error {
	body := "username=" + username + "\npassword=" + password + "\n"
	// Windows sends a domain even in a workgroup, and an empty one is what a
	// home machine wants; stating it stops mount.cifs prompting for it.
	body += "domain=\n"
	return writeRootFile(remoteCredentialsPath(name), body, 0o600)
}

// remoteMountUnit renders the mount unit for one remote folder.
//
// No [Install] section, unlike a managed disk. A disk is expected at boot and a
// missing one is a fault; a laptop in the next room is expected to be *absent*
// most of the time, and a unit that tried at boot would either delay the
// machine starting or fill the journal with failures nobody can act on. These
// are mounted when somebody asks and reconnected the same way.
func remoteMountUnit(folder RemoteFolder, mountPoint string, uid, gid int) string {
	return fmt.Sprintf(`# Written by Homebase. Do not edit.
#
# A folder on another computer on this network. Regenerated whenever it changes
# and deleted when it is disconnected; edits will be lost.
#
# Read-only on purpose. This mounts a disk that belongs to somebody who is
# standing next to it, and nothing at this end has any business writing to it.

[Unit]
Description=Homebase — %s on %s
DefaultDependencies=no
Conflicts=umount.target
Before=umount.target

[Mount]
What=//%s/%s
Where=%s
Type=cifs
# ro          nothing here may write to somebody else's disk.
# vers=3.0    the same floor the file server requires; Windows 8 and later.
# soft        a request to a sleeping laptop fails instead of hanging for ever,
#             which is the difference between a Files screen that says so and
#             one that spins.
# uid/gid     the service account, so Homebase can read what it mounted.
# nosuid,nodev,noexec  it is somebody else's disk.
Options=credentials=%s,ro,vers=3.0,soft,uid=%d,gid=%d,file_mode=0640,dir_mode=0750,nosuid,nodev,noexec
TimeoutSec=15
`, unitSafe(folder.Name), unitSafe(folder.Host),
		folder.Host, folder.Share, mountPoint,
		remoteCredentialsPath(folder.Name), uid, gid)
}

// mountRemoteFolder writes the unit and starts it.
func mountRemoteFolder(ctx context.Context, root string, folder RemoteFolder) error {
	mountPoint := remoteMountPoint(root, folder.Name)
	if err := underStorageRoot(root, mountPoint); err != nil {
		return internalError(err.Error())
	}
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		return internalError("creating " + mountPoint + ": " + err.Error())
	}

	account, err := user.Lookup(serviceAccount)
	if err != nil {
		return internalError("looking up the " + serviceAccount + " account: " + err.Error())
	}
	uid, _ := strconv.Atoi(account.Uid)
	gid, _ := strconv.Atoi(account.Gid)

	body := remoteMountUnit(folder, mountPoint, uid, gid)
	if err := writeRootFile(unitPath(mountPoint), body, 0o644); err != nil {
		return err
	}
	if err := runSystemctl(ctx, "daemon-reload"); err != nil {
		return internalError("reloading systemd: " + err.Error())
	}

	unit := mountUnitName(mountPoint)
	if err := runSystemctl(ctx, "start", unit); err != nil {
		return &Error{
			Code:        "remote.could_not_connect",
			Message:     "Homebase could not open that folder.",
			Detail:      strings.TrimSpace(err.Error()),
			Recoverable: true,
			Recovery: "Check that the computer is switched on and awake, that the " +
				"folder is still shared on it, and that the name and password are " +
				"the ones it expects.",
			Status: 502,
		}
	}
	return nil
}

// unmountRemoteFolder stops the mount and removes everything Homebase wrote.
//
// Every step is attempted even if an earlier one failed. Half-disconnecting is
// worse than either outcome: a credentials file left behind holds somebody's
// Windows password for a folder nobody can see any more.
func unmountRemoteFolder(ctx context.Context, root, name string) {
	mountPoint := remoteMountPoint(root, name)
	if err := underStorageRoot(root, mountPoint); err != nil {
		return
	}
	unit := mountUnitName(mountPoint)

	_ = runSystemctl(ctx, "stop", unit)
	_ = os.Remove(unitPath(mountPoint))
	_ = runSystemctl(ctx, "daemon-reload")
	_ = os.Remove(remoteCredentialsPath(name))
	// Only the empty directory. If the unmount failed the files are still
	// under it, and Remove refuses a non-empty directory — which is the
	// behaviour wanted rather than a case to handle.
	_ = os.Remove(mountPoint)
}

// remoteFolderConnected reports whether the other computer is answering.
//
// Reads the mount table directly rather than through readMounts, which is the
// disk-shaped one: it keeps only entries whose source begins with /dev/, so a
// mount from `//dads-laptop/sandisk` is not in it at all. This reported every
// remote folder as disconnected while it was mounted and readable, which meant
// the Files screen would never have offered a single one — found on the machine
// with the folder plainly there in `findmnt`.
func remoteFolderConnected(root, name string) bool {
	return isMountPoint(remoteMountPoint(root, name), mountInfoPath)
}

// isMountPoint reports whether something is mounted exactly there, whatever it
// is mounted from.
func isMountPoint(path, mountInfo string) bool {
	data, err := os.ReadFile(mountInfo)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if unescapeMountPath(fields[4]) == path {
			return true
		}
	}
	return false
}

// cifsInstalled reports whether this machine can open a folder on another one.
func cifsInstalled() bool {
	_, err := exec.LookPath("mount.cifs")
	return err == nil
}

// installCifs fetches the CIFS client, which is not part of the base install.
func installCifs(ctx context.Context) error {
	if cifsInstalled() {
		return nil
	}
	out, err := runUpdateUnit(ctx, "homebase-install-cifs.service")
	if err != nil {
		return &Error{
			Code:        "remote.could_not_install",
			Message:     "Homebase could not install what it needs to open folders on other computers.",
			Detail:      strings.TrimSpace(out) + ": " + err.Error(),
			Recoverable: true,
			Recovery: "Check that this server can reach the internet with " +
				"`homebasectl network`, then try again.",
			Status: 503,
		}
	}
	if !cifsInstalled() {
		return &Error{
			Code:        "remote.could_not_install",
			Message:     "Homebase could not install what it needs to open folders on other computers.",
			Detail:      "the installation finished without mount.cifs being present",
			Recoverable: true,
			Recovery:    "Run `journalctl -u homebase-install-cifs` to see what apt said.",
			Status:      500,
		}
	}
	return nil
}
