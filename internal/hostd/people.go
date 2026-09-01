package hostd

import (
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A folder that belongs to one person.
//
// Shared folders are the house's: everybody with an account can open every one
// of them, which is what a family server is mostly for. This is the other half —
// somewhere to put something that is yours, reachable from your own laptop and
// from a phone away from home, without it being reachable by everybody at the
// dinner table.
//
//	<location>/shares/<name>      the house's, unchanged
//	<location>/people/<username>  one per person
//
// Owned by the service account and readable by nobody else, which is a
// deliberately narrower thing than it sounds. It keeps the *applications* out:
// Jellyfin and the rest run in the service account's group and can read every
// shared folder, and must not be able to read these. It does not, on its own,
// keep one member of the household out of another's, because everything that
// serves these folders — Homebase itself, and smbd with `force user` — acts as
// the same Unix account.
//
// That boundary is Homebase's check on every request rather than the kernel's,
// and it is a trade made openly: the alternative is a folder the Files screen
// cannot list, a phone cannot open, and no future application can reach. It is
// written down in the user guide rather than left to be discovered.
const peopleDirName = "people"

// PeopleLocation is the disk private folders are kept on.
//
// The server's own, always, and not a choice yet. A removable disk that is
// unplugged when somebody signs in would give them an empty folder where their
// files used to be, or no folder at all — and the difference between those two
// is not something a person should have to work out from an error message.
const PeopleLocation = InternalLocationID

// peopleRoot is where private folders live, whether or not any exist.
//
// Distinct from peopleDir, which answers a different question: this is where
// they *would* be, and is what core needs to open somebody's folder; that one
// is whether there is anything for the file server to publish.
func (s *ShareServices) peopleRoot() string {
	mountPoint, ok := s.storage.ResolveLocation(PeopleLocation)
	if !ok {
		return ""
	}
	return filepath.Join(mountPoint, peopleDirName)
}

// peopleDir is the directory the `[people]` share is served from, or empty if
// there is nothing to serve.
//
// Empty when nobody has a personal folder yet, so a server that has never had a
// second account does not advertise a share with nothing behind it.
func (s *ShareServices) peopleDir() string {
	mountPoint, ok := s.storage.ResolveLocation(PeopleLocation)
	if !ok {
		return ""
	}
	path := filepath.Join(mountPoint, peopleDirName)
	entries, err := os.ReadDir(path)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), retiredPrefix) {
			return path
		}
	}
	return ""
}

// retiredPrefix marks a folder whose person no longer has an account.
//
// Removing somebody does not delete their files. Homebase does not delete
// anybody's files as a side effect of anything, and an administrator removing an
// account is not saying "destroy what is in there" — they may be saying "my
// brother has moved out" about the only copy of his photographs.
//
// But leaving the folder where it is would be worse than either: names get
// reused, and the next person called `sam` would sign in to a private folder
// full of the last one's files. So it is moved aside, where it is out of the way
// of a new account, still on the disk, and obvious to anybody looking.
const retiredPrefix = ".removed-"

// ownershipCapable reports whether a filesystem can express who owns a file.
//
// The Windows and camera-card filesystems cannot. Everything on an NTFS or FAT
// disk belongs to whoever mounted it, with permissions invented by the mount
// options, so a folder created there is not private however it is chmodded —
// `os.Chmod` succeeds, reports success, and changes nothing. A private folder
// that is silently not private is worse than no private folder, so this refuses
// instead.
//
// An allowlist rather than a list of the bad ones. The failure of a name not
// thought of should be a refusal to make the folder, which somebody sees and
// asks about, rather than a folder that quietly is not private.
func ownershipCapable(filesystem string) bool {
	switch filesystem {
	case "ext2", "ext3", "ext4", "btrfs", "xfs", "zfs", "f2fs", "jfs", "reiserfs",
		"overlay", "tmpfs":
		return true
	}
	return false
}

// filesystemAt names the filesystem a path is on.
//
// Read from the mount table rather than from the location record, and that is
// the fix for a real refusal: the server's own disk is a directory on the root
// filesystem rather than a volume Homebase went looking for, so its record has
// no filesystem in it at all. Asking the kernel where the path actually is
// answers for every location the same way.
//
// The longest matching mount point wins, because mount points nest: /srv is not
// the answer for a path under /srv/homebase/storage/photos when a disk is
// mounted there.
func filesystemAt(path, mountInfo string) string {
	data, err := os.ReadFile(mountInfo)
	if err != nil {
		return ""
	}

	best, kind := "", ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		separator := slices.Index(fields, "-")
		if separator < 0 || separator+1 >= len(fields) {
			continue
		}
		point := unescapeMountPath(fields[4])
		if point != "/" && !strings.HasPrefix(point, "/") {
			continue
		}
		if path != point && !strings.HasPrefix(path, strings.TrimSuffix(point, "/")+"/") {
			continue
		}
		if len(point) >= len(best) {
			best, kind = point, fields[separator+1]
		}
	}
	return kind
}

// personalFolderRoot is where one person's folder lives, whether or not it
// exists yet.
func personalFolderRoot(mountPoint, username string) string {
	return filepath.Join(mountPoint, peopleDirName, username)
}

// makePersonalFolder creates somebody's private folder.
//
// Converging rather than refusing when it is already there: this runs when an
// account is created, and an account that exists with no folder — because the
// disk was unplugged at the time, or because Homebase was upgraded past this
// change — has to be able to acquire one on the next attempt.
func (s *ShareServices) makePersonalFolder(location, username string) (string, error) {
	if !validShareName.MatchString(username) {
		return "", &Error{
			Code:        "share.invalid_username",
			Message:     "That is not a name Homebase can give a folder to.",
			Detail:      "must match " + shareNamePattern,
			Recoverable: true,
			Recovery:    "Use lowercase letters, numbers and hyphens.",
			Status:      400,
		}
	}

	state, found := s.storage.LocationByID(location)
	if !found {
		return "", unknownLocation(location)
	}
	mountPoint, ok := s.storage.ResolveLocation(location)
	if !ok {
		return "", &Error{
			Code:        "share.disk_not_available",
			Message:     "That disk is not connected, so nothing can be kept on it.",
			Detail:      location,
			Recoverable: true,
			Recovery:    "Plug the disk in and try again.",
			Status:      409,
		}
	}

	if filesystem := filesystemAt(mountPoint, mountInfoPath); !ownershipCapable(filesystem) {
		return "", &Error{
			Code:    "share.disk_cannot_be_private",
			Message: "Private folders cannot be kept on this disk.",
			Detail: state.Name + " is formatted " + describeFilesystem(filesystem) +
				", which does not record who owns a file",
			Recoverable: true,
			Recovery: "Keep private folders on the server's own disk, or on one " +
				"formatted for Linux.",
			Status: 409,
		}
	}

	path, err := makePersonalFolderAt(mountPoint, username)
	if err != nil {
		return "", err
	}
	if err := giveToServiceAccount(path); err != nil {
		return "", err
	}
	// Set again after the chown, which clears the set-group-id bit on some
	// filesystems and would otherwise undo part of this.
	if err := os.Chmod(path, 0o700); err != nil {
		return "", internalError("setting permissions on " + path + ": " + err.Error())
	}
	return path, nil
}

// makePersonalFolderAt is the filesystem half, separated so it can be tested
// without being root and without a `homebase` account on the machine running
// the tests.
func makePersonalFolderAt(mountPoint, username string) (string, error) {
	// The parent is created readable and the folder inside it is not. Anybody
	// may see that `people` exists; only its owner may see what is in one.
	parent := filepath.Join(mountPoint, peopleDirName)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", internalError("creating " + parent + ": " + err.Error())
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		return "", internalError("setting permissions on " + parent + ": " + err.Error())
	}

	path := personalFolderRoot(mountPoint, username)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", internalError("creating " + path + ": " + err.Error())
	}
	// MkdirAll applies the umask, so the mode asked for is not the mode made.
	if err := os.Chmod(path, 0o700); err != nil {
		return "", internalError("setting permissions on " + path + ": " + err.Error())
	}
	return path, nil
}

// retirePersonalFolder moves somebody's folder out of the way of the next person
// to have their name. The files are kept: see retiredPrefix.
func (s *ShareServices) retirePersonalFolder(location, username string) (string, error) {
	if !validShareName.MatchString(username) {
		return "", nil
	}
	mountPoint, ok := s.storage.ResolveLocation(location)
	if !ok {
		// The disk is not here. Nothing to move, and refusing would mean an
		// account that cannot be removed because a USB disk is unplugged.
		return "", nil
	}
	return retirePersonalFolderAt(mountPoint, username)
}

// retirePersonalFolderAt is the filesystem half. See makePersonalFolderAt.
func retirePersonalFolderAt(mountPoint, username string) (string, error) {
	path := personalFolderRoot(mountPoint, username)
	if _, err := os.Stat(path); err != nil {
		return "", nil
	}

	retired := filepath.Join(mountPoint, peopleDirName,
		retiredPrefix+username+"-"+time.Now().UTC().Format("20060102-150405"))
	// A second removal in the same second would otherwise land on the first,
	// and os.Rename would put one folder inside the other rather than fail.
	for suffix := 2; ; suffix++ {
		if _, err := os.Stat(retired); err != nil {
			break
		}
		retired = filepath.Join(mountPoint, peopleDirName,
			retiredPrefix+username+"-"+time.Now().UTC().Format("20060102-150405")+
				"-"+strconv.Itoa(suffix))
	}
	if err := os.Rename(path, retired); err != nil {
		return "", internalError("moving " + path + " aside: " + err.Error())
	}
	return retired, nil
}

// giveToServiceAccount hands a path to the account Homebase itself runs as.
//
// Distinct from giveToServiceGroup, which leaves root as the owner and shares
// the group. Here the owner is the point: it is what separates a folder Homebase
// can read from one every application can.
func giveToServiceAccount(path string) error {
	account, err := user.Lookup(serviceAccount)
	if err != nil {
		return internalError("looking up the " + serviceAccount + " account: " + err.Error())
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return internalError("reading the " + serviceAccount + " account: " + err.Error())
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return internalError("reading the " + serviceAccount + " group: " + err.Error())
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return internalError("giving " + path + " to " + serviceAccount + ": " + err.Error())
	}
	return nil
}

// describeFilesystem names a filesystem the way somebody who has not heard of
// them would recognise it.
func describeFilesystem(filesystem string) string {
	switch filesystem {
	case "":
		return "with nothing Homebase recognises"
	case "ntfs", "ntfs3", "fuseblk":
		return "for Windows (NTFS)"
	case "exfat":
		return "for cameras and phones (exFAT)"
	case "vfat", "msdos":
		return "for cameras and phones (FAT)"
	case "iso9660":
		return "as a disc image, which cannot be written to"
	}
	return "as " + filesystem
}
