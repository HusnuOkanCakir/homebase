package hostd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// What a backup is, on disk.
//
// Plain files in a plain directory tree, with a JSON manifest beside them. No
// archive, no proprietary format, nothing that needs Homebase to read. See
// ADR-0014 — that decision is the reason for most of the awkwardness here, and
// it is worth restating: the machine that broke is the machine the backup
// software was on, and the person recovering is standing in front of a different
// computer holding a disk.
//
// A backup looks like this:
//
//	<location>/homebase-backups/<id>/
//	  manifest.json          what is here, and the SHA-256 of every file
//	  README.txt             how to get your files back without Homebase
//	  system/
//	    homebase.db          exported with VACUUM INTO, never copied
//	    etc/                 /etc/homebase
//	    hostd/               locations.json, assignments.json
//	  apps/<app-id>/         each application's private data
//	  data/<location-id>/    user-selected storage, when included

const (
	// backupDirName is the folder Homebase creates on a destination disk.
	// Deliberately not hidden: somebody browsing the disk on another computer
	// should find it.
	backupDirName = "homebase-backups"

	manifestName = "manifest.json"
	readmeName   = "README.txt"

	// backupFormatVersion is the shape of the manifest. Read by a future
	// Homebase that has to restore something written by this one.
	backupFormatVersion = 1
)

// BackupManifest is the record of one backup. It is written as JSON so that a
// person can open it in a text editor and understand what they are looking at.
type BackupManifest struct {
	FormatVersion int `json:"format_version"`

	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`

	// Hostname and Version describe the machine that wrote it, so somebody
	// looking at three backup folders can tell which is which.
	Hostname string `json:"hostname"`
	Version  string `json:"version"`

	// Kind is "configuration" or "full". A configuration backup is small and
	// fast and holds everything except user data.
	Kind string `json:"kind"`

	// Applications installed when this was taken. Restoring reinstalls them
	// from the catalogue rather than restoring container images: the image is
	// pinned in the manifest and can be fetched again, and a copied container
	// is not something Homebase can promise to run.
	Applications []string `json:"applications"`

	// Files is every file in the backup, with its checksum. This is what makes
	// bit-rot and truncation detectable rather than silent.
	Files []BackupFile `json:"files"`

	TotalBytes uint64 `json:"total_bytes"`

	// Notes records anything deliberately left out, in words, so that a person
	// reading the manifest is not misled about what they have.
	Notes []string `json:"notes,omitempty"`
}

// BackupFile is one file in a backup.
type BackupFile struct {
	// Path is relative to the backup directory, always with forward slashes.
	Path  string `json:"path"`
	Bytes uint64 `json:"bytes"`
	// SHA256 is hex-encoded. Empty only for a file that could not be read, which
	// is recorded rather than skipped.
	SHA256 string `json:"sha256"`
	// Mode is the permission bits, so a restore can put them back.
	Mode uint32 `json:"mode"`
	// ModifiedAt is RFC 3339, for a person comparing two backups.
	ModifiedAt string `json:"modified_at"`
}

// backupReadme is written into every backup.
//
// The audience is somebody whose server has died, who has never read the
// documentation, and who is looking at this disk on a borrowed computer. It is
// the only part of Homebase guaranteed to be present at the moment it is most
// needed, so it says the important thing first: your files are here, in folders,
// and you can just copy them.
const backupReadme = `Homebase backup
===============

Your files are in this folder. You do not need Homebase to get them back.

WHERE THINGS ARE

  apps/          Each application's own files, one folder per application.
  data/          The folders you chose to back up — photographs, films, whatever
                 you pointed Homebase at.
  system/        Homebase's own settings and its database. Only useful to
                 Homebase; you can ignore it.

TO GET A FILE BACK

  Open the folder, find the file, copy it. That is all. These are ordinary
  files and folders, on an ordinary disk.

TO RESTORE A WHOLE SERVER

  Install Homebase on the new machine, plug this disk in, and choose Restore.
  It will tell you what it is going to do before it does anything.

IS THIS BACKUP COMPLETE?

  manifest.json lists every file with a checksum. Homebase can check the whole
  backup against it — look for "Check this backup" in the Storage section.

WHAT IS NOT HERE

  Saved passwords for applications are not backed up. After restoring you will
  be asked for them again. This is deliberate: a disk that leaves the house
  should not carry them.

  Anyone holding this disk can read everything on it. Keep it somewhere you
  would keep a box of photographs.
`

// --- Walking a tree into a backup ---------------------------------------------

// copyTree copies a directory into the backup, recording every file.
//
// Returns what was copied. Errors on individual files are recorded and the copy
// continues: a backup that gives up on the first unreadable file is a backup
// that does not exist, and one bad file is worth knowing about rather than
// worth abandoning eleven good ones for.
func copyTree(source, destination, prefix string) ([]BackupFile, []string, error) {
	var files []BackupFile
	var problems []string

	info, err := os.Stat(source)
	if os.IsNotExist(err) {
		// Nothing to copy is not a failure. An application with no data yet, or
		// a machine with no configuration, is an ordinary state.
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", source, err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a directory", source)
	}

	err = filepath.Walk(source, func(path string, entry os.FileInfo, err error) error {
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s could not be read: %v", path, err))
			return nil
		}

		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}

		target := filepath.Join(destination, relative)

		switch {
		case entry.IsDir():
			return os.MkdirAll(target, 0o700)

		case entry.Mode()&os.ModeSymlink != 0:
			// Symlinks are recorded as a problem rather than followed. Following
			// one can copy a filesystem into a backup, or escape the tree
			// entirely; recreating one can point at something that does not
			// exist on the restored machine.
			problems = append(problems,
				fmt.Sprintf("%s is a shortcut and was not copied", relative))
			return nil

		case !entry.Mode().IsRegular():
			problems = append(problems,
				fmt.Sprintf("%s is not an ordinary file and was not copied", relative))
			return nil
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}

		sum, written, err := copyFile(path, target, entry.Mode().Perm())
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s could not be copied: %v", relative, err))
			// Do not leave a half-written file behind claiming to be a backup.
			os.Remove(target)
			return nil
		}

		files = append(files, BackupFile{
			Path:       joinSlash(prefix, filepath.ToSlash(relative)),
			Bytes:      uint64(written),
			SHA256:     sum,
			Mode:       uint32(entry.Mode().Perm()),
			ModifiedAt: entry.ModTime().UTC().Format(time.RFC3339),
		})
		return nil
	})

	return files, problems, err
}

// copyFile copies one file and returns its SHA-256.
//
// Checksummed while copying rather than by reading it again afterwards: a second
// read is a second chance for the source to have changed, and on a large media
// library it doubles the time.
func copyFile(source, destination string, mode os.FileMode) (string, int64, error) {
	in, err := os.Open(source)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()

	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return "", 0, err
	}
	defer out.Close()

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(out, hash), in)
	if err != nil {
		return "", written, err
	}

	// Flushed before the manifest claims it is there. A backup whose manifest
	// describes files still sitting in the page cache is a backup that a power
	// cut turns into a lie.
	if err := out.Sync(); err != nil {
		return "", written, err
	}

	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

// checksum reads a file and returns its SHA-256, for verification.
func checksum(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// --- The manifest --------------------------------------------------------------

func writeManifest(directory string, manifest BackupManifest) error {
	sort.Slice(manifest.Files, func(i, j int) bool {
		return manifest.Files[i].Path < manifest.Files[j].Path
	})

	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the manifest: %w", err)
	}

	// Written last and atomically. A directory with no manifest is an
	// incomplete backup and is recognisable as one; a directory with a manifest
	// that does not match its contents is a trap.
	temporary := filepath.Join(directory, manifestName+".new")
	if err := os.WriteFile(temporary, append(body, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(directory, manifestName))
}

func readManifest(directory string) (BackupManifest, error) {
	var manifest BackupManifest

	body, err := os.ReadFile(filepath.Join(directory, manifestName))
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return manifest, fmt.Errorf("the manifest could not be read: %w", err)
	}
	if manifest.FormatVersion > backupFormatVersion {
		return manifest, fmt.Errorf(
			"this backup was written by a newer version of Homebase (format %d, this one understands %d)",
			manifest.FormatVersion, backupFormatVersion)
	}
	return manifest, nil
}

// --- Verification ---------------------------------------------------------------

// VerificationResult is what checking a backup found.
type VerificationResult struct {
	ID    string `json:"id"`
	Valid bool   `json:"valid"`

	FilesChecked int `json:"files_checked"`

	// Missing, Corrupt and Unexpected are reported separately because they mean
	// different things: a missing file is an incomplete backup, a corrupt one is
	// a failing disk, and an unexpected one is somebody having put something in
	// the folder.
	Missing    []string `json:"missing,omitempty"`
	Corrupt    []string `json:"corrupt,omitempty"`
	Unexpected []string `json:"unexpected,omitempty"`

	CheckedAt string `json:"checked_at"`
	Message   string `json:"message"`
}

// verifyBackup re-reads every file and compares it with the manifest.
//
// This is the whole reason checksums are recorded. A backup on a disk in a
// drawer bit-rots, is partially overwritten, or was never fully written because
// the disk filled up — and every one of those is silent until somebody looks.
func verifyBackup(directory string) (VerificationResult, error) {
	result := VerificationResult{CheckedAt: time.Now().UTC().Format(time.RFC3339)}

	manifest, err := readManifest(directory)
	if err != nil {
		return result, err
	}
	result.ID = manifest.ID

	described := make(map[string]struct{}, len(manifest.Files))

	for _, file := range manifest.Files {
		described[file.Path] = struct{}{}
		path := filepath.Join(directory, filepath.FromSlash(file.Path))

		info, err := os.Stat(path)
		if err != nil {
			result.Missing = append(result.Missing, file.Path)
			continue
		}
		result.FilesChecked++

		// Size first: it is free, and catches truncation without reading a
		// gigabyte.
		if uint64(info.Size()) != file.Bytes {
			result.Corrupt = append(result.Corrupt, file.Path)
			continue
		}

		sum, err := checksum(path)
		if err != nil || sum != file.SHA256 {
			result.Corrupt = append(result.Corrupt, file.Path)
		}
	}

	// Anything present but not described. Not a failure — somebody may have put
	// a note in the folder — but worth reporting, because it is also what a
	// half-finished restore or a second backup written on top looks like.
	_ = filepath.Walk(directory, func(path string, entry os.FileInfo, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return nil
		}
		slashed := filepath.ToSlash(relative)
		if slashed == manifestName || slashed == readmeName {
			return nil
		}
		if _, known := described[slashed]; !known {
			result.Unexpected = append(result.Unexpected, slashed)
		}
		return nil
	})

	sort.Strings(result.Missing)
	sort.Strings(result.Corrupt)
	sort.Strings(result.Unexpected)

	result.Valid = len(result.Missing) == 0 && len(result.Corrupt) == 0
	result.Message = describeVerification(result, len(manifest.Files))
	return result, nil
}

func describeVerification(result VerificationResult, total int) string {
	switch {
	case result.Valid && len(result.Unexpected) > 0:
		return fmt.Sprintf(
			"All %d files are present and unchanged. %d other files are in the folder that "+
				"Homebase did not put there.", total, len(result.Unexpected))
	case result.Valid:
		return fmt.Sprintf("All %d files are present and unchanged.", total)
	case len(result.Corrupt) > 0 && len(result.Missing) > 0:
		return fmt.Sprintf(
			"%d files are missing and %d have been damaged. This backup cannot be relied on.",
			len(result.Missing), len(result.Corrupt))
	case len(result.Corrupt) > 0:
		return fmt.Sprintf(
			"%d files have been damaged since this backup was made. The disk may be failing.",
			len(result.Corrupt))
	default:
		return fmt.Sprintf(
			"%d files are missing. This backup was probably never finished — the disk may "+
				"have filled up.", len(result.Missing))
	}
}

// --- Paths ---------------------------------------------------------------------

// safeBackupPath resolves a manifest entry against the destination and refuses
// anything that escapes it.
//
// A backup disk is untrusted input. It can be written on another machine, and a
// manifest naming ../../etc/shadow must not be able to write there — the same
// rule application storage follows, for the same reason.
func safeBackupPath(root, relative string) (string, error) {
	if relative == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.Contains(relative, "\x00") {
		return "", fmt.Errorf("path contains a null byte")
	}

	cleaned := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("%s is an absolute path", relative)
	}

	target := filepath.Join(root, cleaned)
	if target != root && !strings.HasPrefix(target, root+string(filepath.Separator)) {
		return "", fmt.Errorf("%s escapes %s", relative, root)
	}
	return target, nil
}

// joinSlash joins manifest paths, which are always forward-slashed regardless
// of the platform — a manifest is meant to be readable anywhere.
func joinSlash(prefix, rest string) string {
	if prefix == "" {
		return rest
	}
	return strings.TrimSuffix(prefix, "/") + "/" + rest
}
