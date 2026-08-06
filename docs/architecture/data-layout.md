# Data layout

Where Homebase puts things, and why it matters.

This looks like a filing convention. It is really a set of promises about what survives a
reinstall, what gets backed up, and what a misbehaving application can reach. Those promises
are only keepable if every component agrees on them from the beginning — retrofitting a
directory layout onto software that already has users means migrating live data, which is
the single most dangerous thing this project could do to itself.

## The four locations

| Path | Owner | Contents | Backed up | Survives reinstall |
|---|---|---|---|---|
| `/etc/homebase/` | `root:homebase` | Configuration | Config backup | Restored |
| `/var/lib/homebase/` | `homebase:homebase` | System state, database | Config backup | Restored |
| `/srv/homebase/` | `homebase:homebase` | User and application data | Data backup | **In place** |
| `/var/log/homebase/` | `homebase:homebase` | Logs | No | No |

The important distinction is the last column. `/srv/homebase/` is **preserved in place**
during a reinstall — the reinstall path is expected to rebuild the operating system around
existing user data without moving it. Everything else is reconstructed from backup.

## `/etc/homebase/` — configuration

```text
/etc/homebase/
├── homebase.yaml           # Server identity, network, update channel
├── core.yaml               # Listen address, session policy
├── hostd.yaml              # Operation timeouts, risk overrides
└── conf.d/                 # Drop-ins, applied in lexical order
```

Written by `hostd` on behalf of `core`; never by an application. Readable by the `homebase`
group, writable only by root — a configuration file that `core` can rewrite is a
configuration file an attacker who owns `core` can rewrite.

Secrets do not live here. See below.

## `/var/lib/homebase/` — system state

```text
/var/lib/homebase/
├── homebase.db             # SQLite: apps, jobs, events, users, audit
├── homebase.db-wal
├── secrets/                # Encrypted credential store, mode 0700
├── apps/<app-id>/          # Per-app metadata and resolved manifest
├── catalogue/              # Cached application catalogue
└── backups/                # Backup metadata, not backup contents
```

Machine-generated and reconstructable from a configuration backup. A user should never need
to open anything here, and nothing here is meaningful on a different machine without a
restore.

### Secrets

`/var/lib/homebase/secrets/` holds credentials encrypted at rest, mode `0700`, owned by
`homebase`.

Nothing outside the secrets service reads it. Components ask for a credential by reference:

```json
{ "credential_ref": "jellyfin-admin" }
```

The reference is what appears in configuration, in the API, in logs and in job records. The
value is resolved at the point of use and never returned through the API.

This indirection exists mainly for Stage 2. When a model asks about Jellyfin's status, it
needs to know a credential exists and which one — it never needs the password, and an
architecture where it *could* have it is one where prompt injection eventually gets it.

## `/srv/homebase/` — user data

```text
/srv/homebase/
├── apps/
│   ├── jellyfin/
│   │   ├── config/         # Private: only this app
│   │   └── cache/
│   └── filebrowser/
│       └── config/
├── media/                  # Shared, user-selected
├── documents/
├── photos/
└── mounts/                 # External disks
    └── <disk-label>/
```

This is the directory that matters. It holds the only copy of things people cannot replace.

Rules:

- An application gets a **private** directory under `apps/<id>/`, bind-mounted into its
  container. It cannot see other applications' directories.
- **Shared** locations (`media/`, `photos/`) are mounted into an application only when the
  user explicitly assigns them, and the manifest declares the storage as `user-selected`.
- External disks mount under `mounts/`, never directly into an application's namespace.
- Uninstalling an application does **not** delete its data by default. Removing data is a
  separate, explicitly confirmed action — the two are different intentions and must not be
  collapsed into one button.

## `/var/log/homebase/` — logs

```text
/var/log/homebase/
├── core.log
├── hostd.log
├── audit.log              # Append-only; every privileged action
└── apps/<app-id>.log
```

Rotated by size and age. Not backed up: logs are for diagnosis, and a diagnostic bundle is
built on demand from what is present.

`audit.log` is treated differently. It is append-only, it records every privileged operation
whether it succeeded or not, and it is written before the action rather than after. An
action that panics the kernel half-way through must still leave evidence it was attempted —
otherwise the record is only trustworthy for actions that worked, which is precisely
backwards.

## What must never happen

- **An application writing outside its assigned directories.** Enforced by what is
  bind-mounted into the container, not by the application's good behaviour.
- **User data under `/var/lib/`.** It would be silently destroyed by a reinstall, because
  reinstall treats that path as reconstructable. This is the single most expensive mistake
  available in this layout.
- **Configuration under `/srv/`.** It would be restored onto a different machine by a data
  restore, carrying the old machine's identity with it.
- **Secrets anywhere but the secrets store.** Not in `homebase.yaml`, not in an app manifest,
  not in a job record, not in a log line.
- **An absolute path in a manifest.** Applications declare storage by role; Homebase decides
  where it lives.

## Permissions

| Path | Owner | Mode |
|---|---|---|
| `/etc/homebase/` | `root:homebase` | `0750` |
| `/etc/homebase/*.yaml` | `root:homebase` | `0640` |
| `/var/lib/homebase/` | `homebase:homebase` | `0750` |
| `/var/lib/homebase/secrets/` | `homebase:homebase` | `0700` |
| `/srv/homebase/` | `homebase:homebase` | `0750` |
| `/srv/homebase/apps/<id>/` | `homebase:homebase` | `0750` |
| `/run/homebase/hostd.sock` | `root:homebase` | `0660` |

The socket mode is what makes the privilege split real. `0660` with group `homebase` means
`core` can reach `hostd` and nothing else on the machine can. A packaging change that widens
it to `0666` silently removes the boundary without touching a line of Go — which is why
`packaging/` requires security review.

## Backups

**Configuration backup** — `/etc/homebase/`, `/var/lib/homebase/` (database exported, not
copied live), and app manifests. Small, frequent, and enough to rebuild the server's
identity and application set.

**Data backup** — user-selected paths under `/srv/homebase/`. Large, less frequent, and the
part that actually matters.

They are separate because they have different sizes, different schedules and different
restore semantics. Restoring configuration onto a working machine is routine. Restoring data
is not.
