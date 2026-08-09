// Package events records what happened on this machine, as structured facts.
//
// The rule that shapes this package: an event is a record with a machine-readable
// `type`, and `message` is a rendering of it rather than the fact itself. A
// consumer — the dashboard today, the Stage 2 operator later — must never have to
// parse a human-readable string to learn what happened. Once something is
// reported only in prose, every consumer of it becomes a text parser, and the
// wording can never be changed or translated again.
//
// Events are not logs. Logs are for whoever is debugging Homebase; events are
// part of the API and are as much a contract as any endpoint.
package events

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Severity is how much the user should care.
type Severity string

const (
	// SeverityInfo is something that happened and went well.
	SeverityInfo Severity = "info"
	// SeverityWarning is something the user should know about but which needs no
	// action right now.
	SeverityWarning Severity = "warning"
	// SeverityError is something that failed.
	SeverityError Severity = "error"
	// SeverityCritical is data or availability at risk. Reserved: if everything
	// is critical, nothing is.
	SeverityCritical Severity = "critical"
)

func (s Severity) valid() bool {
	switch s {
	case SeverityInfo, SeverityWarning, SeverityError, SeverityCritical:
		return true
	}
	return false
}

// Event matches the Event schema in api/openapi.yaml.
type Event struct {
	ID       string   `json:"event_id"`
	Type     string   `json:"type"`
	Severity Severity `json:"severity"`

	// Subject is what the event is about — an application id, a disk, a user.
	Subject *string `json:"subject"`

	// Reason is a machine-readable cause, so a consumer can branch on why
	// something happened rather than on how it was phrased.
	Reason *string `json:"reason"`

	// Recoverable is null where recoverability is not a meaningful question,
	// which is not the same as false.
	Recoverable *bool `json:"recoverable"`

	Message    *string   `json:"message"`
	OccurredAt time.Time `json:"occurred_at"`
}

// Recorder persists events and hands them to anyone watching.
type Recorder struct {
	db  *sql.DB
	log *slog.Logger

	mu          sync.Mutex
	subscribers map[int]chan Event
	nextID      int
}

func NewRecorder(db *sql.DB, log *slog.Logger) *Recorder {
	return &Recorder{db: db, log: log, subscribers: make(map[int]chan Event)}
}

// Record writes an event.
//
// It returns no error, and that is deliberate. Every caller is doing something
// more important than recording that it happened, and none of them has a
// sensible response to "the event could not be written". Making this fallible
// would put `if err := events.Record(...)` around code paths that are already
// handling a real failure, and the realistic reaction to that error at every one
// of those sites is to ignore it. So it is ignored in one place, loudly, instead
// of quietly in twenty.
func (r *Recorder) Record(ctx context.Context, event Event) {
	if event.Type == "" {
		r.log.Error("an event was recorded with no type; dropping it")
		return
	}
	if !event.Severity.valid() {
		r.log.Error("an event was recorded with an unknown severity",
			"type", event.Type, "severity", event.Severity)
		event.Severity = SeverityInfo
	}

	if event.ID == "" {
		event.ID = newID()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	event.OccurredAt = event.OccurredAt.UTC()

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO events (id, type, severity, subject, reason, recoverable, message, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.Type, string(event.Severity), event.Subject, event.Reason,
		event.Recoverable, event.Message, event.OccurredAt.Format(time.RFC3339Nano))
	if err != nil {
		// Logged at error rather than warn: a machine that has stopped recording
		// events looks, from the dashboard, exactly like a machine where nothing
		// is happening.
		r.log.Error("could not record an event", "type", event.Type, "error", err)
		return
	}

	r.publish(event)
}

// Info, Warn and Error are the shorthands most callers want.

func (r *Recorder) Info(ctx context.Context, eventType, subject, message string) {
	r.Record(ctx, Event{
		Type: eventType, Severity: SeverityInfo,
		Subject: text(subject), Message: text(message),
	})
}

func (r *Recorder) Warn(ctx context.Context, eventType, subject, reason, message string) {
	r.Record(ctx, Event{
		Type: eventType, Severity: SeverityWarning,
		Subject: text(subject), Reason: text(reason), Message: text(message),
	})
}

func (r *Recorder) Error(ctx context.Context, eventType, subject, reason, message string, recoverable bool) {
	r.Record(ctx, Event{
		Type: eventType, Severity: SeverityError,
		Subject: text(subject), Reason: text(reason), Message: text(message),
		Recoverable: &recoverable,
	})
}

// --- Reading ------------------------------------------------------------------

// Query filters a listing. A zero Query returns the most recent events.
type Query struct {
	Severity Severity
	Since    time.Time
	Limit    int
}

const (
	defaultLimit = 50
	maxLimit     = 500
)

// List returns events newest first.
func (r *Recorder) List(ctx context.Context, q Query) ([]Event, error) {
	if q.Severity != "" && !q.Severity.valid() {
		return nil, fmt.Errorf("%w: severity %q", ErrInvalidQuery, q.Severity)
	}

	limit := q.Limit
	switch {
	case limit <= 0:
		limit = defaultLimit
	case limit > maxLimit:
		limit = maxLimit
	}

	where := []string{"1 = 1"}
	args := []any{}
	if q.Severity != "" {
		where = append(where, "severity = ?")
		args = append(args, string(q.Severity))
	}
	if !q.Since.IsZero() {
		where = append(where, "occurred_at > ?")
		args = append(args, q.Since.UTC().Format(time.RFC3339Nano))
	}
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, type, severity, subject, reason, recoverable, message, occurred_at
		FROM events
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY occurred_at DESC, id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []Event{}
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// ErrInvalidQuery means the caller asked for something that cannot be filtered.
var ErrInvalidQuery = errors.New("invalid event query")

func scanEvent(rows *sql.Rows) (Event, error) {
	var (
		event       Event
		severity    string
		occurred    string
		recoverable sql.NullBool
	)
	if err := rows.Scan(&event.ID, &event.Type, &severity, &event.Subject,
		&event.Reason, &recoverable, &event.Message, &occurred); err != nil {
		return Event{}, err
	}
	event.Severity = Severity(severity)
	if recoverable.Valid {
		value := recoverable.Bool
		event.Recoverable = &value
	}
	// Tolerant of both, because RFC3339Nano drops trailing zeroes and a row
	// written at a whole second comes back without a fractional part.
	if parsed, err := time.Parse(time.RFC3339Nano, occurred); err == nil {
		event.OccurredAt = parsed
	} else if parsed, err := time.Parse(time.RFC3339, occurred); err == nil {
		event.OccurredAt = parsed
	}
	return event, nil
}

// --- Live subscription --------------------------------------------------------

// Subscribe returns a channel of events as they happen, and a function to stop.
//
// The channel is buffered and lossy on purpose: a subscriber that stops reading
// must not be able to block the operation that produced the event. Losing an
// event from a live stream is recoverable — the durable record is in the
// database and the client can re-list. Blocking an install because a browser tab
// froze is not.
func (r *Recorder) Subscribe() (<-chan Event, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id := r.nextID
	r.nextID++
	channel := make(chan Event, 64)
	r.subscribers[id] = channel

	return channel, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if existing, ok := r.subscribers[id]; ok {
			delete(r.subscribers, id)
			close(existing)
		}
	}
}

func (r *Recorder) publish(event Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for id, channel := range r.subscribers {
		select {
		case channel <- event:
		default:
			r.log.Warn("an event stream subscriber is not keeping up; dropping an event",
				"subscriber", id, "type", event.Type)
		}
	}
}

// Subscribers reports how many live streams are open, for diagnostics.
func (r *Recorder) Subscribers() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.subscribers)
}

func text(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func newID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		// An id derived from the clock rather than no id at all. A collision
		// here loses one event; refusing to record loses the reason something
		// went wrong.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}
