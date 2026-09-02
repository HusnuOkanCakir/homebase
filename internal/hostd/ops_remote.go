package hostd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Operations for folders on other computers. See remote.go for what they are
// and why they are not storage locations.

// remoteHostPattern is what may be typed as the computer.
//
// A hostname or an IPv4 address, and nothing else. This string becomes part of
// a `What=//host/share` line in a systemd unit, so the characters that would
// end a line, start a new directive, or add a mount option are refused here
// rather than escaped — there is no escaping in unit-file syntax to get right.
const remoteHostPattern = `^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`

// remoteSharePattern is what may be typed as the folder's shared name.
//
// Windows allows spaces in a share name and this does not, for the same reason:
// it reaches a unit file, where a space separates arguments. Somebody whose
// share is called "My Documents" is told to use a name without one, which is a
// better outcome than a mount that fails with a message about systemd.
const remoteSharePattern = `^[A-Za-z0-9][A-Za-z0-9._$-]{0,62}$`

var (
	validRemoteHost  = regexp.MustCompile(remoteHostPattern)
	validRemoteShare = regexp.MustCompile(remoteSharePattern)
)

// RemoteServices holds the folders on other computers.
type RemoteServices struct {
	storage   *StorageServices
	stateFile string
}

func NewRemoteServices(storage *StorageServices, stateDir string) *RemoteServices {
	if stateDir == "" {
		stateDir = DefaultStateDir
	}
	return &RemoteServices{
		storage:   storage,
		stateFile: filepath.Join(stateDir, "remote-folders.json"),
	}
}

func RegisterRemoteOperations(r *Registry, services *RemoteServices) {
	r.MustRegister(Operation{
		Name:    "remote.status",
		Summary: "List the folders on other computers this server can open.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 30 * time.Second,
		Handler: Typed(services.statusOp),
	})

	r.MustRegister(Operation{
		Name: "remote.connect",
		Summary: "Open a folder that another computer on this network is " +
			"sharing, and offer it here.",
		// High. It hands this server a credential for somebody else's computer
		// and puts the contents of a disk in front of the household. Nothing is
		// destroyed, and that is not the measure.
		Risk:        RiskHigh,
		Permissions: []string{"storage.modify"},
		Confirm:     ConfirmRequired,
		// Installing cifs-utils on a domestic connection.
		Timeout:  10 * time.Minute,
		Rollback: "remote.disconnect, with the same name",
		// The password reaches mount.cifs through a root-only file and is never
		// returned by any operation. Declared here so the audit log records that
		// a folder was connected and never what the password was.
		Secret:  []string{"password"},
		Handler: Typed(services.connect),
	})

	r.MustRegister(Operation{
		Name:    "remote.disconnect",
		Summary: "Stop offering a folder from another computer. Nothing on it is touched.",
		// Medium: it takes something away, which is recoverable by connecting it
		// again, and it deliberately cannot alter the other computer.
		Risk:        RiskMedium,
		Permissions: []string{"storage.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     2 * time.Minute,
		Rollback:    "remote.connect, with the same details",
		Handler:     Typed(services.disconnect),
	})

	r.MustRegister(Operation{
		Name: "remote.reconnect",
		Summary: "Try a folder on another computer again, after that computer " +
			"has been woken up.",
		// Low: it retries something already agreed to, with credentials already
		// held. The common case is a laptop that went to sleep.
		Risk:        RiskLow,
		Permissions: []string{"storage.modify"},
		Confirm:     ConfirmNone,
		Timeout:     2 * time.Minute,
		Handler:     Typed(services.reconnect),
	})
}

// --- State ------------------------------------------------------------------------

func (s *RemoteServices) load() ([]RemoteFolder, error) {
	raw, err := os.ReadFile(s.stateFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, internalError("reading " + s.stateFile + ": " + err.Error())
	}
	var folders []RemoteFolder
	if err := json.Unmarshal(raw, &folders); err != nil {
		return nil, internalError("reading " + s.stateFile + ": " + err.Error())
	}
	return folders, nil
}

func (s *RemoteServices) save(folders []RemoteFolder) error {
	if folders == nil {
		folders = []RemoteFolder{}
	}
	encoded, err := json.MarshalIndent(folders, "", "  ")
	if err != nil {
		return internalError("encoding the remote folders: " + err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(s.stateFile), 0o750); err != nil {
		return internalError("creating " + filepath.Dir(s.stateFile) + ": " + err.Error())
	}
	return writeRootFile(s.stateFile, string(encoded)+"\n", 0o640)
}

func findRemoteFolder(folders []RemoteFolder, name string) (RemoteFolder, int, bool) {
	for i, folder := range folders {
		if folder.Name == name {
			return folder, i, true
		}
	}
	return RemoteFolder{}, -1, false
}

// --- Operations -------------------------------------------------------------------

func (s *RemoteServices) statusOp(_ context.Context, _ NoParams) (any, error) {
	folders, err := s.load()
	if err != nil {
		return nil, err
	}

	root := s.storage.root
	states := []RemoteFolderState{}
	for _, folder := range folders {
		states = append(states, RemoteFolderState{
			RemoteFolder: folder,
			Path:         remoteMountPoint(root, folder.Name),
			Connected:    remoteFolderConnected(root, folder.Name),
		})
	}
	return map[string]any{
		"installed": cifsInstalled(),
		"folders":   states,
	}, nil
}

type ConnectRemoteParams struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Share    string `json:"share"`
	Username string `json:"username"`
	Password string `json:"password"`
	AddedBy  string `json:"added_by"`

	// Access is who may open it. Empty means everybody with an account.
	Access []string `json:"access"`
}

func (s *RemoteServices) connect(ctx context.Context, params ConnectRemoteParams) (any, error) {
	name := strings.ToLower(strings.TrimSpace(params.Name))
	if !validShareName.MatchString(name) {
		return nil, &Error{
			Code:        "remote.invalid_name",
			Message:     "That is not a name Homebase can give a folder.",
			Detail:      "must match " + shareNamePattern,
			Recoverable: true,
			Recovery:    "Use lowercase letters, numbers and hyphens — for example \"dads-disk\".",
			Status:      400,
		}
	}

	host := strings.TrimSpace(params.Host)
	if !validRemoteHost.MatchString(host) {
		return nil, &Error{
			Code:        "remote.invalid_host",
			Message:     "That is not a computer name Homebase can use.",
			Detail:      host + " must match " + remoteHostPattern,
			Recoverable: true,
			Recovery: "Use the computer's name on the network, or its address — " +
				"for example \"dads-laptop\" or \"192.168.1.42\".",
			Status: 400,
		}
	}

	share := strings.TrimSpace(params.Share)
	if !validRemoteShare.MatchString(share) {
		return nil, &Error{
			Code:        "remote.invalid_share",
			Message:     "That is not a shared-folder name Homebase can use.",
			Detail:      share + " must match " + remoteSharePattern,
			Recoverable: true,
			Recovery: "Use the name the folder is shared under on that computer, " +
				"without spaces. On Windows it is under the folder's Properties, " +
				"Sharing — and it can be renamed there if it has a space in it.",
			Status: 400,
		}
	}

	if strings.ContainsAny(params.Username, "\n\r") ||
		strings.ContainsAny(params.Password, "\n\r") {
		// A newline would add a line to the credentials file, which is how a
		// password field becomes a way to set a mount option.
		return nil, &Error{
			Code:        "remote.invalid_credentials",
			Message:     "That name or password cannot be used.",
			Detail:      "it contains a line break",
			Recoverable: true,
			Recovery:    "Check what was pasted and try again.",
			Status:      400,
		}
	}

	people, err := validatedAccessList(params.Access)
	if err != nil {
		return nil, err
	}

	folders, err := s.load()
	if err != nil {
		return nil, err
	}
	if _, _, exists := findRemoteFolder(folders, name); exists {
		return nil, &Error{
			Code:        "remote.name_in_use",
			Message:     "A folder from another computer is already called that.",
			Detail:      name,
			Recoverable: true,
			Recovery:    "Choose a different name, or disconnect the existing one first.",
			Status:      409,
		}
	}

	if err := installCifs(ctx); err != nil {
		return nil, err
	}

	folder := RemoteFolder{
		Name:     name,
		Host:     host,
		Share:    share,
		Username: strings.TrimSpace(params.Username),
		AddedBy:  strings.TrimSpace(params.AddedBy),
		Access:   people,
		AddedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	// Credentials first, then the mount. The other order writes a unit that
	// refers to a file that is not there.
	if err := writeRemoteCredentials(name, folder.Username, params.Password); err != nil {
		return nil, err
	}
	if err := mountRemoteFolder(ctx, s.storage.root, folder); err != nil {
		// Nothing is recorded and nothing is left behind. A folder that could
		// not be opened is not a folder somebody has to remember to remove, and
		// a credentials file for it would be a password kept for nothing.
		unmountRemoteFolder(ctx, s.storage.root, name)
		return nil, err
	}

	folders = append(folders, folder)
	if err := s.save(folders); err != nil {
		unmountRemoteFolder(ctx, s.storage.root, name)
		return nil, err
	}

	return map[string]any{
		"name":      name,
		"host":      host,
		"share":     share,
		"connected": true,
		"message": name + " is now open on this server, from " + host +
			". It is read-only: nothing here can change what is on that computer.",
	}, nil
}

type RemoteFolderRef struct {
	Name string `json:"name"`
}

func (s *RemoteServices) disconnect(ctx context.Context, params RemoteFolderRef) (any, error) {
	name := strings.ToLower(strings.TrimSpace(params.Name))

	folders, err := s.load()
	if err != nil {
		return nil, err
	}
	_, index, found := findRemoteFolder(folders, name)
	if !found {
		return nil, &Error{
			Code:        "remote.no_such_folder",
			Message:     "There is no folder from another computer by that name.",
			Detail:      name,
			Recoverable: true,
			Recovery:    "Check the Files page for what is connected.",
			Status:      404,
		}
	}

	// The record goes first. If the unmount fails — a file still open, a laptop
	// that vanished mid-read — the folder must not come back on the next
	// listing as though nothing happened.
	folders = append(folders[:index], folders[index+1:]...)
	if err := s.save(folders); err != nil {
		return nil, err
	}
	unmountRemoteFolder(ctx, s.storage.root, name)

	return map[string]any{
		"name":         name,
		"disconnected": true,
		"message": "That folder is no longer open here. Nothing on the other " +
			"computer was changed — Homebase could not write to it.",
	}, nil
}

func (s *RemoteServices) reconnect(ctx context.Context, params RemoteFolderRef) (any, error) {
	name := strings.ToLower(strings.TrimSpace(params.Name))

	folders, err := s.load()
	if err != nil {
		return nil, err
	}
	folder, _, found := findRemoteFolder(folders, name)
	if !found {
		return nil, &Error{
			Code:        "remote.no_such_folder",
			Message:     "There is no folder from another computer by that name.",
			Detail:      name,
			Recoverable: true,
			Recovery:    "Check the Files page for what is connected.",
			Status:      404,
		}
	}

	root := s.storage.root
	if remoteFolderConnected(root, name) {
		return map[string]any{
			"name":      name,
			"connected": true,
			"message":   name + " was already open.",
		}, nil
	}

	// The unit and the credentials are written again rather than reused. A
	// reconnect happens hours or days later, and whatever is on disk from last
	// time may have been written by a version of Homebase that is no longer
	// running.
	if err := mountRemoteFolder(ctx, root, folder); err != nil {
		return nil, err
	}
	return map[string]any{
		"name":      name,
		"connected": true,
		"message":   name + " is open again.",
	}, nil
}

// validatedAccessList cleans a list of Homebase usernames for a `valid users`
// style rule. Shared with per-share access: see setAccess.
func validatedAccessList(access []string) ([]string, error) {
	people := make([]string, 0, len(access))
	for _, name := range access {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if !validShareName.MatchString(name) {
			return nil, &Error{
				Code:        "share.invalid_username",
				Message:     "That is not a name Homebase can give access to.",
				Detail:      name + " must match " + shareNamePattern,
				Recoverable: true,
				Recovery:    "Use lowercase letters, numbers and hyphens.",
				Status:      400,
			}
		}
		if !slices.Contains(people, name) {
			people = append(people, name)
		}
	}
	return people, nil
}
