package jobs

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/HusnuOkanCakir/homebase/internal/store"
)

func testManager(t *testing.T) (*Manager, *sql.DB) {
	t.Helper()

	s, err := store.Open(context.Background(), t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	return NewManager(s.DB(), slog.New(slog.NewTextHandler(io.Discard, nil))), s.DB()
}

func waitForState(t *testing.T, m *Manager, id string, want State) *Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last State
	for time.Now().Before(deadline) {
		job, err := m.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("getting job: %v", err)
		}
		last = job.State
		if job.State == want {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s reached %q, expected %q", id, last, want)
	return nil
}

func TestJobSucceeds(t *testing.T) {
	m, _ := testManager(t)

	job, err := m.Submit(context.Background(), Definition{
		Operation: "test.work",
		Run: func(ctx context.Context, report *Reporter) error {
			report.Progress("working", nil, "Doing the thing…")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	done := waitForState(t, m, job.ID, StateSucceeded)
	if done.FinishedAt == nil {
		t.Error("a finished job has no finish time")
	}
	if done.Error != nil {
		t.Errorf("a successful job carries an error: %+v", done.Error)
	}
}

// A failure must keep the code and the user-facing message it was raised with.
// Flattening it into "something went wrong" throws away the one thing the user
// could have acted on.
func TestJobFailurePreservesTheError(t *testing.T) {
	m, _ := testManager(t)

	job, err := m.Submit(context.Background(), Definition{
		Operation: "test.fails",
		Run: func(context.Context, *Reporter) error {
			return &Error{
				Code:        "storage.disk_not_found",
				Message:     "The backup disk is not connected.",
				Recoverable: true,
				Recovery:    "Reconnect the backup disk and try again.",
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	done := waitForState(t, m, job.ID, StateFailed)
	if done.Error == nil {
		t.Fatal("a failed job has no error")
	}
	if done.Error.Code != "storage.disk_not_found" {
		t.Errorf("code = %q, want storage.disk_not_found", done.Error.Code)
	}
	if done.Error.Recovery == "" {
		t.Error("the error is recoverable but says nothing about how")
	}
}

// A panic must not take the process with it, and must leave a job somebody can
// see rather than one stuck at running forever.
func TestPanicBecomesAFailedJob(t *testing.T) {
	m, _ := testManager(t)

	job, err := m.Submit(context.Background(), Definition{
		Operation: "test.panics",
		Run: func(context.Context, *Reporter) error {
			panic("something unexpected")
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	done := waitForState(t, m, job.ID, StateFailed)
	if done.Error == nil || done.Error.Code != "jobs.internal_error" {
		t.Errorf("unexpected error: %+v", done.Error)
	}
}

// A user on poor Wi-Fi presses the button twice; an automated client that loses
// a connection retries. Neither should start the work a second time.
func TestIdempotencyKeyReturnsTheOriginalJob(t *testing.T) {
	m, _ := testManager(t)

	calls := make(chan struct{}, 10)
	def := Definition{
		Operation:      "test.once",
		IdempotencyKey: "the-same-key",
		Run: func(context.Context, *Reporter) error {
			calls <- struct{}{}
			return nil
		},
	}

	first, err := m.Submit(context.Background(), def)
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, m, first.ID, StateSucceeded)

	second, err := m.Submit(context.Background(), def)
	if err != nil {
		t.Fatal(err)
	}

	if second.ID != first.ID {
		t.Fatalf("a repeated key started a second job (%s then %s)", first.ID, second.ID)
	}
	if len(calls) != 1 {
		t.Errorf("the work ran %d times, expected once", len(calls))
	}
}

func TestDifferentKeysAreDifferentJobs(t *testing.T) {
	m, _ := testManager(t)

	run := func(key string) string {
		job, err := m.Submit(context.Background(), Definition{
			Operation:      "test.twice",
			IdempotencyKey: key,
			Run:            func(context.Context, *Reporter) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		return job.ID
	}

	if run("first") == run("second") {
		t.Error("different idempotency keys returned the same job")
	}
}

// --- The reboot design -------------------------------------------------------
//
// A reboot job cannot observe its own success: the connection dies with the
// machine. These tests cover how it is settled afterwards, which is the part
// that decides whether the job system can be trusted about anything.

// A job that interrupts the host, whose recorded boot id differs from the
// current one, did what it set out to do. The machine went down and came back —
// that is exactly what a reboot means by success.
func TestRebootJobResolvesAsSucceededWhenTheMachineRestarted(t *testing.T) {
	m, db := testManager(t)

	// A job left running from a previous boot.
	_, err := db.Exec(`
		INSERT INTO jobs (id, operation, state, boot_id, interrupts_host, created_at)
		VALUES (?, ?, ?, ?, 1, ?)`,
		"job_REBOOT", "system.reboot", string(StateRunning),
		"a-boot-id-from-before-the-restart",
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}

	resolved, failed, err := m.ResolveInterrupted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 1 || failed != 0 {
		t.Fatalf("resolved=%d failed=%d, want 1 and 0", resolved, failed)
	}

	job, err := m.Get(context.Background(), "job_REBOOT")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != StateSucceeded {
		t.Errorf("state = %q, want succeeded", job.State)
	}
	if job.Message == nil || *job.Message == "" {
		t.Error("no message explaining what happened")
	}
}

// The same job, but the boot id has not changed: the machine never went down,
// so core restarted for some other reason and the reboot did not happen.
// Reporting success here would be inventing an outcome.
func TestRebootJobDoesNotClaimSuccessWithoutEvidence(t *testing.T) {
	m, db := testManager(t)

	current := bootID()
	if current == "" {
		t.Skip("no /proc/sys/kernel/random/boot_id on this machine")
	}

	_, err := db.Exec(`
		INSERT INTO jobs (id, operation, state, boot_id, interrupts_host, created_at)
		VALUES (?, ?, ?, ?, 1, ?)`,
		"job_NOREBOOT", "system.reboot", string(StateRunning), current,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}

	resolved, failed, err := m.ResolveInterrupted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 0 || failed != 1 {
		t.Fatalf("resolved=%d failed=%d, want 0 and 1 — the machine did not restart", resolved, failed)
	}

	job, _ := m.Get(context.Background(), "job_NOREBOOT")
	if job.State != StateFailed {
		t.Errorf("state = %q, want failed", job.State)
	}
}

// An ordinary job that was running when the process stopped is over and did not
// finish. Leaving it at "running" would show progress forever with no process
// behind it — which a user cannot tell apart from work still in flight.
func TestOrdinaryInterruptedJobsAreMarkedFailed(t *testing.T) {
	m, db := testManager(t)

	_, err := db.Exec(`
		INSERT INTO jobs (id, operation, state, boot_id, interrupts_host, created_at)
		VALUES (?, ?, ?, ?, 0, ?)`,
		"job_STUCK", "apps.install", string(StateRunning), "some-old-boot-id",
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := m.ResolveInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}

	job, _ := m.Get(context.Background(), "job_STUCK")
	if job.State != StateFailed {
		t.Fatalf("state = %q, want failed", job.State)
	}
	if job.Error == nil || job.Error.Code != "jobs.interrupted" {
		t.Errorf("unexpected error: %+v", job.Error)
	}
	if !job.Error.Recoverable || job.Error.Recovery == "" {
		t.Error("an interrupted job should tell the user they can try again")
	}
}

// Jobs that already finished must not be disturbed by a restart.
func TestResolveLeavesFinishedJobsAlone(t *testing.T) {
	m, db := testManager(t)

	for _, state := range []State{StateSucceeded, StateFailed, StateCancelled} {
		_, err := db.Exec(`
			INSERT INTO jobs (id, operation, state, boot_id, interrupts_host, created_at, finished_at)
			VALUES (?, ?, ?, ?, 1, ?, ?)`,
			"job_"+string(state), "system.reboot", string(state), "old-boot-id",
			time.Now().UTC().Format(time.RFC3339Nano),
			time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			t.Fatal(err)
		}
	}

	resolved, failed, err := m.ResolveInterrupted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 0 || failed != 0 {
		t.Errorf("resolved=%d failed=%d; finished jobs were touched", resolved, failed)
	}
}

func TestCancellation(t *testing.T) {
	m, _ := testManager(t)

	started := make(chan struct{})
	job, err := m.Submit(context.Background(), Definition{
		Operation:   "test.cancellable",
		Cancellable: true,
		Run: func(ctx context.Context, _ *Reporter) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	<-started
	if err := m.Cancel(context.Background(), job.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	waitForState(t, m, job.ID, StateCancelled)
}

// A job that says it cannot be cancelled must not be, or the UI would offer a
// button that silently does nothing.
func TestUncancellableJobRefusesCancellation(t *testing.T) {
	m, _ := testManager(t)

	release := make(chan struct{})
	job, err := m.Submit(context.Background(), Definition{
		Operation:   "test.uncancellable",
		Cancellable: false,
		Run: func(context.Context, *Reporter) error {
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer close(release)

	err = m.Cancel(context.Background(), job.ID)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("cancel returned %v, want ErrConflict", err)
	}
}

func TestGetUnknownJob(t *testing.T) {
	m, _ := testManager(t)
	if _, err := m.Get(context.Background(), "job_NOPE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// Jobs survive a restart of core, which is what makes them worth reporting on
// at all.
func TestJobsPersist(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.db"

	s1, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	m1 := NewManager(s1.DB(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	job, err := m1.Submit(context.Background(), Definition{
		Operation: "test.persist",
		Run:       func(context.Context, *Reporter) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, m1, job.ID, StateSucceeded)
	s1.Close()

	s2, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	m2 := NewManager(s2.DB(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	found, err := m2.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("the job did not survive a restart: %v", err)
	}
	if found.State != StateSucceeded {
		t.Errorf("state after restart = %q", found.State)
	}
}
