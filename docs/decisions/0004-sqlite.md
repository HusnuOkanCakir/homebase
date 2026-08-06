# ADR-0004: SQLite for system state

- **Status:** Accepted
- **Date:** 2026-08-06

## Context

`core` needs durable state: users and sessions, installed applications and their manifests,
jobs and their history, events, the audit log, storage assignments, backup records.

The workload is small and lopsided. A household has one to five users, perhaps a dozen
applications, and a handful of jobs at a time. Writes are infrequent; reads are mostly the
dashboard polling. The largest table will be the audit log, growing by hundreds of rows a day.

The operating constraints matter more than the workload. This runs unattended on an old
laptop that loses power without warning, and the person who owns it cannot be asked to
administer a database.

## Decision

SQLite, in WAL mode, at `/var/lib/homebase/homebase.db`, embedded in `core`.

## Alternatives considered

### PostgreSQL

The obvious default for a service that stores relational data, and better at nearly
everything SQLite is worse at.

Rejected because every one of those advantages addresses a problem Homebase does not have,
while adding a second thing that must be installed, started, upgraded and backed up on a
machine whose owner will not be doing any of that. A failed Postgres upgrade on a user's home
server is a support burden this project cannot carry.

It would also add a genuine failure mode: `core` cannot start because the database will not
start, on a machine where the user has no terminal and no idea what a database is.

### A file-based store — JSON or YAML on disk

Tempting for a small amount of state, and inspectable with a text editor.

Rejected on crash safety, which is the deciding constraint. This machine loses power. Writing
JSON atomically is possible; keeping several files mutually consistent across a power cut is a
transaction system, and writing one badly is far worse than using one that already exists.

Concurrent access from a job worker and an HTTP handler would need locking that also has to be
correct.

### An embedded key-value store — BoltDB, Badger

Crash-safe, transactional, no server, good Go support.

Rejected mainly for inspectability and querying. When a user's server misbehaves, being able
to open `homebase.db` with the standard `sqlite3` tool and read the job history is worth a
great deal during support. "What happened before the backup failed?" is a SQL query against
SQLite, and a custom program against a key-value store.

Migrations are also a solved problem in SQL and an exercise in KV stores.

## Consequences

### What this makes easier

- No database to install, start, secure, upgrade or explain
- Backing up system state is a file — a `VACUUM INTO` snapshot, consistent by construction
- Crash safety comes from WAL rather than from code we would have to write
- Support and debugging via the standard `sqlite3` tool
- Real SQL for the audit log, which is where ad-hoc questions actually get asked

### What this makes harder

- One writer at a time. Fine for this workload, but the job system must not hold a write
  transaction open across a long operation — that is a rule with teeth, and a mistake here
  looks like the whole dashboard hanging
- No network access to the database, so a future multi-server feature would need something
  else entirely
- WAL files complicate naive backup: copying `homebase.db` alone can produce a torn snapshot.
  Backups must go through `VACUUM INTO` or the backup API, and this is exactly the kind of
  detail that gets forgotten and then loses somebody's configuration
- Schema migrations must be tested against real upgrade paths, since there is no DBA to
  intervene when one fails

### Security impact

Positive, on balance. No listening socket, no network authentication, no default credentials
— a category of misconfiguration simply removed.

The database is a file, so its protection is filesystem permissions: `0640`, owned by
`homebase`. Anything that can read it reads sessions and application configuration.

Secrets do **not** live in the database. They live in the encrypted secrets store — see
[data layout](../architecture/data-layout.md#secrets).

### What would make us revisit this

- Multi-server support, which is explicitly out of scope but would invalidate this immediately
- Write contention appearing in practice, most likely from the audit log during a large job
- The audit log growing past the point where a single-file database is comfortable, which
  would more likely be answered by moving audit records to their own rotated store than by
  changing databases
