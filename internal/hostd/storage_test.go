package hostd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A sysfs tree built by hand, so the assertions are about the parsing rather
// than about whatever disks the machine running the tests happens to have.
type fakeMachine struct {
	root    string
	scanner scanner
}

func newFakeMachine(t *testing.T) *fakeMachine {
	t.Helper()

	root := t.TempDir()
	for _, dir := range []string{"sys/block", "dev/disk/by-uuid", "dev/disk/by-label", "proc"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	machine := &fakeMachine{root: root}
	machine.scanner = scanner{
		sysBlock:  filepath.Join(root, "sys/block"),
		byUUID:    filepath.Join(root, "dev/disk/by-uuid"),
		byLabel:   filepath.Join(root, "dev/disk/by-label"),
		mountInfo: filepath.Join(root, "proc/mountinfo"),
		devPrefix: "/dev/",
		// Filesystem detection reads a real device; the tests that care about it
		// exercise detectFilesystem directly against crafted superblocks.
		filesystem: func(string) (string, bool) { return "ext4", false },
	}
	machine.writeMountInfo()
	return machine
}

// addDisk creates a whole block device. sizeSectors is in 512-byte units, as
// /sys reports.
func (m *fakeMachine) addDisk(t *testing.T, name string, sizeSectors int, removable bool, model, transport string) {
	t.Helper()

	dir := filepath.Join(m.scanner.sysBlock, name)
	if err := os.MkdirAll(filepath.Join(dir, "device"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "size"), itoa(sizeSectors))
	write(t, filepath.Join(dir, "removable"), map[bool]string{true: "1", false: "0"}[removable])
	write(t, filepath.Join(dir, "device", "model"), model)

	// The transport is read from where the sysfs symlink points. A directory
	// cannot also be a symlink, so the link lives beside the tree and the
	// scanner is pointed at it — same shape as the real /sys/block, which is
	// entirely symlinks.
	if transport != "" {
		target := filepath.Join(m.root, "devices", transport+"-bus", name)
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func (m *fakeMachine) addPartition(t *testing.T, disk, name string, sizeSectors int) {
	t.Helper()

	dir := filepath.Join(m.scanner.sysBlock, disk, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "size"), itoa(sizeSectors))
	write(t, filepath.Join(dir, "partition"), "1")
}

// addNoise creates a child directory that is not a partition, which /sys/block
// is full of.
func (m *fakeMachine) addNoise(t *testing.T, disk, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(m.scanner.sysBlock, disk, name), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (m *fakeMachine) setUUID(t *testing.T, device, uuid string) {
	t.Helper()
	link := filepath.Join(m.scanner.byUUID, uuid)
	if err := os.Symlink("../../"+device, link); err != nil {
		t.Fatal(err)
	}
}

func (m *fakeMachine) setLabel(t *testing.T, device, label string) {
	t.Helper()
	link := filepath.Join(m.scanner.byLabel, label)
	if err := os.Symlink("../../"+device, link); err != nil {
		t.Fatal(err)
	}
}

func (m *fakeMachine) writeMountInfo(lines ...string) {
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	os.WriteFile(m.scanner.mountInfo, []byte(body), 0o644)
}

// mountLine builds a /proc/self/mountinfo record.
func mountLine(point, device, fstype, options string) string {
	return "36 25 8:1 / " + point + " " + options +
		" shared:1 - " + fstype + " " + device + " rw"
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// --- Discovery ---------------------------------------------------------------

func TestFindsDisksAndTheirPartitions(t *testing.T) {
	m := newFakeMachine(t)
	m.addDisk(t, "sda", 4_000_000, false, "Samsung SSD 860", "")
	m.addPartition(t, "sda", "sda1", 3_900_000)
	m.addNoise(t, "sda", "subsystem")
	m.addNoise(t, "sda", "queue")
	m.setUUID(t, "sda1", "aaaa-1111")
	m.setLabel(t, "sda1", "system")

	disks, err := m.scanner.disks()
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 1 {
		t.Fatalf("got %d disks", len(disks))
	}

	disk := disks[0]
	if disk.Device != "sda" {
		t.Errorf("device = %q", disk.Device)
	}
	if disk.SizeBytes != 4_000_000*512 {
		t.Errorf("size = %d", disk.SizeBytes)
	}
	if disk.Model != "Samsung SSD 860" {
		t.Errorf("model = %q", disk.Model)
	}

	// The noise directories are not partitions. /sys/block/sda has a dozen
	// children and only some of them are.
	if len(disk.Volumes) != 1 {
		t.Fatalf("got %d volumes: %+v", len(disk.Volumes), disk.Volumes)
	}
	if disk.Volumes[0].UUID != "aaaa-1111" {
		t.Errorf("uuid = %q", disk.Volumes[0].UUID)
	}
	if disk.Volumes[0].Label != "system" {
		t.Errorf("label = %q", disk.Volumes[0].Label)
	}
}

// A USB stick formatted without a partition table is a whole-device filesystem,
// and it is what a disk Homebase formatted looks like.
func TestAWholeDiskFilesystemIsAVolume(t *testing.T) {
	m := newFakeMachine(t)
	m.addDisk(t, "sdb", 4_000_000, true, "SanDisk Ultra", "")
	m.setUUID(t, "sdb", "bbbb-2222")

	disks, err := m.scanner.disks()
	if err != nil {
		t.Fatal(err)
	}
	if len(disks[0].Volumes) != 1 {
		t.Fatalf("got %d volumes", len(disks[0].Volumes))
	}
	if got := disks[0].Volumes[0].Device; got != "sdb" {
		t.Errorf("device = %q, want the disk itself", got)
	}
	if !disks[0].Removable {
		t.Error("a removable disk was not reported as removable")
	}
}

// Loop devices are snap packages, and a user has no business being shown thirty
// of them.
func TestUninterestingDevicesAreSkipped(t *testing.T) {
	m := newFakeMachine(t)
	for _, name := range []string{"loop0", "loop1", "ram0", "zram0", "sr0", "dm-0", "md0"} {
		m.addDisk(t, name, 1000, false, "", "")
	}
	m.addDisk(t, "sda", 4_000_000, false, "real disk", "")

	disks, err := m.scanner.disks()
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 1 || disks[0].Device != "sda" {
		names := []string{}
		for _, d := range disks {
			names = append(names, d.Device)
		}
		t.Errorf("got %v, want only sda", names)
	}
}

// The disk holding the running system must be marked, because it is the one
// Homebase will refuse to format however it is asked.
func TestTheSystemDiskIsMarked(t *testing.T) {
	m := newFakeMachine(t)
	m.addDisk(t, "sda", 4_000_000, false, "system", "")
	m.addPartition(t, "sda", "sda1", 3_900_000)
	m.addDisk(t, "sdb", 2_000_000, true, "usb stick", "")
	m.writeMountInfo(mountLine("/", "/dev/sda1", "ext4", "rw,relatime"))

	disks, err := m.scanner.disks()
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]Disk{}
	for _, disk := range disks {
		byName[disk.Device] = disk
	}
	if !byName["sda"].System {
		t.Error("the disk holding / was not marked as the system disk")
	}
	if byName["sdb"].System {
		t.Error("a removable disk was marked as the system disk")
	}
}

// A separate /boot partition is still the system's, even though / is elsewhere.
func TestABootPartitionMarksItsDiskAsSystem(t *testing.T) {
	m := newFakeMachine(t)
	m.addDisk(t, "sda", 4_000_000, false, "", "")
	m.addPartition(t, "sda", "sda1", 1_000_000)
	m.writeMountInfo(mountLine("/boot/efi", "/dev/sda1", "vfat", "rw,relatime"))

	disks, _ := m.scanner.disks()
	if !disks[0].System {
		t.Error("a disk holding /boot/efi was not marked as the system disk")
	}
}

func TestMountPointsAreReported(t *testing.T) {
	m := newFakeMachine(t)
	m.addDisk(t, "sdb", 4_000_000, true, "", "")
	m.writeMountInfo(
		mountLine("/srv/homebase/storage/media", "/dev/sdb", "ext4", "rw,nosuid,nodev"))

	disks, _ := m.scanner.disks()
	volume := disks[0].Volumes[0]

	if volume.MountPoint != "/srv/homebase/storage/media" {
		t.Errorf("mount point = %q", volume.MountPoint)
	}
	if volume.ReadOnly {
		t.Error("a read-write mount was reported read-only")
	}
}

func TestAReadOnlyMountIsReportedAsOne(t *testing.T) {
	m := newFakeMachine(t)
	m.addDisk(t, "sdb", 4_000_000, true, "", "")
	m.writeMountInfo(mountLine("/mnt/backup", "/dev/sdb", "ext4", "ro,nosuid,nodev"))

	disks, _ := m.scanner.disks()
	if !disks[0].Volumes[0].ReadOnly {
		t.Error("a read-only mount was not reported as one")
	}
}

// mountinfo escapes spaces as \040. A path read without unescaping does not
// match the path anything else refers to.
func TestMountPathsWithSpacesAreDecoded(t *testing.T) {
	m := newFakeMachine(t)
	m.addDisk(t, "sdb", 4_000_000, true, "", "")
	m.writeMountInfo(mountLine(`/media/My\040Photos`, "/dev/sdb", "ext4", "rw"))

	disks, _ := m.scanner.disks()
	if got := disks[0].Volumes[0].MountPoint; got != "/media/My Photos" {
		t.Errorf("mount point = %q, want %q", got, "/media/My Photos")
	}
}

// --- Assignability -----------------------------------------------------------

// ADR-0013: a volume with no UUID cannot be assigned to an application, because
// there is no way to find it again reliably.
func TestAVolumeWithoutAUUIDIsNotAssignable(t *testing.T) {
	cases := []struct {
		name   string
		volume Volume
		want   bool
	}{
		{"formatted with a UUID", Volume{UUID: "abc", Filesystem: "ext4"}, true},
		{"no UUID", Volume{Filesystem: "ext4"}, false},
		{"no filesystem", Volume{UUID: "abc"}, false},
		{"neither", Volume{}, false},
	}

	for _, tc := range cases {
		if got := tc.volume.Assignable(); got != tc.want {
			t.Errorf("%s: Assignable() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// --- Filesystem detection ----------------------------------------------------

// Crafted superblocks rather than real filesystems: what is being tested is that
// the right bytes are read from the right offsets, and mkfs is not available in
// a unit test anyway.
func TestDetectsFilesystemsByTheirSuperblock(t *testing.T) {
	cases := []struct {
		name   string
		build  func([]byte)
		expect string
	}{
		{
			name: "ext4",
			build: func(b []byte) {
				b[0x438], b[0x439] = 0x53, 0xEF
				// The extents feature, which is what makes it ext4 rather than ext3.
				b[0x400+0x60] = 0x40
			},
			expect: "ext4",
		},
		{
			name: "ext2",
			build: func(b []byte) {
				b[0x438], b[0x439] = 0x53, 0xEF
			},
			expect: "ext2",
		},
		{
			name: "ext3",
			build: func(b []byte) {
				b[0x438], b[0x439] = 0x53, 0xEF
				b[0x400+0x5c] = 0x04 // has_journal
			},
			expect: "ext3",
		},
		{
			name:   "btrfs",
			build:  func(b []byte) { copy(b[0x10040:], "_BHRfS_M") },
			expect: "btrfs",
		},
		{
			name:   "xfs",
			build:  func(b []byte) { copy(b[0:], "XFSB") },
			expect: "xfs",
		},
		{
			name:   "ntfs",
			build:  func(b []byte) { copy(b[3:], "NTFS    ") },
			expect: "ntfs",
		},
		{
			name:   "exfat",
			build:  func(b []byte) { copy(b[3:], "EXFAT   ") },
			expect: "exfat",
		},
		{
			name:   "fat32",
			build:  func(b []byte) { copy(b[0x52:], "FAT32") },
			expect: "vfat",
		},
		{
			name:   "luks",
			build:  func(b []byte) { copy(b[0:], "LUKS\xba\xbe") },
			expect: "crypto_LUKS",
		},
		{
			// A blank disk. This is the answer that decides whether Homebase is
			// about to erase nothing or somebody's photographs.
			name:   "nothing recognisable",
			build:  func(b []byte) {},
			expect: "",
		},
		{
			name: "a filesystem we do not know",
			build: func(b []byte) {
				copy(b[0:], "SOMETHINGELSE")
			},
			expect: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			image := make([]byte, 0x11000)
			tc.build(image)

			path := filepath.Join(t.TempDir(), "image")
			if err := os.WriteFile(path, image, 0o600); err != nil {
				t.Fatal(err)
			}

			got, unreadable := detectFilesystem(path)
			if got != tc.expect {
				t.Errorf("detectFilesystem = %q, want %q", got, tc.expect)
			}
			if unreadable {
				t.Error("a readable image was reported as unreadable")
			}
		})
	}
}

// A device that cannot be read must say so, rather than reporting itself blank.
//
// This is the distinction that decides whether Homebase offers to erase
// something. A disk that vanished mid-scan, or one hostd cannot open, must never
// be described to a user as empty.
func TestAnUnreadableDeviceIsNotReportedAsBlank(t *testing.T) {
	fstype, unreadable := detectFilesystem(filepath.Join(t.TempDir(), "gone"))
	if fstype != "" {
		t.Errorf("fstype = %q", fstype)
	}
	if !unreadable {
		t.Fatal("an absent device was reported as readable, and therefore as blank")
	}

	volume := Volume{UUID: "abc", Unreadable: true}
	if volume.Blank() {
		t.Error("an unreadable volume reported itself blank")
	}
	if volume.Assignable() {
		t.Error("an unreadable volume reported itself assignable")
	}

	// And the positive case, so the test cannot pass by making everything false.
	blank := Volume{UUID: "abc"}
	if !blank.Blank() {
		t.Error("a volume read successfully with no filesystem is blank")
	}
}

// --- The machine this actually runs on ---------------------------------------

// A smoke test against real hardware. It cannot assert much — the disks differ
// per machine — but it does prove the parsing survives a real /sys and a real
// mountinfo, which the fixtures cannot.
func TestScanningThisMachineFindsItsSystemDisk(t *testing.T) {
	disks, err := ListDisks()
	if err != nil {
		t.Fatalf("listing disks: %v", err)
	}
	if len(disks) == 0 {
		t.Fatal("no disks at all, on a machine that is running from one")
	}

	var system int
	for _, disk := range disks {
		if disk.System {
			system++
		}
		if disk.SizeBytes == 0 {
			t.Errorf("%s reported a size of zero", disk.Device)
		}
	}
	if system == 0 {
		t.Error("no disk was identified as holding the running system")
	}
}
