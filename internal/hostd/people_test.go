package hostd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A private folder that is silently not private is worse than none, and every
// disk somebody plugs into a home server is formatted one of these ways.
func TestPrivateFoldersAreRefusedOnDisksThatCannotOwnAFile(t *testing.T) {
	for _, filesystem := range []string{
		"ntfs", "ntfs3", "fuseblk", "exfat", "vfat", "msdos", "iso9660", "",
	} {
		if ownershipCapable(filesystem) {
			t.Errorf("%q is treated as able to keep a folder private; chmod on it "+
				"succeeds and changes nothing", filesystem)
		}
	}
	for _, filesystem := range []string{"ext4", "btrfs", "xfs"} {
		if !ownershipCapable(filesystem) {
			t.Errorf("%q cannot be used for private folders, which rules out the "+
				"server's own disk", filesystem)
		}
	}
}

// The folder is created without the group bit. Every application on this server
// runs in the service account's group and can read every shared folder; these
// are the folders they must not be able to read.
func TestAPrivateFolderIsNotReadableByTheApplications(t *testing.T) {
	root := t.TempDir()
	path, _, err := makePersonalFolderAt(root, "alice")
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("alice's folder is %04o; anything with a group or other bit is "+
			"readable by every container on this server", mode)
	}

	// The folder holding them is not secret. Somebody has to be able to see
	// that `people` exists to be told they have nothing in it.
	parent, err := os.Stat(filepath.Join(root, peopleDirName))
	if err != nil {
		t.Fatal(err)
	}
	if mode := parent.Mode().Perm(); mode != 0o755 {
		t.Fatalf("the people directory is %04o, not 0755", mode)
	}
}

// Asked for twice, because an account can exist without a folder — the disk was
// unplugged, or the server was upgraded past the change that added them — and
// the second attempt has to be the one that succeeds rather than the one that
// refuses.
func TestMakingAPrivateFolderTwiceIsNotAnError(t *testing.T) {
	root := t.TempDir()
	first, _, err := makePersonalFolderAt(root, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "notes.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	second, created, err := makePersonalFolderAt(root, "alice")
	if err != nil {
		t.Fatalf("the second attempt failed: %v", err)
	}
	if second != first {
		t.Fatalf("the second attempt made %s rather than %s", second, first)
	}
	if created {
		t.Error("the second attempt reported making a folder that was already there, " +
			"which reconfigures the file server on every sign-in")
	}
	if _, err := os.Stat(filepath.Join(first, "notes.txt")); err != nil {
		t.Fatal("converging on an existing folder emptied it")
	}
}

// Names get reused. Without this, the next person called sam signs in to a
// private folder full of the last one's files — and neither of them is told.
func TestANewAccountDoesNotInheritTheLastPersonsFiles(t *testing.T) {
	root := t.TempDir()
	path, _, err := makePersonalFolderAt(root, "sam")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "photos.txt"), []byte("his"), 0o600); err != nil {
		t.Fatal(err)
	}

	retired, err := retirePersonalFolderAt(root, "sam")
	if err != nil {
		t.Fatal(err)
	}
	if retired == "" {
		t.Fatal("nothing was moved aside, so the folder is still in the way")
	}

	// Kept, not deleted. Removing an account is not a decision to destroy the
	// only copy of somebody's photographs.
	if _, err := os.Stat(filepath.Join(retired, "photos.txt")); err != nil {
		t.Fatalf("the files were destroyed with the account: %v", err)
	}
	if !strings.Contains(filepath.Base(retired), "sam") {
		t.Errorf("%s does not say whose it was", retired)
	}

	fresh, _, err := makePersonalFolderAt(root, "sam")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the new sam signed in to %d of the old sam's files", len(entries))
	}
}

// Twice in the same second, which is what a script removing two accounts does.
// os.Rename onto an existing directory does not fail — it puts one inside the
// other — so the second person's files would end up inside the first's.
func TestTwoFoldersRetiredInTheSameSecondDoNotLandOnEachOther(t *testing.T) {
	root := t.TempDir()
	var retired []string
	for range 2 {
		if _, _, err := makePersonalFolderAt(root, "sam"); err != nil {
			t.Fatal(err)
		}
		path, err := retirePersonalFolderAt(root, "sam")
		if err != nil {
			t.Fatal(err)
		}
		retired = append(retired, path)
	}
	if retired[0] == retired[1] {
		t.Fatalf("both went to %s", retired[0])
	}
	for _, path := range retired {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s is not where it was reported to be: %v", path, err)
		}
	}
}

// A retired folder is not somebody's folder. Left counted, the [people] share
// would be written for a server where every account has been removed.
func TestARetiredFolderDoesNotCountAsSomebodyHavingOne(t *testing.T) {
	root := t.TempDir()
	if _, _, err := makePersonalFolderAt(root, "sam"); err != nil {
		t.Fatal(err)
	}
	if _, err := retirePersonalFolderAt(root, "sam"); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(root, peopleDirName))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), retiredPrefix) {
			t.Fatalf("%s still counts as a folder somebody has", entry.Name())
		}
	}
}

// --- What the [people] share says ----------------------------------------------

func TestThePeopleShareServesEachPersonTheirOwnFolder(t *testing.T) {
	stanza := renderPeopleShare("/srv/homebase/storage/internal/people")

	required := map[string]string{
		"path = /srv/homebase/storage/internal/people/%U": "everybody the same folder",
		"valid users = @homebase":                         "anybody who can reach the port",
		// Without this smbd impersonates hbshare-alice, which cannot read a
		// folder owned by the service account and readable by nobody else.
		"force user = homebase": "a folder nobody can open",
		"create mask = 0600":    "files an application can read",
	}
	for line, otherwise := range required {
		if !strings.Contains(stanza, line) {
			t.Errorf("missing %q — without it the share is %s", line, otherwise)
		}
	}
}

// Nobody has a private folder yet: there is nothing to serve, and a share with
// nothing behind it is surface with no purpose.
func TestThePeopleShareIsAbsentUntilSomebodyHasAFolder(t *testing.T) {
	config := renderSambaConfig("homebase", []ShareState{{
		Share: Share{Name: "backup", Location: "internal"},
		Path:  "/srv/homebase/storage/internal/shares/backup",
	}}, "", nil)
	if strings.Contains(config, "[people]") {
		t.Fatal("the people share is written when nobody has a folder")
	}

	withPeople := renderSambaConfig("homebase", nil,
		"/srv/homebase/storage/internal/people", nil)
	if !strings.Contains(withPeople, "[people]") {
		t.Fatal("the people share is missing when somebody does")
	}
}

// The server's own disk was refused, and the message said it was formatted with
// nothing Homebase recognises. It is ext4. The internal location is a directory
// on the root filesystem rather than a volume Homebase went looking for, so the
// filesystem in its record is empty — which is a fact about the record, not
// about the disk. The kernel is asked instead.
func TestTheFilesystemComesFromTheKernelRatherThanTheRecord(t *testing.T) {
	table := filepath.Join(t.TempDir(), "mountinfo")
	contents := "" +
		"25 1 8:2 / / rw,relatime shared:1 - ext4 /dev/sda2 rw\n" +
		"31 25 0:24 / /srv/homebase/storage/photos rw,relatime shared:9 - exfat /dev/sdb1 rw\n" +
		"36 25 0:31 / /mnt/with\\040space rw,relatime shared:11 - btrfs /dev/sdc1 rw\n"
	if err := os.WriteFile(table, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"/srv/homebase/storage/internal": "ext4",
		// The longest match, not the first. Mount points nest, and / matches
		// everything.
		"/srv/homebase/storage/photos":          "exfat",
		"/srv/homebase/storage/photos/holidays": "exfat",
		"/mnt/with space":                       "btrfs",
		// A prefix that is not a path boundary is a different directory.
		"/srv/homebase/storage/photos-old": "ext4",
	}
	for path, want := range cases {
		if got := filesystemAt(path, table); got != want {
			t.Errorf("%s is on %q, not %q", path, got, want)
		}
	}

	if ownershipCapable(filesystemAt("/srv/homebase/storage/internal", table)) != true {
		t.Error("the server's own disk cannot keep a private folder")
	}
	if ownershipCapable(filesystemAt("/srv/homebase/storage/photos", table)) {
		t.Error("an exFAT disk is treated as able to keep a folder private")
	}

	// An unreadable mount table refuses rather than allows. A privacy control
	// that fails open is not one.
	if ownershipCapable(filesystemAt("/anywhere", filepath.Join(t.TempDir(), "gone"))) {
		t.Error("a folder would be created as private with no way to know it is")
	}
}
