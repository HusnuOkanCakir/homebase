package hostd

import (
	"context"
	"slices"
	"strings"
	"time"
)

// The file-sharing operations.
//
// This is the half of a home server that a browser cannot be: a drive on
// somebody's laptop, that Windows backs up to and Linux mounts, without
// installing anything at either end.
//
// Every operation here changes what is on the local network, so none of them is
// RiskLow, and the one that hands out a password declares it so the audit log
// never sees it.

// RegisterShareOperations adds the sharing domain to a registry.
func RegisterShareOperations(r *Registry, services *ShareServices) {
	r.MustRegister(Operation{
		Name:    "share.status",
		Summary: "Report which folders are shared onto the local network.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 30 * time.Second,
		Handler: Typed(services.statusOp),
	})

	r.MustRegister(Operation{
		Name:    "share.add",
		Summary: "Share a folder onto the local network.",
		// High. This is the operation that puts a folder of somebody's files
		// where every device in the house can open it, and installs a listening
		// service to do it. Nothing is destroyed, and that is not the measure.
		Risk:        RiskHigh,
		Permissions: []string{"storage.modify", "network.modify"},
		Confirm:     ConfirmRequired,
		// Installing Samba on a domestic connection.
		Timeout:  10 * time.Minute,
		Rollback: "share.remove, with the same name",
		Handler:  Typed(services.add),
	})

	r.MustRegister(Operation{
		Name:    "share.remove",
		Summary: "Stop sharing a folder. The files stay where they are.",
		// Medium: it takes something off the network, which is recoverable, and
		// it deliberately does not touch the files. Deleting them is
		// storage's job and a different intention.
		Risk:        RiskMedium,
		Permissions: []string{"storage.modify", "network.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     2 * time.Minute,
		Rollback:    "share.add, with the same name and disk",
		Handler:     Typed(services.remove),
	})

	r.MustRegister(Operation{
		Name: "share.set_password",
		Summary: "Create or change the password somebody types to open a " +
			"shared folder.",
		Risk:        RiskHigh,
		Permissions: []string{"storage.modify", "network.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     2 * time.Minute,
		// The audit log is kept for ever and is readable by whoever can read the
		// machine. A file-sharing password is typed into a Windows dialog once
		// and saved there, so it is exactly the kind that is never changed.
		Secret:  []string{"password"},
		Handler: Typed(services.setPassword),
	})

	r.MustRegister(Operation{
		Name: "share.set_access",
		Summary: "Choose who may open a shared folder: everybody with an " +
			"account, or named people.",
		// High, and the same risk as sharing it in the first place. Widening
		// this puts somebody's files in front of the whole house, and it does
		// so without moving a file or changing anything visible on the disk.
		Risk:        RiskHigh,
		Permissions: []string{"storage.modify", "network.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     2 * time.Minute,
		Rollback:    "share.set_access, with the previous list",
		Handler:     Typed(services.setAccess),
	})

	r.MustRegister(Operation{
		Name: "share.make_personal_folder",
		Summary: "Create the private folder that belongs to one person on " +
			"this server.",
		// Low. It makes an empty directory on the server's own disk, and the
		// only thing it can overwrite is a folder of the same name, which it
		// converges on rather than replaces.
		Risk:        RiskLow,
		Permissions: []string{"storage.modify"},
		Confirm:     ConfirmNone,
		Timeout:     1 * time.Minute,
		Handler:     Typed(services.makePersonalFolderOp),
	})

	r.MustRegister(Operation{
		Name: "share.retire_personal_folder",
		Summary: "Move somebody's private folder aside when their account is " +
			"removed. The files are kept.",
		// Medium: nothing is deleted, but a folder moves and the person it
		// belonged to is gone, so the only record of where it went is the
		// answer this returns and the audit entry beside it.
		Risk:        RiskMedium,
		Permissions: []string{"storage.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     1 * time.Minute,
		Rollback:    "rename the folder back by hand; nothing is deleted",
		Handler:     Typed(services.retirePersonalFolderOp),
	})

	r.MustRegister(Operation{
		Name:        "share.remove_user",
		Summary:     "Stop somebody being able to open the shared folders.",
		Risk:        RiskMedium,
		Permissions: []string{"storage.modify", "network.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     1 * time.Minute,
		Handler:     Typed(services.removeUser),
	})
}

func (s *ShareServices) statusOp(ctx context.Context, _ NoParams) (any, error) {
	return s.status(ctx)
}

type AddShareParams struct {
	// Name is what the folder is called on the network, and is what somebody
	// types after the server's name.
	Name string `json:"name"`

	// Location is the disk it lives on.
	Location string `json:"location"`

	ReadOnly bool `json:"read_only"`
}

func (s *ShareServices) add(ctx context.Context, params AddShareParams) (any, error) {
	name := strings.ToLower(strings.TrimSpace(params.Name))
	if !validShareName.MatchString(name) {
		return nil, &Error{
			Code:        "share.invalid_name",
			Message:     "That is not a name Homebase can give a shared folder.",
			Detail:      "must match " + shareNamePattern,
			Recoverable: true,
			Recovery:    "Use lowercase letters, numbers and hyphens — for example \"backup\".",
			Status:      400,
		}
	}

	// The disk is checked before anything is installed, so that a mistyped disk
	// name does not leave a file server on the machine.
	if _, found := s.storage.LocationByID(params.Location); !found {
		return nil, unknownLocation(params.Location)
	}

	shares, err := s.load()
	if err != nil {
		return nil, err
	}
	if existing, _, found := findShare(shares, name); found {
		if existing.Location != params.Location || existing.ReadOnly != params.ReadOnly {
			return nil, &Error{
				Code:        "share.name_in_use",
				Message:     "A folder with that name is already shared, from a different disk.",
				Detail:      name + " is on " + existing.Location,
				Recoverable: true,
				Recovery:    "Choose a different name, or stop sharing the existing one.",
				Status:      409,
			}
		}
		// The same request again. Converging rather than refusing, for the same
		// reason as adding a disk: a caller retrying after a failure has no way
		// to know how far the last attempt got, and this one genuinely can stop
		// half way — the record is saved before the file server is configured,
		// so a failure to start leaves a share that exists and is not on the
		// network. Refusing the retry would report "already shared" about a
		// folder that is not shared, and leave no way forward but removing it.
		if err := s.apply(ctx, shares); err != nil {
			return nil, err
		}
		return s.addedResult(ctx, existing)
	}

	if err := installSamba(ctx); err != nil {
		return nil, err
	}

	share := Share{
		Name:     name,
		Location: params.Location,
		ReadOnly: params.ReadOnly,
		AddedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	if _, err := s.makeShareFolder(share); err != nil {
		return nil, err
	}

	shares = append(shares, share)
	if err := s.save(shares); err != nil {
		return nil, err
	}
	if err := s.apply(ctx, shares); err != nil {
		return nil, err
	}
	return s.addedResult(ctx, share)
}

// addedResult describes a share that is now on the network.
func (s *ShareServices) addedResult(ctx context.Context, share Share) (any, error) {
	status, err := s.status(ctx)
	if err != nil {
		return nil, err
	}
	state := s.describe(share, status.ServerName)

	result := map[string]any{
		"name":    share.Name,
		"path":    state.Path,
		"address": state.Address,
		"users":   status.Users,
	}
	// A share nobody can open is the state every first one lands in, and it
	// looks exactly like success. Said here rather than discovered when Windows
	// asks for a password that does not exist yet.
	if len(status.Users) == 0 {
		result["next"] = "Nobody can open it yet. Give somebody a password with " +
			"`homebasectl share password <name>`."
	}
	return result, nil
}

type ShareRef struct {
	Name string `json:"name"`
}

func (s *ShareServices) remove(ctx context.Context, params ShareRef) (any, error) {
	shares, err := s.load()
	if err != nil {
		return nil, err
	}
	share, index, found := findShare(shares, params.Name)
	if !found {
		return nil, unknownShare(params.Name)
	}

	remaining := append(shares[:index:index], shares[index+1:]...)
	if err := s.save(remaining); err != nil {
		return nil, err
	}
	if err := s.apply(ctx, remaining); err != nil {
		return nil, err
	}

	// The files are deliberately left. "Stop sharing this" and "delete my
	// files" are different intentions and must not be collapsed — the same rule
	// as uninstalling an application.
	state := s.describe(share, serverName())
	return map[string]any{
		"name":    share.Name,
		"removed": true,
		"path":    state.Path,
		"message": share.Name + " is no longer on the network. The files are still " +
			"on the server.",
	}, nil
}

type SharePasswordParams struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *ShareServices) setPassword(ctx context.Context, params SharePasswordParams) (any, error) {
	username := strings.ToLower(strings.TrimSpace(params.Username))
	if !validShareName.MatchString(username) {
		return nil, &Error{
			Code:        "share.invalid_username",
			Message:     "That is not a name Homebase can use for a file-sharing account.",
			Detail:      "must match " + shareNamePattern,
			Recoverable: true,
			Recovery:    "Use lowercase letters, numbers and hyphens.",
			Status:      400,
		}
	}
	// Long enough to be worth having. This one is typed into a dialog on a
	// laptop and saved there, so it is never changed and never remembered —
	// which is an argument for length rather than for character classes.
	if len(params.Password) < 8 {
		return nil, &Error{
			Code:        "share.password_too_short",
			Message:     "That password is too short to protect a folder of files.",
			Detail:      "at least 8 characters",
			Recoverable: true,
			Recovery: "Use at least eight characters. This is typed once and saved " +
				"by the computer that connects, so length costs nothing.",
			Status: 400,
		}
	}
	if !sambaInstalled() {
		return nil, &Error{
			Code:        "share.not_set_up",
			Message:     "Nothing is shared from this server yet.",
			Recoverable: true,
			Recovery:    "Share a folder first with `homebasectl share add`.",
			Status:      409,
		}
	}

	if err := setSharePassword(ctx, username, params.Password); err != nil {
		return nil, err
	}

	// The name map is rendered from the accounts that exist, so it has to be
	// written again now that there is one more. Without this the account is
	// real, the password is right, and typing the name it was created with
	// fails — which is what happened to the first person to use this.
	shares, err := s.load()
	if err != nil {
		return nil, err
	}
	if err := s.apply(ctx, shares); err != nil {
		return nil, err
	}

	return map[string]any{
		"username": username,
		"message":  username + " can now open the shared folders.",
		"login":    username,
	}, nil
}

type ShareUserRef struct {
	Username string `json:"username"`
}

func (s *ShareServices) removeUser(ctx context.Context, params ShareUserRef) (any, error) {
	username := strings.ToLower(strings.TrimSpace(params.Username))
	if err := removeShareUser(ctx, username); err != nil {
		return nil, err
	}
	return map[string]any{
		"username": username,
		"removed":  true,
		"message":  username + " can no longer open the shared folders.",
	}, nil
}

// --- Private folders ------------------------------------------------------------

type PersonalFolderParams struct {
	Username string `json:"username"`
}

func (s *ShareServices) makePersonalFolderOp(ctx context.Context, params PersonalFolderParams) (any, error) {
	username := strings.ToLower(strings.TrimSpace(params.Username))

	// Whether the folder was already there decides whether Samba is
	// reconfigured below, so it is worked out before rather than assumed. This
	// operation runs on every sign-in, to catch up accounts made before private
	// folders existed, and most of those calls have nothing to do.
	existing := s.personalFolderExists(PeopleLocation, username)

	path, err := s.makePersonalFolder(PeopleLocation, username)
	if err != nil {
		return nil, err
	}

	// The file server is told about it now rather than at the next share
	// change. `[people]` is only written once a folder exists, so the first
	// person to get one is also the moment the share appears — and without
	// this, it would appear whenever somebody next happened to share a folder.
	//
	// Only if there is a file server at all. A Homebase where nobody has shared
	// anything still gives people their folders; the Files screen serves them
	// either way, and Samba is what a Windows drive letter needs.
	if !existing && sambaInstalled() {
		shares, err := s.load()
		if err != nil {
			return nil, err
		}
		if err := s.apply(ctx, shares); err != nil {
			return nil, err
		}
	}

	return map[string]any{
		"username": username,
		"path":     path,
		"created":  !existing,
		"message":  username + " has a private folder on this server.",
	}, nil
}

func (s *ShareServices) retirePersonalFolderOp(ctx context.Context, params PersonalFolderParams) (any, error) {
	username := strings.ToLower(strings.TrimSpace(params.Username))
	retired, err := s.retirePersonalFolder(PeopleLocation, username)
	if err != nil {
		return nil, err
	}
	if retired == "" {
		return map[string]any{
			"username": username,
			"retired":  false,
			"message":  username + " had no private folder on this server.",
		}, nil
	}

	if sambaInstalled() {
		shares, err := s.load()
		if err != nil {
			return nil, err
		}
		if err := s.apply(ctx, shares); err != nil {
			return nil, err
		}
	}

	return map[string]any{
		"username": username,
		"retired":  true,
		"path":     retired,
		// Said plainly, because an administrator removing an account is
		// entitled to assume it took the files with it, and it did not.
		"message": "The files that were in " + username + "'s folder are still on " +
			"the server, at " + retired + ".",
	}, nil
}

// --- Who may open a folder --------------------------------------------------------

type ShareAccessParams struct {
	Name string `json:"name"`

	// Access is the accounts that may open it. Empty means everybody with an
	// account, which is what every share is until somebody says otherwise.
	Access []string `json:"access"`
}

func (s *ShareServices) setAccess(ctx context.Context, params ShareAccessParams) (any, error) {
	shares, err := s.load()
	if err != nil {
		return nil, err
	}
	share, index, found := findShare(shares, params.Name)
	if !found {
		return nil, unknownShare(params.Name)
	}

	// Cleaned rather than trusted. These names are written into smb.conf as a
	// `valid users` line, and a malformed one does not produce a share with a
	// broken rule: it produces a file server that refuses to start, taking
	// every other folder with it.
	people := make([]string, 0, len(params.Access))
	for _, name := range params.Access {
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

	share.Access = people
	shares[index] = share
	if err := s.save(shares); err != nil {
		return nil, err
	}
	if err := s.apply(ctx, shares); err != nil {
		return nil, err
	}

	if len(people) == 0 {
		return map[string]any{
			"name":     share.Name,
			"access":   []string{},
			"everyone": true,
			"message":  share.Name + " can be opened by everybody with an account.",
		}, nil
	}
	return map[string]any{
		"name":     share.Name,
		"access":   people,
		"everyone": false,
		"message": share.Name + " can now be opened by " +
			strings.Join(people, ", ") + " and nobody else.",
	}, nil
}
