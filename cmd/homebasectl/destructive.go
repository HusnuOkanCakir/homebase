package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// The commands that destroy things.
//
// These were the last part of the CLI to be written, and deliberately so: the
// confirmation the dashboard uses does not survive the move to a terminal, and
// copying it across would have looked like safety without being any.
//
// **In a form field, "type the backup id to confirm" works.** The id is on the
// screen, the field is empty, and typing it means having read it. At a shell it
// means almost nothing: the id is already in the command that listed it, one
// press of the up arrow re-runs whatever was done last, and a `--yes` flag
// becomes muscle memory within a week.
//
// So what protects somebody here is different, and it is three things.
//
// **The preview.** Before anything irreversible, the server is asked what would
// happen and the answer is printed. It is specific — this many files, this much
// replaced, from this machine, on this date — and it is the part that actually
// stops a mistake, because a wrong choice usually looks wrong when described.
//
// **A terminal is required.** Without one, these refuse and say what to pass
// instead. That is the difference between a browser and a CLI: a script can run
// a command by accident in a way nobody can click a button by accident, and the
// scripted path should have to be asked for.
//
// **The confirmation is a value, not a word.** Scripted, it is `--confirm` with
// the thing's own name — the same string the API demands, which cannot be
// replayed against a different disk or a different backup. There is no `--yes`
// and there should not be.

// confirmDestruction asks, having first said what will happen.
//
// `expected` is what has to be typed: the backup's id, the disk's device, the
// server's name. Never a word like "yes" — a confirmation that is the same on
// every machine is one that can be typed without looking.
func confirmDestruction(stdout io.Writer, expected, prompt string, scripted string) (bool, error) {
	if scripted != "" {
		if scripted != expected {
			return false, usageError{fmt.Errorf(
				"--confirm is %q; it has to be %q", scripted, expected)}
		}
		return true, nil
	}

	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false, usageError{fmt.Errorf(
			"this needs a terminal to confirm on.\n\n"+
				"From a script, pass it explicitly:  --confirm %s\n"+
				"There is no --yes, because a flag that means \"do it anyway\" is one\n"+
				"that ends up in every invocation.", expected)}
	}

	fmt.Fprintf(stdout, "\n%s\n", prompt)
	fmt.Fprintf(stdout, "Type %s to confirm, or anything else to stop: ", expected)

	typed, err := readLine()
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(typed) != expected {
		fmt.Fprintln(stdout, "\nStopped. Nothing was changed.")
		return false, nil
	}
	return true, nil
}

// --- Restoring a backup ------------------------------------------------------------

func restoreCommand(ctx context.Context, c *Client, o *options, rest []string, w io.Writer) error {
	if len(rest) != 1 {
		return usageError{errors.New(
			"which backup? — homebasectl backup restore ID --from DISK")}
	}
	id := rest[0]
	if o.from == "" {
		return usageError{errors.New("which disk is it on? — add --from DISK")}
	}

	// The preview first, always. It changes nothing, and it is the part that
	// stops a mistake: a wrong backup usually looks wrong once described.
	var preview struct {
		CreatedAt       string   `json:"created_at"`
		Hostname        string   `json:"hostname"`
		FilesToWrite    int      `json:"files_to_write"`
		BytesToWrite    uint64   `json:"bytes_to_write"`
		WouldOverwrite  int      `json:"would_overwrite"`
		Verified        bool     `json:"verified"`
		IntegrityIssues []string `json:"integrity_issues"`
		Message         string   `json:"message"`
	}
	if err := c.Get(ctx, "/backups/"+id+"/preview?location="+o.from, &preview); err != nil {
		return err
	}

	fmt.Fprintf(w, "Backup:      %s\n", id)
	fmt.Fprintf(w, "Taken:       %s, from %s\n", preview.CreatedAt, preview.Hostname)
	fmt.Fprintf(w, "Would write: %d files (%s)\n",
		preview.FilesToWrite, humanBytes(preview.BytesToWrite))
	// The number somebody actually needs before agreeing.
	fmt.Fprintf(w, "Would replace: %d files on this server\n", preview.WouldOverwrite)

	if !preview.Verified {
		fmt.Fprintf(w, "\nWARNING: this backup is damaged — %d files are missing or "+
			"changed.\nRestoring it will not bring everything back.\n",
			len(preview.IntegrityIssues))
	}

	confirmed, err := confirmDestruction(w, id,
		"Restoring replaces those files with the ones in the backup. Anything on this\n"+
			"server that is not in the backup is left alone.", o.confirm)
	if err != nil || !confirmed {
		return err
	}

	var job jobReply
	if err := c.Post(ctx, "/backups/"+id+"/restore?location="+o.from,
		map[string]any{"confirm": id}, &job); err != nil {
		return err
	}
	return followJob(ctx, c, o, job, w)
}

// --- Formatting a disk -------------------------------------------------------------

func formatCommand(ctx context.Context, c *Client, o *options, rest []string, w io.Writer) error {
	if len(rest) != 1 {
		return usageError{errors.New(
			"which disk? — homebasectl storage format /dev/sdX\n" +
				"`homebasectl storage disks` lists what is plugged in.")}
	}
	device := rest[0]

	// What is on it, from the server rather than from the user's memory. This is
	// the only operation in Homebase that can destroy data Homebase never
	// created, so the description has to come from the disk.
	var disks struct {
		Items []struct {
			Path      string `json:"path"`
			Model     string `json:"model"`
			SizeBytes uint64 `json:"size_bytes"`
			Removable bool   `json:"removable"`
			Volumes   []struct {
				Path       string `json:"path"`
				Filesystem string `json:"filesystem"`
				Label      string `json:"label"`
				SizeBytes  uint64 `json:"size_bytes"`
			} `json:"volumes"`
		} `json:"items"`
	}
	if err := c.Get(ctx, "/storage/disks", &disks); err != nil {
		return err
	}

	found := false
	for _, disk := range disks.Items {
		matches := disk.Path == device
		for _, volume := range disk.Volumes {
			if volume.Path == device {
				matches = true
			}
		}
		if !matches {
			continue
		}
		found = true

		fmt.Fprintf(w, "Disk:      %s\n", disk.Path)
		fmt.Fprintf(w, "Model:     %s\n", disk.Model)
		fmt.Fprintf(w, "Size:      %s\n", humanBytes(disk.SizeBytes))
		fmt.Fprintf(w, "Removable: %v\n", disk.Removable)
		if len(disk.Volumes) > 0 {
			fmt.Fprintln(w, "\nWhat is on it now:")
			for _, volume := range disk.Volumes {
				label := volume.Label
				if label == "" {
					label = "(no label)"
				}
				fmt.Fprintf(w, "  %s  %-8s %-20s %s\n", volume.Path,
					volume.Filesystem, label, humanBytes(volume.SizeBytes))
			}
		}
	}
	if !found {
		return fmt.Errorf("this server cannot see %s.\n\n"+
			"    homebasectl storage disks    lists what is plugged in", device)
	}

	confirmed, err := confirmDestruction(w, device,
		"EVERYTHING ON THIS DISK WILL BE DESTROYED. There is no undo, and Homebase\n"+
			"keeps no copy — anything on it that is not backed up elsewhere is gone.",
		o.confirm)
	if err != nil || !confirmed {
		return err
	}

	var job jobReply
	body := map[string]any{"device": device, "confirm": device}
	if o.name != "" {
		body["label"] = o.name
	}
	if err := c.Post(ctx, "/storage/format", body, &job); err != nil {
		return err
	}
	return followJob(ctx, c, o, job, w)
}

// --- Attaching a disk --------------------------------------------------------------

func attachCommand(ctx context.Context, c *Client, o *options, rest []string, w io.Writer) error {
	if len(rest) != 2 {
		return usageError{errors.New(
			"homebasectl storage attach UUID NAME\n" +
				"The UUID comes from `homebasectl storage disks`. NAME is what you\n" +
				"will call it — \"backups\", \"films\".")}
	}

	// Not destructive: attaching a disk reads it and mounts it, and leaves
	// everything on it alone. No confirmation, for the same reason nothing else
	// harmless asks for one — friction on a safe action teaches people to click
	// through the friction on an unsafe one.
	var job jobReply
	if err := c.Post(ctx, "/storage/locations", map[string]any{
		"uuid": rest[0], "id": rest[1], "name": rest[1],
	}, &job); err != nil {
		return err
	}
	return followJob(ctx, c, o, job, w)
}

func detachCommand(ctx context.Context, c *Client, o *options, rest []string, w io.Writer) error {
	if len(rest) != 1 {
		return usageError{errors.New("which disk? — homebasectl storage detach NAME")}
	}

	// Medium risk in hostd, and it asks — but what it destroys is nothing: the
	// disk is unmounted and Homebase forgets it. The confirmation is here
	// because an application whose storage disappears stops working, which is
	// alarming if it was not expected.
	confirmed, err := confirmDestruction(w, rest[0],
		"Homebase will stop using this disk. Nothing on it is deleted, but any\n"+
			"application keeping its files there will stop working until it is back.",
		o.confirm)
	if err != nil || !confirmed {
		return err
	}

	var job jobReply
	if err := c.Post(ctx, "/storage/locations/"+rest[0]+"/remove",
		map[string]any{"confirm": rest[0]}, &job); err != nil {
		return err
	}
	return followJob(ctx, c, o, job, w)
}

// --- Starting again ----------------------------------------------------------------

func factoryResetCommand(args []string, stdout io.Writer) error {
	return withClient("factory-reset", args, stdout,
		func(ctx context.Context, c *Client, o *options, _ []string, w io.Writer) error {
			// The machine's own name, which is the only string specific to this
			// server — and therefore the only one that cannot be typed by
			// somebody who thinks they are on a different machine.
			var system struct {
				Hostname string `json:"hostname"`
			}
			if err := c.Get(ctx, "/system", &system); err != nil {
				return err
			}

			fmt.Fprintf(w, "This is %s.\n\n", system.Hostname)
			fmt.Fprintln(w, "A factory reset removes every account and every setting on it.")
			fmt.Fprintln(w, "Afterwards it asks to be set up again, like a new server.")
			fmt.Fprintln(w, "\nYour files are kept. Everything on your storage disks stays")
			fmt.Fprintln(w, "where it is. So is where this server gets its updates from.")
			fmt.Fprintln(w, "\nThe accounts and settings this removes cannot be brought back")
			fmt.Fprintln(w, "except from a backup made beforehand.")

			confirmed, err := confirmDestruction(w, system.Hostname,
				"This cannot be undone.", o.confirm)
			if err != nil || !confirmed {
				return err
			}

			var result struct {
				Removed []string `json:"removed"`
				Kept    []string `json:"kept"`
				Message string   `json:"message"`
			}
			if err := c.Post(ctx, "/system/factory-reset",
				map[string]any{"confirm": system.Hostname, "keep_data": true},
				&result); err != nil {
				return err
			}
			if o.asJSON {
				return printResponse(w, c, result)
			}

			fmt.Fprintf(w, "\n%s\n", result.Message)
			for _, kept := range result.Kept {
				fmt.Fprintf(w, "  kept: %s\n", kept)
			}
			return nil
		})
}
