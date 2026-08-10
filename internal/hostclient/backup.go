package hostclient

import "context"

// Backup and restore.
//
// core names a backup by its id and a destination by its location id, never by
// a path — the same rule as storage (ADR-0013). There is no method here that
// takes a directory.

// BackupSummary is one backup on a disk.
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
	// that was interrupted. Reported rather than hidden: a folder that looks
	// like a backup and is not is exactly what nobody must rely on.
	Complete bool   `json:"complete"`
	Problem  string `json:"problem,omitempty"`
}

// VerificationResult is what checking a backup found.
type VerificationResult struct {
	ID           string   `json:"id"`
	Valid        bool     `json:"valid"`
	FilesChecked int      `json:"files_checked"`
	Missing      []string `json:"missing,omitempty"`
	Corrupt      []string `json:"corrupt,omitempty"`
	Unexpected   []string `json:"unexpected,omitempty"`
	CheckedAt    string   `json:"checked_at"`
	Message      string   `json:"message"`
}

// RestorePreview says what restoring would change, before anything is touched.
type RestorePreview struct {
	ID       string `json:"id"`
	Location string `json:"location"`

	CreatedAt string `json:"created_at"`
	Hostname  string `json:"hostname"`
	Kind      string `json:"kind"`

	Applications          []string `json:"applications"`
	UnavailableApps       []string `json:"unavailable_applications,omitempty"`
	ApplicationsToInstall []string `json:"applications_to_install,omitempty"`

	FilesToWrite int    `json:"files_to_write"`
	BytesToWrite uint64 `json:"bytes_to_write"`

	// WouldOverwrite is what is on the machine now and would be replaced. The
	// number somebody actually needs before agreeing.
	WouldOverwrite int `json:"would_overwrite"`

	Verified        bool     `json:"verified"`
	IntegrityIssues []string `json:"integrity_issues,omitempty"`

	Notes   []string `json:"notes,omitempty"`
	Message string   `json:"message"`
}

// RestoreResult is what a restore did.
type RestoreResult struct {
	ID                    string   `json:"id"`
	Restored              int      `json:"restored"`
	Skipped               int      `json:"skipped"`
	ApplicationsToInstall []string `json:"applications_to_install,omitempty"`
	Problems              []string `json:"problems,omitempty"`
	Message               string   `json:"message"`
}

// CreateBackupResult is what a backup produced.
type CreateBackupResult struct {
	ID         string   `json:"id"`
	Location   string   `json:"location"`
	Kind       string   `json:"kind"`
	Files      int      `json:"files"`
	TotalBytes uint64   `json:"total_bytes"`
	Problems   []string `json:"problems,omitempty"`
	Message    string   `json:"message"`
}

func (c *Client) Backups(ctx context.Context, location string) ([]BackupSummary, error) {
	var out struct {
		Backups []BackupSummary `json:"backups"`
	}
	if err := c.Call(ctx, "backup.list", backupRef{Location: location}, false, &out); err != nil {
		return nil, err
	}
	return out.Backups, nil
}

// CreateBackup copies this server onto a disk.
//
// includeData separates a configuration backup, which takes seconds, from a full
// one, which can take hours. Somebody wanting the first should not be made to
// wait for the second.
func (c *Client) CreateBackup(ctx context.Context, location string, includeData bool) (*CreateBackupResult, error) {
	params := struct {
		Location    string `json:"location"`
		IncludeData bool   `json:"include_data"`
	}{Location: location, IncludeData: includeData}

	var result CreateBackupResult
	if err := c.Call(ctx, "backup.create", params, false, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) VerifyBackup(ctx context.Context, location, id string) (*VerificationResult, error) {
	var result VerificationResult
	if err := c.Call(ctx, "backup.verify", backupRef{Location: location, ID: id}, false, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// PreviewRestore reports what would change. It changes nothing itself, which is
// what makes it worth showing somebody before they agree.
func (c *Client) PreviewRestore(ctx context.Context, location, id string) (*RestorePreview, error) {
	var preview RestorePreview
	if err := c.Call(ctx, "backup.preview", backupRef{Location: location, ID: id}, false, &preview); err != nil {
		return nil, err
	}
	return &preview, nil
}

// RestoreBackup puts a backup back. confirm must be the backup's id; hostd
// checks it again.
func (c *Client) RestoreBackup(ctx context.Context, location, id, confirm string) (*RestoreResult, error) {
	params := struct {
		Location string `json:"location"`
		ID       string `json:"id"`
		Confirm  string `json:"confirm"`
	}{Location: location, ID: id, Confirm: confirm}

	var result RestoreResult
	if err := c.Call(ctx, "backup.restore", params, true, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) DeleteBackup(ctx context.Context, location, id, confirm string) error {
	params := struct {
		Location string `json:"location"`
		ID       string `json:"id"`
		Confirm  string `json:"confirm"`
	}{Location: location, ID: id, Confirm: confirm}
	return c.Call(ctx, "backup.delete", params, true, nil)
}

type backupRef struct {
	Location string `json:"location"`
	ID       string `json:"id,omitempty"`
}
