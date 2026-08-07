// Package store owns Homebase's persistent state.
//
// SQLite, in WAL mode, at /var/lib/homebase/homebase.db — see ADR-0004. The
// database is a file, so its protection is filesystem permissions; nothing here
// listens on a network and there are no credentials to misconfigure.
//
// Secrets do not live here. Credentials are held in the secrets store and
// referenced by name; this database holds the reference, never the value.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so the binary stays static
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Store is the database handle.
type Store struct {
	db *sql.DB
}

// Open opens the database and brings its schema up to date.
//
// The pragmas are not tuning. WAL is what makes a power cut survivable, which
// on a machine that lives in a cupboard is an ordinary event rather than an
// exceptional one. foreign_keys is off by default in SQLite, which quietly turns
// every declared constraint into a comment.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := path + "?" + strings.Join([]string{
		"_pragma=journal_mode(WAL)",
		"_pragma=foreign_keys(1)",
		"_pragma=busy_timeout(5000)",
		// NORMAL rather than FULL: with WAL, NORMAL survives a process crash and
		// loses at most the last transaction on a power cut. FULL costs an fsync
		// per commit, which on the spinning disks this may run on is the
		// difference between usable and not.
		"_pragma=synchronous(NORMAL)",
	}, "&")

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	// SQLite takes one writer at a time. Allowing several connections invites
	// SQLITE_BUSY under concurrency; one connection serialises writes in the
	// driver instead, which is both simpler and what the workload wants.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connecting to %s: %w", path, err)
	}

	// SQLite creates the file 0644 less the umask, which leaves it readable by
	// anyone who can reach it. The containing directory is 0750, so today that
	// is nobody — but the file holds sessions and application configuration, and
	// relying on a directory mode set somewhere else to protect it means one
	// unrelated change can expose it. Tighten it here, where the reason is
	// visible.
	if err := os.Chmod(path, 0o640); err != nil && !os.IsNotExist(err) {
		db.Close()
		return nil, fmt.Errorf("setting permissions on %s: %w", path, err)
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}

	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for packages that own their own tables.
func (s *Store) DB() *sql.DB { return s.db }

// migrate applies any migrations that have not run yet.
//
// Forward-only and recorded by filename. There is no "down" migration: on a
// machine holding somebody's only copy of their photographs, an automated
// reversal is a way to lose data quickly. Recovery from a bad migration is
// restore-from-backup, which is a deliberate act with a known outcome.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`); err != nil {
		return fmt.Errorf("creating the migration table: %w", err)
	}

	applied := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		applied[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	// Lexical order, which is why migrations are numbered.
	sort.Strings(names)

	for _, name := range names {
		if applied[name] {
			continue
		}

		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}

		// Each migration is one transaction: it applies completely or not at
		// all. A half-applied schema is the state nobody has code for.
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (name) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing %s: %w", name, err)
		}
	}

	return nil
}

// SchemaVersion returns the most recently applied migration, for diagnostics
// and for the release metadata that decides upgrade paths.
func (s *Store) SchemaVersion(ctx context.Context) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx,
		`SELECT name FROM schema_migrations ORDER BY name DESC LIMIT 1`).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return name, err
}
