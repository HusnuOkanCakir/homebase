package hostd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func backupServices(t *testing.T) *BackupServices {
	t.Helper()

	root := t.TempDir()
	storage := NewStorageServices(root+"/storage", root+"/state")
	apps := NewAppServices(NewCatalogue(t.TempDir()), "", root+"/apps", root+"/state")

	return NewBackupServices(storage, apps,
		root+"/db/homebase.db", root+"/etc", root+"/state", "test")
}

// --- Copying a tree -------------------------------------------------------------

func TestCopyingATreeRecordsEveryFile(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()

	write(t, filepath.Join(source, "one.txt"), "first")
	if err := os.MkdirAll(filepath.Join(source, "nested", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(source, "nested", "deep", "two.txt"), "second")

	files, problems, err := copyTree(source, destination, "apps/example")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Errorf("problems: %v", problems)
	}
	if len(files) != 2 {
		t.Fatalf("recorded %d files: %+v", len(files), files)
	}

	// Manifest paths are forward-slashed and prefixed, so they are readable on
	// any machine.
	paths := map[string]bool{}
	for _, file := range files {
		paths[file.Path] = true
		if file.SHA256 == "" {
			t.Errorf("%s has no checksum", file.Path)
		}
		if strings.Contains(file.Path, `\`) {
			t.Errorf("%s is not forward-slashed", file.Path)
		}
	}
	for _, want := range []string{"apps/example/one.txt", "apps/example/nested/deep/two.txt"} {
		if !paths[want] {
			t.Errorf("%s is missing from the manifest: %v", want, paths)
		}
	}

	// And the contents actually arrived.
	body, err := os.ReadFile(filepath.Join(destination, "nested", "deep", "two.txt"))
	if err != nil || strings.TrimSpace(string(body)) != "second" {
		t.Errorf("the copy is wrong: %q %v", body, err)
	}
}

// A source that does not exist is an ordinary state — an application with no
// data yet — rather than a failure.
func TestCopyingAnAbsentDirectoryIsNotAnError(t *testing.T) {
	files, problems, err := copyTree(filepath.Join(t.TempDir(), "gone"), t.TempDir(), "apps/x")
	if err != nil {
		t.Fatalf("an absent source was an error: %v", err)
	}
	if len(files) != 0 || len(problems) != 0 {
		t.Errorf("files=%v problems=%v", files, problems)
	}
}

// Symlinks are recorded and skipped, never followed. Following one can copy a
// whole filesystem into a backup, or escape the tree entirely.
func TestSymlinksAreSkippedAndReported(t *testing.T) {
	source := t.TempDir()
	write(t, filepath.Join(source, "real.txt"), "content")
	if err := os.Symlink("/etc/passwd", filepath.Join(source, "link")); err != nil {
		t.Skip("symlinks are not supported here")
	}

	files, problems, err := copyTree(source, t.TempDir(), "apps/x")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Errorf("copied %d files; the symlink should not be one", len(files))
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "shortcut") {
		t.Errorf("the symlink was not reported: %v", problems)
	}
}

// --- Verification ------------------------------------------------------------------

func TestVerifyingAnUntouchedBackupPasses(t *testing.T) {
	directory := writeTestBackup(t, map[string]string{
		"apps/example/one.txt": "first",
		"system/etc/conf.yaml": "settings",
	})

	result, err := verifyBackup(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid {
		t.Errorf("a fresh backup did not verify: %+v", result)
	}
	if result.FilesChecked != 2 {
		t.Errorf("checked %d files", result.FilesChecked)
	}
}

// Bit-rot. A backup on a disk in a drawer changes underneath you, and every one
// of those failures is silent until somebody looks.
func TestVerifyingDetectsAChangedFile(t *testing.T) {
	directory := writeTestBackup(t, map[string]string{"apps/example/one.txt": "first"})

	// Same length, different content — so a size check alone would miss it.
	write(t, filepath.Join(directory, "apps", "example", "one.txt"), "FIRST")

	result, err := verifyBackup(directory)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid {
		t.Fatal("a corrupted backup verified successfully")
	}
	if len(result.Corrupt) != 1 || result.Corrupt[0] != "apps/example/one.txt" {
		t.Errorf("corrupt = %v", result.Corrupt)
	}
	if !strings.Contains(result.Message, "damaged") {
		t.Errorf("the message does not say it is damaged: %q", result.Message)
	}
}

// A backup that ran out of disk part-way through. It looks finished until
// somebody counts.
func TestVerifyingDetectsAMissingFile(t *testing.T) {
	directory := writeTestBackup(t, map[string]string{
		"apps/example/one.txt": "first",
		"apps/example/two.txt": "second",
	})
	os.Remove(filepath.Join(directory, "apps", "example", "two.txt"))

	result, err := verifyBackup(directory)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid {
		t.Fatal("a backup with a missing file verified successfully")
	}
	if len(result.Missing) != 1 {
		t.Errorf("missing = %v", result.Missing)
	}
	// The message should point at the likely cause rather than just the fact.
	if !strings.Contains(result.Message, "disk may have filled up") {
		t.Errorf("the message does not suggest why: %q", result.Message)
	}
}

// Truncation is caught by size before a checksum is computed, which matters on
// a file too large to read twice.
func TestVerifyingDetectsTruncation(t *testing.T) {
	directory := writeTestBackup(t, map[string]string{"apps/example/one.txt": "a long line of text"})
	write(t, filepath.Join(directory, "apps", "example", "one.txt"), "a")

	result, _ := verifyBackup(directory)
	if result.Valid {
		t.Fatal("a truncated file verified successfully")
	}
}

func TestAManifestFromANewerHomebaseIsRefused(t *testing.T) {
	directory := writeTestBackup(t, map[string]string{"apps/example/one.txt": "x"})

	manifest, err := readManifest(directory)
	if err != nil {
		t.Fatal(err)
	}
	manifest.FormatVersion = backupFormatVersion + 1
	if err := writeManifest(directory, manifest); err != nil {
		t.Fatal(err)
	}

	if _, err := readManifest(directory); err == nil {
		t.Fatal("a backup from a newer Homebase was read anyway")
	} else if !strings.Contains(err.Error(), "newer version") {
		t.Errorf("the error does not explain why: %v", err)
	}
}

// --- Paths --------------------------------------------------------------------------

// A backup disk is untrusted input: it can be written on another machine. A
// manifest naming ../../etc/shadow must not be able to write there.
func TestARestorePathCannotEscapeItsRoot(t *testing.T) {
	root := t.TempDir()

	for _, relative := range []string{
		"../escape",
		"../../etc/shadow",
		"nested/../../escape",
		"/etc/shadow",
		"/",
		"",
		"file\x00name",
	} {
		if _, err := safeBackupPath(root, relative); err == nil {
			t.Errorf("%q was accepted", relative)
		}
	}

	for _, relative := range []string{"one.txt", "nested/two.txt", "a/b/c/d.txt"} {
		resolved, err := safeBackupPath(root, relative)
		if err != nil {
			t.Errorf("%q was refused: %v", relative, err)
			continue
		}
		if !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			t.Errorf("%q resolved outside the root: %s", relative, resolved)
		}
	}
}

// The set of places a restore may write is a fixed table in the code, not
// anything derived from the manifest.
func TestRestoreOnlyWritesWhereHomebaseDecides(t *testing.T) {
	s := backupServices(t)

	for _, path := range []string{
		"somewhere/else.txt",
		"etc/passwd",
		"../escape",
		"",
		"system/../../etc/shadow",
	} {
		if target, err := s.restoreTarget(path); err == nil {
			t.Errorf("%q was mapped to %s", path, target)
		}
	}

	// And the four prefixes that are legitimate.
	for _, path := range []string{
		"system/homebase.db",
		"system/etc/homebase.yaml",
		"system/hostd/locations.json",
		"apps/jellyfin/config/settings.xml",
		"data/media/film.mkv",
	} {
		if _, err := s.restoreTarget(path); err != nil {
			t.Errorf("%q was refused: %v", path, err)
		}
	}
}

func TestBackupIDsAreRestrictive(t *testing.T) {
	// It becomes a path component on a disk that may have been written
	// elsewhere.
	for _, id := range []string{
		"", "..", "../escape", "a", "with spaces", "UPPER-CASE",
		"semi;colon", "slash/inside", strings.Repeat("a", 100),
	} {
		if validBackupID(id) {
			t.Errorf("%q was accepted", id)
		}
	}

	generated := backupID()
	if !validBackupID(generated) {
		t.Errorf("a generated id was rejected: %q", generated)
	}
	// Sortable, so listing newest-first needs no manifest parsing.
	if !strings.HasPrefix(generated, "20") {
		t.Errorf("a generated id does not start with a date: %q", generated)
	}
}

// --- The README ------------------------------------------------------------------------

// Every backup carries instructions for somebody who has lost the server. It is
// the only part of Homebase guaranteed to be present at the moment it is most
// needed.
func TestTheBackupExplainsItselfToSomebodyWithoutHomebase(t *testing.T) {
	// The important thing first: the files are here and you can just copy them.
	if !strings.Contains(backupReadme, "You do not need Homebase") {
		t.Error("the README does not say the backup is readable without Homebase")
	}
	// And the two things that would otherwise be discovered at the worst moment.
	if !strings.Contains(backupReadme, "passwords for applications are not backed up") {
		t.Error("the README does not say credentials are absent")
	}
	if !strings.Contains(backupReadme, "Anyone holding this disk can read everything") {
		t.Error("the README does not warn that the disk is unencrypted")
	}
	// No jargon: the reader is anxious and does not know what a volume is.
	for _, jargon := range []string{"tarball", "sqlite", "chmod", "sudo", "daemon"} {
		if strings.Contains(strings.ToLower(backupReadme), jargon) {
			t.Errorf("the README uses %q", jargon)
		}
	}
}

// --- Helpers ---------------------------------------------------------------------------

// writeTestBackup builds a backup directory with a correct manifest.
func writeTestBackup(t *testing.T, contents map[string]string) string {
	t.Helper()

	directory := t.TempDir()
	manifest := BackupManifest{
		FormatVersion: backupFormatVersion,
		ID:            "2026-08-09-120000-abcdef01",
		CreatedAt:     "2026-08-09T12:00:00Z",
		Hostname:      "test",
		Kind:          "full",
	}

	for relative, body := range contents {
		path := filepath.Join(directory, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		sum, err := checksum(path)
		if err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, BackupFile{
			Path:   relative,
			Bytes:  uint64(len(body)),
			SHA256: sum,
			Mode:   0o600,
		})
	}

	if err := writeManifest(directory, manifest); err != nil {
		t.Fatal(err)
	}
	return directory
}

// --- The round trip -----------------------------------------------------------------

// The milestone's exit condition, in miniature: back something up, destroy it,
// restore it, and check it is the same.
//
// Not a substitute for the VM test, which does it across two machines. This is
// the version that runs in a second and fails clearly.
func TestRestoringPutsFilesBackWhereTheyCameFrom(t *testing.T) {
	s := backupServices(t)

	// A machine with some application data and some configuration.
	appData := filepath.Join(s.apps.dataRoot, "jellyfin", "config")
	if err := os.MkdirAll(appData, 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(appData, "settings.xml"), "<library>films</library>")

	if err := os.MkdirAll(s.configDir, 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s.configDir, "homebase.yaml"), "name: the-server")

	// A backup of exactly those, assembled the way create() would.
	backupDir := t.TempDir()
	manifest := BackupManifest{
		FormatVersion: backupFormatVersion,
		ID:            "2026-08-09-120000-abcdef01",
		CreatedAt:     "2026-08-09T12:00:00Z",
		Kind:          "full",
	}

	for _, source := range []struct{ from, into, prefix string }{
		{s.apps.dataRoot, filepath.Join(backupDir, "apps"), "apps"},
		{s.configDir, filepath.Join(backupDir, "system", "etc"), "system/etc"},
	} {
		files, _, err := copyTree(source.from, source.into, source.prefix)
		if err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, files...)
	}
	if err := writeManifest(backupDir, manifest); err != nil {
		t.Fatal(err)
	}

	// The machine loses everything.
	if err := os.RemoveAll(s.apps.dataRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(s.configDir); err != nil {
		t.Fatal(err)
	}

	// Restore, file by file, exactly as restore() does.
	restored := 0
	for _, file := range manifest.Files {
		target, err := s.restoreTarget(file.Path)
		if err != nil {
			t.Fatalf("%s could not be mapped: %v", file.Path, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(backupDir, filepath.FromSlash(file.Path))
		if _, _, err := copyFile(source, target, os.FileMode(file.Mode)); err != nil {
			t.Fatalf("%s could not be restored: %v", file.Path, err)
		}
		restored++
	}

	if restored != 2 {
		t.Fatalf("restored %d files, want 2", restored)
	}

	settings, err := os.ReadFile(filepath.Join(appData, "settings.xml"))
	if err != nil || string(settings) != "<library>films</library>\n" {
		t.Errorf("the application's settings did not come back: %q %v", settings, err)
	}
	config, err := os.ReadFile(filepath.Join(s.configDir, "homebase.yaml"))
	if err != nil || string(config) != "name: the-server\n" {
		t.Errorf("the configuration did not come back: %q %v", config, err)
	}
}

// A restore is a merge, not a mirror. Somebody recovering one application from
// last month's backup must not lose the three they added since.
func TestRestoringDoesNotDeleteWhatTheBackupDoesNotContain(t *testing.T) {
	s := backupServices(t)

	added := filepath.Join(s.apps.dataRoot, "added-since", "notes.txt")
	if err := os.MkdirAll(filepath.Dir(added), 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, added, "written after the backup was taken")

	// A backup that knows nothing about it.
	backupDir := writeTestBackup(t, map[string]string{
		"apps/jellyfin/settings.xml": "<library/>",
	})
	manifest, err := readManifest(backupDir)
	if err != nil {
		t.Fatal(err)
	}

	for _, file := range manifest.Files {
		target, err := s.restoreTarget(file.Path)
		if err != nil {
			continue
		}
		os.MkdirAll(filepath.Dir(target), 0o750)
		copyFile(filepath.Join(backupDir, filepath.FromSlash(file.Path)),
			target, os.FileMode(file.Mode))
	}

	if _, err := os.Stat(added); err != nil {
		t.Error("restoring deleted a file the backup did not contain")
	}
}

// A file that is damaged in the backup must not be written over a good one.
// Restoring corruption on top of working data is the worst outcome available.
func TestADamagedFileIsNotRestoredOverAGoodOne(t *testing.T) {
	directory := writeTestBackup(t, map[string]string{"apps/example/one.txt": "correct"})

	// Damage it after the manifest recorded the original.
	write(t, filepath.Join(directory, "apps", "example", "one.txt"), "damaged")

	manifest, err := readManifest(directory)
	if err != nil {
		t.Fatal(err)
	}

	source := filepath.Join(directory, "apps", "example", "one.txt")
	sum, err := checksum(source)
	if err != nil {
		t.Fatal(err)
	}
	if sum == manifest.Files[0].SHA256 {
		t.Fatal("the test did not actually damage the file")
	}

	// Which is what restore() checks before writing anything.
	result, err := verifyBackup(directory)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid {
		t.Error("a damaged backup was reported as valid, so restore would trust it")
	}
}

// A backup on the disk holding the data protects against one thing — deleting a
// file by accident — and nothing else. Disks fail whole, and presenting that as
// a backup is worse than having none.
func TestBackingUpOntoTheDiskHoldingTheDataIsRefused(t *testing.T) {
	root := t.TempDir()
	storage := NewStorageServices(root+"/storage", root+"/state")
	catalogue := NewCatalogue(t.TempDir())
	apps := NewAppServices(catalogue, "", root+"/apps", root+"/state").WithStorage(storage)
	s := NewBackupServices(storage, apps, root+"/db.sqlite", root+"/etc", root+"/state", "test")

	writeCatalogueFile(t, catalogue, "jellyfin.json", map[string]any{
		"manifest_version": 1, "id": "jellyfin", "name": "Jellyfin",
		"container": map[string]any{"image": "example/app", "version": "1.0.0"},
		"storage": []any{
			map[string]any{"id": "media", "type": "user-selected", "mount_path": "/media"},
		},
		"health": map[string]any{"type": "none"},
	})

	// Jellyfin keeps its films on this disk.
	if err := storage.saveAssignments([]Assignment{
		{App: "jellyfin", StorageID: "media", Location: "films", Subdirectory: "jellyfin"},
	}); err != nil {
		t.Fatal(err)
	}

	err := s.requireUsableDestination("films", LocationState{
		Location: Location{ID: "films", Name: "Films drive"},
		Mounted:  true,
	}, true)

	if err == nil {
		t.Fatal("a backup was allowed onto the disk holding the data it backs up")
	}

	var hostErr *Error
	if !asHostError(err, &hostErr) || hostErr.Code != "backup.destination_holds_data" {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(hostErr.Message, "Jellyfin") {
		t.Errorf("the message does not say which application: %q", hostErr.Message)
	}
	// The user has to be told *why*, or this reads as Homebase being awkward.
	if !strings.Contains(hostErr.Recovery, "disks fail as a whole") {
		t.Errorf("the recovery advice does not explain the reason: %q", hostErr.Recovery)
	}

	// A disk nothing keeps files on is fine.
	if err := s.requireUsableDestination("spare", LocationState{
		Location: Location{ID: "spare", Name: "Spare drive"},
		Mounted:  true,
	}, true); err != nil {
		t.Errorf("an unused disk was refused: %v", err)
	}
}

// A disconnected or read-only destination fails before anything is written,
// with an explanation rather than a filesystem error.
func TestAnUnusableDestinationIsRefusedEarly(t *testing.T) {
	s := backupServices(t)

	cases := []struct {
		name     string
		location LocationState
		found    bool
		want     string
	}{
		{"unknown", LocationState{}, false, "storage.unknown_location"},
		{
			"not connected",
			LocationState{Location: Location{ID: "spare", Name: "Spare"}},
			true, "backup.destination_not_connected",
		},
		{
			"read only",
			LocationState{
				Location: Location{ID: "spare", Name: "Spare"},
				Mounted:  true, ReadOnly: true,
			},
			true, "backup.destination_read_only",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.requireUsableDestination("spare", tc.location, tc.found)
			var hostErr *Error
			if !asHostError(err, &hostErr) {
				t.Fatalf("got %T: %v", err, err)
			}
			if hostErr.Code != tc.want {
				t.Errorf("code = %q, want %q", hostErr.Code, tc.want)
			}
		})
	}
}
