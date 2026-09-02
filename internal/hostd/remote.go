package hostd

import (
	"context"
	"fmt"
	"net"
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

// remoteConfigDirFor is where credentials files live. A variable so a test can
// write one somewhere it is allowed to.
var remoteConfigDirFor = func() string { return remoteConfigDir }

// remoteCredentialsPath is the root-only file holding one folder's password.
func remoteCredentialsPath(name string) string {
	return filepath.Join(remoteConfigDirFor(), "remote-"+name+".cred")
}

// writeRemoteCredentials stores the password where only root can read it.
//
// A file rather than a mount option, and this is the whole reason the file
// exists: mount options are visible in /proc/mounts, in `mount` output, and in
// the process list while the mount runs. A password in there is a password
// every account on this machine can read.
func writeRemoteCredentials(name, username, password string) error {
	// No domain line, deliberately.
	//
	// There was one, empty, written on the reasoning that "a home machine wants
	// an empty domain". That was a guess and it is the wrong kind of guess to
	// leave in: an empty domain overrides what the person typed. Somebody
	// signing in to Windows with a Microsoft account has to authenticate as
	// `MicrosoftAccount\their@email.com`, and a `user@domain` or `DOMAIN\user`
	// name is something mount.cifs splits for itself — all of which a blank
	// domain= line quietly undoes.
	//
	// Left out, mount.cifs uses what the username carries and asks Windows.
	// That is one fewer thing between the person and an answer.
	body := "username=" + username + "\npassword=" + password + "\n"
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

// mountRemoteFolder writes the unit and starts it, and returns the host that
// actually worked.
//
// That return value is the interesting part. See below: a bare Windows computer
// name usually cannot be resolved here and `<name>.local` usually can, so the
// name that succeeds is not always the name that was typed.
func mountRemoteFolder(ctx context.Context, root string, folder RemoteFolder) (string, error) {
	mountPoint := remoteMountPoint(root, folder.Name)
	if err := underStorageRoot(root, mountPoint); err != nil {
		return "", internalError(err.Error())
	}
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		return "", internalError("creating " + mountPoint + ": " + err.Error())
	}

	account, err := user.Lookup(serviceAccount)
	if err != nil {
		return "", internalError("looking up the " + serviceAccount + " account: " + err.Error())
	}
	uid, _ := strconv.Atoi(account.Uid)
	gid, _ := strconv.Atoi(account.Gid)

	reason, err := attemptMount(ctx, folder, mountPoint, uid, gid)
	if err == nil {
		return folder.Host, nil
	}

	// A name that would not resolve, tried again as an mDNS name.
	//
	// This is the failure the first household hit, and it looked like nothing
	// they had done wrong: they typed the computer name their own PC reports —
	// `whoami` says `ozan\fozan` — and got "Homebase could not open that
	// folder". The journal said `could not resolve address for ozan`.
	//
	// hostd cannot resolve anything itself; RestrictAddressFamilies leaves it
	// AF_UNIX and AF_NETLINK. The lookup happens inside mount.cifs, which
	// systemd starts unrestricted. On that network Windows answered to
	// `ozan.local` over mDNS and to nothing else: NetBIOS was silent and DNS
	// had never heard of it. So rather than sending somebody to find an
	// address, try the name that works.
	if reason == mountUnresolved && !strings.Contains(folder.Host, ".") &&
		net.ParseIP(folder.Host) == nil {
		withMDNS := folder
		withMDNS.Host += ".local"
		retryReason, retryErr := attemptMount(ctx, withMDNS, mountPoint, uid, gid)
		if retryErr == nil {
			return withMDNS.Host, nil
		}
		// The retry's answer wins whenever it got further, and it usually does.
		// Reporting the first attempt's "no computer called ozan" after the
		// second one reached that computer and was told the password was wrong
		// sends somebody to check their network while the actual problem is on
		// the account screen.
		if retryReason != mountUnresolved {
			return "", retryErr
		}
	}
	return "", err
}

// attemptMount writes the unit for one host and starts it, reporting why it
// failed in a form the caller can act on.
func attemptMount(ctx context.Context, folder RemoteFolder, mountPoint string,
	uid, gid int) (mountFailure, error) {
	body := remoteMountUnit(folder, mountPoint, uid, gid)
	if err := writeRootFile(unitPath(mountPoint), body, 0o644); err != nil {
		return mountUnknown, err
	}
	if err := runSystemctl(ctx, "daemon-reload"); err != nil {
		return mountUnknown, internalError("reloading systemd: " + err.Error())
	}

	unit := mountUnitName(mountPoint)
	if err := runSystemctl(ctx, "start", unit); err != nil {
		// systemctl says "Job failed. See journalctl -xe", which is true and
		// useless to somebody standing at a laptop with a disk in their hand.
		// The reason is one line in the journal and it is the whole answer, so
		// it is fetched and turned into something they can act on.
		said := mountErrorFromJournal(ctx, unit)
		reason := classifyMountFailure(said)
		return reason, reason.asError(folder, said)
	}
	return mountUnknown, nil
}

// mountErrorFromJournal digs out what mount.cifs actually said.
func mountErrorFromJournal(ctx context.Context, unit string) string {
	journalctl, err := exec.LookPath("journalctl")
	if err != nil {
		return ""
	}
	cmd := exec.CommandContext(ctx, journalctl, "-u", unit, "-n", "25",
		"--no-pager", "-o", "cat")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	// The last line that came from mount rather than from systemd. systemd's
	// lines describe the job; mount's line describes the problem.
	said := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "mount error") || strings.Contains(line, "CIFS:") {
			said = line
		}
	}
	return said
}

// mountFailure is why a mount would not happen, in terms somebody can do
// something about.
type mountFailure int

const (
	mountUnknown mountFailure = iota
	mountUnresolved
	mountRefused
	mountNoSuchShare
	mountUnreachable
)

// classifyMountFailure reads what mount.cifs said.
//
// Matching on text, which is ordinarily a thing to avoid. The alternative here
// is showing somebody "Job failed" and letting them guess between a sleeping
// laptop, a wrong password, a wrong share name and a name that does not
// resolve — four different evenings. A phrase that changes in a future release
// costs the specific message and falls back to the general one, which is
// exactly what they had before.
func classifyMountFailure(said string) mountFailure {
	lower := strings.ToLower(said)
	switch {
	case strings.Contains(lower, "could not resolve address"),
		strings.Contains(lower, "name or service not known"):
		return mountUnresolved
	case strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "logon_failure"),
		strings.Contains(lower, "access_denied"):
		return mountRefused
	case strings.Contains(lower, "bad_network_name"),
		strings.Contains(lower, "no such file or directory"):
		return mountNoSuchShare
	case strings.Contains(lower, "host is down"),
		strings.Contains(lower, "no route to host"),
		strings.Contains(lower, "connection timed out"),
		strings.Contains(lower, "unreachable"):
		return mountUnreachable
	}
	return mountUnknown
}

// asError turns a reason into the sentence somebody reads.
func (f mountFailure) asError(folder RemoteFolder, said string) error {
	problem := &Error{
		Code:        "remote.could_not_connect",
		Message:     "Homebase could not open that folder.",
		Detail:      said,
		Recoverable: true,
		Status:      502,
	}
	switch f {
	case mountUnresolved:
		problem.Message = "Homebase could not find a computer called " + folder.Host +
			" on this network."
		problem.Recovery = "Use that computer's address instead of its name. On it, " +
			"press Windows+R, type cmd, then type ipconfig — the address is the line " +
			"marked IPv4 and looks like 192.168.1.42."
	case mountRefused:
		problem.Message = folder.Host + " refused that name and password."
		problem.Recovery = "Use an account that exists on that computer, not the name " +
			"you sign in with here. If that computer signs in with an email address, " +
			"sharing will not accept it — make a local account on it and use that."
	case mountNoSuchShare:
		problem.Message = folder.Host + " has nothing shared called " + folder.Share + "."
		problem.Recovery = "On that computer, right-click the disk, Properties, " +
			"Sharing — the name to use is the share name, which is not the drive " +
			"letter and is often not the folder's name either."
	case mountUnreachable:
		problem.Message = folder.Host + " did not answer."
		problem.Recovery = "Check that computer is switched on and awake, and on the " +
			"same network as this server."
	default:
		problem.Recovery = "Check that the computer is switched on and awake, that the " +
			"folder is still shared on it, and that the name and password are the " +
			"ones it expects."
	}
	return problem
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
