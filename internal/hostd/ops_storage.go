package hostd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Storage operations.
//
// Every one takes a filesystem UUID or a location id — never a device path and
// never a mount point. hostd resolves the identifier to a device itself, so
// nothing core sends can name a place on the filesystem. See ADR-0013.
//
// This is the domain where a mistake destroys data that Homebase did not create
// and cannot replace, so two rules are absolute:
//
//   Homebase never selects a disk on the user's behalf, in any operation, for
//   any reason — including when there is only one candidate.
//
//   Nothing is erased without the caller naming what is being erased.

const (
	// locationIDPattern is what a location id may be. It becomes a directory
	// name under the storage root and a systemd unit name.
	locationIDPattern = `^[a-z][a-z0-9-]{0,30}[a-z0-9]$`

	// formatTimeout bounds mkfs. Making a filesystem on a large slow USB disk
	// takes minutes, not seconds.
	formatTimeout = 20 * time.Minute
)

var validLocationID = regexp.MustCompile(locationIDPattern)

// Location is a disk Homebase manages, as recorded on this machine.
//
// The UUID is the identity; everything else is either derived from it at read
// time or is a label for a person.
type Location struct {
	ID string `json:"id"`

	// Name is what the user calls it. Shown everywhere the id is not.
	Name string `json:"name"`

	// UUID identifies the filesystem. This is the only thing persisted that
	// points at a disk.
	UUID string `json:"uuid"`

	// Filesystem and Label are recorded when the location is added, so a
	// disconnected disk can still be described to the person looking for it.
	// Refreshed whenever it is seen.
	Filesystem string `json:"filesystem,omitempty"`
	Label      string `json:"label,omitempty"`

	AddedAt string `json:"added_at"`
}

// LocationState is a location plus what is true about it right now.
type LocationState struct {
	Location

	MountPoint string `json:"mount_point"`

	// Connected means the disk is present. Mounted means it is also usable.
	// Both are reported because a disk that is plugged in but failed to mount is
	// a different problem from one that is not plugged in.
	Connected bool `json:"connected"`
	Mounted   bool `json:"mounted"`
	ReadOnly  bool `json:"read_only"`

	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`

	// Device is where it currently appears. Reported for diagnostics only, and
	// deliberately never stored: it changes.
	Device string `json:"device,omitempty"`
}

// StorageServices is what the storage operations need from their environment.
type StorageServices struct {
	// root is where managed locations are mounted.
	root string

	// stateFile records the locations. hostd owns this: core must not be able to
	// rewrite which disk an application's data is on.
	stateFile string

	mu sync.Mutex
}

func NewStorageServices(root, stateDir string) *StorageServices {
	if root == "" {
		root = DefaultStorageRoot
	}
	if stateDir == "" {
		stateDir = DefaultStateDir
	}
	return &StorageServices{
		root:      filepath.Clean(root),
		stateFile: filepath.Join(stateDir, "locations.json"),
	}
}

// RegisterStorageOperations adds the storage domain to a registry.
func RegisterStorageOperations(r *Registry, services *StorageServices) {
	r.MustRegister(Operation{
		Name:    "storage.list_disks",
		Summary: "List the disks attached to this server and what is on them.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 20 * time.Second,
		Handler: Typed(services.listDisks),
	})

	r.MustRegister(Operation{
		Name:    "storage.list_locations",
		Summary: "List the storage locations Homebase manages, and whether each is connected.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 20 * time.Second,
		Handler: Typed(services.listLocations),
	})

	r.MustRegister(Operation{
		Name:    "storage.add_location",
		Summary: "Start managing a disk: mount it, and keep mounting it at every boot.",
		// Medium rather than low: it writes a unit file and mounts a filesystem.
		// Nothing is destroyed, and storage.remove_location undoes it.
		Risk:        RiskMedium,
		Permissions: []string{"storage.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     2 * time.Minute,
		Rollback:    "storage.remove_location",
		Handler:     Typed(services.addLocation),
	})

	r.MustRegister(Operation{
		Name:        "storage.remove_location",
		Summary:     "Stop managing a disk. Its contents are left alone.",
		Risk:        RiskMedium,
		Permissions: []string{"storage.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     2 * time.Minute,
		Handler:     Typed(services.removeLocation),
	})

	r.MustRegister(Operation{
		Name:        "storage.mount",
		Summary:     "Mount a managed location that is connected but not mounted.",
		Risk:        RiskLow,
		Permissions: []string{"storage.modify"},
		Confirm:     ConfirmNone,
		Timeout:     2 * time.Minute,
		Rollback:    "storage.unmount",
		Handler:     Typed(services.mount),
	})

	r.MustRegister(Operation{
		Name:        "storage.unmount",
		Summary:     "Unmount a managed location, so the disk can be unplugged safely.",
		Risk:        RiskMedium,
		Permissions: []string{"storage.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     2 * time.Minute,
		Rollback:    "storage.mount",
		Handler:     Typed(services.unmount),
	})

	r.MustRegister(Operation{
		Name:    "storage.format",
		Summary: "Erase a disk and put a fresh filesystem on it. Everything on it is lost.",
		// The second operation in Homebase that destroys data irreversibly, and
		// the first that can destroy data Homebase never created.
		Risk:        RiskCritical,
		Permissions: []string{"storage.modify"},
		Confirm:     ConfirmExplicit,
		Timeout:     formatTimeout,
		Rollback:    "",
		Handler:     Typed(services.format),
	})
}

// --- Reading ------------------------------------------------------------------

type DiskList struct {
	Disks []Disk `json:"disks"`
}

func (s *StorageServices) listDisks(_ context.Context, _ NoParams) (any, error) {
	disks, err := ListDisks()
	if err != nil {
		return nil, internalError("listing disks: " + err.Error())
	}
	return DiskList{Disks: disks}, nil
}

type LocationList struct {
	Locations []LocationState `json:"locations"`
}

func (s *StorageServices) listLocations(_ context.Context, _ NoParams) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	locations, err := s.load()
	if err != nil {
		return nil, err
	}

	states := make([]LocationState, 0, len(locations))
	for _, location := range locations {
		states = append(states, s.describe(location))
	}
	return LocationList{Locations: states}, nil
}

// describe reports what is currently true about a location.
func (s *StorageServices) describe(location Location) LocationState {
	state := LocationState{
		Location:   location,
		MountPoint: s.mountPointFor(location.ID),
	}

	if volume, found := FindVolume(location.UUID); found {
		state.Connected = true
		state.Device = volume.Device
		state.ReadOnly = volume.ReadOnly
		// Mounted where Homebase put it, specifically. A disk mounted somewhere
		// else by somebody else is connected but not managed-mounted.
		state.Mounted = volume.MountPoint == state.MountPoint
	}

	if state.Mounted {
		if total, available, err := diskUsage(state.MountPoint); err == nil {
			state.TotalBytes = total
			state.AvailableBytes = available
		}
	}

	return state
}

func (s *StorageServices) mountPointFor(id string) string {
	return filepath.Join(s.root, id)
}

// --- Adding and removing ------------------------------------------------------

type AddLocationParams struct {
	// UUID names the filesystem to manage. Not a device path: see ADR-0013.
	UUID string `json:"uuid"`

	// ID is what the location is called on this machine. It becomes a directory
	// name and part of a systemd unit name.
	ID string `json:"id"`

	// Name is for the person reading it.
	Name string `json:"name"`
}

func (s *StorageServices) addLocation(ctx context.Context, params AddLocationParams) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !validLocationID.MatchString(params.ID) {
		return nil, &Error{
			Code:        "storage.invalid_id",
			Message:     "That is not a name Homebase can use for a storage location.",
			Detail:      "must match " + locationIDPattern,
			Recoverable: true,
			Recovery:    "Use lowercase letters, numbers and hyphens.",
			Status:      400,
		}
	}

	volume, found := FindVolume(params.UUID)
	if !found {
		return nil, &Error{
			Code:        "storage.disk_not_found",
			Message:     "Homebase cannot find that disk.",
			Detail:      "no volume with UUID " + params.UUID,
			Recoverable: true,
			Recovery:    "Check the disk is connected, then try again.",
			Status:      404,
		}
	}

	// Refused rather than reformatted. Homebase does not erase anything the user
	// did not ask it to erase, and "this disk has no filesystem" is a thing to
	// report, not to fix silently.
	if !volume.Assignable() {
		return nil, unassignableVolume(volume)
	}

	locations, err := s.load()
	if err != nil {
		return nil, err
	}

	for _, existing := range locations {
		if existing.UUID == params.UUID {
			// Already managed. Converging rather than erroring: a caller
			// retrying after a lost connection has no way to know.
			return map[string]any{
				"id":      existing.ID,
				"uuid":    existing.UUID,
				"message": existing.Name + " is already set up.",
			}, nil
		}
		if existing.ID == params.ID {
			return nil, &Error{
				Code:        "storage.id_in_use",
				Message:     "Another disk is already using that name.",
				Detail:      "location " + params.ID + " exists",
				Recoverable: true,
				Recovery:    "Choose a different name.",
				Status:      409,
			}
		}
	}

	name := strings.TrimSpace(params.Name)
	if name == "" {
		name = volume.Label
	}
	if name == "" {
		name = params.ID
	}

	location := Location{
		ID:         params.ID,
		Name:       name,
		UUID:       volume.UUID,
		Filesystem: volume.Filesystem,
		Label:      volume.Label,
		AddedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	mountPoint := s.mountPointFor(location.ID)
	if err := s.prepareMountPoint(mountPoint); err != nil {
		return nil, err
	}

	if err := writeMountUnit(s.root, location.UUID, mountPoint, location.Filesystem,
		"Homebase storage: "+location.Name); err != nil {
		return nil, internalError("writing the mount unit: " + err.Error())
	}

	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return nil, err
	}
	if err := systemctl(ctx, "enable", "--now", mountUnitName(mountPoint)); err != nil {
		// Leave nothing half-configured behind.
		_ = removeMountUnit(s.root, mountPoint)
		_ = systemctl(ctx, "daemon-reload")
		return nil, &Error{
			Code:        "storage.mount_failed",
			Message:     "Homebase could not use " + name + ".",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Check the disk is working, then try again.",
			Status:      500,
		}
	}

	locations = append(locations, location)
	if err := s.save(locations); err != nil {
		return nil, err
	}

	return map[string]any{
		"id":          location.ID,
		"uuid":        location.UUID,
		"mount_point": mountPoint,
		"message":     name + " is set up and ready to use.",
	}, nil
}

type LocationRef struct {
	ID string `json:"id"`
}

func (s *StorageServices) removeLocation(ctx context.Context, params LocationRef) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	locations, err := s.load()
	if err != nil {
		return nil, err
	}

	location, index, found := findLocation(locations, params.ID)
	if !found {
		return nil, unknownLocation(params.ID)
	}

	mountPoint := s.mountPointFor(location.ID)
	unit := mountUnitName(mountPoint)

	// Order matters: disable before removing the file, or systemd is left with a
	// symlink pointing at a unit that no longer exists.
	_ = systemctl(ctx, "disable", "--now", unit)

	if err := removeMountUnit(s.root, mountPoint); err != nil {
		return nil, internalError("removing the mount unit: " + err.Error())
	}
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return nil, err
	}

	// The immutable flag has to come off first, or the directory cannot be
	// removed — not even by root, which is the whole point of it.
	_ = setImmutable(mountPoint, false)

	// The directory goes, but only if it is empty — which it is once unmounted.
	// Never RemoveAll: if the unmount somehow failed, that would delete the
	// contents of the user's disk.
	if err := os.Remove(mountPoint); err != nil && !os.IsNotExist(err) {
		if isNotEmpty(err) {
			return nil, &Error{
				Code:    "storage.still_mounted",
				Message: "Homebase could not finish removing " + location.Name + ".",
				Detail: mountPoint + " is not empty, which means it is probably " +
					"still mounted",
				Recoverable: true,
				Recovery:    "Try again in a moment.",
				Status:      409,
			}
		}
		return nil, internalError("removing " + mountPoint + ": " + err.Error())
	}

	locations = append(locations[:index], locations[index+1:]...)
	if err := s.save(locations); err != nil {
		return nil, err
	}

	return map[string]any{
		"id": location.ID,
		"message": location.Name + " is no longer managed by Homebase. " +
			"Nothing on the disk was changed.",
	}, nil
}

// --- Mounting -----------------------------------------------------------------

func (s *StorageServices) mount(ctx context.Context, params LocationRef) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	locations, err := s.load()
	if err != nil {
		return nil, err
	}
	location, _, found := findLocation(locations, params.ID)
	if !found {
		return nil, unknownLocation(params.ID)
	}

	if _, present := FindVolume(location.UUID); !present {
		return nil, diskNotConnected(location)
	}

	mountPoint := s.mountPointFor(location.ID)
	if err := s.prepareMountPoint(mountPoint); err != nil {
		return nil, err
	}
	if err := systemctl(ctx, "start", mountUnitName(mountPoint)); err != nil {
		return nil, &Error{
			Code:        "storage.mount_failed",
			Message:     "Homebase could not open " + location.Name + ".",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Check the disk is working. It may need repairing on another computer.",
			Status:      500,
		}
	}

	return map[string]any{"id": location.ID, "mounted": true}, nil
}

func (s *StorageServices) unmount(ctx context.Context, params LocationRef) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	locations, err := s.load()
	if err != nil {
		return nil, err
	}
	location, _, found := findLocation(locations, params.ID)
	if !found {
		return nil, unknownLocation(params.ID)
	}

	mountPoint := s.mountPointFor(location.ID)
	if err := systemctl(ctx, "stop", mountUnitName(mountPoint)); err != nil {
		return nil, &Error{
			Code:        "storage.unmount_failed",
			Message:     "Homebase could not safely disconnect " + location.Name + ".",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery: "Something is still using it. Stop any application that uses " +
				"this disk, then try again.",
			Status: 409,
		}
	}

	// Back to inert, both ways. See prepareMountPoint.
	_ = os.Chmod(mountPoint, 0o555)
	_ = setImmutable(mountPoint, true)

	return map[string]any{
		"id":      location.ID,
		"mounted": false,
		"message": location.Name + " can now be unplugged safely.",
	}, nil
}

// --- Formatting ---------------------------------------------------------------

type FormatParams struct {
	// UUID names the filesystem to erase. Where a disk has no filesystem there
	// is nothing to name, so Device may be given instead — and is checked
	// against a disk that is genuinely blank.
	UUID   string `json:"uuid,omitempty"`
	Device string `json:"device,omitempty"`

	// Label is what the new filesystem is called.
	Label string `json:"label,omitempty"`

	// Confirm must repeat the UUID, or the device path where there is no UUID.
	// hostd checks it again even though core already did: this is the operation
	// that destroys data Homebase did not create.
	Confirm string `json:"confirm"`
}

func (s *StorageServices) format(ctx context.Context, params FormatParams) (any, error) {
	target, err := s.resolveFormatTarget(params)
	if err != nil {
		return nil, err
	}

	expected := params.UUID
	if expected == "" {
		expected = params.Device
	}
	if params.Confirm != expected {
		return nil, &Error{
			Code:        "storage.confirmation_required",
			Message:     "Please confirm which disk you want to erase.",
			Detail:      "confirm must be " + expected,
			Recoverable: true,
			Recovery: "Everything on that disk will be permanently deleted. " +
				"Confirm by naming it exactly.",
			Status: 428,
		}
	}

	label := strings.TrimSpace(params.Label)
	if label == "" {
		label = "Homebase"
	}
	if !validFilesystemLabel(label) {
		return nil, &Error{
			Code:        "storage.invalid_label",
			Message:     "That name cannot be used for a disk.",
			Detail:      "up to 16 characters: letters, numbers, spaces, hyphens and underscores",
			Recoverable: true,
			Recovery:    "Choose a shorter, simpler name.",
			Status:      400,
		}
	}

	// A fixed argument vector. The only caller-influenced values are the label,
	// checked above against a strict pattern, and the device path, which hostd
	// resolved itself from /sys rather than accepting from core.
	//
	// ext4 rather than a choice: it is the only filesystem where Linux ownership
	// and permissions work properly, which application data needs.
	cmd := exec.CommandContext(ctx, "/usr/sbin/mkfs.ext4",
		"-F",
		"-L", label,
		// No lazy initialisation: the filesystem is fully ready when this
		// returns, rather than doing hours of background work on a USB disk that
		// the user may unplug in the meantime.
		"-E", "lazy_itable_init=0,lazy_journal_init=0",
		target.Path,
	)
	cmd.Env = withoutSystemdVariables(os.Environ())

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &Error{
			Code:        "storage.format_failed",
			Message:     "Homebase could not prepare that disk.",
			Detail:      strings.TrimSpace(string(output)) + " (" + err.Error() + ")",
			Recoverable: true,
			Recovery:    "The disk may be faulty or write-protected.",
			Status:      500,
		}
	}

	// Re-read, so the caller gets the new UUID rather than the old one. A caller
	// that went on to add a location using the UUID it sent would be naming a
	// filesystem that no longer exists.
	settle(ctx)
	fresh, _ := detectFilesystem(target.Path)

	var newUUID string
	for _, disk := range mustListDisks() {
		for _, volume := range disk.Volumes {
			if volume.Path == target.Path {
				newUUID = volume.UUID
			}
		}
	}

	return map[string]any{
		"device":     target.Path,
		"uuid":       newUUID,
		"filesystem": fresh,
		"label":      label,
		"message":    "The disk is ready to use.",
	}, nil
}

// resolveFormatTarget turns the parameters into a device, refusing everything
// that must not be erased.
func (s *StorageServices) resolveFormatTarget(params FormatParams) (Volume, error) {
	disks, err := ListDisks()
	if err != nil {
		return Volume{}, internalError("listing disks: " + err.Error())
	}

	var target Volume
	var owner Disk
	var found bool

	for _, disk := range disks {
		for _, volume := range disk.Volumes {
			switch {
			case params.UUID != "" && volume.UUID == params.UUID:
			case params.UUID == "" && params.Device != "" && volume.Path == params.Device:
			default:
				continue
			}
			target, owner, found = volume, disk, true
		}
	}

	if !found {
		return Volume{}, &Error{
			Code:        "storage.disk_not_found",
			Message:     "Homebase cannot find that disk.",
			Recoverable: true,
			Recovery:    "Check the disk is connected, then try again.",
			Status:      404,
		}
	}

	// The system disk is never formattable, whatever was asked and however it
	// was confirmed. There is no flag for this.
	if owner.System {
		return Volume{}, &Error{
			Code:        "storage.refused_system_disk",
			Message:     "That disk holds the server itself and cannot be erased.",
			Detail:      owner.Path + " carries the running system",
			Recoverable: false,
			Status:      409,
		}
	}

	// Nor is anything currently mounted. Erasing a filesystem out from under a
	// running mount corrupts whatever was writing to it.
	if target.MountPoint != "" {
		return Volume{}, &Error{
			Code:        "storage.in_use",
			Message:     "That disk is in use and cannot be erased.",
			Detail:      "mounted at " + target.MountPoint,
			Recoverable: true,
			Recovery:    "Disconnect it in Homebase first.",
			Status:      409,
		}
	}

	// A disk hostd could not read is not a disk hostd will erase. Unreadable and
	// blank are different answers, and only one of them is safe to act on.
	if target.Unreadable {
		return Volume{}, &Error{
			Code:        "storage.unreadable",
			Message:     "Homebase could not read that disk, so it will not erase it.",
			Detail:      target.Path + " could not be read",
			Recoverable: true,
			Recovery: "The disk may be faulty or disconnected. Homebase will not " +
				"erase a disk it cannot see the contents of.",
			Status: 409,
		}
	}

	return target, nil
}

// --- Assignments --------------------------------------------------------------
//
// Which managed location holds an application's user-selected storage. Recorded
// by hostd, in hostd's own state: core must not be able to change which disk an
// application's files are on.

// Assignment binds one of an application's declared storage slots to a location.
type Assignment struct {
	App       string `json:"app"`
	StorageID string `json:"storage_id"`
	Location  string `json:"location"`
	// Subdirectory is where under the location the data lives. Applications get
	// a directory of their own rather than the whole disk, so one disk can hold
	// several applications' data without them seeing each other's.
	Subdirectory string `json:"subdirectory"`
	AssignedAt   string `json:"assigned_at"`
}

// Assignments returns everything assigned for one application.
func (s *StorageServices) Assignments(app string) map[string]Assignment {
	s.mu.Lock()
	defer s.mu.Unlock()

	assignments, err := s.loadAssignments()
	if err != nil {
		return nil
	}

	result := map[string]Assignment{}
	for _, assignment := range assignments {
		if assignment.App == app {
			result[assignment.StorageID] = assignment
		}
	}
	return result
}

// Assign records that a location holds one of an application's storage slots.
func (s *StorageServices) Assign(app, storageID, locationID, subdirectory string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	locations, err := s.load()
	if err != nil {
		return err
	}
	location, _, found := findLocation(locations, locationID)
	if !found {
		return unknownLocation(locationID)
	}

	// Assigning a disk that is not connected would let a user set something up
	// that cannot work, and find out later. Refused now, while they are looking
	// at it.
	state := s.describe(location)
	if !state.Mounted {
		return diskNotConnected(location)
	}

	assignments, err := s.loadAssignments()
	if err != nil {
		return err
	}

	next := Assignment{
		App:          app,
		StorageID:    storageID,
		Location:     locationID,
		Subdirectory: subdirectory,
		AssignedAt:   time.Now().UTC().Format(time.RFC3339),
	}

	replaced := false
	for i, existing := range assignments {
		if existing.App == app && existing.StorageID == storageID {
			assignments[i] = next
			replaced = true
			break
		}
	}
	if !replaced {
		assignments = append(assignments, next)
	}

	return s.saveAssignments(assignments)
}

// Unassign forgets an application's assignments. Nothing on the disk is touched.
func (s *StorageServices) Unassign(app string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	assignments, err := s.loadAssignments()
	if err != nil {
		return err
	}

	kept := assignments[:0]
	for _, assignment := range assignments {
		if assignment.App != app {
			kept = append(kept, assignment)
		}
	}
	return s.saveAssignments(kept)
}

func (s *StorageServices) assignmentFile() string {
	return filepath.Join(filepath.Dir(s.stateFile), "assignments.json")
}

func (s *StorageServices) loadAssignments() ([]Assignment, error) {
	data, err := os.ReadFile(s.assignmentFile())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, internalError("reading assignments: " + err.Error())
	}

	var assignments []Assignment
	if err := json.Unmarshal(data, &assignments); err != nil {
		return nil, internalError("the storage assignments could not be read: " + err.Error())
	}
	return assignments, nil
}

func (s *StorageServices) saveAssignments(assignments []Assignment) error {
	if err := os.MkdirAll(filepath.Dir(s.stateFile), 0o700); err != nil {
		return internalError("creating the state directory: " + err.Error())
	}

	body, err := json.MarshalIndent(assignments, "", "  ")
	if err != nil {
		return internalError("encoding assignments: " + err.Error())
	}

	path := s.assignmentFile()
	temporary := path + ".new"
	if err := os.WriteFile(temporary, append(body, '\n'), 0o600); err != nil {
		return internalError("writing " + temporary + ": " + err.Error())
	}
	if err := os.Rename(temporary, path); err != nil {
		os.Remove(temporary)
		return internalError("saving assignments: " + err.Error())
	}
	return nil
}

// --- Resolving a location for an application ----------------------------------

// ResolveLocation returns the mount point of a managed location, if it is
// mounted right now.
//
// Used when starting an application with user-selected storage. Returning false
// is what makes the application refuse to start rather than run against an empty
// directory on the system disk — see ADR-0013.
func (s *StorageServices) ResolveLocation(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	locations, err := s.load()
	if err != nil {
		return "", false
	}
	location, _, found := findLocation(locations, id)
	if !found {
		return "", false
	}

	state := s.describe(location)
	if !state.Mounted {
		return "", false
	}
	return state.MountPoint, true
}

// Locations returns every managed location with its current state.
//
// Exported for the backup operations, which need to know which disks hold data
// worth copying — and, more importantly, which one is the destination, because a
// backup must never be written to a disk it is backing up.
func (s *StorageServices) Locations() ([]LocationState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	locations, err := s.load()
	if err != nil {
		return nil, err
	}

	states := make([]LocationState, 0, len(locations))
	for _, location := range locations {
		states = append(states, s.describe(location))
	}
	return states, nil
}

// LocationByID returns a managed location and its current state.
func (s *StorageServices) LocationByID(id string) (LocationState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	locations, err := s.load()
	if err != nil {
		return LocationState{}, false
	}
	location, _, found := findLocation(locations, id)
	if !found {
		return LocationState{}, false
	}
	return s.describe(location), true
}

// --- The mount point ----------------------------------------------------------

// prepareMountPoint creates the directory a location mounts on, and makes it
// inert while nothing is mounted there.
//
// This is the disconnected-disk protection. When a disk is unplugged the
// mountpoint reverts to an ordinary empty directory on the root filesystem, and
// anything still writing to it fills the system disk with files that vanish
// behind the disk the moment it is reconnected. The user sees an application
// that lost their data and a server out of space, and nothing anywhere reported
// an error.
//
// Both a mode of 0555 and the immutable flag, because the mode alone does not
// hold: root ignores it, and an application container frequently runs as root.
// The VM test found exactly that, by writing as root into what was supposed to
// be an unwritable directory and succeeding.
//
// Mounting over it is unaffected either way — a mount does not modify the
// directory it covers.
func (s *StorageServices) prepareMountPoint(mountPoint string) error {
	if err := underStorageRoot(s.root, mountPoint); err != nil {
		return internalError(err.Error())
	}

	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return internalError("creating " + s.root + ": " + err.Error())
	}
	// Cleared first: an immutable directory cannot be chmod'ed, and this runs
	// again on every mount.
	_ = setImmutable(mountPoint, false)

	if err := os.MkdirAll(mountPoint, 0o555); err != nil {
		return internalError("creating " + mountPoint + ": " + err.Error())
	}
	// MkdirAll leaves an existing directory's mode alone, so it is set
	// explicitly — a directory that was writable once must not stay writable.
	if err := os.Chmod(mountPoint, 0o555); err != nil {
		return internalError("securing " + mountPoint + ": " + err.Error())
	}

	// The part that holds against root. Not fatal if the filesystem does not
	// support it — but logged, because a protection that silently is not there
	// is worse than one known to be absent.
	if err := setImmutable(mountPoint, true); err != nil {
		slog.Warn("could not make a mount point immutable; a stray write while the "+
			"disk is absent would land on the system disk",
			"path", mountPoint, "error", err)
	}
	return nil
}

// --- State --------------------------------------------------------------------

// load reads the managed locations. hostd owns this file; core cannot write it.
func (s *StorageServices) load() ([]Location, error) {
	data, err := os.ReadFile(s.stateFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, internalError("reading " + s.stateFile + ": " + err.Error())
	}

	var locations []Location
	if err := json.Unmarshal(data, &locations); err != nil {
		// Refused rather than reset. This file records which disk an
		// application's data is on; silently starting again from empty would
		// unmount somebody's storage and look like Homebase forgot it.
		return nil, internalError(
			"the storage configuration in " + s.stateFile + " could not be read: " + err.Error())
	}

	sort.Slice(locations, func(i, j int) bool { return locations[i].ID < locations[j].ID })
	return locations, nil
}

func (s *StorageServices) save(locations []Location) error {
	if err := os.MkdirAll(filepath.Dir(s.stateFile), 0o700); err != nil {
		return internalError("creating the state directory: " + err.Error())
	}

	body, err := json.MarshalIndent(locations, "", "  ")
	if err != nil {
		return internalError("encoding the storage configuration: " + err.Error())
	}

	// Written and renamed, so a power cut cannot leave a half-written file that
	// load() would then refuse.
	temporary := s.stateFile + ".new"
	if err := os.WriteFile(temporary, append(body, '\n'), 0o600); err != nil {
		return internalError("writing " + temporary + ": " + err.Error())
	}
	if err := os.Rename(temporary, s.stateFile); err != nil {
		os.Remove(temporary)
		return internalError("saving the storage configuration: " + err.Error())
	}
	return nil
}

// --- Helpers ------------------------------------------------------------------

func findLocation(locations []Location, id string) (Location, int, bool) {
	for i, location := range locations {
		if location.ID == id {
			return location, i, true
		}
	}
	return Location{}, -1, false
}

func unknownLocation(id string) error {
	return &Error{
		Code:        "storage.unknown_location",
		Message:     "This server has no storage location by that name.",
		Detail:      id,
		Recoverable: false,
		Status:      404,
	}
}

func diskNotConnected(location Location) error {
	detail := "UUID " + location.UUID
	if location.Label != "" {
		detail = "the disk labelled " + location.Label + " (" + detail + ")"
	}
	return &Error{
		Code:        "storage.disk_not_connected",
		Message:     location.Name + " is not connected.",
		Detail:      detail,
		Recoverable: true,
		Recovery:    "Plug the disk back in. Homebase will pick it up on its own.",
		Status:      409,
	}
}

func unassignableVolume(volume Volume) error {
	switch {
	case volume.Unreadable:
		return &Error{
			Code:        "storage.unreadable",
			Message:     "Homebase could not read that disk.",
			Detail:      volume.Path + " could not be read",
			Recoverable: true,
			Recovery:    "The disk may be faulty. Try a different port or cable.",
			Status:      409,
		}
	case volume.Filesystem == "":
		return &Error{
			Code:        "storage.not_formatted",
			Message:     "That disk has nothing on it that Homebase can use.",
			Detail:      volume.Path + " has no filesystem",
			Recoverable: true,
			Recovery: "Homebase can prepare it for you, which erases everything " +
				"on it first.",
			Status: 409,
		}
	default:
		return &Error{
			Code:    "storage.no_identity",
			Message: "Homebase cannot reliably recognise that disk.",
			Detail: volume.Path + " has a " + volume.Filesystem +
				" filesystem with no unique identifier",
			Recoverable: true,
			Recovery: "Homebase can prepare it for you, which erases everything " +
				"on it first. Without an identifier it cannot tell this disk " +
				"apart from another one.",
			Status: 409,
		}
	}
}

// validFilesystemLabel keeps the label within what ext4 accepts and what is safe
// to pass as an argument.
func validFilesystemLabel(label string) bool {
	if label == "" || len(label) > 16 {
		return false
	}
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == ' ', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func isNotEmpty(err error) bool {
	return strings.Contains(err.Error(), "not empty") ||
		strings.Contains(err.Error(), "directory not empty")
}

func mustListDisks() []Disk {
	disks, err := ListDisks()
	if err != nil {
		return nil
	}
	return disks
}

// systemctl runs one systemd command with a fixed argument vector.
//
// Only unit names Homebase generated itself and fixed verbs reach this. Nothing
// core sends is passed through.
func systemctl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", args...)
	cmd.Env = withoutSystemdVariables(os.Environ())

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %s (%w)",
			strings.Join(args, " "), strings.TrimSpace(string(output)), err)
	}
	return nil
}

// settle waits for udev to catch up after the block device changed.
//
// Without it, a format is immediately followed by a read of /dev/disk/by-uuid
// that still shows the old UUID — and the caller records an identity that does
// not exist.
func settle(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "/usr/bin/udevadm", "settle", "--timeout=10")
	cmd.Env = withoutSystemdVariables(os.Environ())
	if err := cmd.Run(); err != nil {
		// Not fatal: udevadm may not be present. Fall back to waiting, because
		// the alternative is reading a stale UUID.
		time.Sleep(2 * time.Second)
	}
}
