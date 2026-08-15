package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Sharing folders onto the local network.
//
//	homebasectl share                          what is shared, and how to open it
//	homebasectl share add backup internal      share a folder
//	homebasectl share password okan            let somebody open it
//	homebasectl share remove backup            stop sharing; the files stay
//
// The status output is most of the value here. SMB is a protocol every device
// speaks and nobody remembers the syntax of — the difference between a server
// somebody uses and one they mean to get round to is whether the exact thing to
// type is on the screen.

func shareCommand(args []string, stdout io.Writer) error {
	action, rest := defaultTo(args, "status")
	switch action {
	case "status":
		return withClient("share status", rest, stdout, shareStatus)
	case "add":
		return withClient("share add", rest, stdout, addShare)
	case "remove":
		return withClient("share remove", rest, stdout, removeShare)
	case "password":
		return withClient("share password", rest, stdout, setSharePassword)
	case "forget":
		return withClient("share forget", rest, stdout, removeShareUser)
	default:
		return usageError{fmt.Errorf("unknown share command %q — try status, add, "+
			"remove, password or forget", action)}
	}
}

type shareStatusReply struct {
	Installed  bool     `json:"installed"`
	Running    bool     `json:"running"`
	ServerName string   `json:"server_name"`
	Users      []string `json:"users"`
	Shares     []struct {
		Name      string `json:"name"`
		Location  string `json:"location"`
		Path      string `json:"path"`
		ReadOnly  bool   `json:"read_only"`
		Available bool   `json:"available"`
		Address   string `json:"address"`
	} `json:"shares"`
}

func shareStatus(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
	var status shareStatusReply
	if err := c.Get(ctx, "/shares", &status); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, status)
	}

	if len(status.Shares) == 0 {
		fmt.Fprintln(w, "Nothing is shared from this server.")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Share a folder that any computer in the house can open:")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "    homebasectl share add backup internal")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Run `homebasectl storage` for the list of disks.")
		return nil
	}

	// Said first and plainly. A share that is configured and not being served
	// looks identical from the other end to one that was never made, and the
	// person looking at it is on a laptop that says "cannot find the server".
	if !status.Running {
		fmt.Fprintln(w, "The file server is NOT running, so none of this is reachable.")
		fmt.Fprintln(w)
	}

	rows := [][]string{{"NAME", "DISK", "ACCESS", "STATUS"}}
	for _, share := range status.Shares {
		access := "read and write"
		if share.ReadOnly {
			access = "read only"
		}
		state := "shared"
		if !share.Available {
			state = "disk not connected"
		}
		rows = append(rows, []string{share.Name, share.Location, access, state})
	}
	writeTable(w, rows)

	if len(status.Users) == 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Nobody can open these yet — there is no account to sign in with.")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "    homebasectl share password <name>")
		return nil
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Sign in as: %s\n", strings.Join(prefixed(status.Users), ", "))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "To open it, on a computer on the same network:")
	fmt.Fprintln(w)

	first := status.Shares[0].Name
	server := status.ServerName
	fmt.Fprintf(w, "  Windows   File Explorer, into the address bar:\n")
	fmt.Fprintf(w, "            \\\\%s\\%s\n", server, first)
	fmt.Fprintf(w, "            Right-click This PC to map it as a drive letter.\n\n")
	fmt.Fprintf(w, "  macOS     Finder, Go → Connect to Server:\n")
	fmt.Fprintf(w, "            smb://%s.local/%s\n\n", server, first)
	fmt.Fprintf(w, "  Linux     Files, Other Locations, into Connect to Server:\n")
	fmt.Fprintf(w, "            smb://%s.local/%s\n\n", server, first)
	fmt.Fprintf(w, "            Or mount it permanently:\n")
	fmt.Fprintf(w, "            sudo mount -t cifs //%s.local/%s /mnt/%s \\\n",
		server, first, first)
	fmt.Fprintf(w, "                 -o username=%s%s,uid=$(id -u)\n",
		"hbshare-", status.Users[0])
	return nil
}

// prefixed shows the names as they are actually typed. The prefix is what stops
// a file-sharing password from also being a way to log in to the machine, and
// somebody typing it into Windows needs the whole thing.
func prefixed(users []string) []string {
	out := make([]string, 0, len(users))
	for _, user := range users {
		out = append(out, "hbshare-"+user)
	}
	return out
}

func addShare(ctx context.Context, c *Client, o *options, args []string, w io.Writer) error {
	if len(args) < 2 {
		return usageError{errors.New(
			"which folder, and on which disk? — homebasectl share add backup internal")}
	}

	body := map[string]any{"name": args[0], "location": args[1]}
	if len(args) > 2 && args[2] == "read-only" {
		body["read_only"] = true
	}

	fmt.Fprintln(w, "Setting up file sharing. The first time, this installs the")
	fmt.Fprintln(w, "file server and can take a few minutes.")
	fmt.Fprintln(w)

	var result struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Address string `json:"address"`
		Next    string `json:"next"`
	}
	if err := c.Post(ctx, "/shares", body, &result); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, result)
	}

	fmt.Fprintf(w, "%s is shared.\n\n", result.Name)
	fmt.Fprintf(w, "  On the server:   %s\n", result.Path)
	fmt.Fprintf(w, "  From elsewhere:  %s\n", result.Address)
	if result.Next != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, wrapAt(result.Next, 68))
	}
	return nil
}

func removeShare(ctx context.Context, c *Client, o *options, args []string, w io.Writer) error {
	if len(args) != 1 {
		return usageError{errors.New("which folder? — homebasectl share remove NAME")}
	}
	var result struct {
		Message string `json:"message"`
	}
	if err := c.Post(ctx, "/shares/remove",
		map[string]any{"name": args[0]}, &result); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, result)
	}
	fmt.Fprintln(w, result.Message)
	return nil
}

func setSharePassword(ctx context.Context, c *Client, o *options, args []string, w io.Writer) error {
	if len(args) != 1 {
		return usageError{errors.New(
			"who is this for? — homebasectl share password NAME\n" +
				"The password is read from HOMEBASE_SHARE_PASSWORD, or asked for.")}
	}

	// Asked for rather than taken as an argument. An argument is in the shell
	// history and in /proc for every process on the machine while the command
	// runs, and this one is typed into a laptop and saved there for years.
	//
	// The environment is the way out for a script, and for `ssh host
	// homebasectl ...`, which has no terminal to ask on.
	password := os.Getenv("HOMEBASE_SHARE_PASSWORD")
	if password == "" {
		var err error
		if password, err = askForSecret("A password for opening the shared folders: "); err != nil {
			return err
		}
		again, err := askForSecret("Again: ")
		if err != nil {
			return err
		}
		if password != again {
			return errors.New("those did not match. Nothing was changed")
		}
	}

	var result struct {
		Login   string `json:"login"`
		Message string `json:"message"`
	}
	if err := c.Post(ctx, "/shares/users", map[string]any{
		"username": args[0], "password": password,
	}, &result); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, result)
	}

	fmt.Fprintf(w, "Done. Sign in as %s with that password.\n", result.Login)
	fmt.Fprintln(w, "\nRun `homebasectl share` for what to type on each kind of computer.")
	return nil
}

func removeShareUser(ctx context.Context, c *Client, o *options, args []string, w io.Writer) error {
	if len(args) != 1 {
		return usageError{errors.New("who? — homebasectl share forget NAME")}
	}
	var result struct {
		Message string `json:"message"`
	}
	if err := c.Post(ctx, "/shares/users/remove",
		map[string]any{"username": args[0]}, &result); err != nil {
		return err
	}
	if o.asJSON {
		return printResponse(w, c, result)
	}
	fmt.Fprintln(w, result.Message)
	return nil
}
