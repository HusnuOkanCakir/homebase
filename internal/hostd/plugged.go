package hostd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A disk somebody plugs into the server.
//
// The whole feature, in the words it was finally asked for: "the only thing my
// father does is turn on the server and plug in a hard disk". Everything else
// this project built for the same problem — sharing a folder from a Windows PC
// and mounting it from here — works, and asks a person at the far end to find a
// share name, a Windows account and a password that Windows will not tell them.
// Two evenings went into that and the honest conclusion was that the flow was
// wrong, not the implementation.
//
// Plugged into the server there is nothing to configure. No Windows account, no
// sharing dialog, no computer name to resolve, no laptop that has to stay awake.
// The disk appears in Files and anybody with an account can read it, from
// anywhere the dashboard reaches.
//
// # Read-only, always
//
// The same rule as a folder on another computer and for a stronger reason: this
// is somebody's disk, carried in by hand, and the person who plugged it in is
// standing in another room. Nothing here may write to it — not a mistake, not a
// stray delete on the Files screen, not a server that has been broken into.
//
// It also sidesteps every awkwardness of writing to NTFS, which is what these
// disks are almost always formatted as.
//
// # Not a storage location
//
// A location is a disk Homebase manages: it formats it, keeps shares and
// application data on it, and backs it up. A disk that arrives in a pocket for
// an hour is none of those things, and a disk Homebase already manages is
// deliberately left out of this — it has its own folders and is already served.
const pluggedDirName = "plugged"

// pluggedScanInterval is how often the server looks for a disk that was not
// there a moment ago.
//
// A poll rather than a udev rule. udev would be quicker to notice and would put
// a second privileged path onto the machine — a rule file that runs something
// as root when hardware appears — for a saving of a few seconds on an action
// that involves walking to a server with a disk in your hand.
const pluggedScanInterval = 5 * time.Second

// PluggedDisk is one filesystem on a disk somebody plugged in.
type PluggedDisk struct {
	// Name is what it is called in Files: its label if it has one, otherwise
	// its model. Not identity — see UUID.
	Name string `json:"name"`

	// UUID identifies the filesystem, and is what survives the disk being
	// unplugged and put back in a different socket. ADR-0013.
	UUID string `json:"uuid"`

	Label      string `json:"label,omitempty"`
	Filesystem string `json:"filesystem,omitempty"`
	SizeBytes  uint64 `json:"size_bytes"`

	// Path is where it is mounted on the server, and Connected whether it is
	// still there.
	Path      string `json:"path"`
	Connected bool   `json:"connected"`
}

var pluggedNamePattern = regexp.MustCompile(`[^a-z0-9-]+`)

// pluggedName turns a disk's label into something that can be a directory and a
// name in a listing.
//
// The label first, because it is what is printed on the disk and what somebody
// will look for — "KINGSTON" is how the person who carried it in thinks of it.
// The model second. A number as the last resort, which is better than refusing
// to show a disk because it has no name.
func pluggedName(volume Volume, disk Disk, taken []string) string {
	candidate := volume.Label
	if candidate == "" {
		candidate = strings.TrimSpace(disk.Model)
	}
	if candidate == "" {
		candidate = strings.TrimSpace(disk.Vendor)
	}

	slug := pluggedNamePattern.ReplaceAllString(strings.ToLower(candidate), "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 24 {
		slug = strings.Trim(slug[:24], "-")
	}
	if slug == "" {
		slug = "disk"
	}

	name := slug
	for i := 2; slices.Contains(taken, name); i++ {
		name = slug + "-" + strconv.Itoa(i)
	}
	return name
}

// pluggedMountOptions are how a visiting disk is mounted.
//
// Read-only, and never able to run anything or grant anything: nosuid and nodev
// because this filesystem was written by a computer Homebase knows nothing
// about, and noexec because nothing on it has any business being executed here.
//
// The uid and gid matter for the Windows filesystems. NTFS, exFAT and FAT carry
// no Unix ownership, so the kernel invents it from these — without them
// everything belongs to root and the part of Homebase that serves files, which
// is not root, cannot read a byte. The Linux filesystems carry real ownership
// and ignore these options; a disk from another Linux machine may therefore
// hold files this server cannot read, which is a true fact about that disk
// rather than something to paper over.
func pluggedMountOptions(filesystem string, uid, gid int) string {
	options := "ro,nosuid,nodev,noexec"
	switch filesystem {
	case "ntfs", "ntfs3", "exfat", "vfat", "msdos":
		options += fmt.Sprintf(",uid=%d,gid=%d,umask=0027", uid, gid)
	}
	return options
}

// pluggedFilesystemType maps what was found on the disk to what the kernel
// should be asked for.
//
// `ntfs` names the ancient read-only in-kernel driver on some systems and the
// modern ntfs3 on others. Asking for ntfs3 explicitly gets the one that works;
// if the module is absent, mount falls back through /sbin/mount.ntfs to
// ntfs-3g, which is installed here too.
func pluggedFilesystemType(filesystem string) string {
	if filesystem == "ntfs" {
		return "ntfs3"
	}
	return filesystem
}

// PluggedServices keeps track of the disks people plug in.
type PluggedServices struct {
	storage *StorageServices
	log     *slog.Logger
}

func NewPluggedServices(storage *StorageServices, log *slog.Logger) *PluggedServices {
	return &PluggedServices{storage: storage, log: log}
}

// candidates are the filesystems on plugged-in disks that Homebase does not
// already manage.
func (s *PluggedServices) candidates() []PluggedDisk {
	disks, err := ListDisks()
	if err != nil {
		return nil
	}

	// The disks Homebase manages are left alone: they already have folders,
	// they are already served, and mounting one again read-only in a second
	// place would be two answers to the same question.
	managed := map[string]bool{}
	if locations, err := s.storage.load(); err == nil {
		for _, location := range locations {
			managed[location.UUID] = true
		}
	}

	var found []PluggedDisk
	var taken []string
	for _, disk := range disks {
		if disk.System || !disk.Pluggable() {
			continue
		}
		for _, volume := range disk.Volumes {
			if volume.UUID == "" || volume.Filesystem == "" || volume.Unreadable {
				continue
			}
			if managed[volume.UUID] {
				continue
			}
			name := pluggedName(volume, disk, taken)
			taken = append(taken, name)
			found = append(found, PluggedDisk{
				Name:       name,
				UUID:       volume.UUID,
				Label:      volume.Label,
				Filesystem: volume.Filesystem,
				SizeBytes:  volume.SizeBytes,
				Path:       filepath.Join(s.storage.root, pluggedDirName, name),
			})
		}
	}
	return found
}

// Status is what is plugged in and whether it is readable.
func (s *PluggedServices) Status() []PluggedDisk {
	disks := s.candidates()
	for i := range disks {
		disks[i].Connected = isMountPoint(disks[i].Path, mountInfoPath)
	}
	return disks
}

// Watch mounts disks as they appear and clears up after ones that leave.
//
// Started once, runs for the life of hostd. This is the part that makes the
// whole feature "plug it in and that is all": nobody presses anything, because
// the person plugging it in is holding a disk and has been told to plug it in.
func (s *PluggedServices) Watch(ctx context.Context) {
	s.scan(ctx)
	ticker := time.NewTicker(pluggedScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scan(ctx)
		}
	}
}

func (s *PluggedServices) scan(ctx context.Context) {
	wanted := s.candidates()

	for _, disk := range wanted {
		if isMountPoint(disk.Path, mountInfoPath) {
			continue
		}
		if err := s.mount(ctx, disk); err != nil {
			// Logged once per disk rather than every five seconds: a disk that
			// cannot be mounted usually cannot be mounted again either, and a
			// journal filling at twelve lines a minute is a journal nobody
			// reads.
			if !s.alreadyFailed(disk.UUID) {
				s.log.Warn("could not open a disk that was plugged in",
					"disk", disk.Name, "filesystem", disk.Filesystem, "error", err)
				s.noteFailure(disk.UUID)
			}
			continue
		}
		s.log.Info("a disk was plugged in and is now readable",
			"disk", disk.Name, "filesystem", disk.Filesystem, "at", disk.Path)
		s.clearFailure(disk.UUID)
	}

	s.forgetVanished(ctx, wanted)
}

// forgetVanished unmounts and tidies away anything under the plugged directory
// that no longer has a disk behind it.
//
// Somebody pulling a disk out without saying so is the ordinary case, not an
// error: they came to fetch a file and they have gone. What is left behind is a
// mount pointing at nothing, which reads as an empty folder — the same failure
// mode as a sleeping laptop, and just as misleading.
func (s *PluggedServices) forgetVanished(ctx context.Context, wanted []PluggedDisk) {
	root := filepath.Join(s.storage.root, pluggedDirName)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}

	keep := map[string]bool{}
	for _, disk := range wanted {
		keep[disk.Name] = true
	}
	for _, entry := range entries {
		if keep[entry.Name()] {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if isMountPoint(path, mountInfoPath) {
			_ = runSystemctl(ctx, "stop", mountUnitName(path))
		}
		_ = os.Remove(unitPath(path))
		_ = os.Remove(path)
	}
	_ = runSystemctl(ctx, "daemon-reload")
}

// mount makes one disk readable.
func (s *PluggedServices) mount(ctx context.Context, disk PluggedDisk) error {
	if err := underStorageRoot(s.storage.root, disk.Path); err != nil {
		return err
	}
	if err := os.MkdirAll(disk.Path, 0o755); err != nil {
		return err
	}

	account, err := user.Lookup(serviceAccount)
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(account.Uid)
	gid, _ := strconv.Atoi(account.Gid)

	body := pluggedMountUnit(disk, uid, gid)
	if err := writeRootFile(unitPath(disk.Path), body, 0o644); err != nil {
		return err
	}
	if err := runSystemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return runSystemctl(ctx, "start", mountUnitName(disk.Path))
}

// Eject unmounts a disk so it can be pulled out safely.
func (s *PluggedServices) Eject(ctx context.Context, name string) error {
	for _, disk := range s.Status() {
		if disk.Name != name {
			continue
		}
		if err := runSystemctl(ctx, "stop", mountUnitName(disk.Path)); err != nil {
			return &Error{
				Code:        "plugged.could_not_eject",
				Message:     "Homebase could not finish with that disk.",
				Detail:      err.Error(),
				Recoverable: true,
				Recovery: "Somebody may be reading a file from it. Wait a moment " +
					"and try again.",
				Status: 409,
			}
		}
		_ = os.Remove(unitPath(disk.Path))
		_ = runSystemctl(ctx, "daemon-reload")
		_ = os.Remove(disk.Path)
		return nil
	}
	return &Error{
		Code:        "plugged.no_such_disk",
		Message:     "There is no disk here by that name.",
		Detail:      name,
		Recoverable: true,
		Recovery:    "Check the Files page for what is plugged in.",
		Status:      404,
	}
}

// pluggedMountUnit renders the unit for one plugged-in disk.
//
// By filesystem UUID rather than by device path, for the reason ADR-0013 gives:
// /dev/sdb becomes /dev/sdc when something else is plugged in first, and a
// mount pointed at a device path would then be pointed at a different disk.
// That matters more here than anywhere, because these disks arrive in whatever
// order somebody happens to plug them in.
//
// No [Install] section: a disk that is not here at boot is the normal state.
func pluggedMountUnit(disk PluggedDisk, uid, gid int) string {
	return fmt.Sprintf(`# Written by Homebase. Do not edit.
#
# A disk somebody plugged into this server. Written when it appears and removed
# when it is taken out again; edits will be lost.
#
# Read-only on purpose: this disk belongs to whoever carried it in, and nothing
# on this server has any business writing to it.

[Unit]
Description=Homebase — %s, plugged in
DefaultDependencies=no
Conflicts=umount.target
Before=umount.target

[Mount]
What=/dev/disk/by-uuid/%s
Where=%s
Type=%s
Options=%s
TimeoutSec=20
`, unitSafe(disk.Name), disk.UUID, disk.Path,
		pluggedFilesystemType(disk.Filesystem),
		pluggedMountOptions(disk.Filesystem, uid, gid))
}

// isMountPoint reports whether something is mounted exactly there, whatever it
// is mounted from.
//
// Not readMounts, which is the disk-shaped reader: it keeps only entries whose
// source begins with /dev/, and that is most of them here but not a thing to
// rely on.
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

// --- Not shouting about the same failure every five seconds ----------------------

var pluggedFailures = map[string]bool{}

func (s *PluggedServices) alreadyFailed(uuid string) bool { return pluggedFailures[uuid] }
func (s *PluggedServices) noteFailure(uuid string)        { pluggedFailures[uuid] = true }
func (s *PluggedServices) clearFailure(uuid string)       { delete(pluggedFailures, uuid) }
