package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
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
	default:
		return usageError{fmt.Errorf("unknown apps command %q — try list, install, "+
			"start, stop, restart, uninstall, logs", action)}
	}
}

type application struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Version string `json:"version,omitempty"`
	URL     string `json:"url,omitempty"`
	Health  string `json:"health,omitempty"`
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
	for _, app := range reply.Items {
		rows = append(rows, []string{app.ID, app.State, app.Version, app.URL})
	}
	writeTable(w, rows)
	return nil
}

func actOnApp(ctx context.Context, c *Client, o *options, action string,
	names []string, w io.Writer) error {
	if len(names) != 1 {
		return usageError{fmt.Errorf("which application? — homebasectl apps %s NAME", action)}
	}
	id := names[0]

	body := map[string]any{}
	// Uninstalling asks for the name back. The API checks it again, and so does
	// hostd: this is not the confirmation, it is passing one along.
	if action == "uninstall" {
		body["confirm"] = id
	}

	var job jobReply
	path := "/apps/" + id + "/" + action
	if action == "install" {
		path = "/apps/" + id + "/install"
	}
	if err := c.Post(ctx, path, body, &job); err != nil {
		return err
	}
	return followJob(ctx, c, o, job, w)
}

func appLogs(ctx context.Context, c *Client, o *options, names []string, w io.Writer) error {
	if len(names) != 1 {
		return usageError{errors.New("which application? — homebasectl apps logs NAME")}
	}
	var reply struct {
		Lines []string `json:"lines"`
	}
	if err := c.Get(ctx, "/apps/"+names[0]+"/logs?lines=200", &reply); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, reply)
	}
	for _, line := range reply.Lines {
		fmt.Fprintln(w, line)
	}
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
	for _, place := range reply.Items {
		connected := "no"
		if place.Mounted {
			connected = "yes"
		}
		rows = append(rows, []string{place.ID, place.Name, connected,
			humanBytes(place.AvailableBytes), humanBytes(place.TotalBytes)})
	}
	writeTable(w, rows)
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
	default:
		return usageError{fmt.Errorf("unknown network command %q — try status or wifi", action)}
	}
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
				wake = "could be woken, but the card has it switched off — " +
					"sudo ethtool -s " + iface.Name + " wol g"
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

func systemCommand(args []string, stdout io.Writer) error {
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
			if info.Temperature.Message != "" {
				fmt.Fprintf(w, "\n%s\n", info.Temperature.Message)
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
	default:
		return usageError{fmt.Errorf("unknown vpn command %q — try status, setup, "+
			"add-device, remove-device or dns", action)}
	}
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
