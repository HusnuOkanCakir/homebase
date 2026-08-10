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

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "\n%s\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usage(stderr)
		return errors.New("no command given")
	}

	switch args[0] {
	case "recovery-code":
		return recoveryCode(args[1:], stdout)
	case "list-accounts":
		return listAccounts(args[1:], stdout)
	case "-h", "--help", "help":
		usage(stdout)
		return nil
	default:
		usage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `homebasectl — Homebase console tool

  homebasectl recovery-code [--user NAME]
        Create a new recovery code for an account and print it. The previous
        code stops working. Use the code on the sign-in page, under "I have
        forgotten my password", to choose a new password.

  homebasectl list-accounts
        Show the accounts on this server.

Options:
  --database PATH   Where Homebase keeps its database
                    (default `+defaultDatabase+`)

Run as root: the database belongs to the Homebase service account.
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
