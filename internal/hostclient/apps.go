package hostclient

import "context"

// Application operations.
//
// Every one of these sends an application id and nothing else. There is
// deliberately no method here that takes an image name, a bind mount, a port or
// an environment variable — hostd reads the manifest and builds the container
// itself, and core has no vocabulary for describing one. See ADR-0012.
//
// That is why these wrappers are so thin: the interesting property is what they
// cannot express.

// AppState is where an application is in its lifecycle.
type AppState string

const (
	AppNotInstalled AppState = "not_installed"
	AppStopped      AppState = "stopped"
	AppRunning      AppState = "running"
	AppFailed       AppState = "failed"
	// AppUnknown means the container runtime could not be asked. Not the same as
	// not_installed: one of those means a working application Homebase cannot see.
	AppUnknown AppState = "unknown"
)

// App is one application's manifest and current state, as hostd reports it.
type App struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Summary string   `json:"summary,omitempty"`
	Icon    string   `json:"icon,omitempty"`
	State   AppState `json:"state"`

	// Installed is null where the state is unknown — false would be a claim
	// nobody is in a position to make.
	Installed *bool `json:"installed"`

	// Health is the container's own health result, or null where the application
	// declares no check or has not been checked yet. Null is not "unhealthy";
	// conflating the two would report a starting application as broken.
	Health *string `json:"health"`

	Image        string  `json:"image"`
	Version      string  `json:"version,omitempty"`
	InternalPort int     `json:"internal_port,omitempty"`
	StartedAt    *string `json:"started_at"`
	ExitCode     *int    `json:"exit_code"`
	DataPath     string  `json:"data_path"`

	// Where to open it, and whether anything other than this machine can. Both,
	// because an address alone cannot say which — and until these existed,
	// nothing anywhere reported how to reach an application that was running.
	// What each supporting container is doing, for an application made of more
	// than one — a database, a cache. "It is not running" and "its database is
	// not running" need different answers.
	Services []struct {
		Name      string `json:"name"`
		Installed bool   `json:"installed"`
		Running   bool   `json:"running"`
	} `json:"services,omitempty"`

	AfterInstall         string `json:"after_install,omitempty"`
	HostPort             int    `json:"host_port,omitempty"`
	ReachableFromNetwork bool   `json:"reachable_from_network"`

	// Path is the base path the web interface is served under, and URL is the
	// whole address. Both, because a caller composing its own address needs the
	// first and a caller opening a link wants the second.
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`

	// A privilege this application holds that most do not, and why.
	//
	// Reported so that whoever is about to install it can decline. That is the
	// entire justification for both root permissions existing — declared per
	// application, and shown — and for a while it was not true of any of them,
	// because this struct did not have the field and nothing said so.
	Elevation *struct {
		Kind    string `json:"kind"`
		Summary string `json:"summary"`
		Reason  string `json:"reason"`
	} `json:"elevation,omitempty"`
}

// AppList is the catalogue plus the state of everything in it.
type AppList struct {
	Applications []App `json:"applications"`

	// DockerAvailable is false when the container runtime could not be reached.
	// The list is still returned: a machine whose Docker is down should be able
	// to say what it knows about rather than appearing to have no applications.
	DockerAvailable bool `json:"docker_available"`

	// Rejected maps a manifest filename to why it did not load. Surfaced rather
	// than swallowed — an application missing with no explanation is harder to
	// diagnose than one listed with a reason.
	Rejected map[string]string `json:"rejected,omitempty"`
}

func (c *Client) Apps(ctx context.Context) (*AppList, error) {
	var list AppList
	if err := c.Call(ctx, "app.list", nil, false, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

func (c *Client) App(ctx context.Context, id string) (*App, error) {
	var app App
	if err := c.Call(ctx, "app.status", appRef{ID: id}, false, &app); err != nil {
		return nil, err
	}
	return &app, nil
}

func (c *Client) InstallApp(ctx context.Context, id string) error {
	return c.Call(ctx, "app.install", appRef{ID: id}, false, nil)
}

func (c *Client) StartApp(ctx context.Context, id string) error {
	return c.Call(ctx, "app.start", appRef{ID: id}, false, nil)
}

// StopApp, RestartApp and UninstallApp pass confirmed: the user was asked in
// core, because core is where the user is. hostd enforces that the claim was
// made and records it in the audit log.
func (c *Client) StopApp(ctx context.Context, id string) error {
	return c.Call(ctx, "app.stop", appRef{ID: id}, true, nil)
}

func (c *Client) RestartApp(ctx context.Context, id string) error {
	return c.Call(ctx, "app.restart", appRef{ID: id}, true, nil)
}

func (c *Client) UninstallApp(ctx context.Context, id string) error {
	return c.Call(ctx, "app.uninstall", appRef{ID: id}, true, nil)
}

// RemoveAppData deletes an application's data irreversibly.
//
// confirm must be the application's own id. hostd checks it again; the check
// exists in both places because this is the one application operation with no
// rollback, and a confirmation enforced in one place is a confirmation one
// refactor away from being enforced nowhere.
func (c *Client) RemoveAppData(ctx context.Context, id, confirm string) error {
	params := struct {
		ID      string `json:"id"`
		Confirm string `json:"confirm"`
	}{ID: id, Confirm: confirm}
	return c.Call(ctx, "app.remove_data", params, true, nil)
}

// AppLogs returns the application's recent output.
type AppLogs struct {
	ID    string `json:"id"`
	Lines int    `json:"lines"`
	Logs  string `json:"logs"`
}

func (c *Client) AppLogs(ctx context.Context, id string, lines int) (*AppLogs, error) {
	params := struct {
		ID    string `json:"id"`
		Lines int    `json:"lines,omitempty"`
	}{ID: id, Lines: lines}

	var logs AppLogs
	if err := c.Call(ctx, "app.logs", params, false, &logs); err != nil {
		return nil, err
	}
	return &logs, nil
}

type appRef struct {
	ID string `json:"id"`
}
