package hostd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Backup and restore.
//
// The exit condition for this milestone is about restoring, not about backing
// up, and the code is arranged around that: a backup is only ever a means. See
// ADR-0014.
//
// Two rules from that decision show up in almost every function here:
//
//   A backup never goes on a disk holding the data it is backing up. A copy on
//   the same disk protects against one thing — an accidental deletion — and
//   presenting it as a backup is worse than having none.
//
//   Restoring never deletes anything the backup does not contain. A restore is a
//   merge, not a mirror: somebody recovering one application from last month's
//   backup must not lose the three they added since.

const (
	// backupTimeout bounds a full backup. Copying a media library to a USB disk
	// is slow, and the number that matters is "slower than anybody would wait
	// without thinking it had hung", not "slower than it should be".
	backupTimeout = 6 * time.Hour

	// restoreTimeout is the same problem in the other direction.
	restoreTimeout = 6 * time.Hour
)

// BackupServices is what the backup operations need.
type BackupServices struct {
	storage *StorageServices
	apps    *AppServices

	// databasePath is core's SQLite database, exported rather than copied.
	databasePath string
	// configDir and stateDir are Homebase's own settings.
	configDir string
	stateDir  string

	version string

	mu sync.Mutex
}

func NewBackupServices(storage *StorageServices, apps *AppServices,
	databasePath, configDir, stateDir, version string) *BackupServices {

	if databasePath == "" {
		databasePath = "/var/lib/homebase/homebase.db"
	}
	if configDir == "" {
		configDir = "/etc/homebase"
	}
	if stateDir == "" {
		stateDir = DefaultStateDir
	}
	return &BackupServices{
		storage:      storage,
		apps:         apps,
		databasePath: databasePath,
		configDir:    configDir,
		stateDir:     stateDir,
		version:      version,
	}
}

// RegisterBackupOperations adds the backup domain to a registry.
func RegisterBackupOperations(r *Registry, services *BackupServices) {
	r.MustRegister(Operation{
		Name:    "backup.get_schedule",
		Summary: "Report when backups run by themselves, and how the last one went.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 15 * time.Second,
		Handler: Typed(services.getSchedule),
	})

	r.MustRegister(Operation{
		Name:    "backup.set_schedule",
		Summary: "Choose how often backups run by themselves, and where they go.",
		// Low. It destroys nothing and can be changed again — but it decides
		// whether anything gets backed up at all, so it is not a read.
		Risk:        RiskLow,
		Permissions: []string{"backup.run"},
		Confirm:     ConfirmNone,
		Timeout:     30 * time.Second,
		Rollback:    "backup.set_schedule, with the previous schedule",
		Handler:     Typed(services.setSchedule),
	})

	r.MustRegister(Operation{
		Name:    "backup.list",
		Summary: "List the backups on a storage location.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 60 * time.Second,
		Handler: Typed(services.list),
	})

	r.MustRegister(Operation{
		Name:    "backup.create",
		Summary: "Copy this server's settings, and optionally its data, onto a disk.",
		// Low: it writes only to the backup folder on the destination and
		// destroys nothing.
		Risk:        RiskLow,
		Permissions: []string{"backup.run"},
		Confirm:     ConfirmNone,
		Timeout:     backupTimeout,
		Rollback:    "backup.delete",
		Handler:     Typed(services.create),
	})

	r.MustRegister(Operation{
		Name:    "backup.verify",
		Summary: "Re-read a backup and check every file against its checksum.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		// Reading a media library back takes as long as writing it.
		Timeout: backupTimeout,
		Handler: Typed(services.verify),
	})

	r.MustRegister(Operation{
		Name:    "backup.preview",
		Summary: "Report what restoring a backup would change, without changing anything.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 5 * time.Minute,
		Handler: Typed(services.preview),
	})

	r.MustRegister(Operation{
		Name:    "backup.restore",
		Summary: "Put a backup's settings and data back onto this server, overwriting what is here.",
		// The third operation that destroys data irreversibly — and unlike the
		// other two, what it overwrites is usually what somebody is trying to
		// save.
		Risk:        RiskCritical,
		Permissions: []string{"backup.run", "apps.manage", "storage.modify"},
		Confirm:     ConfirmExplicit,
		Timeout:     restoreTimeout,
		Handler:     Typed(services.restore),
	})

	r.MustRegister(Operation{
		Name:        "backup.delete",
		Summary:     "Delete one backup from a disk.",
		Risk:        RiskMedium,
		Permissions: []string{"backup.run"},
		Confirm:     ConfirmRequired,
		Timeout:     10 * time.Minute,
		Handler:     Typed(services.delete),
	})
}

// --- Listing --------------------------------------------------------------------

type BackupRef struct {
	// Location is the managed storage location holding the backups.
	Location string `json:"location"`
	// ID names one backup within it.
	ID string `json:"id,omitempty"`
}

// BackupSummary is one backup, as listed.
type BackupSummary struct {
	ID        string `json:"id"`
	Location  string `json:"location"`
	CreatedAt string `json:"created_at"`
	Hostname  string `json:"hostname"`
	Version   string `json:"version"`
	Kind      string `json:"kind"`

	Files      int      `json:"files"`
	TotalBytes uint64   `json:"total_bytes"`
	Apps       []string `json:"applications,omitempty"`
	Notes      []string `json:"notes,omitempty"`

	// Complete is false for a directory with no readable manifest — a backup
	// that was interrupted. Listed rather than hidden: a folder that looks like
	// a backup and is not is exactly what somebody must not rely on.
	Complete bool   `json:"complete"`
	Problem  string `json:"problem,omitempty"`
}

func (s *BackupServices) list(_ context.Context, params BackupRef) (any, error) {
	root, err := s.backupRoot(params.Location)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return map[string]any{"backups": []BackupSummary{}, "location": params.Location}, nil
	}
	if err != nil {
		return nil, internalError("reading " + root + ": " + err.Error())
	}

	backups := []BackupSummary{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		summary := BackupSummary{ID: entry.Name(), Location: params.Location}
		manifest, err := readManifest(filepath.Join(root, entry.Name()))
		if err != nil {
			summary.Problem = "this backup was not finished, or cannot be read"
			backups = append(backups, summary)
			continue
		}

		summary.Complete = true
		summary.CreatedAt = manifest.CreatedAt
		summary.Hostname = manifest.Hostname
		summary.Version = manifest.Version
		summary.Kind = manifest.Kind
		summary.Files = len(manifest.Files)
		summary.TotalBytes = manifest.TotalBytes
		summary.Apps = manifest.Applications
		summary.Notes = manifest.Notes
		backups = append(backups, summary)
	}

	// Newest first: the one somebody wants is almost always the most recent.
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt > backups[j].CreatedAt
	})

	return map[string]any{"backups": backups, "location": params.Location}, nil
}

// --- Creating ---------------------------------------------------------------------

type CreateBackupParams struct {
	// Location is where to write it.
	Location string `json:"location"`

	// IncludeData controls whether user data is copied as well as settings.
	// Separate because a configuration backup takes seconds and a full one can
	// take hours, and somebody wanting the first should not be made to wait for
	// the second.
	IncludeData bool `json:"include_data"`
}

func (s *BackupServices) create(ctx context.Context, params CreateBackupParams) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	destination, found := s.storage.LocationByID(params.Location)
	if err := s.requireUsableDestination(params.Location, destination, found); err != nil {
		return nil, err
	}

	root, err := s.backupRoot(params.Location)
	if err != nil {
		return nil, err
	}

	kind := "configuration"
	if params.IncludeData {
		kind = "full"
	}

	id := backupID()
	directory := filepath.Join(root, id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, internalError("creating " + directory + ": " + err.Error())
	}

	manifest := BackupManifest{
		FormatVersion: backupFormatVersion,
		ID:            id,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		Hostname:      hostname(),
		Version:       s.version,
		Kind:          kind,
	}

	var problems []string

	// 1. The database, exported rather than copied.
	if files, note, err := s.exportDatabase(ctx, directory); err != nil {
		// Fatal: a backup without Homebase's own state cannot restore a server,
		// and pretending otherwise is the failure this milestone exists to
		// prevent.
		os.RemoveAll(directory)
		return nil, err
	} else {
		manifest.Files = append(manifest.Files, files...)
		if note != "" {
			problems = append(problems, note)
		}
	}

	// 2. Configuration, and hostd's own record of disks and assignments.
	for _, source := range []struct{ from, prefix string }{
		{s.configDir, "system/etc"},
		{s.stateDir, "system/hostd"},
	} {
		files, issues, err := copyTree(source.from,
			filepath.Join(directory, filepath.FromSlash(source.prefix)), source.prefix)
		if err != nil {
			os.RemoveAll(directory)
			return nil, internalError("backing up " + source.from + ": " + err.Error())
		}
		manifest.Files = append(manifest.Files, files...)
		problems = append(problems, issues...)
	}

	// 3. Each application's private data.
	for _, application := range s.apps.Catalogue.All() {
		source := s.apps.appDataDir(application.ID)
		if _, err := os.Stat(source); err != nil {
			continue
		}
		manifest.Applications = append(manifest.Applications, application.ID)

		prefix := "apps/" + application.ID
		files, issues, err := copyTree(source,
			filepath.Join(directory, "apps", application.ID), prefix)
		if err != nil {
			problems = append(problems,
				fmt.Sprintf("%s's files could not be backed up: %v", application.Name, err))
			continue
		}
		manifest.Files = append(manifest.Files, files...)
		problems = append(problems, issues...)
	}

	// 4. User data, when asked for.
	if params.IncludeData {
		included, issues := s.backupUserData(directory, params.Location, &manifest)
		problems = append(problems, issues...)
		if !included {
			manifest.Notes = append(manifest.Notes,
				"No user data was included: no storage locations were available to copy.")
		}
		manifest.Notes = append(manifest.Notes,
			"The disk this backup is stored on is not itself backed up.")
	} else {
		manifest.Notes = append(manifest.Notes,
			"Settings only. The files on your storage disks were not copied.")
	}

	// The secrets store is deliberately absent, and says so in the manifest —
	// somebody reading it later must not believe they have something they do
	// not. See ADR-0014.
	manifest.Notes = append(manifest.Notes,
		"Saved application passwords are not included. They are asked for again after restoring.")

	for _, file := range manifest.Files {
		manifest.TotalBytes += file.Bytes
	}
	if len(problems) > 0 {
		manifest.Notes = append(manifest.Notes, problems...)
	}

	if err := os.WriteFile(filepath.Join(directory, readmeName), []byte(backupReadme), 0o600); err != nil {
		problems = append(problems, "the recovery notes could not be written: "+err.Error())
	}

	// Last, so a directory without one is recognisably incomplete.
	if err := writeManifest(directory, manifest); err != nil {
		os.RemoveAll(directory)
		return nil, internalError("writing the manifest: " + err.Error())
	}

	return map[string]any{
		"id":          id,
		"location":    params.Location,
		"kind":        kind,
		"files":       len(manifest.Files),
		"total_bytes": manifest.TotalBytes,
		"problems":    problems,
		"message":     describeBackup(manifest, problems),
	}, nil
}

func describeBackup(manifest BackupManifest, problems []string) string {
	what := "Your settings have been backed up"
	if manifest.Kind == "full" {
		what = "Your settings and files have been backed up"
	}
	if len(problems) > 0 {
		return fmt.Sprintf("%s, but %d things could not be copied. See the details.",
			what, len(problems))
	}
	return fmt.Sprintf("%s — %d files.", what, len(manifest.Files))
}

// backupUserData copies managed storage locations other than the destination.
func (s *BackupServices) backupUserData(directory, destinationID string, manifest *BackupManifest) (bool, []string) {
	var problems []string
	included := false

	locations, err := s.storage.Locations()
	if err != nil {
		return false, []string{"the storage locations could not be read: " + err.Error()}
	}

	for _, location := range locations {
		// Never the disk being written to. Copying a disk onto itself fills it
		// and backs up nothing.
		if location.ID == destinationID {
			continue
		}
		if !location.Mounted {
			problems = append(problems,
				fmt.Sprintf("%s was not connected and could not be backed up.", location.Name))
			continue
		}

		prefix := "data/" + location.ID
		files, issues, err := copyTree(location.MountPoint,
			filepath.Join(directory, "data", location.ID), prefix)
		if err != nil {
			problems = append(problems,
				fmt.Sprintf("%s could not be backed up: %v", location.Name, err))
			continue
		}
		included = true
		manifest.Files = append(manifest.Files, files...)
		problems = append(problems, issues...)
	}

	return included, problems
}

// exportDatabase writes core's SQLite database out consistently.
//
// VACUUM INTO, never a file copy. A live SQLite database has a write-ahead log
// beside it, and copying the main file while core is running produces something
// either stale or corrupt — usually stale, which is worse, because it restores
// successfully and is quietly missing the last week.
func (s *BackupServices) exportDatabase(ctx context.Context, directory string) ([]BackupFile, string, error) {
	if _, err := os.Stat(s.databasePath); os.IsNotExist(err) {
		// A machine with no database yet is a machine with nothing to lose.
		return nil, "", nil
	}

	target := filepath.Join(directory, "system", "homebase.db")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return nil, "", internalError("creating " + filepath.Dir(target) + ": " + err.Error())
	}
	os.Remove(target) // VACUUM INTO refuses to overwrite.

	// sqlite3 rather than a Go driver: hostd has no third-party dependencies
	// (ADR-0002), and this is a fixed argument vector with no caller-supplied
	// content — the paths are hostd's own.
	binary, err := exec.LookPath("sqlite3")
	if err != nil {
		return nil, "", &Error{
			Code:        "backup.no_sqlite",
			Message:     "Homebase cannot back up its own settings on this server.",
			Detail:      "the sqlite3 command is not installed",
			Recoverable: false,
			Recovery:    "Install the sqlite3 package and try again.",
			Status:      500,
		}
	}

	cmd := exec.CommandContext(ctx, binary, s.databasePath,
		fmt.Sprintf("VACUUM INTO '%s'", strings.ReplaceAll(target, "'", "''")))
	cmd.Env = withoutSystemdVariables(os.Environ())

	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, "", &Error{
			Code:        "backup.database_export_failed",
			Message:     "Homebase could not save a copy of its own settings.",
			Detail:      strings.TrimSpace(string(output)) + " (" + err.Error() + ")",
			Recoverable: true,
			Recovery:    "Try again. If it keeps failing, check the server has free space.",
			Status:      500,
		}
	}

	info, err := os.Stat(target)
	if err != nil {
		return nil, "", internalError("the exported database is missing: " + err.Error())
	}
	sum, err := checksum(target)
	if err != nil {
		return nil, "", internalError("checksumming the exported database: " + err.Error())
	}

	return []BackupFile{{
		Path:       "system/homebase.db",
		Bytes:      uint64(info.Size()),
		SHA256:     sum,
		Mode:       0o600,
		ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
	}}, "", nil
}

// --- Verifying ---------------------------------------------------------------------

func (s *BackupServices) verify(_ context.Context, params BackupRef) (any, error) {
	directory, err := s.backupDirectory(params)
	if err != nil {
		return nil, err
	}

	result, err := verifyBackup(directory)
	if err != nil {
		return nil, &Error{
			Code:        "backup.unreadable",
			Message:     "Homebase could not read that backup.",
			Detail:      err.Error(),
			Recoverable: false,
			Recovery:    "The backup may be incomplete. Try another one.",
			Status:      409,
		}
	}
	return result, nil
}

// --- Previewing ----------------------------------------------------------------------

// RestorePreview says what restoring would do, before anything is touched.
type RestorePreview struct {
	ID       string `json:"id"`
	Location string `json:"location"`

	CreatedAt string `json:"created_at"`
	Hostname  string `json:"hostname"`
	Kind      string `json:"kind"`

	// Applications the backup holds, split by whether this machine still has
	// them in its catalogue. One it cannot install is data restored with nothing
	// to read it.
	Applications          []string `json:"applications"`
	UnavailableApps       []string `json:"unavailable_applications,omitempty"`
	ApplicationsToInstall []string `json:"applications_to_install,omitempty"`

	FilesToWrite int    `json:"files_to_write"`
	BytesToWrite uint64 `json:"bytes_to_write"`

	// WouldOverwrite is what is on the machine now and would be replaced. This
	// is the number somebody actually needs before agreeing.
	WouldOverwrite int `json:"would_overwrite"`

	// Verified reports whether the backup's checksums still match. A restore
	// from an unverified backup is a guess.
	Verified        bool     `json:"verified"`
	IntegrityIssues []string `json:"integrity_issues,omitempty"`

	Notes   []string `json:"notes,omitempty"`
	Message string   `json:"message"`
}

func (s *BackupServices) preview(_ context.Context, params BackupRef) (any, error) {
	directory, err := s.backupDirectory(params)
	if err != nil {
		return nil, err
	}

	manifest, err := readManifest(directory)
	if err != nil {
		return nil, &Error{
			Code:        "backup.unreadable",
			Message:     "Homebase could not read that backup.",
			Detail:      err.Error(),
			Recoverable: false,
			Status:      409,
		}
	}

	preview := RestorePreview{
		ID:           manifest.ID,
		Location:     params.Location,
		CreatedAt:    manifest.CreatedAt,
		Hostname:     manifest.Hostname,
		Kind:         manifest.Kind,
		Applications: manifest.Applications,
		Notes:        manifest.Notes,
		FilesToWrite: len(manifest.Files),
		BytesToWrite: manifest.TotalBytes,
	}

	// Which applications this machine could actually bring back.
	for _, id := range manifest.Applications {
		if _, known := s.apps.Catalogue.Lookup(id); known {
			preview.ApplicationsToInstall = append(preview.ApplicationsToInstall, id)
		} else {
			preview.UnavailableApps = append(preview.UnavailableApps, id)
		}
	}

	// How much of this would land on top of something.
	for _, file := range manifest.Files {
		target, err := s.restoreTarget(file.Path)
		if err != nil {
			continue
		}
		if _, err := os.Stat(target); err == nil {
			preview.WouldOverwrite++
		}
	}

	// Checked before it is offered, not after it has been agreed to.
	if result, err := verifyBackup(directory); err == nil {
		preview.Verified = result.Valid
		preview.IntegrityIssues = append(result.Missing, result.Corrupt...)
	}

	preview.Message = describePreview(preview)
	return preview, nil
}

func describePreview(preview RestorePreview) string {
	var parts []string

	if !preview.Verified {
		parts = append(parts,
			fmt.Sprintf("This backup is damaged — %d files are missing or changed. "+
				"Restoring it will not bring everything back.", len(preview.IntegrityIssues)))
	}

	parts = append(parts, fmt.Sprintf("Taken on %s from %s.",
		humanDate(preview.CreatedAt), preview.Hostname))

	if preview.WouldOverwrite > 0 {
		parts = append(parts, fmt.Sprintf(
			"%s on this server would be replaced. Nothing else is deleted: "+
				"anything added since this backup stays.",
			count(preview.WouldOverwrite, "file")))
	} else {
		parts = append(parts, "Nothing on this server would be replaced.")
	}

	if len(preview.UnavailableApps) > 0 {
		parts = append(parts, fmt.Sprintf(
			"%s in this backup %s not available on this server, so their files will be "+
				"restored but they cannot be reinstalled: %s.",
			count(len(preview.UnavailableApps), "application"),
			isAre(len(preview.UnavailableApps)),
			strings.Join(preview.UnavailableApps, ", ")))
	}

	return strings.Join(parts, " ")
}

// count renders a number and its noun, pluralised.
//
// Worth the eight lines: "1 files would be replaced" is the sort of thing that
// makes somebody trust the rest of a sentence less, and this sentence is asking
// them to agree to something irreversible.
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func humanDate(rfc3339 string) string {
	parsed, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return parsed.Local().Format("2 January 2006 at 15:04")
}

// --- Restoring -----------------------------------------------------------------------

type RestoreParams struct {
	Location string `json:"location"`
	ID       string `json:"id"`

	// Confirm must be the backup's id. Checked here as well as in core: this is
	// the operation that overwrites what somebody is trying to save.
	Confirm string `json:"confirm"`
}

type RestoreResult struct {
	ID       string `json:"id"`
	Restored int    `json:"restored"`
	Skipped  int    `json:"skipped"`

	// Applications that were in the backup and can be reinstalled. Restore puts
	// the files back; installing them is a separate, visible step, because a
	// restore that silently downloads two gigabytes is one nobody expected.
	ApplicationsToInstall []string `json:"applications_to_install,omitempty"`

	Problems []string `json:"problems,omitempty"`
	Message  string   `json:"message"`
}

func (s *BackupServices) restore(ctx context.Context, params RestoreParams) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if params.Confirm != params.ID {
		return nil, &Error{
			Code:        "backup.confirmation_required",
			Message:     "Please confirm which backup you want to restore.",
			Detail:      "confirm must be " + params.ID,
			Recoverable: true,
			Recovery: "Restoring replaces files on this server with the ones in the " +
				"backup. Confirm by naming it exactly.",
			Status: 428,
		}
	}

	directory, err := s.backupDirectory(BackupRef{Location: params.Location, ID: params.ID})
	if err != nil {
		return nil, err
	}

	manifest, err := readManifest(directory)
	if err != nil {
		return nil, &Error{
			Code:        "backup.unreadable",
			Message:     "Homebase could not read that backup.",
			Detail:      err.Error(),
			Recoverable: false,
			Status:      409,
		}
	}

	result := RestoreResult{ID: manifest.ID}

	for _, file := range manifest.Files {
		source := filepath.Join(directory, filepath.FromSlash(file.Path))

		target, err := s.restoreTarget(file.Path)
		if err != nil {
			// A manifest is untrusted input: it can be written on another
			// machine. One naming ../../etc/shadow is refused rather than
			// obeyed.
			result.Problems = append(result.Problems,
				fmt.Sprintf("%s was skipped: %v", file.Path, err))
			result.Skipped++
			continue
		}

		// Checked before it is written, not after. Restoring a corrupted file
		// over a good one is the worst outcome available here.
		if sum, err := checksum(source); err != nil || sum != file.SHA256 {
			result.Problems = append(result.Problems,
				fmt.Sprintf("%s is damaged in the backup and was not restored", file.Path))
			result.Skipped++
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			result.Problems = append(result.Problems,
				fmt.Sprintf("%s could not be restored: %v", file.Path, err))
			result.Skipped++
			continue
		}
		if _, _, err := copyFile(source, target, os.FileMode(file.Mode)); err != nil {
			result.Problems = append(result.Problems,
				fmt.Sprintf("%s could not be restored: %v", file.Path, err))
			result.Skipped++
			continue
		}
		result.Restored++
	}

	// Ownership, so core can read what has been put back. Skipped when not root,
	// which only happens in development.
	if os.Geteuid() == 0 {
		for _, root := range []string{s.apps.dataRoot, filepath.Dir(s.databasePath)} {
			_ = chownTree(root)
		}
	}

	for _, id := range manifest.Applications {
		if _, known := s.apps.Catalogue.Lookup(id); known {
			result.ApplicationsToInstall = append(result.ApplicationsToInstall, id)
		}
	}

	result.Message = describeRestore(result)
	return result, nil
}

func describeRestore(result RestoreResult) string {
	message := fmt.Sprintf("%d files were restored.", result.Restored)
	if result.Skipped > 0 {
		message += fmt.Sprintf(" %d could not be, and are listed below.", result.Skipped)
	}
	if len(result.ApplicationsToInstall) > 0 {
		message += fmt.Sprintf(" %d applications are ready to be installed again.",
			len(result.ApplicationsToInstall))
	}
	message += " Nothing that was already on this server has been deleted."
	return message
}

// restoreTarget maps a manifest path to where it goes on this machine.
//
// The mapping is a fixed table rather than anything derived from the manifest.
// A backup disk is untrusted input, and the set of places a restore can write is
// a decision that belongs in the code, not in a file somebody else wrote.
func (s *BackupServices) restoreTarget(manifestPath string) (string, error) {
	switch {
	case manifestPath == "system/homebase.db":
		return s.databasePath, nil

	case strings.HasPrefix(manifestPath, "system/etc/"):
		return safeBackupPath(s.configDir, strings.TrimPrefix(manifestPath, "system/etc/"))

	case strings.HasPrefix(manifestPath, "system/hostd/"):
		return safeBackupPath(s.stateDir, strings.TrimPrefix(manifestPath, "system/hostd/"))

	case strings.HasPrefix(manifestPath, "apps/"):
		return safeBackupPath(s.apps.dataRoot, strings.TrimPrefix(manifestPath, "apps/"))

	case strings.HasPrefix(manifestPath, "data/"):
		// User data goes back to the storage root, under the location id it came
		// from. If that disk is not present the files land in the directory that
		// would be its mount point — which is immutable while unmounted, so the
		// write fails rather than filling the system disk.
		return safeBackupPath(s.storage.root, strings.TrimPrefix(manifestPath, "data/"))
	}

	return "", fmt.Errorf("%s is not somewhere Homebase restores to", manifestPath)
}

// --- Deleting -------------------------------------------------------------------------

type DeleteBackupParams struct {
	Location string `json:"location"`
	ID       string `json:"id"`
	Confirm  string `json:"confirm"`
}

func (s *BackupServices) delete(_ context.Context, params DeleteBackupParams) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if params.Confirm != params.ID {
		return nil, &Error{
			Code:        "backup.confirmation_required",
			Message:     "Please confirm which backup you want to delete.",
			Detail:      "confirm must be " + params.ID,
			Recoverable: true,
			Recovery:    "Deleting a backup cannot be undone. Confirm by naming it exactly.",
			Status:      428,
		}
	}

	directory, err := s.backupDirectory(BackupRef{Location: params.Location, ID: params.ID})
	if err != nil {
		return nil, err
	}

	// RemoveAll, but only after backupDirectory has established that this is a
	// directory inside the backup folder of a managed location whose name
	// matches a backup id. Three checks before a recursive delete, because this
	// one runs on a disk holding somebody's only other copy.
	if err := os.RemoveAll(directory); err != nil {
		return nil, internalError("removing " + directory + ": " + err.Error())
	}

	return map[string]any{
		"id":      params.ID,
		"message": "That backup has been deleted. Your other backups are untouched.",
	}, nil
}

// --- Paths and checks --------------------------------------------------------------------

// requireUsableDestination refuses to write a backup somewhere useless.
func (s *BackupServices) requireUsableDestination(id string, location LocationState, found bool) error {
	if !found {
		return unknownLocation(id)
	}
	if !location.Mounted {
		return &Error{
			Code:        "backup.destination_not_connected",
			Message:     location.Name + " is not connected, so Homebase cannot back up to it.",
			Recoverable: true,
			Recovery:    "Plug the disk in and try again.",
			Status:      409,
		}
	}
	if location.ReadOnly {
		return &Error{
			Code:        "backup.destination_read_only",
			Message:     location.Name + " can only be read from, so nothing can be written to it.",
			Recoverable: true,
			Recovery:    "The disk may be write-protected, or may need repairing on another computer.",
			Status:      409,
		}
	}

	// A backup onto the disk holding the data protects against exactly one
	// thing — somebody deleting a file by accident — and against nothing else.
	// Disks fail whole. Presenting that as a backup is worse than having none,
	// because the user believes they are covered. See ADR-0014.
	//
	// The check is on assignment rather than on contents: a disk an application
	// keeps its files on is a disk whose loss is what the backup is for.
	if users := s.applicationsUsing(id); len(users) > 0 {
		return &Error{
			Code: "backup.destination_holds_data",
			Message: location.Name + " is where " + strings.Join(users, " and ") +
				" keeps files, so a backup on it would be lost with it.",
			Detail:      "the destination is assigned to " + strings.Join(users, ", "),
			Recoverable: true,
			Recovery: "Choose a different disk. A backup on the same disk as the " +
				"data protects against deleting a file by mistake, and against " +
				"nothing else — disks fail as a whole.",
			Status: 409,
		}
	}

	return nil
}

// applicationsUsing reports which applications keep files on a location.
func (s *BackupServices) applicationsUsing(locationID string) []string {
	var users []string
	for _, manifest := range s.apps.Catalogue.All() {
		for slot, assignment := range s.apps.storage.Assignments(manifest.ID) {
			_ = slot
			if assignment.Location == locationID {
				users = append(users, manifest.Name)
				break
			}
		}
	}
	sort.Strings(users)
	return users
}

// backupRoot is the folder holding backups on a location.
func (s *BackupServices) backupRoot(locationID string) (string, error) {
	location, found := s.storage.LocationByID(locationID)
	if !found {
		return "", unknownLocation(locationID)
	}
	if location.MountPoint == "" {
		return "", diskNotConnected(location.Location)
	}
	return filepath.Join(location.MountPoint, backupDirName), nil
}

// backupDirectory resolves one backup, refusing an id that is not one.
func (s *BackupServices) backupDirectory(params BackupRef) (string, error) {
	root, err := s.backupRoot(params.Location)
	if err != nil {
		return "", err
	}

	// The id becomes a path component, so it is checked rather than trusted —
	// even though core generates it, because a caller can send anything.
	if !validBackupID(params.ID) {
		return "", &Error{
			Code:        "backup.unknown",
			Message:     "That is not a backup Homebase recognises.",
			Detail:      params.ID,
			Recoverable: false,
			Status:      404,
		}
	}

	directory := filepath.Join(root, params.ID)
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		return "", &Error{
			Code:        "backup.unknown",
			Message:     "That backup is not on this disk.",
			Detail:      params.ID,
			Recoverable: false,
			Status:      404,
		}
	}
	return directory, nil
}

// backupID is a sortable, readable identifier: the date, then randomness.
//
// Sortable because listing newest-first should not require parsing the manifest,
// and readable because somebody looking at four folders on a disk should be able
// to tell which is which without opening any of them.
func backupID() string {
	buffer := make([]byte, 4)
	if _, err := rand.Read(buffer); err != nil {
		return time.Now().UTC().Format("2006-01-02-150405")
	}
	return time.Now().UTC().Format("2006-01-02-150405") + "-" + hex.EncodeToString(buffer)
}

func validBackupID(id string) bool {
	if len(id) < 10 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return name
}

// chownTree gives the service account ownership of a restored tree.
func chownTree(root string) error {
	return filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		return chownToService(path)
	})
}

// --- Scheduling ---------------------------------------------------------------

func (s *BackupServices) getSchedule(ctx context.Context, _ struct{}) (any, error) {
	return readBackupSchedule(ctx), nil
}

type setScheduleRequest struct {
	// Every is "daily", "weekly" or "off".
	Every string `json:"every"`

	// Location is where backups go. Required unless turning the schedule off.
	Location string `json:"location,omitempty"`
}

func (s *BackupServices) setSchedule(ctx context.Context, req setScheduleRequest) (any, error) {
	every := strings.ToLower(strings.TrimSpace(req.Every))
	if _, known := schedules[every]; !known && every != "off" {
		return nil, &Error{
			Code:        "backup.unknown_schedule",
			Message:     "That is not a schedule Homebase can keep to.",
			Detail:      fmt.Sprintf("asked for %q; the choices are daily, weekly and off", req.Every),
			Recoverable: true,
			Recovery:    "Choose daily, weekly, or off.",
			Status:      400,
		}
	}

	location := strings.TrimSpace(req.Location)
	if every != "off" {
		// The destination is checked now rather than at three in the morning.
		// A schedule pointing at a disk that cannot hold a backup is a promise
		// that fails in the dark, weeks later, to somebody who was told it was
		// working.
		state, found := s.storage.LocationByID(location)
		if err := s.requireUsableDestination(location, state, found); err != nil {
			return nil, err
		}
	}

	if err := writeBackupSchedule(ctx, every, location); err != nil {
		return nil, &Error{
			Code:        "backup.schedule_failed",
			Message:     "The backup schedule could not be set up.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Try again. If it keeps failing, check the system logs.",
			Status:      500,
		}
	}

	return readBackupSchedule(ctx), nil
}
