// Command homebasectl is the console tool for a server nobody can sign in to.
//
// ADR-0015. It exists for one situation: the password is forgotten and the
// recovery code written down at setup is gone too. Somebody with access to the
// machine runs this, reads a fresh code off the screen, and finishes in the
// browser where the password fields already are.
//
// It deliberately does not set passwords. Typing one at a terminal leaves it in
// scrollback and shell history, and would be a second implementation of
// something the dashboard already does correctly. What requires being at the
// machine is proving you are at the machine; the rest belongs where the user
// already knows how to do it.
//
// Nothing here is privileged in the hostd sense. It is an ordinary program that
// opens Homebase's database, and it needs root only because the database is
// readable by the service account and root — which is the same permission
// somebody would need to edit it with sqlite3, less carefully.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/events"
	"github.com/HusnuOkanCakir/homebase/internal/store"
)

const defaultDatabase = "/var/lib/homebase/homebase.db"

// version is stamped in at build time, the same way core's is, and travels onto
// the installation media so a machine can say which stick produced it.
var version = "dev"

func main() {
	err := run(os.Args[1:], os.Stdout, os.Stderr)
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "\n%s\n", err)

	// Exit codes a script can branch on. "Failed" and "used wrongly" and "the
	// server is not there" want different handling, and a caller that cannot
	// tell them apart will eventually treat all three the same way.
	var usage usageError
	switch {
	case errors.As(err, &usage):
		os.Exit(exitUsage)
	case strings.Contains(err.Error(), "not answering on this machine"):
		os.Exit(exitNotAnswering)
	default:
		os.Exit(exitFailed)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	// Shared flags may come before the subcommand as well as after it, because
	// the help has always presented them as global and a flag that is documented
	// and refused is worse than one that does not exist.
	args, err := takeGlobalFlags(args)
	if err != nil {
		return err
	}

	if len(args) == 0 {
		usage(stderr)
		return usageError{errors.New("no command given")}
	}

	switch args[0] {
	// These two read the database directly rather than going through the API,
	// because they exist for a machine whose API cannot be signed into. That is
	// the whole point of them (ADR-0015).
	case "recovery-code":
		return recoveryCode(args[1:], stdout)
	case "list-accounts":
		return listAccounts(args[1:], stdout)
	case "installer":
		return installer(args[1:], stdout, stderr)

	// Everything below is an ordinary API client, with the same permission
	// checks, job records and events as the dashboard.
	case "setup":
		return setupCommand(args[1:], stdout)
	case "system":
		return systemCommand(args[1:], stdout)
	case "apps":
		return appsCommand(args[1:], stdout)
	case "storage":
		return storageCommand(args[1:], stdout)
	case "share":
		return shareCommand(args[1:], stdout)
	case "backup":
		return backupCommand(args[1:], stdout)
	case "update":
		return updateCommand(args[1:], stdout)
	case "network":
		return networkCommand(args[1:], stdout)
	case "vpn":
		return vpnCommand(args[1:], stdout)
	case "factory-reset":
		return factoryResetCommand(args[1:], stdout)
	case "wake":
		return wakeCommand(args[1:], stdout)
	case "shutdown":
		return shutdownCommand(args[1:], stdout)
	case "restart":
		return restartCommand(args[1:], stdout)
	case "repair":
		return repairCommand(args[1:], stdout)
	case "diagnostics":
		return diagnosticsCommand(args[1:], stdout)

	case "-h", "--help", "help":
		usage(stdout)
		return nil
	default:
		usage(stderr)
		return usageError{fmt.Errorf("unknown command %q", args[0])}
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `homebasectl — Homebase from a terminal

  homebasectl setup NAME
        Create the first administrator, on a server that has none. The
        password is read from HOMEBASE_PASSWORD or asked for. Prints a
        recovery code, once — write it down.

  homebasectl system
        What this machine is: version, uptime, memory, load, temperature.

  homebasectl apps [list]
  homebasectl apps install|start|stop|restart|uninstall NAME
  homebasectl apps logs NAME
        The application catalogue, and what is installed from it.

  homebasectl apps storage APP [SLOT DISK]
  homebasectl system history [DAYS]
  homebasectl share [status]
  homebasectl share add NAME DISK
  homebasectl share password NAME
  homebasectl storage [list]
  homebasectl storage disks
  homebasectl storage format /dev/sdX [--name NAME]
  homebasectl storage attach UUID NAME
  homebasectl storage detach NAME
        The disks Homebase manages, and every disk it can see. Formatting
        shows what is on the disk first and then asks; it destroys
        everything on it.

  homebasectl shutdown
  homebasectl restart
        Switch this server off, or restart it. Both say what stops and
        what starts again by itself; "shutdown" also says whether this
        machine can be switched back on without walking to it.

  homebasectl factory-reset
        Remove every account and every setting. Your files are kept.

  homebasectl backup list DISK
  homebasectl backup now DISK
  homebasectl backup restore ID --from DISK
  homebasectl backup schedule [daily|weekly|off [DISK]]
        Backups. With no arguments, "schedule" reports the one in force —
        including whether systemd is actually running it, and how the last
        one went.

  homebasectl update [status]
  homebasectl update check
  homebasectl update apply
        Where updates come from, whether one is waiting, and applying it.

  homebasectl network [status]
  homebasectl network wifi scan
  homebasectl network wifi join "NETWORK NAME"
        How this server is connected. The Wi-Fi password is read from
        HOMEBASE_WIFI_PASSWORD or asked for — never passed as an argument,
        because arguments are visible in ps and in shell history.

  homebasectl vpn [status]
  homebasectl vpn setup yours.duckdns.org
  homebasectl vpn add-device phone
  homebasectl vpn remove-device phone
        Reaching this server from outside the house, over WireGuard. Adding
        a device prints its configuration and a QR code — once. It is stored
        nowhere, so if it is lost, remove the device and add it again.

  homebasectl wake AA:BB:CC:DD:EE:FF
        Wake a sleeping machine on this network. Useful over the VPN, to
        start the desktop at home from somewhere else. It talks to nothing
        and needs no privilege: a wake-up packet is an ordinary broadcast.

  homebasectl repair
        Check a short list of things that are often wrong and put right what
        it can. Deletes nothing.

  homebasectl diagnostics
        Write a file describing this server, safe to send to somebody. It
        prints what the file does not contain.

  homebasectl recovery-code [--user NAME]
        Create a new recovery code and print it. The previous one stops
        working. For a server nobody can sign in to.

  homebasectl list-accounts
        Show the accounts on this server.

  homebasectl installer ...
        Make Homebase installation media.
        Run "homebasectl installer help" for what it can do.

Options:
  --confirm VALUE   Agree to something irreversible without being asked. It
                    must name the thing itself — the backup\'s id, the
                    disk\'s device, the server\'s name. There is no --yes.
  --json            Print the server\'s answer as JSON, unmodified. This is
                    the interface to build on; the readable form is not.
  --address URL     The server to talk to (default `+defaultAddress+`)
  --database PATH   Where Homebase keeps its database
                    (default `+defaultDatabase+`)

Authentication:
  Run as root and homebasectl reads the database to authenticate itself,
  which is what root can do anyway. Otherwise it needs a token in
  HOMEBASE_TOKEN or `+configPath()+`.

Exit codes:
  0  it worked          2  the command was used wrongly
  1  it failed          3  Homebase is not answering on this machine
`)
}

func recoveryCode(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("recovery-code", flag.ContinueOnError)
	database := flags.String("database", defaultDatabase, "path to the Homebase database")
	username := flags.String("user", "", "which account; the only one, if there is only one")
	if err := flags.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	service, recorder, closeDB, err := open(ctx, *database)
	if err != nil {
		return err
	}
	defer closeDB()

	name, err := chooseAccount(ctx, service, *username)
	if err != nil {
		return err
	}

	user, err := service.UserByName(ctx, name)
	if err != nil {
		if errors.Is(err, auth.ErrNoSuchUser) {
			return fmt.Errorf("there is no account called %q on this server.\n"+
				"Run `homebasectl list-accounts` to see the accounts there are.", name)
		}
		return err
	}

	code, err := service.IssueRecoveryCode(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("creating a recovery code: %w", err)
	}

	// The same record the dashboard would have written. Somebody reading the
	// event log later must be able to see that this happened, and from where.
	recorder.Warn(ctx, "auth.recovery_code_reissued", user.Username, "console",
		"A new recovery code was created for "+user.Username+
			" from the server's console. The previous one no longer works.")

	fmt.Fprintf(stdout, `
Recovery code for %s

    %s

Write it down. It is shown once and cannot be displayed again.

Next: open Homebase in a browser, choose "I have forgotten my password" on the
sign-in page, and enter this code. You will be asked to choose a new password,
and you will be given a replacement code to keep.

Anyone holding this code can take over the server, so keep it as you would a
spare key.
`, user.Username, code)

	return nil
}

func listAccounts(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("list-accounts", flag.ContinueOnError)
	database := flags.String("database", defaultDatabase, "path to the Homebase database")
	if err := flags.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	service, _, closeDB, err := open(ctx, *database)
	if err != nil {
		return err
	}
	defer closeDB()

	names, err := service.Usernames(ctx)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Fprintln(stdout, "This server has no accounts yet. Open it in a browser to set it up.")
		return nil
	}

	for _, name := range names {
		fmt.Fprintln(stdout, name)
	}
	return nil
}

// chooseAccount resolves which account is meant.
//
// With one account there is no question to ask, and asking it of somebody who
// is already having a bad day is not helpful. With several, it refuses rather
// than guessing: resetting the wrong one wastes the trip to the machine.
func chooseAccount(ctx context.Context, service *auth.Service, requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}

	names, err := service.Usernames(ctx)
	if err != nil {
		return "", err
	}

	switch len(names) {
	case 0:
		return "", errors.New("this server has no accounts yet. Open it in a browser to set it up.")
	case 1:
		return names[0], nil
	default:
		return "", fmt.Errorf("this server has several accounts (%s).\n"+
			"Say which one: homebasectl recovery-code --user NAME",
			strings.Join(names, ", "))
	}
}

func open(ctx context.Context, path string) (*auth.Service, *events.Recorder, func(), error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, fmt.Errorf(
				"no Homebase database at %s.\n"+
					"If Homebase keeps its data somewhere else, say where: --database PATH", path)
		}
		if os.IsPermission(err) {
			return nil, nil, nil, fmt.Errorf(
				"cannot read %s. Run this with sudo.", path)
		}
		return nil, nil, nil, err
	}

	db, err := store.Open(ctx, path)
	if err != nil {
		if os.IsPermission(errors.Unwrap(err)) || strings.Contains(err.Error(), "permission denied") {
			return nil, nil, nil, fmt.Errorf("cannot open %s. Run this with sudo.", path)
		}
		return nil, nil, nil, fmt.Errorf("opening %s: %w", path, err)
	}

	// Warnings and above, to stderr: this is a tool somebody is reading the
	// output of, and the useful output is the code.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	return auth.NewService(db.DB()),
		events.NewRecorder(db.DB(), log),
		func() { _ = db.Close() },
		nil
}
