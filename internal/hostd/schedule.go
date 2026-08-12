package hostd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Backups that happen without anybody pressing anything.
//
// Carried here from Milestone 5, where backups worked but only when asked. A
// backup you have to remember is a backup that exists until the week you are
// busy, which is reliably the week the disk fails.
//
// The schedule is a systemd timer. Not a goroutine in core with a ticker: core
// is restarted by every update and stopped whenever somebody is working on the
// machine, and a scheduler that only runs while the thing it is part of is
// running is a scheduler that stops without saying so. systemd already owns
// "run this at three in the morning, and if the machine was off, run it when it
// comes back" — that last part is `Persistent=true`, and it is the whole
// difference between a schedule that works on a laptop in a cupboard and one
// that does not.

const (
	backupScheduleFile = "/etc/homebase/backup-schedule.conf"
	backupTimerUnit    = "homebase-backup.timer"
	backupTimerDropIn  = "/etc/systemd/system/homebase-backup.timer.d/schedule.conf"
)

// schedules maps the words a caller may use to what systemd understands.
//
// A fixed table, and this is the load-bearing part. `OnCalendar` goes into a
// unit file, and a unit file is a way to run things — so no string from a
// request is ever written into one. A caller picks a word; the calendar
// expression is ours.
var schedules = map[string]string{
	"daily":  "*-*-* 03:00:00",
	"weekly": "Sun *-*-* 03:00:00",
}

// scheduleInWords is what to show somebody, per key.
var scheduleInWords = map[string]string{
	"daily":  "every night, at about three in the morning",
	"weekly": "every Sunday night, at about three in the morning",
	"off":    "never — backups only happen when you ask for one",
}

// BackupSchedule is when backups happen, and where they go.
type BackupSchedule struct {
	// Every is "daily", "weekly", or "off".
	Every string `json:"every"`

	// Location is the storage location backups are written to. Empty when
	// nothing is scheduled.
	Location string `json:"location,omitempty"`

	// Description is the schedule in words, for a screen.
	Description string `json:"description"`

	// Enabled is whether systemd will actually run it. Read from systemd rather
	// than inferred from the file, because a schedule recorded on disk and not
	// enabled is the failure this field exists to make visible.
	Enabled bool `json:"enabled"`

	// NextRun is when systemd says it will next fire, if it can say.
	NextRun string `json:"next_run,omitempty"`

	// LastResult is how the last scheduled run ended: "ok", "failed", or empty
	// if it has never run. A schedule nobody checks is worth very little, so
	// this is reported alongside it rather than left in the journal.
	LastResult string `json:"last_result,omitempty"`
}

// readBackupSchedule reports what is configured and whether it is live.
func readBackupSchedule(ctx context.Context) BackupSchedule {
	schedule := BackupSchedule{Every: "off", Description: scheduleInWords["off"]}

	values := readResultFile(backupScheduleFile)
	if every := values["every"]; every != "" {
		schedule.Every = every
		schedule.Location = values["location"]
		if words, ok := scheduleInWords[every]; ok {
			schedule.Description = words
		}
	}

	schedule.Enabled = unitIsActive(ctx, backupTimerUnit)
	schedule.NextRun = nextElapse(ctx, backupTimerUnit)
	schedule.LastResult = lastRunResult(ctx, "homebase-backup.service")
	return schedule
}

// nextElapse asks systemd when the timer will next fire.
//
// systemd's own answer rather than one computed here. It accounts for
// RandomizedDelaySec, for Persistent catching up a missed run, and for the
// machine's timezone — none of which a reimplementation would get right, and
// all of which a user would notice being wrong.
func nextElapse(ctx context.Context, unit string) string {
	out, err := systemctlShow(ctx, unit, "NextElapseUSecRealtime")
	if err != nil {
		return ""
	}
	when, ok := parseSystemdTime(out)
	if !ok {
		return ""
	}
	return when.UTC().Format(time.RFC3339)
}

// parseSystemdTime reads back a timestamp property from `systemctl show`.
//
// Both forms, because systemd's own has changed and the name of the property has
// not. `NextElapseUSecRealtime` sounds like microseconds and older systemd
// prints microseconds; current systemd prints
//
//	Thu 2026-08-13 03:00:00 CEST
//
// Reading only the numeric form is how this field spent its first version always
// empty — parsed, failed, and returned "" to a screen that then had nothing to
// say. Silence is what a missing value looks like, so nothing looked wrong.
//
// The zone is resolved against the machine's own location: systemd prints the
// local abbreviation, this runs on the same machine, and Go cannot turn "CEST"
// into an offset any other way.
func parseSystemdTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == "0" || value == "n/a" || value == "infinity" {
		return time.Time{}, false
	}

	if micros, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.UnixMicro(micros), true
	}

	for _, layout := range []string{
		"Mon 2006-01-02 15:04:05 MST",
		"Mon 2006-01-02 15:04:05.000000 MST",
		"2006-01-02 15:04:05 MST",
	} {
		if when, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return when, true
		}
	}
	return time.Time{}, false
}

// lastRunResult reports how the last scheduled backup ended.
func lastRunResult(ctx context.Context, unit string) string {
	out, err := systemctlShow(ctx, unit, "Result")
	if err != nil || out == "" {
		return ""
	}
	if out == "success" {
		return "ok"
	}
	// systemd distinguishes exit-code, signal, timeout and more. A person does
	// not need those words; they need to know it did not work, and the journal
	// has the rest.
	return "failed"
}

func systemctlShow(ctx context.Context, unit, property string) (string, error) {
	binary, err := exec.LookPath("systemctl")
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, binary, "show", unit, "--property="+property, "--value")
	cmd.Env = aptEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// writeBackupSchedule records the schedule and makes systemd act on it.
func writeBackupSchedule(ctx context.Context, every, location string) error {
	if every == "off" {
		if err := runSystemctl(ctx, "disable", "--now", backupTimerUnit); err != nil {
			return err
		}
		// The disk is kept on purpose. Turning a schedule off and turning it on
		// again should not lose the choice — and "off" is sent without a
		// destination, because a caller turning something off has no reason to
		// say where it was pointing. Taking the request at face value here is
		// what silently discarded it.
		if location == "" {
			location = readResultFile(backupScheduleFile)["location"]
		}
		return writeScheduleFile("every=off\nlocation=" + location + "\n")
	}

	calendar, ok := schedules[every]
	if !ok {
		return fmt.Errorf("no calendar for %q", every)
	}

	if err := os.MkdirAll(filepath.Dir(backupTimerDropIn), 0o755); err != nil {
		return fmt.Errorf("creating the timer directory: %w", err)
	}

	// A drop-in rather than a rewritten unit: the unit stays the file the
	// package installed and can be read against the package, and everything
	// this writes is one OnCalendar line from a fixed table.
	dropIn := "" +
		"# Written by Homebase. Change the schedule from the dashboard rather\n" +
		"# than by editing this file.\n" +
		"[Timer]\n" +
		"OnCalendar=\n" +
		"OnCalendar=" + calendar + "\n"
	if err := writeRootFile(backupTimerDropIn, dropIn, 0o644); err != nil {
		return err
	}

	if err := writeScheduleFile("every=" + every + "\nlocation=" + location + "\n"); err != nil {
		return err
	}

	if err := runSystemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return runSystemctl(ctx, "enable", "--now", backupTimerUnit)
}

// writeScheduleFile writes the schedule where the account that acts on it can
// read it.
//
// root:homebase 0640, and the group is the entire point. hostd writes this file
// as root, and the thing that reads it is `homebase-backup.service`, which runs
// as `homebase` — the same unprivileged account the dashboard's requests arrive
// under, because a schedule must not be a way to reach hostd more directly than
// a person can.
//
// Written root-owned rather than handed to the account outright: this file names
// the disk somebody's data is copied onto, and the account that reads it is the
// one an application would be running as if it got out of its container.
func writeScheduleFile(content string) error {
	if err := writeRootFile(backupScheduleFile, content, 0o640); err != nil {
		return err
	}
	return giveToServiceGroup(backupScheduleFile)
}

func runSystemctl(ctx context.Context, args ...string) error {
	binary, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl is not on this machine")
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = aptEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %s: %s", strings.Join(args, " "),
			strings.TrimSpace(string(out)))
	}
	return nil
}
