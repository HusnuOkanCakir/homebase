package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/HusnuOkanCakir/homebase/internal/auth"
	"github.com/HusnuOkanCakir/homebase/internal/store"
)

// Granting the right to talk to a model with its refusals removed.
//
// This is the only permission Homebase has that is not granted by setting the
// machine up, and the only one with no route through the API. That is
// deliberate: an endpoint that grants a permission is an endpoint a stolen
// session can call, and the whole value of this particular permission is that
// acquiring it has to be a decision somebody made at the machine.
//
// So it works like `wake` — a local command that talks to nothing. It opens the
// database directly, which means the file's own permissions decide who may run
// it, which means root. There is no token to steal and no request to forge.

const assistantUsage = `homebasectl assistant unrestricted USER [on|off]

  Grant or withdraw the right to use a model whose refusal behaviour was
  removed by a third party.

  Not available through the dashboard or the API, by design. Run it on the
  server, as root:

      sudo homebasectl assistant unrestricted alex on
      sudo homebasectl assistant unrestricted alex off
      sudo homebasectl assistant unrestricted alex        # just report
`

func assistantCommand(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("assistant", flag.ContinueOnError)
	flags.SetOutput(stdout)
	dbPath := flags.String("db", "/var/lib/homebase/homebase.db", "the Homebase database")
	if err := flags.Parse(reorderFlags(args)); err != nil {
		return usageError{err}
	}

	rest := flags.Args()
	if len(rest) < 2 || rest[0] != "unrestricted" {
		return usageError{errors.New(assistantUsage)}
	}
	username := rest[1]

	var wanted *bool
	if len(rest) > 2 {
		switch rest[2] {
		case "on", "yes", "true":
			value := true
			wanted = &value
		case "off", "no", "false":
			value := false
			wanted = &value
		default:
			return usageError{fmt.Errorf("say `on` or `off`, not %q", rest[2])}
		}
	}

	ctx := context.Background()
	database, err := store.Open(ctx, *dbPath)
	if err != nil {
		if os.IsPermission(errors.Unwrap(err)) || os.IsPermission(err) {
			return fmt.Errorf("cannot open %s: run this as root on the server", *dbPath)
		}
		return fmt.Errorf("opening %s: %w", *dbPath, err)
	}
	defer database.Close()

	permissions, err := readPermissions(ctx, database.DB(), username)
	if err != nil {
		return err
	}

	has := slices.Contains(permissions, auth.PermAssistantUnrestricted)

	if wanted == nil {
		if has {
			fmt.Fprintf(stdout, "%s may use the unrestricted model.\n", username)
		} else {
			fmt.Fprintf(stdout, "%s may not use the unrestricted model.\n", username)
		}
		return nil
	}

	if *wanted == has {
		fmt.Fprintf(stdout, "No change: %s already %s.\n", username,
			map[bool]string{true: "has it", false: "does not have it"}[has])
		return nil
	}

	if *wanted {
		permissions = append(permissions, auth.PermAssistantUnrestricted)
	} else {
		permissions = slices.DeleteFunc(permissions, func(p string) bool {
			return p == auth.PermAssistantUnrestricted
		})
	}
	if err := writePermissions(ctx, database.DB(), username, permissions); err != nil {
		return err
	}

	if *wanted {
		fmt.Fprintf(stdout, "%s may now use the unrestricted model.\n\n", username)
		// Said every time it is granted, because the model is contained and the
		// person using it is not.
		fmt.Fprintln(stdout, "It stays contained: no network, no sight of your files,")
		fmt.Fprintln(stdout, "and Homebase cannot start it. It appears in the dashboard")
		fmt.Fprintln(stdout, "only while it is running, and starting it is a command on")
		fmt.Fprintln(stdout, "this machine.")
	} else {
		fmt.Fprintf(stdout, "%s may no longer use the unrestricted model.\n", username)
	}
	// Existing sessions carry their permissions from the database on each
	// request, so withdrawing takes effect immediately rather than at next login.
	return nil
}

func readPermissions(ctx context.Context, db *sql.DB, username string) ([]string, error) {
	var raw string
	err := db.QueryRowContext(ctx,
		`SELECT permissions FROM users WHERE username = ?`, username).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no account called %q on this server", username)
	}
	if err != nil {
		return nil, err
	}
	var permissions []string
	if err := json.Unmarshal([]byte(raw), &permissions); err != nil {
		return nil, fmt.Errorf("the account's permissions are not readable: %w", err)
	}
	return permissions, nil
}

func writePermissions(ctx context.Context, db *sql.DB, username string, permissions []string) error {
	encoded, err := json.Marshal(permissions)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx,
		`UPDATE users SET permissions = ? WHERE username = ?`, string(encoded), username)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("no account called %q on this server", username)
	}
	return nil
}
