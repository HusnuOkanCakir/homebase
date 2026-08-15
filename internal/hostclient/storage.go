package hostclient

import "context"

// Storage operations.
//
// Every one sends a filesystem UUID or a location id. There is deliberately no
// method here that takes a device path or a mount point — hostd resolves the
// identifier to a device itself, so nothing core sends can name a place on the
// filesystem. See ADR-0013.
//
// The one exception is FormatDisk, which accepts a device path for a disk that
// has no filesystem and therefore no UUID to name. hostd checks that path
// against a volume it discovered itself, and refuses anything mounted, anything
// on the system disk, and anything it could not read.

// Volume is a filesystem on a disk.
type Volume struct {
	Device string `json:"device"`
	Path   string `json:"path"`

	// UUID is the identity. Empty means this volume cannot be assigned to an
	// application, because there is no way to find it again reliably.
	UUID  string `json:"uuid,omitempty"`
	Label string `json:"label,omitempty"`

	Filesystem string `json:"filesystem,omitempty"`

	// Unreadable means Homebase could not read the volume. Not the same as an
	// empty Filesystem, which means it was read and found blank — the two must
	// never be conflated, because one of them is safe to erase and the other is
	// a disk nobody can see the contents of.
	Unreadable bool `json:"unreadable"`

	SizeBytes  uint64 `json:"size_bytes"`
	MountPoint string `json:"mount_point,omitempty"`
	ReadOnly   bool   `json:"read_only"`
}

// Disk is a whole block device.
type Disk struct {
	Device    string `json:"device"`
	Path      string `json:"path"`
	Model     string `json:"model,omitempty"`
	Vendor    string `json:"vendor,omitempty"`
	SizeBytes uint64 `json:"size_bytes"`
	Removable bool   `json:"removable"`
	Transport string `json:"transport,omitempty"`

	// System marks the disk holding the running system. Homebase will not offer
	// to erase it, whatever else is asked.
	System bool `json:"system"`

	Volumes []Volume `json:"volumes"`
}

// Location is a disk Homebase manages, with what is true about it now.
type Location struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	UUID       string `json:"uuid"`
	Filesystem string `json:"filesystem,omitempty"`
	Label      string `json:"label,omitempty"`
	AddedAt    string `json:"added_at"`

	MountPoint string `json:"mount_point"`

	// Connected means the disk is present; Mounted means it is also usable.
	// Reported separately because a disk that is plugged in but failed to mount
	// is a different problem from one that is not plugged in.
	Connected bool `json:"connected"`
	Mounted   bool `json:"mounted"`
	ReadOnly  bool `json:"read_only"`

	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	Device         string `json:"device,omitempty"`

	// Internal marks this server's own disk, which is always present and cannot
	// be detached. Running out of space on it is a worse event than running out
	// on an external disk, so the difference has to survive the trip to core.
	Internal bool `json:"internal,omitempty"`
}

func (c *Client) Disks(ctx context.Context) ([]Disk, error) {
	var out struct {
		Disks []Disk `json:"disks"`
	}
	if err := c.Call(ctx, "storage.list_disks", nil, false, &out); err != nil {
		return nil, err
	}
	return out.Disks, nil
}

func (c *Client) Locations(ctx context.Context) ([]Location, error) {
	var out struct {
		Locations []Location `json:"locations"`
	}
	if err := c.Call(ctx, "storage.list_locations", nil, false, &out); err != nil {
		return nil, err
	}
	return out.Locations, nil
}

// AddLocation starts managing a disk, naming it by filesystem UUID.
func (c *Client) AddLocation(ctx context.Context, uuid, id, name string) error {
	params := struct {
		UUID string `json:"uuid"`
		ID   string `json:"id"`
		Name string `json:"name"`
	}{UUID: uuid, ID: id, Name: name}
	return c.Call(ctx, "storage.add_location", params, true, nil)
}

// RemoveLocation stops managing a disk. Nothing on it is changed.
func (c *Client) RemoveLocation(ctx context.Context, id string) error {
	return c.Call(ctx, "storage.remove_location", locationRef{ID: id}, true, nil)
}

func (c *Client) MountLocation(ctx context.Context, id string) error {
	return c.Call(ctx, "storage.mount", locationRef{ID: id}, false, nil)
}

func (c *Client) UnmountLocation(ctx context.Context, id string) error {
	return c.Call(ctx, "storage.unmount", locationRef{ID: id}, true, nil)
}

// FormatDisk erases a disk and puts a fresh filesystem on it.
//
// confirm must repeat the UUID, or the device path where there is none. hostd
// checks it again: this is the operation that destroys data Homebase never
// created, and a confirmation enforced in one place is one refactor away from
// being enforced nowhere.
func (c *Client) FormatDisk(ctx context.Context, uuid, device, label, confirm string) (*FormatResult, error) {
	params := struct {
		UUID    string `json:"uuid,omitempty"`
		Device  string `json:"device,omitempty"`
		Label   string `json:"label,omitempty"`
		Confirm string `json:"confirm"`
	}{UUID: uuid, Device: device, Label: label, Confirm: confirm}

	var result FormatResult
	if err := c.Call(ctx, "storage.format", params, true, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// FormatResult carries the *new* identity. The old UUID no longer exists, so a
// caller that went on using it would be naming a filesystem that is gone.
type FormatResult struct {
	Device     string `json:"device"`
	UUID       string `json:"uuid"`
	Filesystem string `json:"filesystem"`
	Label      string `json:"label"`
	Message    string `json:"message"`
}

// --- An application's storage -------------------------------------------------

// AppStorageSlot is one of an application's declared storage locations.
type AppStorageSlot struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	MountPath   string `json:"mount_path"`
	ReadOnly    bool   `json:"read_only"`

	Location     string `json:"location,omitempty"`
	LocationName string `json:"location_name,omitempty"`

	// Ready means this slot can be used right now. Always true for storage
	// Homebase places itself; for user-selected storage it means a disk has been
	// chosen and is connected.
	Ready bool   `json:"ready"`
	Path  string `json:"path,omitempty"`
}

type AppStorage struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	Storage []AppStorageSlot `json:"storage"`

	// Ready is what decides whether the application can start at all.
	Ready bool `json:"ready"`
}

func (c *Client) AppStorage(ctx context.Context, id string) (*AppStorage, error) {
	var storage AppStorage
	if err := c.Call(ctx, "app.storage", appRef{ID: id}, false, &storage); err != nil {
		return nil, err
	}
	return &storage, nil
}

// AssignStorage chooses which disk holds one of an application's storage slots.
func (c *Client) AssignStorage(ctx context.Context, app, storageID, location string) error {
	params := struct {
		ID        string `json:"id"`
		StorageID string `json:"storage_id"`
		Location  string `json:"location"`
	}{ID: app, StorageID: storageID, Location: location}
	return c.Call(ctx, "app.assign_storage", params, true, nil)
}

type locationRef struct {
	ID string `json:"id"`
}
