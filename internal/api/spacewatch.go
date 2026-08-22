package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/events"
	"github.com/HusnuOkanCakir/homebase/internal/hostclient"
)

// Watching for disks that are filling up.
//
// A server that runs out of space does not announce it. Applications start
// failing to write, in whatever way each of them fails, and the common thread —
// that the disk is full — is visible only to somebody who thinks to look. By
// then a database is often already corrupt, because "no space left on device"
// arrives in the middle of a write.
//
// So this notices first. It is deliberately not a poll the dashboard does: the
// point is to raise an event on a machine nobody is looking at, so that the
// history says what happened when somebody eventually does look.

const (
	// spaceCheckInterval is how often the disks are examined. Slow on purpose:
	// disks fill over days, and a check that runs constantly is one that shows
	// up in the logs more than it helps.
	spaceCheckInterval = 10 * time.Minute

	// The two thresholds a location can cross. Two rather than one because they
	// mean different things: at 90 % somebody has time to decide what to delete,
	// and at 98 % applications are about to start failing.
	warningThreshold  = 90
	criticalThreshold = 98

	// clearThreshold is where a location stops being a worry. Below the warning
	// level rather than at it, so a disk hovering at 90 % does not alternate
	// between warning and clear every ten minutes — which teaches people that
	// the alerts mean nothing.
	clearThreshold = 85
)

// SpaceWatcher raises events when a managed disk is filling up.
type SpaceWatcher struct {
	host   *hostclient.Client
	events *events.Recorder

	mu sync.Mutex
	// reported remembers the highest level already announced for each location,
	// so one filling disk produces one event rather than one every ten minutes.
	reported map[string]int
}

func NewSpaceWatcher(host *hostclient.Client, recorder *events.Recorder) *SpaceWatcher {
	return &SpaceWatcher{host: host, events: recorder, reported: map[string]int{}}
}

// Watch checks periodically until the context is cancelled.
func (w *SpaceWatcher) Watch(ctx context.Context) {
	ticker := time.NewTicker(spaceCheckInterval)
	defer ticker.Stop()

	// Once at startup, so a machine that has been off for a month says something
	// before the first interval elapses.
	w.Check(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.Check(ctx)
		}
	}
}

// Check examines every managed location once.
func (w *SpaceWatcher) Check(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	locations, err := w.host.Locations(ctx)
	if err != nil {
		// Not reported as an event. hostd being unreachable is already visible
		// in /health, and raising a disk event about it would be describing the
		// wrong problem.
		return
	}

	for _, location := range locations {
		w.consider(ctx, location)
	}
}

func (w *SpaceWatcher) consider(ctx context.Context, location hostclient.Location) {
	// A disk that is not mounted has no free space to report, and its absence is
	// somebody else's event.
	if !location.Mounted || location.TotalBytes == 0 {
		w.forget(location.ID)
		return
	}

	used := usedPercent(location)

	// The system disk filling up is a different event from an external disk
	// filling up, and needs different words. An external disk that fills stops
	// one application; this one stops the server — updates, backups, logs, the
	// database, and every application at once.
	critical := "Applications using this disk are about to stop being able to " +
		"save anything. Delete something, or move it to another disk."
	warning := "About a tenth of the disk is left. It is worth deciding now what " +
		"to remove, rather than when it is full."
	if location.Internal {
		critical = "This is the disk the server itself runs from. When it fills, " +
			"updates, backups and every application stop together. Delete " +
			"something now, or move it onto another disk."
		warning = "About a tenth of the server's own disk is left. Anything kept " +
			"here shares space with the system itself, so it is worth moving " +
			"large things — films especially — onto a disk of their own."
	}

	switch {
	case used >= criticalThreshold:
		w.announce(ctx, location, criticalThreshold, used, events.SeverityCritical,
			fmt.Sprintf("%s is almost completely full.", location.Name), critical)

	case used >= warningThreshold:
		w.announce(ctx, location, warningThreshold, used, events.SeverityWarning,
			fmt.Sprintf("%s is running out of space.", location.Name), warning)

	case used <= clearThreshold:
		w.clear(ctx, location, used)
	}
}

// announce raises an event, unless this location has already been reported at
// this level or worse.
func (w *SpaceWatcher) announce(ctx context.Context, location hostclient.Location,
	level, used int, severity events.Severity, message, recovery string) {

	w.mu.Lock()
	previous, seen := w.reported[location.ID]
	if seen && previous >= level {
		w.mu.Unlock()
		return
	}
	w.reported[location.ID] = level
	w.mu.Unlock()

	recoverable := true
	w.events.Record(ctx, events.Event{
		Type:     "storage_space_low",
		Severity: severity,
		Subject:  &location.ID,
		Reason:   text(fmt.Sprintf("%d_percent_used", level)),
		Message: text(fmt.Sprintf("%s %s %s",
			message, humanFree(location), recovery)),
		Recoverable: &recoverable,
	})
}

// clear announces recovery, but only for a location that was actually reported.
func (w *SpaceWatcher) clear(ctx context.Context, location hostclient.Location, used int) {
	w.mu.Lock()
	_, seen := w.reported[location.ID]
	delete(w.reported, location.ID)
	w.mu.Unlock()

	if !seen {
		return
	}

	w.events.Record(ctx, events.Event{
		Type:     "storage_space_recovered",
		Severity: events.SeverityInfo,
		Subject:  &location.ID,
		Message: text(fmt.Sprintf("%s has space again — %d%% used.",
			location.Name, used)),
	})
}

func (w *SpaceWatcher) forget(id string) {
	w.mu.Lock()
	delete(w.reported, id)
	w.mu.Unlock()
}

func usedPercent(location hostclient.Location) int {
	if location.TotalBytes == 0 {
		return 0
	}
	used := location.TotalBytes - location.AvailableBytes
	percent := int(float64(used) / float64(location.TotalBytes) * 100)
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func humanFree(location hostclient.Location) string {
	return fmt.Sprintf("There is %s left of %s.",
		humanBytes(location.AvailableBytes), humanBytes(location.TotalBytes))
}

// humanBytes renders a size the way a person would say it.
func humanBytes(value uint64) string {
	units := []string{"B", "kB", "MB", "GB", "TB"}
	size := float64(value)
	unit := 0
	for size >= 1000 && unit < len(units)-1 {
		size /= 1000
		unit++
	}
	if size >= 10 || unit == 0 {
		return fmt.Sprintf("%.0f %s", size, units[unit])
	}
	return fmt.Sprintf("%.1f %s", size, units[unit])
}

func text(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
