package hostd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func storageServices(t *testing.T) *StorageServices {
	t.Helper()
	return NewStorageServices(t.TempDir()+"/storage", t.TempDir()+"/state")
}

// --- The state file -----------------------------------------------------------

func TestLocationsSurviveARestart(t *testing.T) {
	root := t.TempDir() + "/storage"
	state := t.TempDir() + "/state"

	before := NewStorageServices(root, state)
	if err := before.save([]Location{
		{ID: "media", Name: "Films", UUID: "abcd-1234", Filesystem: "ext4"},
	}); err != nil {
		t.Fatal(err)
	}

	after := NewStorageServices(root, state)
	locations, err := after.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(locations) != 1 || locations[0].UUID != "abcd-1234" {
		t.Fatalf("got %+v", locations)
	}
}

// A machine with no storage set up is an ordinary state, not an error.
func TestNoLocationsYetIsNotAnError(t *testing.T) {
	s := storageServices(t)
	locations, err := s.load()
	if err != nil {
		t.Fatalf("a fresh machine reported an error: %v", err)
	}
	if len(locations) != 0 {
		t.Errorf("got %d locations", len(locations))
	}
}

// A corrupt state file must be refused, not silently reset.
//
// This file records which disk an application's data is on. Starting again from
// empty would unmount somebody's storage and present it as Homebase simply
// having forgotten — with no error anywhere to explain it.
func TestACorruptStateFileIsRefusedRatherThanReset(t *testing.T) {
	s := storageServices(t)
	if err := os.MkdirAll(filepath.Dir(s.stateFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.stateFile, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.load(); err == nil {
		t.Fatal("a corrupt storage configuration was accepted as an empty one")
	}
}

func TestTheStateFileIsNotReadableByOtherAccounts(t *testing.T) {
	s := storageServices(t)
	if err := s.save([]Location{{ID: "media", UUID: "abcd"}}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(s.stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the state file is %o, want 600", mode)
	}
}

// --- The mount point ----------------------------------------------------------

// The mountpoint is inert while nothing is mounted on it.
//
// When a disk is unplugged the mountpoint reverts to an ordinary directory on
// the root filesystem, and an application still writing to it fills the system
// disk with files that vanish behind the disk when it is reconnected. That is
// the quietest way to lose data in this whole milestone.
func TestAnEmptyMountPointCannotBeWrittenTo(t *testing.T) {
	s := storageServices(t)
	mountPoint := s.mountPointFor("media")

	if err := s.prepareMountPoint(mountPoint); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(mountPoint)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o555 {
		t.Fatalf("the mount point is %o, want 555", mode)
	}

	// As root every mode is writable, so the assertion has to be about the mode
	// rather than about attempting a write. The VM test does the real one.
	if os.Geteuid() != 0 {
		if err := os.WriteFile(filepath.Join(mountPoint, "stray"), []byte("x"), 0o644); err == nil {
			t.Error("a file was written into an empty mount point")
		}
	}
}

// Preparing runs again on every mount. A directory that was writable once must
// not stay writable.
func TestPreparingAnExistingMountPointTightensIt(t *testing.T) {
	s := storageServices(t)
	mountPoint := s.mountPointFor("media")

	if err := os.MkdirAll(mountPoint, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := s.prepareMountPoint(mountPoint); err != nil {
		t.Fatal(err)
	}

	info, _ := os.Stat(mountPoint)
	if mode := info.Mode().Perm(); mode != 0o555 {
		t.Errorf("a world-writable mount point was left at %o", mode)
	}
}

func TestMountPointsAreRefusedOutsideTheStorageRoot(t *testing.T) {
	s := storageServices(t)

	for _, path := range []string{"/etc/homebase", "/", s.root, s.root + "/../escape"} {
		if err := s.prepareMountPoint(path); err == nil {
			t.Errorf("%s was accepted as a mount point", path)
		}
	}
}

// --- Location ids -------------------------------------------------------------

// The id becomes a directory name and part of a systemd unit name.
func TestLocationIDsAreRestrictive(t *testing.T) {
	valid := []string{"media", "films", "backup-disk", "usb1", "a1"}
	invalid := []string{
		"",
		"a",        // too short to be meaningful
		"Media",    // uppercase: the unit name would differ from the id
		"my media", // a space
		"../escape",
		"media/",
		"-leading",
		"trailing-",
		"media.disk", // a dot, which systemd escapes
		strings.Repeat("a", 40),
	}

	for _, id := range valid {
		if !validLocationID.MatchString(id) {
			t.Errorf("%q was rejected", id)
		}
	}
	for _, id := range invalid {
		if validLocationID.MatchString(id) {
			t.Errorf("%q was accepted", id)
		}
	}
}

// --- Filesystem labels --------------------------------------------------------

// The label is the one caller-supplied value that reaches mkfs.
func TestFilesystemLabelsAreRestrictive(t *testing.T) {
	valid := []string{"Homebase", "Media", "My Films", "backup-2026", "a_b"}
	invalid := []string{
		"",
		strings.Repeat("a", 17), // ext4 stops at 16
		"films;rm -rf /",
		"$(whoami)",
		"`id`",
		"a\nb",
		"../etc",
		"films\x00",
	}

	for _, label := range valid {
		if !validFilesystemLabel(label) {
			t.Errorf("%q was rejected", label)
		}
	}
	for _, label := range invalid {
		if validFilesystemLabel(label) {
			t.Errorf("%q was accepted", label)
		}
	}
}

// --- What formatting refuses --------------------------------------------------

// The system disk is never formattable, whatever was asked and however it was
// confirmed. There is no flag for this.
func TestFormattingRefusesTheSystemDisk(t *testing.T) {
	// Against this machine's real disks: whichever one holds the running system
	// must be refused. Nothing is written — resolveFormatTarget only decides.
	s := storageServices(t)

	disks, err := ListDisks()
	if err != nil {
		t.Fatal(err)
	}

	var checked int
	for _, disk := range disks {
		if !disk.System {
			continue
		}
		for _, volume := range disk.Volumes {
			if volume.UUID == "" {
				continue
			}
			checked++
			_, err := s.resolveFormatTarget(FormatParams{
				UUID: volume.UUID, Confirm: volume.UUID,
			})
			if err == nil {
				t.Fatalf("formatting %s was allowed, and it holds the running system",
					volume.Path)
			}
			var hostErr *Error
			if !asHostError(err, &hostErr) || hostErr.Code != "storage.refused_system_disk" {
				t.Errorf("%s was refused for the wrong reason: %v", volume.Path, err)
			}
		}
	}

	if checked == 0 {
		t.Skip("no system volume with a UUID on this machine")
	}
}

func TestFormattingRefusesADiskItCannotFind(t *testing.T) {
	s := storageServices(t)

	_, err := s.resolveFormatTarget(FormatParams{
		UUID: "00000000-0000-0000-0000-000000000000", Confirm: "x",
	})
	if err == nil {
		t.Fatal("a disk that does not exist was accepted")
	}
	var hostErr *Error
	if !asHostError(err, &hostErr) || hostErr.Code != "storage.disk_not_found" {
		t.Errorf("got %v", err)
	}
}

// --- What adding a location refuses -------------------------------------------

// ADR-0013: a volume with no UUID cannot be assigned, because there is no way to
// find it again reliably.
func TestVolumesThatCannotBeIdentifiedAreRefusedWithAReason(t *testing.T) {
	cases := []struct {
		name   string
		volume Volume
		want   string
	}{
		{
			name:   "unreadable",
			volume: Volume{Path: "/dev/sdb", Unreadable: true},
			want:   "storage.unreadable",
		},
		{
			name:   "no filesystem",
			volume: Volume{Path: "/dev/sdb"},
			want:   "storage.not_formatted",
		},
		{
			name:   "a filesystem with no UUID",
			volume: Volume{Path: "/dev/sdb", Filesystem: "vfat"},
			want:   "storage.no_identity",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := unassignableVolume(tc.volume)
			var hostErr *Error
			if !asHostError(err, &hostErr) {
				t.Fatalf("got %T", err)
			}
			if hostErr.Code != tc.want {
				t.Errorf("code = %q, want %q", hostErr.Code, tc.want)
			}
			// Each of these is something a user can act on, so each has to say
			// what to do about it.
			if hostErr.Recovery == "" {
				t.Error("no recovery advice, on an error a user is expected to fix")
			}
		})
	}
}

// --- Resolving for an application ---------------------------------------------

// An application must not be given a path when its disk is not mounted. That is
// what makes it refuse to start rather than write to an empty directory on the
// system disk.
func TestResolvingAnUnmountedLocationFails(t *testing.T) {
	s := storageServices(t)

	if err := s.save([]Location{
		{ID: "media", Name: "Films", UUID: "no-such-uuid-0000", Filesystem: "ext4"},
	}); err != nil {
		t.Fatal(err)
	}

	if path, ok := s.ResolveLocation("media"); ok {
		t.Errorf("resolved to %q while the disk is not connected", path)
	}
	if _, ok := s.ResolveLocation("not-a-location"); ok {
		t.Error("an unknown location resolved to something")
	}
}

func TestAnUnknownLocationSaysSo(t *testing.T) {
	err := unknownLocation("media")
	var hostErr *Error
	if !asHostError(err, &hostErr) || hostErr.Status != 404 {
		t.Fatalf("got %v", err)
	}
}

// A disconnected disk is a recoverable situation with an obvious remedy, and the
// message has to name the disk the user is looking for.
func TestADisconnectedDiskIsExplained(t *testing.T) {
	err := diskNotConnected(Location{
		ID: "media", Name: "Films", UUID: "abcd-1234", Label: "MyDrive",
	})

	var hostErr *Error
	if !asHostError(err, &hostErr) {
		t.Fatalf("got %T", err)
	}
	if !hostErr.Recoverable {
		t.Error("an unplugged disk was reported as unrecoverable")
	}
	if !strings.Contains(hostErr.Message, "Films") {
		t.Errorf("the message does not name the location: %q", hostErr.Message)
	}
	if !strings.Contains(hostErr.Detail, "MyDrive") {
		t.Errorf("the detail does not name the disk's label: %q", hostErr.Detail)
	}
}

// --- Free space ----------------------------------------------------------------

// Reserved blocks are not free space. On a default ext4 that is 5 % of the disk,
// which on a 4 TB drive is 200 GB a user cannot actually use.
func TestFreeSpaceExcludesTheRootReserve(t *testing.T) {
	total, available, err := diskUsage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 {
		t.Fatal("total space reported as zero")
	}
	if available > total {
		t.Errorf("available (%d) exceeds total (%d)", available, total)
	}
}

// The immutable flag is what makes an empty mount point genuinely unwritable.
//
// A mode of 0555 does not stop root, and an application container frequently
// runs as root — so the mode alone protects against every writer except the most
// likely one. This was found by the VM test writing as root into what was
// supposed to be an unwritable directory, and succeeding.
func TestAnEmptyMountPointIsImmutableNotJustUnwritable(t *testing.T) {
	s := storageServices(t)
	mountPoint := s.mountPointFor("media")

	if err := s.prepareMountPoint(mountPoint); err != nil {
		t.Fatal(err)
	}

	// Setting the flag needs CAP_LINUX_IMMUTABLE. hostd is root and has it; a
	// test process usually is not, and prepareMountPoint deliberately treats
	// that as a warning rather than a failure — a development instance should
	// still work. So the assertion only holds where the capability exists, and
	// the VM test is where this is really checked.
	if os.Geteuid() != 0 {
		if err := setImmutable(mountPoint, true); err == nil {
			t.Error("the flag was set without privilege, which should not be possible")
		}
		t.Skip("not root: the immutable flag cannot be set, so this is checked in the VM test")
	}

	immutable, err := isImmutable(mountPoint)
	if err != nil {
		// tmpfs does not support these flags. That is a fact about where the
		// test happens to run rather than about the code.
		t.Skipf("this filesystem does not support the immutable flag: %v", err)
	}
	if !immutable {
		t.Error("the mount point is not immutable, so root can write into it while " +
			"the disk is absent")
	}

	// And it can be undone, or a location could never be removed.
	if err := setImmutable(mountPoint, false); err != nil {
		t.Fatalf("clearing the flag: %v", err)
	}
	if immutable, _ := isImmutable(mountPoint); immutable {
		t.Error("the flag could not be cleared")
	}
}
