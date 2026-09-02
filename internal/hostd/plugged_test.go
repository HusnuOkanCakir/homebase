package hostd

import (
	"strings"
	"testing"
)

// The name is what somebody looks for in a listing, and what they look for is
// what is printed on the disk.
func TestAPluggedDiskIsNamedAfterWhatIsPrintedOnIt(t *testing.T) {
	disk := Disk{Model: "Ultra USB 3.0", Vendor: "SanDisk"}

	if got := pluggedName(Volume{Label: "KINGSTON"}, disk, nil); got != "kingston" {
		t.Errorf("a labelled disk is called %q, not its label", got)
	}
	// No label: the model, which is the next thing written on the case.
	if got := pluggedName(Volume{}, disk, nil); got != "ultra-usb-3-0" {
		t.Errorf("an unlabelled disk is called %q", got)
	}
	// Nothing at all is still shown. A disk that cannot be named is not a disk
	// to hide — somebody plugged it in on purpose.
	if got := pluggedName(Volume{}, Disk{}, nil); got != "disk" {
		t.Errorf("a nameless disk is called %q", got)
	}
	// Two disks with the same label is the ordinary case for the cheap ones
	// bought in a pack, and one of them must not vanish.
	taken := []string{"kingston"}
	if got := pluggedName(Volume{Label: "KINGSTON"}, disk, taken); got != "kingston-2" {
		t.Errorf("the second disk of the same name is %q", got)
	}
	// A label is a string from a disk somebody else formatted, so it becomes a
	// directory name only after everything that is not a letter is gone.
	for _, label := range []string{"../../etc", "My Disk!", "a/b", strings.Repeat("x", 80)} {
		got := pluggedName(Volume{Label: label}, disk, nil)
		if strings.ContainsAny(got, "/.! ") || got == "" || len(got) > 24 {
			t.Errorf("a label of %q became %q", label, got)
		}
	}
}

// Read-only is the whole promise. The disk belongs to whoever carried it in and
// is standing in another room.
func TestAPluggedDiskIsMountedReadOnlyAndInert(t *testing.T) {
	disk := PluggedDisk{Name: "kingston", UUID: "1234-ABCD", Filesystem: "ntfs",
		Path: "/srv/homebase/storage/plugged/kingston"}
	unit := pluggedMountUnit(disk, 111, 113)

	for _, option := range []string{"ro,", "nosuid", "nodev", "noexec"} {
		if !strings.Contains(unit, option) {
			t.Errorf("the mount is missing %q", option)
		}
	}
	// By UUID, never by device path: /dev/sdb becomes /dev/sdc when something
	// else is plugged in first, and these disks arrive in whatever order
	// somebody happens to plug them in. ADR-0013.
	if !strings.Contains(unit, "What=/dev/disk/by-uuid/1234-ABCD") {
		t.Error("the disk is named by device path rather than by filesystem")
	}
	// A disk that is not here at boot is the normal state, so it must not be
	// something systemd waits for.
	if strings.Contains(unit, "[Install]") {
		t.Error("this would be waited for at boot, when the disk is in a drawer")
	}
}

// NTFS, exFAT and FAT carry no Unix ownership, so the kernel invents it. Without
// these the whole disk belongs to root and the part of Homebase that serves
// files — which is not root — cannot read a byte of it.
func TestTheWindowsFilesystemsAreGivenAnOwner(t *testing.T) {
	for _, filesystem := range []string{"ntfs", "exfat", "vfat"} {
		options := pluggedMountOptions(filesystem, 111, 113)
		if !strings.Contains(options, "uid=111") || !strings.Contains(options, "gid=113") {
			t.Errorf("%s is mounted as %q, which Homebase cannot read", filesystem, options)
		}
	}
	// The Linux filesystems carry real ownership and would refuse these.
	for _, filesystem := range []string{"ext4", "btrfs"} {
		if strings.Contains(pluggedMountOptions(filesystem, 111, 113), "uid=") {
			t.Errorf("%s was given an invented owner", filesystem)
		}
	}
	// `ntfs` names the ancient read-only driver on some systems. Asking for
	// ntfs3 gets the one that works.
	if pluggedFilesystemType("ntfs") != "ntfs3" {
		t.Error("an NTFS disk would be mounted with whichever driver answers first")
	}
}
