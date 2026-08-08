package hostd

import (
	"context"
	"fmt"
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

	docker *docker
}

func NewAppServices(catalogue *Catalogue, dockerSocketPath, dataRoot string) *AppServices {
	if dataRoot == "" {
		dataRoot = DefaultAppDataRoot
	}
	return &AppServices{
		Catalogue: catalogue,
		dataRoot:  filepath.Clean(dataRoot),
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
	case state.State.ExitCode != 0:
		status.State = StateFailed
	default:
		status.State = StateStopped
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
			return map[string]any{"id": manifest.ID, "running": true}, nil
		}
		return nil, wrapDockerError(err, "start_failed",
			manifest.Name+" would not start.",
			"Check the application's logs for the reason.")
	}
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
			return map[string]any{"id": manifest.ID, "running": false}, nil
		}
		return nil, wrapDockerError(err, "stop_failed",
			manifest.Name+" would not stop.", "")
	}
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

	for _, storage := range manifest.Storage {
		if storage.Type != "private" {
			continue
		}

		hostPath := filepath.Join(s.appDataDir(manifest.ID), storage.ID)

		// Constructed from an id the catalogue validated, but checked anyway:
		// this path is about to be handed to a container as a bind mount.
		if !strings.HasPrefix(hostPath, s.dataRoot+"/") || strings.Contains(hostPath, "..") {
			return nil, internalError("refusing to mount " + hostPath + ": outside the application data root")
		}

		if err := os.MkdirAll(hostPath, 0o750); err != nil {
			return nil, &Error{
				Code:        "app.storage_unavailable",
				Message:     "Homebase could not create somewhere for " + manifest.Name + " to keep its files.",
				Detail:      err.Error(),
				Recoverable: false,
				Status:      500,
			}
		}

		// Owned by the service account so core can include it in a backup, and
		// so the container writing as a non-root user can use it.
		//
		// Skipped when hostd is not root, which only happens in development —
		// there is no service account to chown to and no privilege to do it
		// with. The failure it would otherwise produce is not informative about
		// anything a developer can fix. On a real server hostd is root, so this
		// branch is not reachable there.
		if os.Geteuid() == 0 {
			if err := chownToService(hostPath); err != nil {
				return nil, internalError("setting ownership on " + hostPath + ": " + err.Error())
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
