package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The product, from a terminal.
//
// Every command here is a client of core's HTTP API — the same surface the
// dashboard uses, with the same permission checks, job records and events on it.
// Nothing reaches hostd directly, because a second path to a privileged
// operation is a second place for the checks to be wrong.
//
// Two rules shape the output.
//
// **`--json` is the real interface.** The human-readable form is for reading;
// the JSON is for composing with other things, and it is core's response
// unmodified rather than something reformatted here. A CLI whose JSON is its own
// invention is one that drifts from the API it claims to expose.
//
// **Exit codes distinguish "failed" from "did nothing".** A script that cannot
// tell those apart will one day do neither and report success. `0` is success,
// `1` is a failure the server described, `2` is the command being used wrongly,
// and `3` is the server not answering at all.

const (
	exitOK           = 0
	exitFailed       = 1
	exitUsage        = 2
	exitNotAnswering = 3
)

// options are what every command shares.
type options struct {
	address  string
	database string
	asJSON   bool

	// confirm is how a script agrees to something irreversible. It has to equal
	// the thing's own name — never a word like "yes", and there is no --yes.
	confirm string

	// from names the disk a backup is on; name is what to call a disk.
	from string
	name string
}

// globals are the shared flags when they are given *before* the subcommand.
//
// `homebasectl --address https://other system` is what somebody types for a
// setting that is about the connection rather than about the command, and the
// help text has always listed these under "Options" as though they were global.
// They were not: the parser only saw flags after the subcommand, so putting one
// first was read as an unknown command and exited 2. The help was right and the
// parser was wrong.
var globals = options{address: defaultAddress, database: defaultDatabase}

// takeGlobalFlags consumes shared flags appearing before the subcommand.
//
// Hand-rolled rather than a FlagSet, because a FlagSet stops at the first
// non-flag argument and would then have to be re-created for the subcommand with
// the same definitions — two places to add a flag to, one of which somebody
// would forget.
func takeGlobalFlags(args []string) ([]string, error) {
	for len(args) > 0 {
		name, value, hasValue := strings.Cut(args[0], "=")
		switch strings.TrimLeft(name, "-") {
		case "json":
			if name != "--json" && name != "-json" {
				return args, nil
			}
			globals.asJSON = true
			args = args[1:]
		case "address", "database":
			if !strings.HasPrefix(name, "-") {
				return args, nil
			}
			if !hasValue {
				if len(args) < 2 {
					return nil, usageError{fmt.Errorf("%s needs a value", name)}
				}
				value, args = args[1], args[1:]
			}
			if strings.TrimLeft(name, "-") == "address" {
				globals.address = value
			} else {
				globals.database = value
			}
			args = args[1:]
		default:
			return args, nil
		}
	}
	return args, nil
}

func bind(flags *flag.FlagSet) *options {
	o := &options{}
	// The global values are the defaults, so a flag given after the subcommand
	// overrides one given before it — which is the way round anybody would
	// expect if they wrote both.
	flags.StringVar(&o.address, "address", globals.address, "the server to talk to")
	flags.StringVar(&o.database, "database", globals.database, "path to the Homebase database")
	flags.BoolVar(&o.asJSON, "json", globals.asJSON, "print the server's answer as JSON")
	flags.StringVar(&o.confirm, "confirm", "",
		"agree to something irreversible from a script; must name the thing itself")
	flags.StringVar(&o.from, "from", "", "which disk a backup is on")
	flags.StringVar(&o.name, "name", "", "what to call it")
	return o
}

// withClient parses flags, connects, and runs the body.
func withClient(name string, args []string, stdout io.Writer,
	body func(context.Context, *Client, *options, []string, io.Writer) error) error {

	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stdout)
	o := bind(flags)
	if err := flags.Parse(args); err != nil {
		return usageError{err}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	client, err := connect(ctx, o.address, o.database)
	if err != nil {
		return err
	}
	defer client.Close()

	return body(ctx, client, o, flags.Args(), stdout)
}

// usageError means the command was used wrongly rather than that it failed.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }

// printJSON writes a value as JSON.
//
// Used only where there is no server response to pass through — everything that
// answers a request uses printResponse instead.
func printJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// printResponse writes what the server said, unmodified.
//
// Not the struct this package decoded into: those differ whenever core knows a
// field homebasectl does not, and the difference is silent — the field is simply
// absent, and a script relying on it breaks with nothing to read. `--json` is
// documented as the interface to build on, and this is what makes that true.
func printResponse(w io.Writer, c *Client, fallback any) error {
	if len(c.lastBody) == 0 {
		return printJSON(w, fallback)
	}

	// Re-indented rather than passed through byte for byte, because core answers
	// compactly and this is read by people as well as by jq.
	var pretty any
	if err := json.Unmarshal(c.lastBody, &pretty); err != nil {
		_, err := w.Write(append(c.lastBody, '\n'))
		return err
	}
	return printJSON(w, pretty)
}

// --- First use ---------------------------------------------------------------------

// setupCommand creates the first administrator.
//
// The one command here that needs no authentication, because there is nobody to
// authenticate as yet: `/setup` is unauthenticated by design and refuses once an
// account exists. It is also the one that hands back something that must be
// written down — the recovery code, which is shown once and stored the way a
// password is.
func setupCommand(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(stdout)
	o := bind(flags)
	username := flags.String("user", "", "the name for the administrator account")
	if err := flags.Parse(args); err != nil {
		return usageError{err}
	}

	name := *username
	if name == "" && flags.NArg() == 1 {
		name = flags.Arg(0)
	}
	if name == "" {
		return usageError{errors.New(
			"what should the administrator be called? — homebasectl setup NAME\n" +
				"The password is read from HOMEBASE_PASSWORD, or asked for.")}
	}

	password := os.Getenv("HOMEBASE_PASSWORD")
	if password == "" {
		var err error
		password, err = askForSecret("Choose a password: ")
		if err != nil {
			return err
		}
		again, err := askForSecret("Type it again: ")
		if err != nil {
			return err
		}
		if again != password {
			return usageError{errors.New("those did not match")}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// No credentials, and none to be had: this runs before there is an account.
	client := &Client{address: strings.TrimSuffix(o.address, "/"), http: insecureLocalClient()}

	var result struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
		RecoveryCode string `json:"recovery_code"`
	}
	if err := client.Post(ctx, "/setup",
		map[string]any{"username": name, "password": password}, &result); err != nil {
		return err
	}

	if o.asJSON {
		return printJSON(stdout, result)
	}

	fmt.Fprintf(stdout, "The administrator %s has been created.\n\n", result.User.Username)
	fmt.Fprintln(stdout, "Write this down and keep it somewhere other than this machine:")
	fmt.Fprintf(stdout, "\n    %s\n\n", result.RecoveryCode)
	fmt.Fprintln(stdout, "It is the way back in if the password is forgotten, it is shown")
	fmt.Fprintln(stdout, "once, and it travels with your backups — so it still works on a")
	fmt.Fprintln(stdout, "machine rebuilt from one.")
	return nil
}

// --- Applications --------------------------------------------------------------

// defaultTo picks the subcommand, treating a leading flag as "the default one".
//
// `homebasectl apps --json` means "list the applications, as JSON". Without this
// the dispatcher reads `--json` as a subcommand name and refuses it, which is
// both wrong and the first thing anybody types.
func defaultTo(args []string, fallback string) (string, []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return fallback, args
	}
	return args[0], args[1:]
}

func appsCommand(args []string, stdout io.Writer) error {
	action, rest := defaultTo(args, "list")

	switch action {
	case "list":
		return withClient("apps list", rest, stdout, listApps)
	case "install", "start", "stop", "restart", "uninstall":
		return withClient("apps "+action, rest, stdout,
			func(ctx context.Context, c *Client, o *options, names []string, w io.Writer) error {
				return actOnApp(ctx, c, o, action, names, w)
			})
	case "logs":
		return withClient("apps logs", rest, stdout, appLogs)
	case "storage":
		return withClient("apps storage", rest, stdout, appStorage)
	case "open":
		return withClient("apps open", rest, stdout, openApp)
	default:
		return usageError{fmt.Errorf("unknown apps command %q — try list, install, "+
			"start, stop, restart, uninstall, logs, storage, open", action)}
	}
}

// openApp prints the address of a running application, and opens it if there is
// a desktop to open it on.
//
// There was no way to find this out from anywhere in Homebase. An application
// would install, start, pass its health check, and sit at an address nothing
// reported — which for a media server is the whole of what it is for.
func openApp(ctx context.Context, c *Client, o *options, args []string, w io.Writer) error {
	if len(args) != 1 {
		return usageError{errors.New("which application? — homebasectl apps open NAME")}
	}
	var app application
	if err := c.Get(ctx, "/apps/"+args[0], &app); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, app)
	}

	if app.URL == "" {
		fmt.Fprintf(w, "%s has no address to open.\n", app.Name)
		if app.State != "running" {
			fmt.Fprintf(w, "\nIt is %s. Start it with: homebasectl apps start %s\n",
				strings.ReplaceAll(app.State, "_", " "), app.ID)
		}
		return nil
	}

	fmt.Fprintln(w, app.URL)
	if !app.ReachableFromNetwork {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "That address only works on the server itself. Reach it from")
		fmt.Fprintln(w, "another computer with an ssh tunnel:")
		fmt.Fprintf(w, "\n    ssh -L 8000:%s console@%s\n",
			strings.TrimPrefix(strings.TrimPrefix(app.URL, "http://"), "https://"),
			shortHost())
		return nil
	}

	// Opened as well as printed, when there is a desktop to open it on. Printed
	// first and always, because the usual place this runs is an ssh session,
	// where there is nothing to open and the address is the whole answer.
	if opener, err := exec.LookPath("xdg-open"); err == nil && os.Getenv("DISPLAY") != "" {
		_ = exec.CommandContext(ctx, opener, app.URL).Start()
	}
	return nil
}

// appStorage shows where an application keeps its files, and chooses.
//
//	homebasectl apps storage jellyfin                   what it needs and has
//	homebasectl apps storage jellyfin media internal    put it on this server
//
// There was no way to do this from a terminal at all until now: the operation
// and the endpoint both existed and nothing invoked them, so an application
// declaring user-selected storage could be installed and could never be started.
func appStorage(ctx context.Context, c *Client, o *options, args []string, w io.Writer) error {
	if len(args) == 0 {
		return usageError{errors.New(
			"which application? Try `homebasectl apps storage jellyfin`")}
	}
	app := args[0]

	if len(args) >= 2 {
		if len(args) < 3 {
			return usageError{fmt.Errorf(
				"which disk should hold %q? Run `homebasectl storage` to see them", args[1])}
		}
		body := map[string]any{"storage_id": args[1], "location": args[2]}
		if len(args) > 3 {
			// The folder on that disk. Given so that a media server can read
			// the same folder a laptop copies films into, rather than a
			// directory of its own that somebody then has to fill twice.
			body["folder"] = args[3]
		}

		var job jobReply
		if err := c.Post(ctx, "/apps/"+app+"/storage", body, &job); err != nil {
			return err
		}
		if err := followJob(ctx, c, o, job, w); err != nil {
			return err
		}
		if o.asJSON {
			return printResponse(w, c, job)
		}

		// Read back rather than echoed. The response to this is a job envelope,
		// so the folder is not in it — printing the one that was asked for
		// reported an empty folder as though it had been set, which is a claim
		// about the server made from the request.
		var placed struct {
			Storage []struct {
				ID   string `json:"id"`
				Path string `json:"path"`
			} `json:"storage"`
		}
		if err := c.Get(ctx, "/apps/"+app+"/storage", &placed); err != nil {
			return err
		}
		for _, slot := range placed.Storage {
			if slot.ID == args[1] && slot.Path != "" {
				fmt.Fprintf(w, "%s will read its %s from %s\n", app, args[1], slot.Path)
			}
		}
		// No "now restart it". The container is rebuilt by the assignment,
		// because a restart would have kept the old directories — Docker fixes
		// bind mounts when a container is created. Telling somebody to run a
		// command that would not have worked is worse than saying nothing.
		return nil
	}

	var storage struct {
		Name    string `json:"name"`
		Ready   bool   `json:"ready"`
		Storage []struct {
			ID           string `json:"id"`
			Type         string `json:"type"`
			Description  string `json:"description"`
			Location     string `json:"location"`
			LocationName string `json:"location_name"`
			Ready        bool   `json:"ready"`
			Path         string `json:"path"`
		} `json:"storage"`
	}
	if err := c.Get(ctx, "/apps/"+app+"/storage", &storage); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, storage)
	}

	rows := [][]string{{"WHAT", "WHERE", "STATUS"}}
	var missing []string
	for _, slot := range storage.Storage {
		where := "on this server, with the application"
		if slot.Type == "user-selected" {
			where = "not chosen yet"
			if slot.LocationName != "" {
				where = slot.LocationName
			}
		}
		status := "ready"
		if !slot.Ready {
			status = "waiting"
			if slot.Type == "user-selected" {
				missing = append(missing, slot.ID)
			}
		}
		rows = append(rows, []string{slot.ID, where, status})
	}
	writeTable(w, rows)

	for _, slot := range storage.Storage {
		if slot.Type == "user-selected" && slot.Description != "" {
			fmt.Fprintf(w, "\n%s — %s\n", slot.ID, slot.Description)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(w, "\nChoose a disk:\n\n    homebasectl apps storage %s %s internal\n",
			app, missing[0])
		fmt.Fprintln(w, "\nRun `homebasectl storage` for the list. `internal` is this")
		fmt.Fprintln(w, "server's own disk, which is fine for anything that fits on it.")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "To point it at folders you can also reach from your own computer,")
		fmt.Fprintln(w, "share them first and then name the folder:")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "    homebasectl share add films internal")
		fmt.Fprintln(w, "    homebasectl share add shows internal")
		fmt.Fprintf(w, "    homebasectl apps storage %s %s internal shares\n",
			app, missing[0])
	}
	return nil
}

type application struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Version string `json:"version,omitempty"`
	URL     string `json:"url,omitempty"`
	Health  string `json:"health,omitempty"`

	// What is still left for a person to do, from the manifest.
	AfterInstall string `json:"after_install,omitempty"`

	// Whether anything other than this machine can open it. Without this an
	// address is worse than none: a loopback URL is a real place that is not
	// there from the laptop somebody is reading it on.
	ReachableFromNetwork bool `json:"reachable_from_network"`
}

func listApps(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
	var reply struct {
		Items []application `json:"items"`
	}
	if err := c.Get(ctx, "/apps", &reply); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, reply)
	}

	if len(reply.Items) == 0 {
		fmt.Fprintln(w, "No applications in the catalogue.")
		return nil
	}
	sort.Slice(reply.Items, func(a, b int) bool { return reply.Items[a].ID < reply.Items[b].ID })

	rows := [][]string{{"ID", "STATE", "VERSION", "ADDRESS"}}
	onlyHere := false
	for _, app := range reply.Items {
		address := app.URL
		if address != "" && !app.ReachableFromNetwork {
			address += "  (this server only)"
			onlyHere = true
		}
		rows = append(rows, []string{app.ID, app.State, app.Version, address})
	}
	writeTable(w, rows)
	if onlyHere {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "\"this server only\" means the application is not published onto")
		fmt.Fprintln(w, "the network. Reach it with an ssh tunnel:")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "    ssh -L 8000:127.0.0.1:PORT console@"+shortHost()+"")
	}
	return nil
}

// shortHost is the name this client is talking to, for use in an example
// command. The address as typed, so the example can be pasted as printed.
func shortHost() string {
	host := os.Getenv("HOMEBASE_ADDRESS")
	if host == "" {
		return "homebase.local"
	}
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	return strings.TrimSuffix(strings.SplitN(host, "/", 2)[0], ":443")
}

func actOnApp(ctx context.Context, c *Client, o *options, action string,
	names []string, w io.Writer) error {
	if len(names) != 1 {
		return usageError{fmt.Errorf("which application? — homebasectl apps %s NAME", action)}
	}
	id := names[0]

	body := map[string]any{}
	// Stopping, restarting and uninstalling all ask for the name back — each of
	// them takes a service away from whoever is using it, possibly somebody else
	// in the house. In a terminal the command itself is the confirmation: the
	// name is already typed, deliberately, in the line that was run.
	//
	// Only uninstall sent it, so `apps stop` and `apps restart` failed with
	// "confirm must be jellyfin" against every server they were ever pointed at.
	// The same shape as `apps logs` decoding the wrong field: an endpoint with a
	// caller nobody had run.
	switch action {
	case "stop", "restart", "uninstall":
		body["confirm"] = id
	}

	var job jobReply
	path := "/apps/" + id + "/" + action
	if action == "install" {
		path = "/apps/" + id + "/install"
	}
	if err := c.Post(ctx, path, body, &job); err != nil {
		// The one refusal that has a fix a person can type. hostd cannot phrase
		// it: it answers a dashboard and a terminal from the same sentence, and
		// "choose a disk in the storage settings" is no help at a prompt.
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == "app.storage_not_assigned" {
			return fmt.Errorf("%w\n\nChoose a disk first:\n\n    "+
				"homebasectl apps storage %s", err, id)
		}
		return err
	}
	if err := followJob(ctx, c, o, job, w); err != nil {
		return err
	}

	// The address, at the moment somebody wants it. Anything that starts an
	// application ends with a person wanting to look at it, and until this was
	// added there was nowhere at all in Homebase that would say where.
	if o.asJSON || (action != "install" && action != "start" && action != "restart") {
		return nil
	}
	var app application
	if err := c.Get(ctx, "/apps/"+id, &app); err != nil || app.URL == "" {
		return nil
	}
	if app.ReachableFromNetwork {
		fmt.Fprintf(w, "\nOpen it at: %s\n", app.URL)
	} else {
		fmt.Fprintf(w, "\nIt is at %s, on the server only.\n", app.URL)
	}
	// What is left to do, from the manifest. Printed after an install rather
	// than only in a screen somebody might visit: an application asking for a
	// password nobody was given looks exactly like one that is broken.
	if action == "install" && app.AfterInstall != "" {
		fmt.Fprintf(w, "\n%s\n", wrapAt(app.AfterInstall, 72))
	}
	return nil
}

func appLogs(ctx context.Context, c *Client, o *options, names []string, w io.Writer) error {
	if len(names) != 1 {
		return usageError{errors.New("which application? — homebasectl apps logs NAME")}
	}
	// `lines` is the number asked for and `logs` is the text. This decoded
	// `lines` as the log itself and failed on every application with
	// "cannot unmarshal number into Go struct field .lines of type []string" —
	// which is what `homebasectl apps logs` did for its whole existence, because
	// nothing ever ran it against a server.
	var reply struct {
		Logs string `json:"logs"`
	}
	if err := c.Get(ctx, "/apps/"+names[0]+"/logs?lines=200", &reply); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, reply)
	}
	if strings.TrimSpace(reply.Logs) == "" {
		fmt.Fprintln(w, "Nothing in the log yet.")
		return nil
	}
	fmt.Fprintln(w, strings.TrimRight(reply.Logs, "\n"))
	return nil
}

// --- Jobs ------------------------------------------------------------------------

type jobReply struct {
	JobID     string  `json:"job_id"`
	Operation string  `json:"operation"`
	State     string  `json:"state"`
	Progress  *int    `json:"progress"`
	Message   *string `json:"message"`
	Error     *struct {
		Code     string `json:"code"`
		Message  string `json:"message"`
		Detail   string `json:"detail"`
		Recovery string `json:"recovery"`
	} `json:"error"`
}

func terminal(state string) bool {
	switch state {
	case "succeeded", "failed", "cancelled", "rolled_back", "rollback_failed":
		return true
	}
	return false
}

// followJob waits for a job and reports how it ended.
//
// Waiting rather than returning the id, because a command that returns
// immediately having started something is one a script has to poll — and every
// caller would then write the same polling loop, slightly differently.
func followJob(ctx context.Context, c *Client, o *options, job jobReply, w io.Writer) error {
	if job.JobID == "" {
		if o.asJSON {
			return printResponse(w, c, job)
		}
		fmt.Fprintln(w, "Done.")
		return nil
	}

	last := ""
	for !terminal(job.State) {
		if !o.asJSON && job.Message != nil && *job.Message != last {
			last = *job.Message
			fmt.Fprintln(w, last)
		}
		time.Sleep(time.Second)
		if err := c.Get(ctx, "/jobs/"+job.JobID, &job); err != nil {
			return err
		}
	}

	if o.asJSON {
		return printResponse(w, c, job)
	}

	if job.State != "succeeded" {
		if job.Error != nil {
			return &APIError{
				Code: job.Error.Code, Message: job.Error.Message,
				Detail: job.Error.Detail, Recovery: job.Error.Recovery,
			}
		}
		return fmt.Errorf("that did not work (%s)", job.State)
	}
	if job.Message != nil && *job.Message != last {
		fmt.Fprintln(w, *job.Message)
	}
	return nil
}

// --- Storage ----------------------------------------------------------------------

func storageCommand(args []string, stdout io.Writer) error {
	action, rest := defaultTo(args, "list")
	switch action {
	case "list":
		return withClient("storage list", rest, stdout, listStorage)
	case "disks":
		return withClient("storage disks", rest, stdout, listDisks)
	case "format":
		return withClient("storage format", rest, stdout, formatCommand)
	case "attach":
		return withClient("storage attach", rest, stdout, attachCommand)
	case "detach":
		return withClient("storage detach", rest, stdout, detachCommand)
	default:
		return usageError{fmt.Errorf("unknown storage command %q — try list, disks, "+
			"format, attach or detach", action)}
	}
}

func listStorage(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
	var reply struct {
		Items []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			Mounted        bool   `json:"mounted"`
			MountPoint     string `json:"mount_point"`
			TotalBytes     uint64 `json:"total_bytes"`
			AvailableBytes uint64 `json:"available_bytes"`
			Internal       bool   `json:"internal"`
		} `json:"items"`
	}
	if err := c.Get(ctx, "/storage/locations", &reply); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, reply)
	}
	if len(reply.Items) == 0 {
		fmt.Fprintln(w, "No disks are set up for Homebase to use.")
		return nil
	}

	rows := [][]string{{"ID", "NAME", "CONNECTED", "FREE", "OF"}}
	external := false
	for _, place := range reply.Items {
		connected := "no"
		if place.Mounted {
			connected = "yes"
		}
		if place.Internal {
			// "always" rather than "yes". The column asks whether the disk is
			// plugged in, and for this one the question does not apply — a row
			// saying "yes" invites somebody to wonder when it might say no.
			connected = "always"
		} else {
			external = true
		}
		rows = append(rows, []string{place.ID, place.Name, connected,
			humanBytes(place.AvailableBytes), humanBytes(place.TotalBytes)})
	}
	writeTable(w, rows)

	// Said once, here, rather than at the moment somebody tries to schedule a
	// backup and is refused. The refusal is correct and it arrives too late to
	// be useful: by then they have decided backups are set up.
	if !external {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Applications can keep their files on this server's own disk.")
		fmt.Fprintln(w, "Backups cannot — a copy on the same disk as the original is")
		fmt.Fprintln(w, "lost with it. Those need a disk you plug in.")
	}
	return nil
}

func listDisks(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
	var reply any
	if err := c.Get(ctx, "/storage/disks", &reply); err != nil {
		return err
	}
	return printResponse(w, c, reply)
}

// --- Backup -----------------------------------------------------------------------

func backupCommand(args []string, stdout io.Writer) error {
	// `schedule` rather than `list`, because listing needs a disk named and
	// reading the schedule needs nothing. A bare `homebasectl backup` should
	// answer something rather than complain.
	action, rest := defaultTo(args, "schedule")
	switch action {
	case "list":
		return withClient("backup list", rest, stdout, listBackups)
	case "now":
		return withClient("backup now", rest, stdout, makeBackup)
	case "schedule":
		return withClient("backup schedule", rest, stdout, backupSchedule)
	case "restore":
		return withClient("backup restore", rest, stdout, restoreCommand)
	default:
		return usageError{fmt.Errorf("unknown backup command %q — try list, now, "+
			"schedule or restore", action)}
	}
}

func listBackups(ctx context.Context, c *Client, o *options, rest []string, w io.Writer) error {
	if len(rest) != 1 {
		return usageError{errors.New("which disk? — homebasectl backup list DISK")}
	}
	var reply struct {
		Items []struct {
			ID         string `json:"id"`
			CreatedAt  string `json:"created_at"`
			Kind       string `json:"kind"`
			Files      int    `json:"files"`
			TotalBytes uint64 `json:"total_bytes"`
			Complete   bool   `json:"complete"`
		} `json:"items"`
	}
	if err := c.Get(ctx, "/backups?location="+rest[0], &reply); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, reply)
	}
	if len(reply.Items) == 0 {
		fmt.Fprintln(w, "No backups on that disk yet.")
		return nil
	}
	rows := [][]string{{"ID", "WHEN", "WHAT", "FILES", "SIZE"}}
	for _, backup := range reply.Items {
		what := backup.Kind
		if !backup.Complete {
			what = "INCOMPLETE"
		}
		rows = append(rows, []string{backup.ID, backup.CreatedAt, what,
			fmt.Sprint(backup.Files), humanBytes(backup.TotalBytes)})
	}
	writeTable(w, rows)
	return nil
}

func makeBackup(ctx context.Context, c *Client, o *options, rest []string, w io.Writer) error {
	if len(rest) != 1 {
		return usageError{errors.New("which disk? — homebasectl backup now DISK")}
	}
	var job jobReply
	if err := c.Post(ctx, "/backups",
		map[string]any{"location": rest[0], "include_data": true}, &job); err != nil {
		return err
	}
	return followJob(ctx, c, o, job, w)
}

func backupSchedule(ctx context.Context, c *Client, o *options, rest []string, w io.Writer) error {
	var schedule struct {
		Every       string `json:"every"`
		Location    string `json:"location"`
		Description string `json:"description"`
		Enabled     bool   `json:"enabled"`
		NextRun     string `json:"next_run"`
		LastResult  string `json:"last_result"`
	}

	// With no arguments this reads; with them it writes. A separate `set`
	// subcommand would be tidier and would also mean typing the word every time
	// for the thing people actually do.
	if len(rest) == 0 {
		if err := c.Get(ctx, "/backups/schedule", &schedule); err != nil {
			return err
		}
	} else {
		body := map[string]any{"every": rest[0]}
		if len(rest) > 1 {
			body["location"] = rest[1]
		}
		if err := c.Post(ctx, "/backups/schedule", body, &schedule); err != nil {
			return err
		}
	}

	if o.asJSON {
		return printResponse(w, c, schedule)
	}
	fmt.Fprintf(w, "Backups: %s\n", schedule.Description)
	if schedule.Every != "off" {
		fmt.Fprintf(w, "Onto:    %s\n", schedule.Location)
		if !schedule.Enabled {
			fmt.Fprintln(w, "\nWARNING: this is recorded but systemd is not running it.")
		}
		if schedule.NextRun != "" {
			fmt.Fprintf(w, "Next:    %s\n", schedule.NextRun)
		}
		if schedule.LastResult != "" {
			fmt.Fprintf(w, "Last:    %s\n", schedule.LastResult)
		}
	}
	return nil
}

// --- Updates ----------------------------------------------------------------------

func updateCommand(args []string, stdout io.Writer) error {
	action, rest := defaultTo(args, "status")
	switch action {
	case "status":
		return withClient("update status", rest, stdout, updateStatus)
	case "check":
		return withClient("update check", rest, stdout, updateCheck)
	case "apply":
		return withClient("update apply", rest, stdout, updateApply)
	default:
		return usageError{fmt.Errorf("unknown update command %q — try status, check or apply", action)}
	}
}

func updateStatus(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
	var status struct {
		Version     string `json:"version"`
		Consistent  bool   `json:"consistent"`
		Interrupted bool   `json:"interrupted"`
		Channel     string `json:"channel"`
	}
	if err := c.Get(ctx, "/system/update", &status); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, status)
	}
	fmt.Fprintf(w, "Version: %s\nChannel: %s\n", status.Version, status.Channel)
	if status.Interrupted || !status.Consistent {
		fmt.Fprintln(w, "\nAn update did not finish. Run: sudo homebasectl repair")
	}
	return nil
}

func updateCheck(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
	var result struct {
		Current         string `json:"current"`
		Available       string `json:"available"`
		UpdateAvailable bool   `json:"update_available"`
		Reachable       bool   `json:"reachable"`
		Detail          string `json:"detail"`
	}
	if err := c.Post(ctx, "/system/update/check", nil, &result); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, result)
	}
	switch {
	case !result.Reachable:
		fmt.Fprintf(w, "Could not reach the update source.\n    %s\n", result.Detail)
	case result.UpdateAvailable:
		fmt.Fprintf(w, "%s is available. You have %s.\n\n"+
			"    sudo homebasectl update apply\n", result.Available, result.Current)
	default:
		fmt.Fprintf(w, "Up to date (%s).\n", result.Current)
	}
	return nil
}

func updateApply(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
	var started struct {
		Started bool `json:"started"`
	}
	if err := c.Post(ctx, "/system/update/apply", nil, &started); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, started)
	}
	fmt.Fprintln(w, "The update has started. It restarts Homebase's services, so this")
	fmt.Fprintln(w, "connection will drop. Watch it with:")
	fmt.Fprintln(w, "\n    sudo homebasectl update progress")
	return nil
}

// --- Repair and diagnostics --------------------------------------------------------

func repairCommand(args []string, stdout io.Writer) error {
	return withClient("repair", args, stdout,
		func(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
			var result struct {
				Steps []struct {
					What    string `json:"what"`
					Done    string `json:"done"`
					Problem string `json:"problem"`
				} `json:"steps"`
				Changed int    `json:"changed"`
				Healthy bool   `json:"healthy"`
				Message string `json:"message"`
			}
			if err := c.Post(ctx, "/system/repair", nil, &result); err != nil {
				return err
			}
			if o.asJSON {
				return printResponse(w, c, result)
			}
			for _, step := range result.Steps {
				switch {
				case step.Problem != "":
					fmt.Fprintf(w, "  ✗ %s\n      %s\n", step.What, step.Problem)
				case step.Done != "":
					fmt.Fprintf(w, "  ✓ %s\n      %s\n", step.What, step.Done)
				}
			}
			fmt.Fprintf(w, "\n%s\n", result.Message)
			if !result.Healthy {
				return errors.New("repair could not fix everything")
			}
			return nil
		})
}

func diagnosticsCommand(args []string, stdout io.Writer) error {
	return withClient("diagnostics", args, stdout,
		func(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
			var result struct {
				Path     string   `json:"path"`
				Bytes    int64    `json:"bytes"`
				Excludes []string `json:"excludes"`
				Message  string   `json:"message"`
			}
			if err := c.Post(ctx, "/system/diagnostics", nil, &result); err != nil {
				return err
			}
			if o.asJSON {
				return printResponse(w, c, result)
			}
			fmt.Fprintf(w, "%s\n\n%s (%s)\n\nIt does not contain:\n",
				result.Message, result.Path, humanBytes(uint64(result.Bytes)))
			for _, excluded := range result.Excludes {
				fmt.Fprintf(w, "  - %s\n", excluded)
			}
			return nil
		})
}

// --- Network ----------------------------------------------------------------------

func networkCommand(args []string, stdout io.Writer) error {
	action, rest := defaultTo(args, "status")
	switch action {
	case "status":
		return withClient("network status", rest, stdout, networkStatus)
	case "wifi":
		return wifiCommand(rest, stdout)
	case "wake-on-lan":
		return wakeOnLANCommand(rest, stdout)
	default:
		return usageError{fmt.Errorf(
			"unknown network command %q — try status, wifi or wake-on-lan", action)}
	}
}

// wrapAt breaks a sentence over lines no longer than width.
//
// Needed because this is one of the few places where the words come from the
// server rather than from this source file, so they cannot be wrapped by hand
// where they are written. Terminals are not all 80 columns and this does not
// ask: 72 is narrow enough to survive an ssh session in a small window, which is
// where a server is usually being talked to.
func wrapAt(text string, width int) string {
	var out strings.Builder
	column := 0
	for i, word := range strings.Fields(text) {
		switch {
		case i == 0:
		case column+1+len(word) > width:
			out.WriteString("\n")
			column = 0
		default:
			out.WriteString(" ")
			column++
		}
		out.WriteString(word)
		column += len(word)
	}
	return out.String()
}

// wakeOnLANCommand lets a magic packet start this server, or stops it.
//
// `homebasectl network wake-on-lan <interface> [on|off]`, defaulting to on,
// because somebody typing this has read that their server can be woken and is
// trying to make that true.
func wakeOnLANCommand(args []string, stdout io.Writer) error {
	return withClient("network wake-on-lan", args, stdout,
		func(ctx context.Context, c *Client, o *options, rest []string, w io.Writer) error {
			if len(rest) == 0 {
				return usageError{errors.New(
					"which network card? Run `homebasectl network` to see them")}
			}
			enabled := true
			switch {
			case len(rest) == 1:
			case rest[1] == "on":
			case rest[1] == "off":
				enabled = false
			default:
				return usageError{fmt.Errorf("say on or off, not %q", rest[1])}
			}

			var result struct {
				Interface string `json:"interface"`
				Enabled   bool   `json:"enabled"`
				Note      string `json:"note"`
			}
			if err := c.Post(ctx, "/network/wake-on-lan", map[string]any{
				"interface": rest[0],
				"enabled":   enabled,
			}, &result); err != nil {
				return err
			}
			if o.asJSON {
				return printResponse(w, c, result)
			}

			if !result.Enabled {
				fmt.Fprintf(w, "%s will no longer start this server.\n", result.Interface)
				return nil
			}
			fmt.Fprintf(w, "%s will now start this server when a magic packet "+
				"arrives, and after a restart too.\n\n", result.Interface)
			fmt.Fprintf(w, "Try it: shut the server down, then from another machine on\n"+
				"the same network run\n\n    homebasectl wake <its MAC address>\n\n")
			if result.Note != "" {
				fmt.Fprintln(w, wrapAt(result.Note, 72))
			}
			return nil
		})
}

func networkStatus(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
	var status struct {
		Hostname   string `json:"hostname"`
		MDNSName   string `json:"mdns_name"`
		MDNSWorks  bool   `json:"mdns_works"`
		Gateway    string `json:"gateway"`
		Online     bool   `json:"online"`
		Reachable  bool   `json:"reachable"`
		Interfaces []struct {
			Name               string   `json:"name"`
			Kind               string   `json:"kind"`
			Up                 bool     `json:"up"`
			Addresses          []string `json:"addresses"`
			MAC                string   `json:"mac"`
			WakeOnLAN          bool     `json:"wake_on_lan"`
			WakeOnLANSupported bool     `json:"wake_on_lan_supported"`
			WakeOnLANKnown     bool     `json:"wake_on_lan_known"`
		} `json:"interfaces"`
	}
	if err := c.Get(ctx, "/network", &status); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, status)
	}

	fmt.Fprintf(w, "Name:     %s", status.Hostname)
	if status.MDNSWorks {
		fmt.Fprintf(w, "  (https://%s)", status.MDNSName)
	}
	fmt.Fprintln(w)
	switch {
	case !status.Reachable:
		fmt.Fprintln(w, "Network:  not connected — this server has no address")
	case !status.Online:
		fmt.Fprintln(w, "Network:  connected, but the internet is not reachable")
	default:
		fmt.Fprintln(w, "Network:  connected")
	}

	for _, iface := range status.Interfaces {
		if iface.Kind == "loopback" || iface.Kind == "container" {
			continue
		}
		fmt.Fprintf(w, "  %-8s %-9s %s\n", iface.Name, iface.Kind,
			strings.Join(iface.Addresses, ", "))
		if iface.MAC != "" {
			// The hardware address is what a router lists this machine under,
			// and what a wake-up packet is addressed to — which is worth knowing
			// before the machine is asleep, because nothing on a sleeping
			// machine can tell you afterwards.
			// Three states, and the middle one is the only actionable one.
			wake := "Homebase cannot tell whether this card can be woken"
			switch {
			case !iface.WakeOnLANKnown:
				// Left as it is. Saying "cannot be woken" here was a confident
				// false statement about hardware that supports it perfectly well.
			case !iface.WakeOnLANSupported:
				wake = "this card cannot be woken by a network packet"
			case iface.WakeOnLAN:
				wake = "can be woken with: homebasectl wake " + normaliseMAC(iface.MAC)
			case iface.WakeOnLANSupported:
				// The command Homebase can run, rather than the ethtool
				// incantation this used to print. `ethtool -s` was also wrong in
				// a way nobody would notice until it mattered: it lasts until
				// the machine is restarted, which is the one moment the setting
				// exists for.
				wake = "could be woken — homebasectl network wake-on-lan " + iface.Name
			}
			fmt.Fprintf(w, "           %s — %s\n", normaliseMAC(iface.MAC), wake)
		}
	}
	return nil
}

func wifiCommand(args []string, stdout io.Writer) error {
	action, rest := defaultTo(args, "status")
	switch action {
	case "status":
		return withClient("network wifi status", rest, stdout,
			func(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
				var status any
				if err := c.Get(ctx, "/network/wifi", &status); err != nil {
					return err
				}
				return printResponse(w, c, status)
			})
	case "scan":
		return withClient("network wifi scan", rest, stdout, scanWifi)
	case "join":
		return withClient("network wifi join", rest, stdout, joinWifi)
	default:
		return usageError{fmt.Errorf("unknown wifi command %q — try status, scan or join", action)}
	}
}

func scanWifi(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
	var reply struct {
		Networks []struct {
			SSID     string `json:"ssid"`
			Bars     int    `json:"bars"`
			Security string `json:"security"`
			Current  bool   `json:"current"`
		} `json:"networks"`
		Message string `json:"message"`
	}
	if err := c.Post(ctx, "/network/wifi/scan", nil, &reply); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, reply)
	}
	if len(reply.Networks) == 0 {
		fmt.Fprintln(w, reply.Message)
		return nil
	}
	rows := [][]string{{"NETWORK", "SIGNAL", "SECURITY", ""}}
	for _, network := range reply.Networks {
		here := ""
		if network.Current {
			here = "connected"
		}
		rows = append(rows, []string{network.SSID,
			strings.Repeat("|", network.Bars), network.Security, here})
	}
	writeTable(w, rows)
	return nil
}

// joinWifi takes the password from the environment or a prompt, never from a
// flag.
//
// A password on the command line is in the shell history, in `ps` output for
// every user on the machine while it runs, and in whatever collects process
// arguments. There is no flag for it and there should not be.
func joinWifi(ctx context.Context, c *Client, o *options, rest []string, w io.Writer) error {
	if len(rest) != 1 {
		return usageError{errors.New(
			"which network? — homebasectl network wifi join \"NAME\"\n" +
				"The password is read from HOMEBASE_WIFI_PASSWORD, or asked for.")}
	}

	passphrase := os.Getenv("HOMEBASE_WIFI_PASSWORD")
	if passphrase == "" {
		var err error
		passphrase, err = askForSecret("Wi-Fi password (leave empty for an open network): ")
		if err != nil {
			return err
		}
	}

	var status any
	if err := c.Post(ctx, "/network/wifi",
		map[string]any{"ssid": rest[0], "passphrase": passphrase}, &status); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, status)
	}
	fmt.Fprintf(w, "Joined %s.\n", rest[0])
	return nil
}

// --- System -----------------------------------------------------------------------

// systemHistory shows how hot the machine has been, as a chart.
//
// A chart in a terminal rather than a table of numbers, because every question
// worth asking here is about shape: is it hotter than it was, does it climb
// whenever something transcodes, did cleaning the fan help. A column of two
// thousand readings answers none of those and a line does.
func systemHistory(ctx context.Context, c *Client, o *options, args []string, w io.Writer) error {
	days := 7
	if len(args) > 0 {
		parsed, err := strconv.Atoi(args[0])
		if err != nil || parsed < 1 || parsed > 365 {
			return usageError{fmt.Errorf("how many days? — homebasectl system history 7")}
		}
		days = parsed
	}

	var history struct {
		Samples []struct {
			Time    string `json:"time"`
			Celsius *int   `json:"celsius"`
			FanRPM  *int   `json:"fan_rpm"`
		} `json:"samples"`
		Hottest   *int   `json:"hottest_celsius"`
		Coolest   *int   `json:"coolest_celsius"`
		Average   *int   `json:"average_celsius"`
		Loudest   *int   `json:"loudest_rpm"`
		Quietest  *int   `json:"quietest_rpm"`
		Since     string `json:"since"`
		Recording bool   `json:"recording"`
	}
	if err := c.Get(ctx, fmt.Sprintf("/system/history?days=%d&points=240", days), &history); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, history)
	}

	if len(history.Samples) == 0 {
		// The two empty cases are different and only one needs doing something
		// about, so they are not given the same sentence.
		if history.Recording {
			fmt.Fprintf(w, "Nothing recorded in the last %d days.\n", days)
			fmt.Fprintln(w, "\nThe server was probably switched off. Readings are taken "+
				"every five minutes\nwhile it is running.")
			return nil
		}
		fmt.Fprintln(w, "No readings have been recorded yet.")
		fmt.Fprintln(w, "\nThe first one is taken when the server starts, so this "+
			"clears within a\nfew minutes of a restart.")
		return nil
	}

	temperatures := make([]float64, 0, len(history.Samples))
	speeds := make([]float64, 0, len(history.Samples))
	for _, sample := range history.Samples {
		if sample.Celsius != nil {
			temperatures = append(temperatures, float64(*sample.Celsius))
		}
		if sample.FanRPM != nil {
			speeds = append(speeds, float64(*sample.FanRPM))
		}
	}

	fmt.Fprintf(w, "The last %d days — %d readings since %s\n\n",
		days, len(history.Samples), shortTime(history.Since))

	// A chart of three readings is a picture of nothing. Said plainly rather
	// than drawn, because a wall of solid blocks looks like data.
	if len(history.Samples) < 6 {
		fmt.Fprintf(w, "Not enough readings yet to draw a chart — one is taken "+
			"every five minutes.\n\n")
		for _, sample := range history.Samples {
			fmt.Fprintf(w, "  %s  ", shortTime(sample.Time))
			if sample.Celsius != nil {
				fmt.Fprintf(w, "%d °C", *sample.Celsius)
			}
			if sample.FanRPM != nil {
				fmt.Fprintf(w, "  %d rpm", *sample.FanRPM)
			}
			fmt.Fprintln(w)
		}
		return nil
	}

	if len(temperatures) > 0 {
		fmt.Fprintln(w, "Temperature")
		drawChart(w, temperatures, "°C")
		fmt.Fprintf(w, "  hottest %d °C, coolest %d °C, average %d °C\n\n",
			valueOr(history.Hottest), valueOr(history.Coolest), valueOr(history.Average))
	}
	if len(speeds) > 0 {
		fmt.Fprintln(w, "Fan")
		drawChart(w, speeds, "rpm")
		fmt.Fprintf(w, "  loudest %d rpm, quietest %d rpm\n\n",
			valueOr(history.Loudest), valueOr(history.Quietest))
	}

	fmt.Fprintln(w, "The full record is a plain CSV, for anything a chart in a terminal")
	fmt.Fprintln(w, "cannot answer:")
	fmt.Fprintln(w, "\n    /var/log/homebase/thermal.csv")
	return nil
}

func valueOr(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func shortTime(stamp string) string {
	when, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return stamp
	}
	return when.Local().Format("2 Jan 15:04")
}

// drawChart plots a series with block characters.
//
// Eight rows and Unicode blocks rather than a sparkline, because the thing being
// looked for is usually a slow climb over days, and one row of sparkline cannot
// show a five-degree drift that matters. Width is fixed at 72 so it survives an
// ssh session in a small window, which is where a server is usually talked to.
func drawChart(w io.Writer, values []float64, unit string) {
	const width, height = 72, 8
	if len(values) == 0 {
		return
	}

	low, high := values[0], values[0]
	for _, value := range values {
		low = min(low, value)
		high = max(high, value)
	}
	// A flat series has no range to scale against. Centred rather than given a
	// range above it, or a machine that has been perfectly steady draws as a
	// line along the floor — which reads as "it stopped" rather than
	// "it did not change".
	if high-low < 1 {
		low -= 0.5
		high += 0.5
	}

	// Averaged into columns rather than sampled, so a single spike in a week of
	// readings cannot vanish between two chosen points.
	columns := make([]float64, width)
	for i := range columns {
		start := i * len(values) / width
		end := (i + 1) * len(values) / width
		if end <= start {
			end = start + 1
		}
		var total float64
		var count int
		for j := start; j < end && j < len(values); j++ {
			total += values[j]
			count++
		}
		if count > 0 {
			columns[i] = total / float64(count)
		} else {
			columns[i] = low
		}
	}

	blocks := []rune(" ▁▂▃▄▅▆▇█")
	for row := height - 1; row >= 0; row-- {
		// The axis label on the top and bottom rows only. A number against
		// every row is noise on a chart this short.
		label := "     "
		switch row {
		case height - 1:
			label = fmt.Sprintf("%4.0f ", high)
		case 0:
			label = fmt.Sprintf("%4.0f ", low)
		}
		fmt.Fprint(w, label)

		for _, value := range columns {
			// How far into this row the value reaches, as eighths.
			scaled := (value - low) / (high - low) * float64(height)
			within := scaled - float64(row)
			switch {
			case within >= 1:
				fmt.Fprint(w, string(blocks[8]))
			case within <= 0:
				fmt.Fprint(w, " ")
			default:
				fmt.Fprint(w, string(blocks[int(within*8)+1]))
			}
		}
		if row == height-1 {
			fmt.Fprint(w, " "+unit)
		}
		fmt.Fprintln(w)
	}
}

func systemCommand(args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] == "history" {
		return withClient("system history", args[1:], stdout, systemHistory)
	}
	return withClient("system", args, stdout,
		func(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
			var info struct {
				Hostname      string `json:"hostname"`
				OS            string `json:"os"`
				Kernel        string `json:"kernel"`
				UptimeSeconds int64  `json:"uptime_seconds"`
				CPU           struct {
					Model string `json:"model"`
					Cores int    `json:"cores"`
				} `json:"cpu"`
				Memory struct {
					TotalBytes     uint64 `json:"total_bytes"`
					AvailableBytes uint64 `json:"available_bytes"`
				} `json:"memory"`
				LoadAverage [3]float64 `json:"load_average"`
				Temperature struct {
					Celsius *int   `json:"celsius"`
					State   string `json:"state"`
					Message string `json:"message"`
				} `json:"temperature"`
				Fan struct {
					RPM        *int   `json:"rpm"`
					Percent    *int   `json:"percent"`
					Controlled string `json:"controlled"`
					Message    string `json:"message"`
				} `json:"fan"`
			}
			if err := c.Get(ctx, "/system", &info); err != nil {
				return err
			}
			if o.asJSON {
				return printResponse(w, c, info)
			}

			fmt.Fprintf(w, "%s — %s, %s\n", info.Hostname, info.OS, info.Kernel)
			fmt.Fprintf(w, "Up:      %s\n", humanDuration(info.UptimeSeconds))
			fmt.Fprintf(w, "CPU:     %s (%d cores), load %.2f\n",
				info.CPU.Model, info.CPU.Cores, info.LoadAverage[0])
			fmt.Fprintf(w, "Memory:  %s free of %s\n",
				humanBytes(info.Memory.AvailableBytes), humanBytes(info.Memory.TotalBytes))
			if info.Temperature.Celsius != nil {
				fmt.Fprintf(w, "Heat:    %d °C (%s)\n",
					*info.Temperature.Celsius, info.Temperature.State)
			}
			// Beside the temperature, because neither number means anything
			// alone: loud and cool is a fan fault, loud and hot is a dust
			// problem, and they sound identical from across a room.
			if info.Fan.RPM != nil || info.Fan.Percent != nil {
				fmt.Fprint(w, "Fan:     ")
				switch {
				case info.Fan.RPM != nil && info.Fan.Percent != nil:
					fmt.Fprintf(w, "%d rpm (%d%%)", *info.Fan.RPM, *info.Fan.Percent)
				case info.Fan.RPM != nil:
					fmt.Fprintf(w, "%d rpm", *info.Fan.RPM)
				default:
					fmt.Fprintf(w, "%d%%", *info.Fan.Percent)
				}
				if info.Fan.Controlled != "" {
					fmt.Fprintf(w, ", %s controlled", info.Fan.Controlled)
				}
				fmt.Fprintln(w)
			}
			for _, message := range []string{info.Temperature.Message, info.Fan.Message} {
				if message != "" {
					fmt.Fprintf(w, "\n%s\n", message)
				}
			}
			return nil
		})
}

// --- Formatting -------------------------------------------------------------------

func writeTable(w io.Writer, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	for _, row := range rows {
		var line strings.Builder
		for i, cell := range row {
			if i == len(row)-1 {
				line.WriteString(cell)
				break
			}
			line.WriteString(cell)
			line.WriteString(strings.Repeat(" ", widths[i]-len(cell)+2))
		}
		fmt.Fprintln(w, strings.TrimRight(line.String(), " "))
	}
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTP"[exp])
}

func humanDuration(seconds int64) string {
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	minutes := (seconds % 3600) / 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d days, %d hours", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d hours, %d minutes", hours, minutes)
	default:
		return fmt.Sprintf("%d minutes", minutes)
	}
}

// --- Remote access ------------------------------------------------------------------

func vpnCommand(args []string, stdout io.Writer) error {
	action, rest := defaultTo(args, "status")
	switch action {
	case "status", "list":
		return withClient("vpn status", rest, stdout, vpnStatus)
	case "setup":
		return withClient("vpn setup", rest, stdout, vpnSetup)
	case "add-device":
		return withClient("vpn add-device", rest, stdout, vpnAddDevice)
	case "remove-device":
		return withClient("vpn remove-device", rest, stdout, vpnRemoveDevice)
	case "dns":
		return withClient("vpn dns", rest, stdout, vpnDNS)
	case "off", "disable":
		return withClient("vpn off", rest, stdout, vpnDisable)
	default:
		return usageError{fmt.Errorf("unknown vpn command %q — try status, setup, "+
			"add-device, remove-device, dns or off", action)}
	}
}

// vpnDisable closes the way in from outside.
//
// It existed as a promise before it existed as a command: `vpn.setup` named
// `vpn.disable` as its rollback and nothing implemented it, so there was a way
// to open a port to the internet and none to shut it.
func vpnDisable(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
	var status vpnStatusReply
	if err := c.Post(ctx, "/network/vpn/disable", map[string]any{}, &status); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, status)
	}
	fmt.Fprintln(w, "Remote access is off, and the port is closed.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The devices you have set up keep their keys and will work")
	fmt.Fprintln(w, "again the moment you switch it back on:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "    homebasectl vpn setup <name>")
	return nil
}

type vpnStatusReply struct {
	Configured bool   `json:"configured"`
	Running    bool   `json:"running"`
	Hostname   string `json:"hostname"`
	Port       int    `json:"port"`
	Devices    []struct {
		Name          string `json:"name"`
		Address       string `json:"address"`
		LastHandshake string `json:"last_handshake"`
	} `json:"devices"`
	EverConnected bool   `json:"ever_connected"`
	Message       string `json:"message"`
}

func vpnStatus(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
	var status vpnStatusReply
	if err := c.Get(ctx, "/network/vpn", &status); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, status)
	}

	if !status.Configured {
		fmt.Fprintln(w, status.Message)
		return nil
	}

	fmt.Fprintf(w, "Reachable at: %s:%d\n", status.Hostname, status.Port)
	fmt.Fprintf(w, "Running:      %v\n", status.Running)

	if len(status.Devices) > 0 {
		fmt.Fprintln(w)
		rows := [][]string{{"DEVICE", "ADDRESS", "LAST CONNECTED"}}
		for _, device := range status.Devices {
			when := device.LastHandshake
			if when == "" {
				when = "never"
			}
			rows = append(rows, []string{device.Name, device.Address, when})
		}
		writeTable(w, rows)
	}

	if status.Message != "" {
		fmt.Fprintf(w, "\n%s\n", status.Message)
	}
	return nil
}

func vpnSetup(ctx context.Context, c *Client, o *options, rest []string, w io.Writer) error {
	if len(rest) != 1 {
		return usageError{errors.New(
			"what name will devices connect to? — homebasectl vpn setup NAME\n" +
				"A dynamic DNS name like yours.duckdns.org, or a fixed address.")}
	}

	var status vpnStatusReply
	if err := c.Post(ctx, "/network/vpn", map[string]any{"hostname": rest[0]}, &status); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, status)
	}

	fmt.Fprintf(w, "Remote access is on, at %s:%d.\n\n", status.Hostname, status.Port)
	// The part that cannot be automated, said plainly rather than left to be
	// discovered when the first device fails to connect.
	fmt.Fprintf(w, "One thing is left, and Homebase cannot do it: forward UDP port %d\n",
		status.Port)
	fmt.Fprintln(w, "on your router to this server. Look for \"port forwarding\" in its")
	fmt.Fprintln(w, "settings.")
	fmt.Fprintln(w, "\nThen add a device:\n\n    sudo homebasectl vpn add-device phone")
	return nil
}

// vpnAddDevice prints a key that is never stored and cannot be shown again.
func vpnAddDevice(ctx context.Context, c *Client, o *options, rest []string, w io.Writer) error {
	if len(rest) != 1 {
		return usageError{errors.New(
			"what should the device be called? — homebasectl vpn add-device NAME")}
	}

	var device struct {
		Name    string `json:"name"`
		Address string `json:"address"`
		Config  string `json:"config"`
		QRCode  string `json:"qr_code"`
		Message string `json:"message"`
	}
	if err := c.Post(ctx, "/network/vpn/devices",
		map[string]any{"name": rest[0]}, &device); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, device)
	}

	fmt.Fprintf(w, "%s — %s\n\n", device.Name, device.Address)
	if device.QRCode != "" {
		fmt.Fprintln(w, device.QRCode)
		fmt.Fprintln(w, "Scan that with the WireGuard app on a phone.")
		fmt.Fprintln(w, "\nOr save this as a file and import it:")
	} else {
		fmt.Fprintln(w, "Save this as a .conf file and import it into WireGuard:")
	}
	fmt.Fprintf(w, "\n%s\n", device.Config)
	fmt.Fprintf(w, "%s\n", device.Message)
	return nil
}

func vpnRemoveDevice(ctx context.Context, c *Client, o *options, rest []string, w io.Writer) error {
	if len(rest) != 1 {
		return usageError{errors.New(
			"which device? — homebasectl vpn remove-device NAME")}
	}

	var status vpnStatusReply
	if err := c.Post(ctx, "/network/vpn/devices/remove",
		map[string]any{"name": rest[0]}, &status); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, status)
	}
	fmt.Fprintf(w, "%s can no longer reach this server from outside.\n", rest[0])
	return nil
}

// vpnDNS sets or reports the name that has to keep pointing at the house.
//
// The token comes from the environment or a prompt, never an argument — it is a
// credential for changing where a name points, and an argument is in the shell
// history and in `ps` for every user on the machine.
func vpnDNS(ctx context.Context, c *Client, o *options, rest []string, w io.Writer) error {
	var status struct {
		Configured  bool   `json:"configured"`
		Provider    string `json:"provider"`
		Name        string `json:"name"`
		Enabled     bool   `json:"enabled"`
		Working     bool   `json:"working"`
		LastChecked string `json:"last_checked"`
		Detail      string `json:"detail"`
	}

	// The document to print for --json.
	//
	// Usually the whole response, which is what printResponse gives. Not here
	// when reading: the name's state is one field *inside* the VPN status, and
	// printing the enclosing document would answer a different question from the
	// one asked. So the field's own bytes are kept — still the server's, just
	// the right part of them.
	var answer json.RawMessage

	switch {
	case len(rest) == 0:
		var whole struct {
			DNS json.RawMessage `json:"dns"`
		}
		if err := c.Get(ctx, "/network/vpn", &whole); err != nil {
			return err
		}
		if err := json.Unmarshal(whole.DNS, &status); err != nil {
			return err
		}
		answer = whole.DNS
	case len(rest) == 1 && rest[0] == "off":
		if err := c.Post(ctx, "/network/vpn/dns/clear", nil, &status); err != nil {
			return err
		}
	case len(rest) == 2:
		token := os.Getenv("HOMEBASE_DNS_TOKEN")
		if token == "" {
			var err error
			token, err = askForSecret("The token from " + rest[0] + ": ")
			if err != nil {
				return err
			}
		}
		if err := c.Post(ctx, "/network/vpn/dns", map[string]any{
			"provider": rest[0], "name": rest[1], "token": token,
		}, &status); err != nil {
			return err
		}
	default:
		return usageError{errors.New(
			"homebasectl vpn dns                    what the name is doing\n" +
				"homebasectl vpn dns duckdns NAME       keep NAME pointing here\n" +
				"homebasectl vpn dns off                stop\n\n" +
				"The token is read from HOMEBASE_DNS_TOKEN, or asked for.")}
	}

	if o.asJSON {
		if len(answer) > 0 {
			var pretty any
			if err := json.Unmarshal(answer, &pretty); err != nil {
				return err
			}
			return printJSON(w, pretty)
		}
		return printResponse(w, c, status)
	}

	if !status.Configured {
		fmt.Fprintln(w, "No name is being kept up to date.")
		fmt.Fprintln(w, "\nIf your home address changes, devices will stop being able to")
		fmt.Fprintln(w, "find this server. Set one up with:")
		fmt.Fprintln(w, "\n    sudo homebasectl vpn dns duckdns yourname")
		return nil
	}

	fmt.Fprintf(w, "Name:     %s (%s)\n", status.Name, status.Provider)
	fmt.Fprintf(w, "Updating: %v\n", status.Enabled)
	if status.LastChecked != "" {
		outcome := "worked"
		if !status.Working {
			outcome = "FAILED"
		}
		fmt.Fprintf(w, "Last try: %s — %s\n", status.LastChecked, outcome)
	}
	if status.Detail != "" {
		fmt.Fprintf(w, "\n%s\n", status.Detail)
	}
	return nil
}
