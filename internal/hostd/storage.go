package hostd

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Block device discovery, read from the kernel rather than from a subprocess.
//
// Same reasoning as the system information in ops_system.go: parsing the output
// of lsblk or blkid means depending on their formatting, their locale and their
// presence, and it means a root process spawning shells — the habit ADR-0006
// exists to prevent.
//
// Three sources, each chosen because it is the authority for what is read from
// it:
//
//   /sys/block/          size, removable, model, partitions
//   /dev/disk/by-uuid/   the filesystem UUID, and by-label the label
//   /proc/self/mountinfo what is mounted where, right now
//
// The UUID deliberately comes from udev's symlinks rather than from the
// superblock. Both would work, but the mount unit Homebase writes refers to
// /dev/disk/by-uuid/<uuid>, so taking the UUID from the same place guarantees
// the two agree. A superblock-derived UUID that is formatted even slightly
// differently — and every filesystem formats its own differently — produces a
// mount unit pointing at a path that does not exist.
//
// The filesystem *type* does come from the superblock, because udev does not
// expose it anywhere as stable, and because "is there a filesystem here at all"
// is a question that has to be answered before offering to erase something.

const (
	sysBlock       = "/sys/block"
	devDiskByUUID  = "/dev/disk/by-uuid"
	devDiskByLabel = "/dev/disk/by-label"
	mountInfoPath  = "/proc/self/mountinfo"
)

// scanner is where discovery reads from.
//
// Fields rather than constants so the tests can point it at a directory tree
// they built, and check the parsing against a machine whose disks they chose.
// The alternative is testing block-device discovery only on whatever hardware
// the test happens to run on, which tests nothing repeatable.
type scanner struct {
	sysBlock  string
	byUUID    string
	byLabel   string
	mountInfo string
	devPrefix string
	// filesystem returns what is on a volume, and whether it could be read at
	// all. The second value is not a detail: see Volume.Unreadable.
	filesystem func(path string) (fstype string, unreadable bool)
}

func systemScanner() scanner {
	return scanner{
		sysBlock:   sysBlock,
		byUUID:     devDiskByUUID,
		byLabel:    devDiskByLabel,
		mountInfo:  mountInfoPath,
		devPrefix:  "/dev/",
		filesystem: detectFilesystem,
	}
}

// Disk is a whole block device.
type Disk struct {
	// Device is the kernel's current name for it — "sda". Shown to nobody and
	// never stored as identity: it is assigned in discovery order, so plugging
	// disks in a different order changes it. Proven in a VM: a disk unplugged as
	// sda came back as sdb, same filesystem, same UUID.
	Device string `json:"device"`
	Path   string `json:"path"`

	Model     string `json:"model,omitempty"`
	Vendor    string `json:"vendor,omitempty"`
	SizeBytes uint64 `json:"size_bytes"`

	// Removable is the kernel's flag, and it does not mean what it sounds like.
	// It reports that the *medium* can be removed from the drive — a card
	// reader, an optical drive, a floppy — not that the drive can be unplugged.
	// A USB hard disk reports false, and so does the USB stick QEMU presents to
	// the VM tests.
	//
	// Reported because it is true information, and never used to answer "can
	// this be unplugged". Transport answers that.
	Removable bool `json:"removable"`

	// Transport is how it is attached: usb, sata, nvme, virtio. This is what
	// tells a user which disk is which — "the USB one" is how people think about
	// their disks — and it is the honest signal for whether a disk is one that
	// comes and goes.
	Transport string `json:"transport,omitempty"`

	// System is true for the disk the running system is on. Homebase will not
	// offer to format it, whatever else is asked.
	System bool `json:"system"`

	Volumes []Volume `json:"volumes"`
}

// Volume is a filesystem: a partition, or a whole disk formatted without one.
type Volume struct {
	Device string `json:"device"`
	Path   string `json:"path"`

	// UUID is the identity. Empty means this volume cannot be assigned to an
	// application — see ADR-0013.
	UUID  string `json:"uuid,omitempty"`
	Label string `json:"label,omitempty"`

	// Filesystem is empty when there is nothing recognisable here.
	Filesystem string `json:"filesystem,omitempty"`

	// Unreadable means Homebase could not read the volume to find out what is on
	// it — a permissions problem, or a disk that failed or vanished mid-scan.
	//
	// Kept separate from an empty Filesystem, which means "read it, found
	// nothing". Conflating the two would let Homebase describe a disk it could
	// not open as blank, and then offer to format it. That is the worst possible
	// direction for this particular mistake to run in.
	Unreadable bool `json:"unreadable"`

	SizeBytes uint64 `json:"size_bytes"`

	// MountPoint is where it is mounted right now, or empty.
	MountPoint string `json:"mount_point,omitempty"`

	// ReadOnly reports how it is mounted, not what the disk supports.
	ReadOnly bool `json:"read_only"`
}

// Pluggable reports whether this disk is one that comes and goes.
//
// Transport rather than the kernel's Removable flag, which is about the medium
// rather than the drive: a USB hard disk is unplugged constantly and reports
// removable=false.
func (d Disk) Pluggable() bool {
	switch d.Transport {
	case "usb", "sd-card":
		return true
	}
	return d.Removable
}

// Assignable reports whether this volume can be given to an application.
func (v Volume) Assignable() bool {
	return v.UUID != "" && v.Filesystem != "" && !v.Unreadable
}

// Blank reports whether this volume is positively known to hold no filesystem.
//
// A volume Homebase could not read is not blank. That is the whole point of the
// distinction: this is the question asked before erasing something.
func (v Volume) Blank() bool {
	return !v.Unreadable && v.Filesystem == ""
}

// ListDisks returns every block device worth showing a person.
func ListDisks() ([]Disk, error) { return systemScanner().disks() }

func (s scanner) disks() ([]Disk, error) {
	entries, err := os.ReadDir(s.sysBlock)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.sysBlock, err)
	}

	uuids := readDeviceLinks(s.byUUID)
	labels := readDeviceLinks(s.byLabel)
	mounts := readMounts(s.mountInfo)
	systemDevice := deviceHoldingRoot(mounts)

	var disks []Disk
	for _, entry := range entries {
		name := entry.Name()
		if !interestingDisk(name) {
			continue
		}

		disk := Disk{
			Device:    name,
			Path:      s.devPrefix + name,
			SizeBytes: readSize(filepath.Join(s.sysBlock, name)),
			Removable: readFlag(filepath.Join(s.sysBlock, name, "removable")),
			Model:     readTrimmed(filepath.Join(s.sysBlock, name, "device", "model")),
			Vendor:    readTrimmed(filepath.Join(s.sysBlock, name, "device", "vendor")),
			Transport: s.transportOf(name),
		}

		for _, volume := range s.volumesOf(name, uuids, labels, mounts) {
			if volume.Device == systemDevice {
				disk.System = true
			}
			// A volume mounted at / or /boot belongs to the running system even
			// if it is not the root filesystem itself.
			if volume.MountPoint == "/" || strings.HasPrefix(volume.MountPoint, "/boot") {
				disk.System = true
			}
			disk.Volumes = append(disk.Volumes, volume)
		}

		disks = append(disks, disk)
	}

	sort.Slice(disks, func(i, j int) bool { return disks[i].Device < disks[j].Device })
	return disks, nil
}

// FindVolume resolves a filesystem UUID to the volume that currently carries it.
//
// This is the only supported way to turn a stored identity into a device, and it
// is why nothing in Homebase persists a device path.
func FindVolume(uuid string) (Volume, bool) {
	if uuid == "" {
		return Volume{}, false
	}
	disks, err := ListDisks()
	if err != nil {
		return Volume{}, false
	}
	for _, disk := range disks {
		for _, volume := range disk.Volumes {
			if volume.UUID == uuid {
				return volume, true
			}
		}
	}
	return Volume{}, false
}

// --- /sys/block --------------------------------------------------------------

// interestingDisk filters out the devices a person has no use for.
func interestingDisk(name string) bool {
	for _, prefix := range []string{
		"loop", // snap packages, mostly
		"ram",
		"zram",
		"dm-", // device-mapper internals; the mapped name is elsewhere
		"md",  // software RAID, which Homebase does not manage
		"sr",  // optical
		"fd",  // floppy, still enumerated on some hardware
	} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

func (s scanner) volumesOf(disk string, uuids, labels map[string]string, mounts map[string]mount) []Volume {
	base := filepath.Join(s.sysBlock, disk)

	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}

	var partitions []string
	for _, entry := range entries {
		name := entry.Name()
		// A partition directory is a child whose name extends the disk's and
		// which has its own `partition` file. The second test matters: `sda` has
		// children like `sda/subsystem` that are not partitions.
		if !strings.HasPrefix(name, disk) || name == disk {
			continue
		}
		if _, err := os.Stat(filepath.Join(base, name, "partition")); err != nil {
			continue
		}
		partitions = append(partitions, name)
	}
	sort.Strings(partitions)

	// No partition table: the whole device is the filesystem. This is what a
	// disk formatted by Homebase looks like, and what many USB sticks arrive as.
	if len(partitions) == 0 {
		return []Volume{s.describeVolume(disk, base, uuids, labels, mounts)}
	}

	volumes := make([]Volume, 0, len(partitions))
	for _, partition := range partitions {
		volumes = append(volumes, s.describeVolume(
			partition, filepath.Join(base, partition), uuids, labels, mounts))
	}
	return volumes
}

func (s scanner) describeVolume(device, sysPath string, uuids, labels map[string]string, mounts map[string]mount) Volume {
	volume := Volume{
		Device:    device,
		Path:      s.devPrefix + device,
		SizeBytes: readSize(sysPath),
		UUID:      uuids[device],
		Label:     labels[device],
	}
	volume.Filesystem, volume.Unreadable = s.filesystem(s.devPrefix + device)
	if m, ok := mounts[device]; ok {
		volume.MountPoint = m.point
		volume.ReadOnly = m.readOnly
	}
	return volume
}

func readSize(sysPath string) uint64 {
	// /sys reports in 512-byte sectors regardless of the device's own sector
	// size. This is one of the few places in Linux where 512 is not a guess.
	raw := readTrimmed(filepath.Join(sysPath, "size"))
	sectors, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return sectors * 512
}

func readFlag(path string) bool {
	return readTrimmed(path) == "1"
}

func readTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// transportOf reports how a device is attached, by walking up the sysfs link.
func (s scanner) transportOf(device string) string {
	target, err := os.Readlink(filepath.Join(s.sysBlock, device))
	if err != nil {
		return ""
	}
	switch {
	case strings.Contains(target, "/usb"):
		return "usb"
	case strings.Contains(target, "/nvme"):
		return "nvme"
	case strings.Contains(target, "/virtio"):
		return "virtio"
	case strings.Contains(target, "/ata"):
		return "sata"
	case strings.Contains(target, "/mmc"):
		return "sd-card"
	}
	return ""
}

// --- /dev/disk/by-* ----------------------------------------------------------

// readDeviceLinks maps a device name to the by-uuid or by-label name pointing at
// it. Absent or unreadable is not an error: a machine with no udev has no
// symlinks, and the honest answer is that nothing has a UUID.
func readDeviceLinks(dir string) map[string]string {
	result := map[string]string{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return result
	}

	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		result[filepath.Base(target)] = entry.Name()
	}
	return result
}

// --- /proc/self/mountinfo ----------------------------------------------------

type mount struct {
	point    string
	readOnly bool
	source   string
}

// readMounts maps a device name to where it is mounted.
//
// mountinfo rather than /proc/mounts: it is the one that reports the mount point
// unambiguously and survives paths containing spaces, which are escaped as \040.
func readMounts(path string) map[string]mount {
	result := map[string]mount{}

	data, err := os.ReadFile(path)
	if err != nil {
		return result
	}

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		// The optional fields are variable in number and terminated by "-", so
		// everything after that separator is found by looking for it.
		separator := -1
		for i, field := range fields {
			if field == "-" {
				separator = i
				break
			}
		}
		if separator < 0 || separator+2 >= len(fields) {
			continue
		}

		point := unescapeMountPath(fields[4])
		source := fields[separator+2]
		options := fields[5]

		if !strings.HasPrefix(source, "/dev/") {
			continue
		}

		result[filepath.Base(source)] = mount{
			point:    point,
			readOnly: hasOption(options, "ro"),
			source:   source,
		}
	}
	return result
}

func hasOption(options, want string) bool {
	for _, option := range strings.Split(options, ",") {
		if option == want {
			return true
		}
	}
	return false
}

// unescapeMountPath undoes the octal escaping mountinfo applies to spaces,
// tabs, newlines and backslashes.
func unescapeMountPath(path string) string {
	if !strings.Contains(path, `\`) {
		return path
	}

	var out strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] == '\\' && i+3 < len(path) {
			if value, err := strconv.ParseUint(path[i+1:i+4], 8, 8); err == nil {
				out.WriteByte(byte(value))
				i += 3
				continue
			}
		}
		out.WriteByte(path[i])
	}
	return out.String()
}

func deviceHoldingRoot(mounts map[string]mount) string {
	for device, m := range mounts {
		if m.point == "/" {
			return device
		}
	}
	return ""
}

// --- Filesystem detection ----------------------------------------------------

// detectFilesystem reads the superblock and reports what is there, and whether
// the volume could be read at all.
//
// Only the magic number is read, and only for the filesystems Homebase can
// mount. Deliberately conservative: an unrecognised filesystem is reported as
// unknown rather than guessed at.
//
// The second return value is why this does not simply return a string. "I read
// it and found nothing" and "I could not read it" are different answers, and the
// question being asked is whether it is safe to erase.
func detectFilesystem(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", true
	}
	defer file.Close()

	// Enough to cover every offset below, in one read. btrfs is the deep one, at
	// 64 KiB into the device.
	header := make([]byte, 0x11000)
	n, err := file.ReadAt(header, 0)
	if n == 0 {
		// Nothing came back at all. A device that cannot be read one byte into
		// is a device we know nothing about.
		return "", true
	}
	if err != nil && n < 0x600 {
		// Too short to hold any superblock this understands. Real: a tiny
		// partition, such as the 16 MB Microsoft reserved one, is genuinely
		// blank rather than unreadable.
		return "", false
	}
	header = header[:n]

	found := func(fstype string) (string, bool) { return fstype, false }

	at := func(offset int, want string) bool {
		if offset+len(want) > len(header) {
			return false
		}
		return string(header[offset:offset+len(want)]) == want
	}

	// ext2/3/4: magic 0xEF53 little-endian at 0x38 into the superblock, which
	// itself starts at 0x400.
	if len(header) > 0x43a && binary.LittleEndian.Uint16(header[0x438:0x43a]) == 0xEF53 {
		return found(extVariant(header))
	}
	if at(0x10040, "_BHRfS_M") {
		return found("btrfs")
	}
	if at(0, "XFSB") {
		return found("xfs")
	}
	if at(3, "NTFS    ") {
		return found("ntfs")
	}
	if at(3, "EXFAT   ") {
		return found("exfat")
	}
	// FAT identifies itself in one of two places depending on whether it is
	// FAT12/16 or FAT32.
	if at(0x36, "FAT12") || at(0x36, "FAT16") || at(0x36, "FAT  ") || at(0x52, "FAT32") {
		return found("vfat")
	}
	if at(0x8001, "CD001") || at(0x8801, "CD001") || at(0x9001, "CD001") {
		return found("iso9660")
	}
	if at(0, "LUKS\xba\xbe") {
		// Reported so a user is told their disk is encrypted rather than told it
		// is blank. Homebase cannot unlock it — see ADR-0013.
		return found("crypto_LUKS")
	}

	// Read successfully; nothing recognisable in it.
	return "", false
}

// extVariant distinguishes ext2, ext3 and ext4 by their feature flags. All three
// mount, but a user shown "ext4" for an ext2 filesystem has been told something
// untrue about their disk.
func extVariant(header []byte) string {
	if len(header) < 0x400+0x68 {
		return "ext4"
	}
	compatible := binary.LittleEndian.Uint32(header[0x400+0x5c:])   // has_journal at bit 2
	incompatible := binary.LittleEndian.Uint32(header[0x400+0x60:]) // extents at bit 6
	readOnlyCompat := binary.LittleEndian.Uint32(header[0x400+0x64:])

	const (
		hasJournal    = 0x0004
		hasExtents    = 0x0040
		has64Bit      = 0x0080
		hasMetadataCk = 0x0400
	)

	if incompatible&(hasExtents|has64Bit) != 0 || readOnlyCompat&hasMetadataCk != 0 {
		return "ext4"
	}
	if compatible&hasJournal != 0 {
		return "ext3"
	}
	return "ext2"
}

// --- Free space ---------------------------------------------------------------

// diskUsage reports the size and free space of the filesystem at a path.
//
// Bavail rather than Bfree: Bfree counts blocks reserved for root, which a user
// cannot use and should not be told about. On a default ext4 that is 5 % of the
// disk, so reporting it would overstate the free space on a 4 TB drive by 200 GB.
func diskUsage(path string) (total, available uint64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	size := uint64(stat.Bsize)
	return stat.Blocks * size, stat.Bavail * size, nil
}
