// Package jobs runs long-running operations so they can be observed, retried
// safely and — where possible — undone.
//
// The shape is described in docs/architecture/jobs.md. The property that drives
// most of the design: a client must always be able to tell the difference
// between "still working", "finished", and "nobody knows". A job stuck at
// "running, 65 %" with no process behind it is worse than an honest failure,
// because the user cannot tell which they are looking at.
package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// State is a job's lifecycle position.
type State string

const (
	StateQueued         State = "queued"
	StateRunning        State = "running"
	StateCancelling     State = "cancelling"
	StateCancelled      State = "cancelled"
	StateSucceeded      State = "succeeded"
	StateFailed         State = "failed"
	StateRollingBack    State = "rolling_back"
	StateRolledBack     State = "rolled_back"
	StateRollbackFailed State = "rollback_failed"
)

func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateCancelled, StateRolledBack, StateRollbackFailed:
		return true
	}
	return false
}

// Error mirrors schemas/error.schema.json.
type Error struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Detail      string `json:"detail,omitempty"`
	Recoverable bool   `json:"recoverable"`
	Recovery    string `json:"recovery,omitempty"`
}

// Error makes this an error value, so a failure can be returned from a job's Run
// and carried to the client with its code and user-facing message intact.
func (e *Error) Error() string {
	if e.Detail != "" {
		return e.Code + ": " + e.Message + " (" + e.Detail + ")"
	}
	return e.Code + ": " + e.Message
}

// Job is one long-running operation.
type Job struct {
	ID          string     `json:"job_id"`
	Operation   string     `json:"operation"`
	State       State      `json:"state"`
	Stage       *string    `json:"stage"`
	Progress    *int       `json:"progress"`
	Message     *string    `json:"message"`
	Cancellable bool       `json:"cancellable"`
	Error       *Error     `json:"error"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

// Run performs the work. Report progress through the supplied Reporter.
type Run func(ctx context.Context, report *Reporter) error

// Definition describes a job before it is started.
type Definition struct {
	Operation string
	Run       Run

	// InterruptsHost marks a job whose own success takes the machine away —
	// a reboot, an update that reboots. Nothing can observe it completing, so
	// it is resolved on the next start by comparing the kernel's boot id.
	InterruptsHost bool

	Cancellable bool

	// IdempotencyKey, when set, makes a repeat submission return the original
	// job rather than starting a second one.
	IdempotencyKey string

	CreatedBy string
}

// Manager owns job persistence and execution.
type Manager struct {
	db  *sql.DB
	log *slog.Logger

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

func NewManager(db *sql.DB, log *slog.Logger) *Manager {
	return &Manager{db: db, log: log, running: make(map[string]context.CancelFunc)}
}

// ErrConflict means an equivalent job is already running.
var ErrConflict = errors.New("a conflicting job is already running")

// ErrNotFound means no such job.
var ErrNotFound = errors.New("no such job")

// bootID reads the kernel's identifier for this boot.
//
// It changes on every restart and cannot be forged from userspace, which makes
// it the one piece of evidence available for "did the machine actually go down
// and come back?".
func bootID() string {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func newID() string {
	// One random byte per output character. Drawing 13 and indexing modulo 26
	// made the second half of every id mirror the first — half the entropy, and
	// obviously wrong to anyone who looked at one.
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	const length = 26

	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "job_" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}

	out := make([]byte, length)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return "job_" + string(out)
}

// Submit creates a job and starts it.
func (m *Manager) Submit(ctx context.Context, def Definition) (*Job, error) {
	if def.IdempotencyKey != "" {
		existing, err := m.byIdempotencyKey(ctx, def.IdempotencyKey)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if existing != nil {
			// Not an error. The caller asked for this exact thing and it is
			// already happening or has happened; returning the original job is
			// the answer to their question.
			return existing, nil
		}
	}

	job := &Job{
		ID:          newID(),
		Operation:   def.Operation,
		State:       StateQueued,
		Cancellable: def.Cancellable,
		CreatedAt:   time.Now().UTC(),
	}

	var key any
	if def.IdempotencyKey != "" {
		key = def.IdempotencyKey
	}
	var createdBy any
	if def.CreatedBy != "" {
		createdBy = def.CreatedBy
	}

	_, err := m.db.ExecContext(ctx, `
		INSERT INTO jobs (id, operation, state, cancellable, idempotency_key,
		                  boot_id, interrupts_host, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Operation, string(job.State), boolToInt(def.Cancellable), key,
		bootID(), boolToInt(def.InterruptsHost), createdBy,
		job.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("recording the job: %w", err)
	}

	m.start(job, def)
	return job, nil
}

func (m *Manager) start(job *Job, def Definition) {
	// Deliberately not the request's context: the job outlives the HTTP request
	// that created it. That is the whole point of returning 202.
	ctx, cancel := context.WithCancel(context.Background())

	m.mu.Lock()
	m.running[job.ID] = cancel
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.running, job.ID)
			m.mu.Unlock()
			cancel()

			if r := recover(); r != nil {
				m.log.Error("job panicked", "job", job.ID, "operation", def.Operation, "panic", r)
				m.finish(job.ID, StateFailed, &Error{
					Code:        "jobs.internal_error",
					Message:     "Something went wrong inside Homebase.",
					Detail:      fmt.Sprint(r),
					Recoverable: false,
				})
			}
		}()

		m.transition(job.ID, StateRunning)

		reporter := &Reporter{manager: m, jobID: job.ID}
		err := def.Run(ctx, reporter)

		switch {
		case err == nil:
			m.finish(job.ID, StateSucceeded, nil)
		case errors.Is(err, context.Canceled):
			m.finish(job.ID, StateCancelled, nil)
		default:
			m.finish(job.ID, StateFailed, asJobError(err))
		}
	}()
}

// Reporter lets a running job publish progress.
type Reporter struct {
	manager *Manager
	jobID   string
}

// Progress records a stage, an optional percentage and a human-readable
// message.
//
// The message is for the person reading it. "Downloading Jellyfin (1.2 GB of
// 1.8 GB)" is a message; "stage 3/7" is a status code with delusions.
func (r *Reporter) Progress(stage string, percent *int, message string) {
	_, err := r.manager.db.Exec(
		`UPDATE jobs SET stage = ?, progress = ?, message = ? WHERE id = ?`,
		stage, percent, message, r.jobID)
	if err != nil {
		r.manager.log.Warn("could not record job progress", "job", r.jobID, "error", err)
	}
}

func (m *Manager) transition(id string, state State) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := m.db.Exec(
		`UPDATE jobs SET state = ?, started_at = COALESCE(started_at, ?) WHERE id = ?`,
		string(state), now, id)
	if err != nil {
		m.log.Error("could not update job state", "job", id, "error", err)
	}
}

func (m *Manager) finish(id string, state State, jobErr *Error) {
	var encoded any
	if jobErr != nil {
		if b, err := json.Marshal(jobErr); err == nil {
			encoded = string(b)
		}
	}
	_, err := m.db.Exec(
		`UPDATE jobs SET state = ?, error = ?, finished_at = ? WHERE id = ?`,
		string(state), encoded, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		m.log.Error("could not finish job", "job", id, "error", err)
	}
}

// Cancel requests cancellation. It is a request, not a guarantee: the job moves
// to cancelling and may still finish.
func (m *Manager) Cancel(ctx context.Context, id string) error {
	job, err := m.Get(ctx, id)
	if err != nil {
		return err
	}
	if job.State.Terminal() {
		return fmt.Errorf("%w: the job has already finished", ErrConflict)
	}
	if !job.Cancellable {
		return fmt.Errorf("%w: this job cannot be cancelled", ErrConflict)
	}

	m.mu.Lock()
	cancel, running := m.running[id]
	m.mu.Unlock()

	if running {
		m.transition(id, StateCancelling)
		cancel()
	}
	return nil
}

func (m *Manager) Get(ctx context.Context, id string) (*Job, error) {
	return m.scanOne(m.db.QueryRowContext(ctx, selectJob+` WHERE id = ?`, id))
}

func (m *Manager) byIdempotencyKey(ctx context.Context, key string) (*Job, error) {
	return m.scanOne(m.db.QueryRowContext(ctx, selectJob+` WHERE idempotency_key = ?`, key))
}

// List returns jobs newest first, optionally filtered by state.
func (m *Manager) List(ctx context.Context, state string, limit int) ([]*Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	query := selectJob
	args := []any{}
	if state != "" {
		query += ` WHERE state = ?`
		args = append(args, state)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := []*Job{}
	for rows.Next() {
		job, err := m.scanRow(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// ResolveInterrupted settles jobs that were running when the process stopped.
//
// Called once at startup. Two cases, and telling them apart is the entire point:
//
//   - A job marked InterruptsHost whose recorded boot id differs from the
//     current one did what it set out to do. The machine went down and came
//     back, which is exactly what a reboot job means by success.
//
//   - Anything else that was running is over and did not finish. It is marked
//     failed with a message that says so plainly. Leaving it at "running" would
//     produce a job that shows progress forever with no process behind it —
//     indistinguishable, to a user, from one that is still working.
func (m *Manager) ResolveInterrupted(ctx context.Context) (resolved, failed int, err error) {
	current := bootID()

	rows, err := m.db.QueryContext(ctx, `
		SELECT id, operation, boot_id, interrupts_host
		FROM jobs
		WHERE state IN ('queued', 'running', 'cancelling', 'rolling_back')`)
	if err != nil {
		return 0, 0, err
	}

	type pending struct {
		id, operation string
		bootID        sql.NullString
		interrupts    bool
	}
	var items []pending

	for rows.Next() {
		var p pending
		var interrupts int
		if err := rows.Scan(&p.id, &p.operation, &p.bootID, &interrupts); err != nil {
			rows.Close()
			return 0, 0, err
		}
		p.interrupts = interrupts == 1
		items = append(items, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	for _, item := range items {
		rebooted := item.interrupts && item.bootID.Valid &&
			item.bootID.String != "" && current != "" && item.bootID.String != current

		if rebooted {
			m.finish(item.id, StateSucceeded, nil)
			_, _ = m.db.ExecContext(ctx,
				`UPDATE jobs SET message = ?, progress = 100 WHERE id = ?`,
				"The server restarted successfully.", item.id)
			resolved++
			m.log.Info("resolved an interrupted job as succeeded",
				"job", item.id, "operation", item.operation,
				"boot_id_before", item.bootID.String, "boot_id_now", current)
			continue
		}

		m.finish(item.id, StateFailed, &Error{
			Code:        "jobs.interrupted",
			Message:     "This was interrupted when the server restarted.",
			Detail:      "the job was still running when Homebase stopped",
			Recoverable: true,
			Recovery:    "Nothing was left half-finished. You can try again.",
		})
		failed++
		m.log.Warn("marked an interrupted job as failed",
			"job", item.id, "operation", item.operation)
	}

	return resolved, failed, nil
}

const selectJob = `
	SELECT id, operation, state, stage, progress, message, cancellable,
	       error, created_at, started_at, finished_at
	FROM jobs`

type scannable interface {
	Scan(dest ...any) error
}

func (m *Manager) scanOne(row scannable) (*Job, error) {
	job, err := m.scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return job, err
}

func (m *Manager) scanRow(row scannable) (*Job, error) {
	var (
		job         Job
		stage       sql.NullString
		progress    sql.NullInt64
		message     sql.NullString
		cancellable int
		errText     sql.NullString
		created     string
		started     sql.NullString
		finished    sql.NullString
		state       string
	)

	if err := row.Scan(&job.ID, &job.Operation, &state, &stage, &progress, &message,
		&cancellable, &errText, &created, &started, &finished); err != nil {
		return nil, err
	}

	job.State = State(state)
	job.Cancellable = cancellable == 1
	if stage.Valid {
		job.Stage = &stage.String
	}
	if progress.Valid {
		p := int(progress.Int64)
		job.Progress = &p
	}
	if message.Valid {
		job.Message = &message.String
	}
	if errText.Valid && errText.String != "" {
		var e Error
		if json.Unmarshal([]byte(errText.String), &e) == nil {
			job.Error = &e
		}
	}
	job.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if started.Valid {
		if t, err := time.Parse(time.RFC3339Nano, started.String); err == nil {
			job.StartedAt = &t
		}
	}
	if finished.Valid {
		if t, err := time.Parse(time.RFC3339Nano, finished.String); err == nil {
			job.FinishedAt = &t
		}
	}

	return &job, nil
}

// asJobError normalises a failure into the error envelope.
//
// A *jobs.Error passes through untouched. That is how a failure originating in
// hostd keeps its own code and its own user-facing message — callers convert it
// at the boundary rather than letting it be flattened into "something went
// wrong", which would throw away the one explanation the user could act on.
func asJobError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}

	return &Error{
		Code:        "jobs.failed",
		Message:     "The operation did not finish.",
		Detail:      err.Error(),
		Recoverable: true,
		Recovery:    "Try again. If it keeps failing, check the system logs.",
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
