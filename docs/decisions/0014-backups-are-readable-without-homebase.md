# ADR-0014 — A backup is readable without Homebase, and restore is the feature

- **Status:** Accepted
- **Date:** 2026-08-09
- **Milestone:** 5 — Backup and restore
- **Related:** [ADR-0004](0004-sqlite.md), [ADR-0012](0012-hostd-owns-the-catalogue.md),
  [ADR-0013](0013-storage-identity-and-mounting.md)

## Context

Milestone 5 is the one that makes every milestone before it safe to rely on. Until now the
honest warning on every page has been that nothing here backs anything up, so nothing here
should hold the only copy of anything. This is where that stops being true.

The roadmap's exit condition is deliberately about the second half: *a clean machine restores
another machine's backup and comes up with its apps, configuration and data.* Not "a backup
is produced" — a backup is only ever a means.

Three things about the setting make the usual answers wrong.

**The machine that broke is the machine that had the backup software on it.** A home server
fails as a whole: the disk dies, the laptop is dropped, somebody spills something on it. The
user is then standing in front of a *different* computer, holding a USB disk, wanting their
photographs. Whatever they need to do next cannot require the thing that just died.

**The person restoring is not the person who set it up, or is the same person two years
later.** They will not remember a passphrase they chose once, and they will not know which
version of anything wrote the disk.

**Nobody tests a backup.** Everyone intends to. The backups that fail are the ones that were
running successfully for two years and had never been read.

## Decision

### A backup can be read without Homebase

The contents are plain files in a plain directory tree, with a JSON manifest beside them.
Recovering a photograph means finding it in a folder and copying it. No archive to extract,
no tool to install, no version to match.

This is the decision everything else follows from, and it is expensive: no deduplication, no
compression of user data, no clever incremental format. Those are real costs on a disk full
of films.

They are accepted because the alternative fails in exactly the situation a backup exists
for. A backup readable only by Homebase does not protect against Homebase being what broke —
and "install this specific version of a pre-1.0 project on a machine you do not have" is not
a recovery procedure, it is an apology.

The manifest is JSON rather than a binary index for the same reason. Somebody should be able
to open it in a text editor and understand what they are looking at.

### Homebase's own state is exported, never copied

The SQLite database is written out with `VACUUM INTO`, not copied.

A live SQLite database has a write-ahead log beside it, and copying the main file while the
service is running produces a file that is either stale or corrupt — usually stale, which is
worse, because it restores successfully and is silently missing the last week. `VACUUM INTO`
asks SQLite for a consistent snapshot and is the only supported way to get one from a
running database ([ADR-0004](0004-sqlite.md)).

### A backup never goes on the disk it is backing up

Homebase refuses to write a backup to a location that holds any of the data being backed up,
and refuses to back up to the system disk.

A copy on the same disk protects against exactly one thing — somebody deleting a file by
accident — and against nothing else. Disks fail whole. Presenting that as a backup is worse
than having none, because the user believes they are covered.

### Every backup carries checksums, and they are checked

Each file's SHA-256 goes in the manifest when the backup is written, and is verified when it
is read. `backup.verify` re-reads a backup and reports what no longer matches.

This is not defensive decoration. A backup on a USB disk in a drawer bit-rots, gets partially
overwritten, or was never fully written because the disk filled up. Every one of those
failures is silent, and every one is discovered at the worst possible moment unless somebody
looks first.

### A failed backup is loud

A backup that stops working is reported as an error event and shown on the dashboard until it
succeeds again. It is never left as a quiet entry in a log.

The failure mode this exists for: backups stop, nobody notices for eight months, and the
first anyone knows is when they need one.

### Restore says what it will do before it does it

`backup.preview` reports what a backup contains, what is on the machine now, and what would
change — before anything is touched. Restoring is the operation with the highest ratio of
"irreversible" to "understood": the user is usually anxious, often in a hurry, and may be
about to overwrite something they still needed.

Restoring **never deletes** anything the backup does not contain. A restore is a merge, not a
mirror. Somebody restoring last month's backup onto a working machine to recover one
application must not lose the three applications they added since.

## Alternatives considered

### restic, borg or similar

Mature, deduplicating, encrypting, incremental. Genuinely better at being a backup tool than
anything written here will be.

Rejected on the first decision: the backup would then be readable only with restic and the
right key. That is a fine trade for somebody who chose restic; it is the wrong trade for
somebody who was promised they would never need a terminal, and who is now holding a disk in
front of a borrowed laptop.

It also puts a large third-party binary in the privileged path, which
[ADR-0002](0002-implementation-languages.md) resists for `hostd` specifically.

Worth revisiting if Homebase ever offers *remote* backup, where deduplication stops being a
nicety and bandwidth makes plain copies untenable.

### A single tar archive per backup

Simpler to write, and one file is tidier than a tree.

Rejected because recovering one photograph would mean extracting or scanning a multi-terabyte
archive, and because a tar file truncated by a full disk loses everything after the
truncation rather than one file. A tree degrades gracefully: a missing file is a missing
file.

Configuration *is* small enough for this, and is stored as a tree anyway so that one rule
covers both.

### Incremental backups with hard links

The rsync-snapshot approach: each backup is a full tree, unchanged files hard-linked to the
previous one. Cheap in space, still plain files.

Genuinely attractive and not ruled out later. Rejected for this milestone because a hard-link
tree is easy to destroy by accident — a user copying the backup folder "to be safe" turns
every link into a full copy and fills the disk — and because the first version of a backup
system should be the one whose failure modes are obvious.

### Encrypting backups by default

The disk leaves the house, or is stolen with the laptop.

Rejected as a default for now, on the recovery argument: a key the user has lost is a backup
they no longer have, and the most likely person to be locked out is its owner. It belongs
with the credential and key management that Milestone 8 has to build anyway, where there is
somewhere sensible to keep a recovery key.

Stated plainly in the user guide meanwhile: **a backup disk is readable by anyone holding
it.**

## Consequences

### What this makes easier

- A user can recover a single file with a file manager, on any computer
- A restore can be verified by reading the manifest, without trusting the code that wrote it
- Homebase's own state and the user's data have the same shape, so one restore path covers
  both
- A partially written or partially corrupted backup is partially usable

### What this makes harder

- **Space.** Every backup is a full copy. A 500 GB media library backed up weekly needs a
  large disk, and the interface has to be honest about that rather than cheerful
- No protection against a stolen disk until encryption arrives
- Backing up a large library is slow, and the job has to survive being watched — a progress
  bar that stalls for twenty minutes on one large file is one people cancel
- `hostd` grows again, which is now the third milestone in a row to say so
  ([ADR-0012](0012-hostd-owns-the-catalogue.md),
  [ADR-0013](0013-storage-identity-and-mounting.md))

### Security impact

A backup is a copy of everything, sitting on a disk that is designed to leave the machine.
That makes it the highest-value target in Homebase.

- **Credentials are not backed up in a usable form.** Password hashes travel with the
  database because the accounts have to survive, but the secrets store is excluded until
  there is a key management story. A restored machine asks for the credentials it needs
  again rather than silently carrying somebody else's
- Backups are written `0700`, owned by the service account, on a filesystem mounted
  `nosuid,nodev,noexec` like every managed location
- `core` names a backup by its id and a destination by its location id. It never sends a
  path, in keeping with [ADR-0013](0013-storage-identity-and-mounting.md)
- `backup.restore` is `critical` and requires explicit confirmation. It is the third
  operation that can destroy data irreversibly — and unlike the other two, the data it
  overwrites is usually the data somebody is trying to save
- The manifest is not a trust boundary. Restore validates every path in it against the
  destination root before writing, exactly as application storage does: a backup disk is
  untrusted input, and a manifest naming `../../etc/shadow` must not be able to write there

### What would make us revisit this

- Remote or off-site backup, where plain copies stop being affordable
- Encryption, which needs key management to exist first
- Libraries large enough that a full copy per backup is no longer sensible, at which point
  hard-linked increments become worth their sharper edges
