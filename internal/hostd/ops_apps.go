package hostd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Application operations.
//
// Every one of these takes an application id and nothing else of consequence.
// hostd looks the manifest up in its own catalogue and constructs the container
// itself; core never sends a container specification. The set of containers this
// machine can run is therefore the set of manifests on disk. See ADR-0012.

const (
	// DefaultAppDataRoot is where an application's private directories live.
	DefaultAppDataRoot = "/srv/homebase/apps"

	// DefaultStateDir is hostd's own bookkeeping, kept away from both the user's
	// data and core's state directory. root-owned: core must not be able to
	// rewrite hostd's record of what it did.
	DefaultStateDir = "/var/lib/homebase-hostd"

	// containerPrefix namespaces the containers Homebase owns, so nothing here
	// can act on a container somebody else created.
	containerPrefix = "homebase-"

	// serviceAccount owns application data, so core can back it up and a
	// container running as a non-root user can write to it.
	serviceAccount = "homebase"

	// stopGraceSeconds is how long an application gets to shut down. Not
	// politeness: an application killed mid-write leaves a partial file, and
	// some of those files are somebody's media library database.
	stopGraceSeconds = 30
)

// AppServices is what the application operations need from their environment.
type AppServices struct {
	Catalogue *Catalogue

	// dataRoot is where application data lives. Configurable so that development
	// can run without root, and so the VM tests can prove data survives a reboot
	// somewhere other than a hard-coded path — but every path handed to a
	// container or to RemoveAll is still checked to be under it. The check is
	// against escaping this root, not against the root being chosen.
	dataRoot string

	// storage resolves user-selected storage to a place on a disk. Optional:
	// hostd runs without it, and an application declaring user-selected storage
	// then refuses to install rather than quietly using the system disk.
	storage *StorageServices

	// stateDir holds hostd's own record of what it has done — currently which
	// applications were stopped deliberately.
	//
	// That record has to exist because Docker does not keep one. A container that
	// somebody stopped and a container that crashed are byte-for-byte identical
	// afterwards: status "exited", and an exit code that says nothing, because a
	// program terminated by SIGTERM chooses its own. traefik/whoami exits 2 when
	// asked to stop, which made every deliberate stop read as "stopped
	// unexpectedly". Homebase is the one doing the stopping, so Homebase is the
	// one that can know.
	stateDir string

	docker *docker
}

// WithStorage gives the application operations access to managed disks.
func (s *AppServices) WithStorage(storage *StorageServices) *AppServices {
	s.storage = storage
	return s
}

func NewAppServices(catalogue *Catalogue, dockerSocketPath, dataRoot, stateDir string) *AppServices {
	if dataRoot == "" {
		dataRoot = DefaultAppDataRoot
	}
	if stateDir == "" {
		stateDir = DefaultStateDir
	}
	return &AppServices{
		Catalogue: catalogue,
		dataRoot:  filepath.Clean(dataRoot),
		stateDir:  filepath.Clean(stateDir),
		docker:    newDocker(dockerSocketPath),
	}
}

// RegisterAppOperations adds the app domain to a registry.
func RegisterAppOperations(r *Registry, services *AppServices) {
	r.MustRegister(Operation{
		Name:    "app.list",
		Summary: "List the applications this server can install, and their state.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 15 * time.Second,
		Handler: Typed(services.list),
	})

	r.MustRegister(Operation{
		Name:    "app.status",
		Summary: "Report whether one application is installed, running and healthy.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 15 * time.Second,
		Handler: Typed(services.status),
	})

	r.MustRegister(Operation{
		Name:    "app.install",
		Summary: "Download an application and create its container.",
		// Low rather than medium: installing touches no existing data and can be
		// undone by uninstalling.
		Risk:        RiskLow,
		Permissions: []string{"apps.manage"},
		Confirm:     ConfirmNone,
		// Pulling Jellyfin is 1.8 GB on a domestic connection.
		Timeout:  30 * time.Minute,
		Rollback: "app.uninstall",
		Handler:  Typed(services.install),
	})

	r.MustRegister(Operation{
		Name:        "app.start",
		Summary:     "Start an installed application.",
		Risk:        RiskLow,
		Permissions: []string{"apps.manage"},
		Confirm:     ConfirmNone,
		Timeout:     2 * time.Minute,
		Rollback:    "app.stop",
		Handler:     Typed(services.start),
	})

	r.MustRegister(Operation{
		Name:    "app.stop",
		Summary: "Stop a running application.",
		// Medium: whoever was watching a film stops watching a film.
		Risk:        RiskMedium,
		Permissions: []string{"apps.manage"},
		Confirm:     ConfirmRequired,
		Timeout:     2 * time.Minute,
		Rollback:    "app.start",
		Handler:     Typed(services.stop),
	})

	r.MustRegister(Operation{
		Name:        "app.restart",
		Summary:     "Restart a running application.",
		Risk:        RiskMedium,
		Permissions: []string{"apps.manage"},
		Confirm:     ConfirmRequired,
		Timeout:     3 * time.Minute,
		Rollback:    "",
		Handler:     Typed(services.restart),
	})

	r.MustRegister(Operation{
		Name:    "app.uninstall",
		Summary: "Remove an application's container, keeping its data.",
		// Medium, not high: this deliberately does not touch data. Removing data
		// is app.remove_data, which is a different intention and must not be
		// collapsed into this one.
		Risk:        RiskMedium,
		Permissions: []string{"apps.manage"},
		Confirm:     ConfirmRequired,
		Timeout:     3 * time.Minute,
		Rollback:    "app.install",
		Handler:     Typed(services.uninstall),
	})

	r.MustRegister(Operation{
		Name:    "app.remove_data",
		Summary: "Permanently delete an application's data.",
		// The only critical operation in the app domain, and the only one that
		// destroys something a user cannot get back.
		Risk:        RiskCritical,
		Permissions: []string{"apps.manage", "storage.modify"},
		Confirm:     ConfirmExplicit,
		Timeout:     5 * time.Minute,
		Rollback:    "", // Cannot be undone. Stated, not implied.
		Handler:     Typed(services.removeData),
	})

	r.MustRegister(Operation{
		Name:    "app.assign_storage",
		Summary: "Choose which disk holds one of an application's storage locations.",
		// Medium: it changes where an application's files live, taking effect the
		// next time it starts. Nothing is moved and nothing is deleted — data
		// already written to the old location stays where it is.
		Risk:        RiskMedium,
		Permissions: []string{"apps.manage", "storage.modify"},
		Confirm:     ConfirmRequired,
		Timeout:     30 * time.Second,
		Handler:     Typed(services.assignStorage),
	})

	r.MustRegister(Operation{
		Name:    "app.storage",
		Summary: "Report which disks an application's storage locations are on.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 15 * time.Second,
		Handler: Typed(services.storageStatus),
	})

	r.MustRegister(Operation{
		Name:    "app.logs",
		Summary: "Read recent output from an application.",
		Risk:    RiskRead,
		Confirm: ConfirmNone,
		Timeout: 30 * time.Second,
		Handler: Typed(services.logs),
	})
}

// --- Parameters and results --------------------------------------------------

// AppRef names an application. Every mutating operation takes exactly this, so
// that nothing describing a container can cross the boundary.
type AppRef struct {
	ID string `json:"id"`
}

type AppState string

const (
	// StateNotInstalled means the manifest exists but nothing has been created.
	// This is a positive answer: the runtime was asked and said so.
	StateNotInstalled AppState = "not_installed"
	StateStopped      AppState = "stopped"
	StateRunning      AppState = "running"
	// StateFailed means the container exited on its own.
	StateFailed AppState = "failed"

	// StateUnknown means the container runtime could not be asked.
	//
	// Deliberately not folded into not_installed, which is what this code did
	// first. The two look identical in an interface and are entirely different
	// facts: one of them means a user's application is fine and Homebase cannot
	// see it. Reporting "not installed" there invites somebody to install it
	// again on top of a running one.
	StateUnknown AppState = "unknown"
)

type AppStatus struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Summary string   `json:"summary,omitempty"`
	State   AppState `json:"state"`

	// Installed is null where the state is unknown. false is a claim that it is
	// not there, which is not something we know when the runtime did not answer.
	Installed *bool `json:"installed"`

	// Health is the container's own health check result, or null when the
	// application declares none or has not been checked yet. Null is not
	// "unhealthy" — the two are different facts.
	Health *string `json:"health"`

	Image        string  `json:"image"`
	Version      string  `json:"version,omitempty"`
	InternalPort int     `json:"internal_port,omitempty"`
	StartedAt    *string `json:"started_at"`
	ExitCode     *int    `json:"exit_code"`

	// DataPath is where this application's data lives, so a user can be told
	// what uninstalling will leave behind.
	DataPath string `json:"data_path"`
}

// --- Handlers ----------------------------------------------------------------

func (s *AppServices) list(ctx context.Context, _ NoParams) (any, error) {
	manifests := s.Catalogue.All()

	// Reaching Docker is not required to list what is installable. A machine
	// whose Docker is down should still be able to say what it knows about,
	// rather than reporting nothing at all.
	dockerUp := s.docker.ping(ctx) == nil

	apps := make([]AppStatus, 0, len(manifests))
	for _, manifest := range manifests {
		status := s.statusFor(ctx, manifest, dockerUp)
		apps = append(apps, status)
	}

	return map[string]any{
		"applications":     apps,
		"docker_available": dockerUp,
		"rejected":         s.Catalogue.Rejected(),
	}, nil
}

func (s *AppServices) status(ctx context.Context, params AppRef) (any, error) {
	manifest, err := s.manifest(params.ID)
	if err != nil {
		return nil, err
	}
	return s.statusFor(ctx, manifest, s.docker.ping(ctx) == nil), nil
}

func (s *AppServices) statusFor(ctx context.Context, manifest Manifest, dockerUp bool) AppStatus {
	status := AppStatus{
		ID:           manifest.ID,
		Name:         manifest.Name,
		Summary:      manifest.Summary,
		State:        StateUnknown,
		Image:        manifest.Container.Image,
		Version:      manifest.Container.Version,
		InternalPort: manifest.Network.InternalPort,
		DataPath:     s.appDataDir(manifest.ID),
	}

	if !dockerUp {
		return status
	}

	state, err := s.docker.inspectContainer(ctx, containerName(manifest.ID))
	if err != nil {
		// The runtime answered something other than "no such container". We do
		// not know what is on this machine, and saying "not installed" would be
		// a confident guess.
		return status
	}
	if state == nil {
		// A 404: asked and answered. This one really is not installed.
		status.State = StateNotInstalled
		status.Installed = boolPtr(false)
		return status
	}

	status.Installed = boolPtr(true)
	switch {
	case state.State.Running:
		status.State = StateRunning

	// Whether Homebase was the one that stopped it, rather than what it exited
	// with. The exit code cannot answer this: a program terminated by SIGTERM
	// chooses its own, and traefik/whoami chooses 2 — so reading a non-zero code
	// as a crash reported every deliberate stop as a fault.
	case s.stoppedDeliberately(manifest.ID):
		status.State = StateStopped

	default:
		// Nobody asked it to stop and it stopped. That is unexpected whatever it
		// exited with: a long-running service exiting cleanly on its own is still
		// a service that is no longer there.
		status.State = StateFailed
	}

	if state.State.StartedAt != "" && state.State.Running {
		started := state.State.StartedAt
		status.StartedAt = &started
	}
	if !state.State.Running {
		code := state.State.ExitCode
		status.ExitCode = &code
	}
	if state.State.Health != nil {
		health := state.State.Health.Status
		status.Health = &health
	}

	return status
}

func (s *AppServices) install(ctx context.Context, params AppRef) (any, error) {
	manifest, err := s.manifest(params.ID)
	if err != nil {
		return nil, err
	}

	if err := s.docker.ping(ctx); err != nil {
		return nil, err
	}

	if err := s.checkResources(manifest); err != nil {
		return nil, err
	}

	name := containerName(manifest.ID)

	// Already installed is not a failure. Installing twice should converge on
	// one installed application rather than erroring, because a caller retrying
	// after a lost connection has no way to know which state it is in.
	if existing, err := s.docker.inspectContainer(ctx, name); err == nil && existing != nil {
		return map[string]any{
			"id":        manifest.ID,
			"installed": true,
			"message":   manifest.Name + " is already installed.",
		}, nil
	}

	reference := manifest.Container.Reference()
	if err := s.docker.pullImage(ctx, reference, nil); err != nil {
		// A failed pull is not the end of it. The image is pinned to a version or
		// a digest, so one already on disk is the same bytes the pull would have
		// fetched — and a home server whose broadband is down, or which is
		// behind a captive portal, or whose DNS has stopped answering, should
		// still be able to install something it already has. Refusing here would
		// make Homebase useless in exactly the situation a local server is for.
		if !s.docker.hasImage(ctx, reference) {
			return nil, wrapDockerError(err,
				"pull_failed",
				"Homebase could not download "+manifest.Name+".",
				"Check that the server is connected to the internet, then try again.")
		}
	}

	// Private directories are created before the container so the bind mounts
	// have somewhere to land. Owned by the service account, mode 0750.
	binds, err := s.prepareStorage(manifest)
	if err != nil {
		return nil, err
	}

	config := s.buildContainer(manifest, binds)
	if _, err := s.docker.createContainer(ctx, name, config); err != nil {
		return nil, wrapDockerError(err,
			"create_failed",
			"Homebase downloaded "+manifest.Name+" but could not set it up.",
			"Try installing it again. If it keeps failing, check the server logs.")
	}

	if err := s.docker.startContainer(ctx, name); err != nil {
		return nil, wrapDockerError(err,
			"start_failed",
			manifest.Name+" was installed but would not start.",
			"Try starting it from the applications list.")
	}

	// A freshly created container carries none of the previous one's history.
	s.forgetStopped(manifest.ID)

	return map[string]any{
		"id":        manifest.ID,
		"installed": true,
		"message":   manifest.Name + " is installed and running.",
	}, nil
}

func (s *AppServices) start(ctx context.Context, params AppRef) (any, error) {
	manifest, err := s.manifest(params.ID)
	if err != nil {
		return nil, err
	}
	if err := s.requireInstalled(ctx, manifest); err != nil {
		return nil, err
	}

	if err := s.docker.startContainer(ctx, containerName(manifest.ID)); err != nil {
		// Already running is the desired state.
		var dockerErr *dockerError
		if asDockerError(err, &dockerErr) && dockerErr.Status == 304 {
			s.forgetStopped(manifest.ID)
			return map[string]any{"id": manifest.ID, "running": true}, nil
		}
		return nil, wrapDockerError(err, "start_failed",
			manifest.Name+" would not start.",
			"Check the application's logs for the reason.")
	}

	s.forgetStopped(manifest.ID)
	return map[string]any{"id": manifest.ID, "running": true}, nil
}

func (s *AppServices) stop(ctx context.Context, params AppRef) (any, error) {
	manifest, err := s.manifest(params.ID)
	if err != nil {
		return nil, err
	}
	if err := s.requireInstalled(ctx, manifest); err != nil {
		return nil, err
	}

	if err := s.docker.stopContainer(ctx, containerName(manifest.ID), stopGraceSeconds); err != nil {
		var dockerErr *dockerError
		if asDockerError(err, &dockerErr) && dockerErr.Status == 304 {
			// Already stopped is the desired state, and it was still Homebase
			// being asked for it.
			s.rememberStopped(manifest.ID)
			return map[string]any{"id": manifest.ID, "running": false}, nil
		}
		return nil, wrapDockerError(err, "stop_failed",
			manifest.Name+" would not stop.", "")
	}

	s.rememberStopped(manifest.ID)
	return map[string]any{"id": manifest.ID, "running": false}, nil
}

func (s *AppServices) restart(ctx context.Context, params AppRef) (any, error) {
	manifest, err := s.manifest(params.ID)
	if err != nil {
		return nil, err
	}
	if err := s.requireInstalled(ctx, manifest); err != nil {
		return nil, err
	}

	if err := s.docker.restartContainer(ctx, containerName(manifest.ID), stopGraceSeconds); err != nil {
		return nil, wrapDockerError(err, "restart_failed",
			manifest.Name+" would not restart.",
			"Check the application's logs for the reason.")
	}

	s.forgetStopped(manifest.ID)
	return map[string]any{"id": manifest.ID, "running": true}, nil
}

// uninstall removes the container and leaves the data alone.
func (s *AppServices) uninstall(ctx context.Context, params AppRef) (any, error) {
	manifest, err := s.manifest(params.ID)
	if err != nil {
		return nil, err
	}

	if err := s.docker.ping(ctx); err != nil {
		return nil, err
	}

	name := containerName(manifest.ID)

	// Stop first so the application shuts down rather than being killed. A
	// failure here is not fatal — removal forces it — but trying is what keeps a
	// half-written library database from being the normal outcome.
	_ = s.docker.stopContainer(ctx, name, stopGraceSeconds)

	if err := s.docker.removeContainer(ctx, name, true); err != nil {
		return nil, wrapDockerError(err, "uninstall_failed",
			"Homebase could not remove "+manifest.Name+".", "")
	}

	// The container is gone, so there is no state left to describe. Leaving the
	// marker would make a fresh install of the same application report itself as
	// stopped before anybody had stopped it.
	s.forgetStopped(manifest.ID)

	dataPath := s.appDataDir(manifest.ID)
	kept := false
	if info, err := os.Stat(dataPath); err == nil && info.IsDir() {
		kept = true
	}

	return map[string]any{
		"id":        manifest.ID,
		"installed": false,
		"data_kept": kept,
		"data_path": dataPath,
		"message": manifest.Name + " has been removed. Its data has been kept, so " +
			"reinstalling will pick up where you left off.",
	}, nil
}

// AppRemoveDataParams requires the application to be named twice: once as the
// target and once as a confirmation.
type AppRemoveDataParams struct {
	ID string `json:"id"`
	// Confirm must equal ID. This is what ConfirmExplicit means for this
	// operation — naming the thing being destroyed, so a confirmation obtained
	// for one application cannot be spent on another.
	Confirm string `json:"confirm"`
}

func (s *AppServices) removeData(ctx context.Context, params AppRemoveDataParams) (any, error) {
	manifest, err := s.manifest(params.ID)
	if err != nil {
		return nil, err
	}

	if params.Confirm != params.ID {
		return nil, &Error{
			Code:        "app.confirmation_mismatch",
			Message:     "The confirmation did not name the application being deleted.",
			Detail:      "confirm must equal the application id",
			Recoverable: true,
			Recovery:    "Confirm again, naming " + manifest.Name + ".",
			Status:      428,
		}
	}

	// The container must be gone first. Deleting the files under a running
	// application produces an application in a state nobody designed.
	if err := s.docker.ping(ctx); err == nil {
		if state, err := s.docker.inspectContainer(ctx, containerName(manifest.ID)); err == nil && state != nil {
			return nil, &Error{
				Code:        "app.still_installed",
				Message:     manifest.Name + " must be removed before its data can be deleted.",
				Detail:      "the container still exists",
				Recoverable: true,
				Recovery:    "Remove the application first, then delete its data.",
				Status:      409,
			}
		}
	}

	dataPath := s.appDataDir(manifest.ID)

	// A last check that the path is what this code intends to delete. Belt and
	// braces against a future change to appDataDir: this is the one operation in
	// Homebase that destroys data a user cannot get back, and the cost of the
	// check is nothing compared with the cost of being wrong.
	if !strings.HasPrefix(dataPath, s.dataRoot+"/") || strings.Contains(dataPath, "..") {
		return nil, internalError("refusing to delete " + dataPath + ": outside the application data root")
	}

	if err := os.RemoveAll(dataPath); err != nil {
		return nil, &Error{
			Code:        "app.data_removal_failed",
			Message:     "Homebase could not delete " + manifest.Name + "'s data.",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "Try again. If it keeps failing, check the server logs.",
			Status:      500,
		}
	}

	return map[string]any{
		"id":      manifest.ID,
		"deleted": true,
		"message": manifest.Name + "'s data has been permanently deleted.",
	}, nil
}

type AppLogsParams struct {
	ID    string `json:"id"`
	Lines int    `json:"lines,omitempty"`
}

func (s *AppServices) logs(ctx context.Context, params AppLogsParams) (any, error) {
	manifest, err := s.manifest(params.ID)
	if err != nil {
		return nil, err
	}
	if err := s.requireInstalled(ctx, manifest); err != nil {
		return nil, err
	}

	lines := params.Lines
	if lines <= 0 {
		lines = 200
	}
	if lines > 2000 {
		lines = 2000
	}

	output, err := s.docker.containerLogs(ctx, containerName(manifest.ID), lines)
	if err != nil {
		return nil, wrapDockerError(err, "logs_unavailable",
			"Homebase could not read "+manifest.Name+"'s logs.", "")
	}

	return map[string]any{"id": manifest.ID, "lines": lines, "logs": output}, nil
}

// --- Container construction --------------------------------------------------

// buildContainer turns a manifest into a create request.
//
// Everything here comes from the manifest hostd read off disk. Nothing comes
// from the caller, which is what makes this safe to do in a root process — see
// ADR-0012.
func (s *AppServices) buildContainer(manifest Manifest, binds []string) containerConfig {
	config := containerConfig{
		Image: manifest.Container.Reference(),
		Cmd:   manifest.Container.Command,
		Labels: map[string]string{
			"homebase.app":      manifest.ID,
			"homebase.managed":  "true",
			"homebase.revision": fmt.Sprint(manifest.Revision),
		},
		HostConfig: hostConfig{
			Binds: binds,
			// Applications should come back after a reboot without anybody
			// asking. `unless-stopped` rather than `always` so an application the
			// user deliberately stopped stays stopped.
			RestartPolicy: restartPolicy{Name: "unless-stopped"},

			// Every capability dropped, then only what the manifest declares
			// added back. Starting from "drop all" means a manifest has to ask,
			// and asking is what gets reviewed.
			CapDrop: []string{"ALL"},
			CapAdd:  manifest.Permissions.Capabilities,

			// No new privileges: a setuid binary inside the image gains nothing.
			SecurityOpt: []string{"no-new-privileges"},

			// Never. Present in the struct so this line can exist.
			Privileged: false,
		},
	}

	for key, value := range manifest.Container.Environment {
		config.Env = append(config.Env, key+"="+value)
	}

	if manifest.Network.InternalPort > 0 && !manifest.Network.HostNetwork {
		port := fmt.Sprintf("%d/tcp", manifest.Network.InternalPort)
		config.ExposedPorts = map[string]struct{}{port: {}}
		config.HostConfig.PortBindings = map[string][]portBinding{
			// Bound to localhost, not 0.0.0.0. Applications are reached through
			// Homebase, which is what applies authentication — an application
			// published straight onto the LAN is one nothing is guarding.
			port: {{HostIP: "127.0.0.1", HostPort: "0"}},
		}
	}
	if manifest.Network.HostNetwork {
		config.HostConfig.NetworkMode = "host"
	}

	if manifest.Resources.MemoryLimitBytes > 0 {
		config.HostConfig.Memory = manifest.Resources.MemoryLimitBytes
	}
	if manifest.Resources.CPUShares > 0 {
		config.HostConfig.CpuShares = manifest.Resources.CPUShares
	}

	if manifest.Permissions.ReadOnlyRoot == nil || *manifest.Permissions.ReadOnlyRoot {
		config.HostConfig.ReadonlyRootfs = true
	}

	for _, device := range manifest.Permissions.Devices {
		// Device names are a fixed enumeration in the schema, mapped here to
		// paths. A manifest cannot name a device path directly.
		if path, ok := deviceePaths[device]; ok {
			config.HostConfig.Devices = append(config.HostConfig.Devices, deviceMapping{
				PathOnHost:        path,
				PathInContainer:   path,
				CgroupPermissions: "rwm",
			})
		}
	}

	return config
}

// deviceePaths maps the schema's device roles to host paths. A manifest declares
// a role; only this table turns one into a path.
var deviceePaths = map[string]string{
	"dri": "/dev/dri",
	"dvb": "/dev/dvb",
}

// prepareStorage creates the private directories a manifest declares and returns
// the bind mounts for them.
//
// user-selected storage is skipped: it has no location until somebody assigns
// one, which is storage.assign's job and lands with the storage milestone. An
// application declaring user-selected storage installs and runs without it
// rather than refusing.
func (s *AppServices) prepareStorage(manifest Manifest) ([]string, error) {
	var binds []string

	assignments := map[string]Assignment{}
	if s.storage != nil {
		assignments = s.storage.Assignments(manifest.ID)
	}

	for _, storage := range manifest.Storage {
		if storage.Type == "user-selected" {
			bind, err := s.prepareUserSelected(manifest, storage, assignments)
			if err != nil {
				return nil, err
			}
			binds = append(binds, bind)
			continue
		}
		if storage.Type != "private" {
			continue
		}

		hostPath := filepath.Join(s.appDataDir(manifest.ID), storage.ID)

		// Constructed from an id the catalogue validated, but checked anyway:
		// this path is about to be handed to a container as a bind mount.
		if !strings.HasPrefix(hostPath, s.dataRoot+"/") || strings.Contains(hostPath, "..") {
			return nil, internalError("refusing to mount " + hostPath + ": outside the application data root")
		}

		// Every directory created below the data root is owned by the service
		// account, not only the leaf. os.MkdirAll creates intermediates as the
		// calling process — root — and chowning just the leaf leaves
		// /srv/homebase/apps/<id> as 0750 root:root. core then cannot traverse
		// into it, which means it cannot back the data up: a silent failure of
		// the one thing a user would most notice missing.
		if err := s.makeOwnedDir(hostPath); err != nil {
			return nil, &Error{
				Code:        "app.storage_unavailable",
				Message:     "Homebase could not create somewhere for " + manifest.Name + " to keep its files.",
				Detail:      err.Error(),
				Recoverable: false,
				Status:      500,
			}
		}

		mode := "rw"
		if storage.ReadOnly() {
			mode = "ro"
		}
		binds = append(binds, hostPath+":"+storage.MountPath+":"+mode)
	}

	return binds, nil
}

// prepareUserSelected resolves storage the user chose a disk for.
//
// Every failure here refuses rather than falling back. An application whose
// media disk is missing must not start against an empty directory on the system
// disk: it produces a media server with an empty library, a database rebuilt
// from nothing, and a root filesystem quietly filling up. Refusing is worse in
// the moment and better every time after it. See ADR-0013.
func (s *AppServices) prepareUserSelected(manifest Manifest, storage ManifestStorage, assignments map[string]Assignment) (string, error) {
	described := storage.Description
	if described == "" {
		described = storage.ID
	}

	if s.storage == nil {
		return "", &Error{
			Code:        "app.storage_not_assigned",
			Message:     manifest.Name + " needs a disk before it can run.",
			Detail:      "storage management is not available on this server",
			Recoverable: false,
			Status:      409,
		}
	}

	assignment, assigned := assignments[storage.ID]
	if !assigned {
		return "", &Error{
			Code:        "app.storage_not_assigned",
			Message:     manifest.Name + " needs somewhere to keep its files.",
			Detail:      described,
			Recoverable: true,
			Recovery: "Choose a disk for " + manifest.Name + " in the storage " +
				"settings, then try again.",
			Status: 409,
		}
	}

	mountPoint, mounted := s.storage.ResolveLocation(assignment.Location)
	if !mounted {
		location, known := s.storage.LocationByID(assignment.Location)
		name := assignment.Location
		if known {
			name = location.Name
		}
		return "", &Error{
			Code:        "app.storage_unavailable",
			Message:     manifest.Name + " cannot start because " + name + " is not connected.",
			Detail:      described + " is on " + name,
			Recoverable: true,
			Recovery: "Plug " + name + " back in. " + manifest.Name +
				" will start on its own once it is there.",
			Status: 409,
		}
	}

	// A directory of its own under the location, so one disk can hold several
	// applications' files without them seeing each other's.
	subdirectory := assignment.Subdirectory
	if subdirectory == "" {
		subdirectory = manifest.ID
	}
	hostPath := filepath.Join(mountPoint, subdirectory)

	// Checked, because this path is about to be handed to a container as a bind
	// mount and it was assembled from stored state.
	if !strings.HasPrefix(hostPath, mountPoint+"/") || strings.Contains(hostPath, "..") {
		return "", internalError("refusing to mount " + hostPath + ": outside " + mountPoint)
	}

	if err := os.MkdirAll(hostPath, 0o750); err != nil {
		return "", &Error{
			Code:        "app.storage_unavailable",
			Message:     "Homebase could not use that disk for " + manifest.Name + ".",
			Detail:      err.Error(),
			Recoverable: true,
			Recovery:    "The disk may be full, faulty or read-only.",
			Status:      500,
		}
	}
	if os.Geteuid() == 0 {
		if err := chownToService(hostPath); err != nil {
			return "", internalError("setting ownership on " + hostPath + ": " + err.Error())
		}
	}

	mode := "rw"
	if storage.ReadOnly() {
		mode = "ro"
	}
	return hostPath + ":" + storage.MountPath + ":" + mode, nil
}

// checkResources refuses an install the machine cannot support, before spending
// twenty minutes downloading it.
func (s *AppServices) checkResources(manifest Manifest) error {
	if manifest.Resources.MemoryMinimumBytes > 0 {
		memory, err := readMemory()
		if err == nil && memory.TotalBytes < uint64(manifest.Resources.MemoryMinimumBytes) {
			return &Error{
				Code:    "app.insufficient_memory",
				Message: manifest.Name + " needs more memory than this server has.",
				Detail: fmt.Sprintf("needs %s, this server has %s",
					humanBytes(manifest.Resources.MemoryMinimumBytes),
					humanBytes(int64(memory.TotalBytes))),
				Recoverable: false,
				Status:      422,
			}
		}
	}
	return nil
}

// --- Helpers -----------------------------------------------------------------

func (s *AppServices) manifest(id string) (Manifest, error) {
	manifest, ok := s.Catalogue.Lookup(id)
	if !ok {
		// Terse, like an unknown operation: enumerating what does exist would
		// help somebody who has already reached the socket map the surface.
		return Manifest{}, &Error{
			Code:        "app.unknown",
			Message:     "This server does not have that application.",
			Recoverable: false,
			Status:      404,
		}
	}
	return manifest, nil
}

func (s *AppServices) requireInstalled(ctx context.Context, manifest Manifest) error {
	if err := s.docker.ping(ctx); err != nil {
		return err
	}
	state, err := s.docker.inspectContainer(ctx, containerName(manifest.ID))
	if err != nil {
		return wrapDockerError(err, "status_unavailable",
			"Homebase could not check on "+manifest.Name+".", "")
	}
	if state == nil {
		return &Error{
			Code:        "app.not_installed",
			Message:     manifest.Name + " is not installed.",
			Recoverable: true,
			Recovery:    "Install it first.",
			Status:      409,
		}
	}
	return nil
}

// makeOwnedDir creates a directory under the data root, giving the service
// account ownership of every level it creates.
//
// Directories that already exist are left alone: an existing one may have been
// set up deliberately, and quietly rewriting ownership on a path somebody else
// manages is not this function's business.
func (s *AppServices) makeOwnedDir(path string) error {
	relative, err := filepath.Rel(s.dataRoot, path)
	if err != nil || strings.HasPrefix(relative, "..") {
		return fmt.Errorf("%s is not under %s", path, s.dataRoot)
	}

	// The root itself first: on a fresh machine /srv/homebase/apps does not
	// exist yet either.
	current := s.dataRoot
	for _, component := range append([]string{""}, strings.Split(relative, string(os.PathSeparator))...) {
		if component != "" {
			current = filepath.Join(current, component)
		}

		if _, err := os.Stat(current); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}

		if err := os.Mkdir(current, 0o750); err != nil && !os.IsExist(err) {
			return err
		}

		// Skipped when hostd is not root, which only happens in development:
		// there is no service account to chown to and no privilege to do it
		// with, and the failure would not be informative about anything a
		// developer can fix. On a real server hostd is root.
		if os.Geteuid() != 0 {
			continue
		}
		if err := chownToService(current); err != nil {
			return fmt.Errorf("setting ownership on %s: %w", current, err)
		}
	}

	return nil
}

// --- Remembering a deliberate stop -------------------------------------------
//
// Docker keeps no record of who stopped a container, and the exit code cannot
// be read as one: a program terminated by SIGTERM chooses what to exit with, and
// plenty choose something other than zero. So the only place this can be known
// is here, at the moment Homebase does the stopping.
//
// The consequence of getting it wrong is not cosmetic. "Stopped unexpectedly"
// on an application somebody deliberately stopped is Homebase reporting a fault
// that did not happen — and once it has done that, no status it reports is worth
// reading.

func (s *AppServices) stopMarker(id string) string {
	return filepath.Join(s.stateDir, "stopped", id)
}

// rememberStopped records that Homebase stopped this application.
//
// A failure here is logged rather than returned: the application really has
// stopped, which is what the user asked for, and turning a successful stop into
// an error because a marker file could not be written would be the worse
// outcome. The cost of the missing marker is that the application reads as
// having stopped on its own.
func (s *AppServices) rememberStopped(id string) {
	path := s.stopMarker(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		slog.Warn("could not record that an application was stopped deliberately",
			"application", id, "error", err)
		return
	}
	if err := os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		slog.Warn("could not record that an application was stopped deliberately",
			"application", id, "error", err)
	}
}

// forgetStopped clears the record, because the application is running again.
func (s *AppServices) forgetStopped(id string) {
	if err := os.Remove(s.stopMarker(id)); err != nil && !os.IsNotExist(err) {
		slog.Warn("could not clear an application's stopped marker",
			"application", id, "error", err)
	}
}

// stoppedDeliberately reports whether Homebase is the one that stopped it.
func (s *AppServices) stoppedDeliberately(id string) bool {
	_, err := os.Stat(s.stopMarker(id))
	return err == nil
}

// --- Storage assignment -------------------------------------------------------

type AssignStorageParams struct {
	ID string `json:"id"`
	// StorageID names which of the application's declared locations this is.
	StorageID string `json:"storage_id"`
	// Location is a managed storage location's id.
	Location string `json:"location"`
}

// AppStorageSlot describes one of an application's declared storage locations
// and what, if anything, is behind it.
type AppStorageSlot struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	MountPath   string `json:"mount_path"`
	ReadOnly    bool   `json:"read_only"`

	// Location is the managed location assigned to this slot, for user-selected
	// storage. Empty means nothing has been chosen yet.
	Location     string `json:"location,omitempty"`
	LocationName string `json:"location_name,omitempty"`

	// Ready means this slot can be used right now. For private storage that is
	// always true; for user-selected it means assigned and connected.
	Ready bool `json:"ready"`

	// Path is where the data actually lives, when it is resolvable.
	Path string `json:"path,omitempty"`
}

func (s *AppServices) assignStorage(_ context.Context, params AssignStorageParams) (any, error) {
	manifest, err := s.manifest(params.ID)
	if err != nil {
		return nil, err
	}
	if s.storage == nil {
		return nil, internalError("storage management is not available")
	}

	var slot ManifestStorage
	var found bool
	for _, declared := range manifest.Storage {
		if declared.ID == params.StorageID {
			slot, found = declared, true
		}
	}
	if !found {
		return nil, &Error{
			Code:        "app.unknown_storage",
			Message:     manifest.Name + " has no storage location by that name.",
			Detail:      params.StorageID,
			Recoverable: false,
			Status:      404,
		}
	}

	// Private storage is Homebase's to place, not the user's. Allowing a disk to
	// be chosen for it would move an application's own configuration onto a
	// removable disk, and the application would then not start without it.
	if slot.Type != "user-selected" {
		return nil, &Error{
			Code:        "app.storage_not_choosable",
			Message:     "That part of " + manifest.Name + " is looked after by Homebase.",
			Detail:      params.StorageID + " is " + slot.Type + " storage",
			Recoverable: false,
			Status:      409,
		}
	}

	if err := s.storage.Assign(manifest.ID, slot.ID, params.Location, manifest.ID); err != nil {
		return nil, err
	}

	return map[string]any{
		"id":         manifest.ID,
		"storage_id": slot.ID,
		"location":   params.Location,
		"message": manifest.Name + " will use that disk. " +
			"It takes effect the next time it starts.",
	}, nil
}

func (s *AppServices) storageStatus(_ context.Context, params AppRef) (any, error) {
	manifest, err := s.manifest(params.ID)
	if err != nil {
		return nil, err
	}

	assignments := map[string]Assignment{}
	if s.storage != nil {
		assignments = s.storage.Assignments(manifest.ID)
	}

	slots := make([]AppStorageSlot, 0, len(manifest.Storage))
	for _, declared := range manifest.Storage {
		slot := AppStorageSlot{
			ID:          declared.ID,
			Type:        declared.Type,
			Description: declared.Description,
			MountPath:   declared.MountPath,
			ReadOnly:    declared.ReadOnly(),
		}

		if declared.Type != "user-selected" {
			slot.Ready = true
			slot.Path = filepath.Join(s.appDataDir(manifest.ID), declared.ID)
			slots = append(slots, slot)
			continue
		}

		if assignment, assigned := assignments[declared.ID]; assigned && s.storage != nil {
			slot.Location = assignment.Location
			if location, known := s.storage.LocationByID(assignment.Location); known {
				slot.LocationName = location.Name
			}
			if mountPoint, mounted := s.storage.ResolveLocation(assignment.Location); mounted {
				slot.Ready = true
				slot.Path = filepath.Join(mountPoint, assignment.Subdirectory)
			}
		}
		slots = append(slots, slot)
	}

	return map[string]any{
		"id":      manifest.ID,
		"name":    manifest.Name,
		"storage": slots,
		// ready is what decides whether the application can start at all.
		"ready": allReady(slots),
	}, nil
}

func allReady(slots []AppStorageSlot) bool {
	for _, slot := range slots {
		if !slot.Ready {
			return false
		}
	}
	return true
}

func boolPtr(v bool) *bool { return &v }

func containerName(id string) string { return containerPrefix + id }

func (s *AppServices) appDataDir(id string) string { return filepath.Join(s.dataRoot, id) }

// wrapDockerError turns a daemon failure into something a person can read,
// keeping the daemon's own words in the detail for whoever is diagnosing.
func wrapDockerError(err error, code, message, recovery string) error {
	// An unreachable daemon already has a good error; do not bury it.
	if e, ok := err.(*Error); ok {
		return e
	}

	detail := err.Error()
	var dockerErr *dockerError
	if asDockerError(err, &dockerErr) {
		detail = dockerErr.Message
	}

	return &Error{
		Code:        "app." + code,
		Message:     message,
		Detail:      detail,
		Recoverable: recovery != "",
		Recovery:    recovery,
		Status:      500,
	}
}

// chownToService gives a directory to the unprivileged service account.
//
// hostd creates these as root; core needs to read them for backups, and a
// container running as a non-root user needs to write to them. Resolved by name
// at call time rather than cached, so a machine where the account does not exist
// yet fails with something legible.
func chownToService(path string) error {
	account, err := user.Lookup(serviceAccount)
	if err != nil {
		return fmt.Errorf("looking up the %s account: %w", serviceAccount, err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	return os.Chown(path, uid, gid)
}
