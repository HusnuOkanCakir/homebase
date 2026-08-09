# Changelog

All notable changes to Homebase are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versioning
follows [Semantic Versioning](https://semver.org/) — with one pre-1.0 caveat spelled out
below.

## Pre-1.0 versioning

Until 1.0.0, **minor releases may contain breaking changes**. Patch releases contain only
compatible fixes.

This is the usual pre-1.0 convention, but it is worth being explicit about what it does
*not* excuse: every release, breaking or not, must migrate existing installations without
destroying data. A user who installed 0.3.0 must be able to reach 0.4.0 with their photos,
their applications and their configuration intact. Breaking an API is acceptable before 1.0;
breaking somebody's server is not.

Each release records the minimum version it can upgrade from. See
[docs/release/versioning.md](docs/release/versioning.md).

## [Unreleased]

Milestone 0 — contracts and project machinery. No product code.

### Added

- Repository skeleton, Apache-2.0 licence, editor and line-ending conventions
- Contributing guide, security policy, code of conduct, roadmap
- Pull request and issue templates, CODEOWNERS
- Hardened CI: hygiene, docs, contracts, workflow-security and secret-scanning jobs,
  with every action pinned to a commit SHA and no secrets exposed to fork pull requests
- Python-only developer tooling — `make bootstrap` needs only Python 3.11 and Git
- Architecture documentation: components, services, jobs, data layout, API conventions
- Decision records 0001–0009, including **ADR-0006**, which fixes the privilege boundary:
  unprivileged `core`, minimal `hostd`, and no generic execution path in any form
- Threat model, privilege boundaries, update security, CI security, disclosure policy
- `api/openapi.yaml` — the v1 API contract, written ahead of its implementation
- `schemas/app-manifest.schema.json` and `schemas/error.schema.json`, with valid *and*
  invalid fixtures; each invalid fixture asserts rejection by a named constraint
- Repository automation for labels, branch protection and environment checks
- `.gitleaks.toml`, allowlisting individual illustrative strings in documentation and
  fixtures rather than exempting paths — exempting `docs/` would let a real credential
  pasted into an example go unnoticed
- **The disposable VM lab** — `make vm-create` / `vm-test` / `vm-destroy` and the rest.
  Raw QEMU, UEFI, Ubuntu cloud images, no libvirt and no root
  ([ADR-0010](docs/decisions/0010-vm-lab-qemu-cloud-image.md))
- **`hostd`**, the privileged host service: a fixed registry of named typed operations over
  a Unix socket, with peer credentials from the kernel, confirmation enforced before the
  handler runs, and an audit record written *before* each action
  ([ADR-0011](docs/decisions/0011-hostd-protocol.md))
- **`core`**, the unprivileged service: HTTP API, first-run administrator setup, argon2id
  passwords, sessions stored as hashes, SQLite state with forward-only migrations, and a
  job system in which a reboot resolves itself on the next boot by comparing `boot_id`
- systemd units for both, hardened so the privilege split is kernel-enforced rather than
  intended
- `ci/go`: build, vet, race tests, govulncheck, and a check that fails if `hostd` ever
  acquires a non-standard-library dependency
- **The dashboard** — first-run setup, sign-in, system overview and a restart that asks
  you to name the machine. React and TypeScript, no UI framework, router or state
  library; about 60 kB gzipped
- A browser journey covering the milestone's exit condition, run against a real machine
  in a VM including a real reboot
- `ci/dashboard`: typecheck, lint, build, a bundle-size gate and `npm audit`
- **Debian packages** for all three components, which is where the privilege split stops
  being unit files and becomes something the kernel enforces: socket mode, service
  account, directory ownership. The build refuses to produce a package containing a
  setuid file, a writable directory, or anything outside `/usr`, `/lib` and `/etc`
- A package lifecycle test covering install, upgrade, reinstall, reboot and purge, with
  a real administrator account and a real file intact throughout — including after
  purge, which deliberately keeps user data
- **Applications.** `hostd` reads root-owned manifests and builds the container itself;
  `core` sends an application id and has no vocabulary for describing a container
  ([ADR-0012](docs/decisions/0012-hostd-owns-the-catalogue.md)). Nine privileged
  operations, a minimal Docker Engine API client written against `net/http` so `hostd`
  still has no third-party dependencies, and a catalogue of three tested applications
  shipped as its own package so that adding one is a change reviewable in a diff
- Containers are built with every capability dropped, `no-new-privileges`, a read-only
  root filesystem where the image tolerates it, and ports bound to `127.0.0.1` only
- **Uninstalling keeps the data.** Deleting it is a separate, critical operation that
  requires the application's id typed as confirmation, checked in `core` and again in
  `hostd`. "I have stopped using this" and "delete my files" are not the same intention
- `GET /events` and `GET /events/stream`, carried over from Milestone 2. Events are
  structured facts with a machine-readable type and reason — nothing downstream should
  ever parse prose to learn what happened. The stream is lossy by design: a subscriber
  that stops reading is skipped rather than allowed to block the install that produced
  the event, which is safe because the durable record is in the database
- Application screens in the dashboard, and an `unknown` state distinct from
  `not_installed` — "Homebase cannot look" and "there is nothing there" are different
  answers and must not look the same
- `make vm-test-apps`: install an application on a real machine, use it over HTTP,
  restart the machine, and check both that it came back on its own and that a file
  written into its data directory is still there after the application is removed. It
  also reads `hostd`'s audit log to confirm nothing describing a container ever crossed
  the socket — ADR-0012 is a claim about the socket, so that is where it is checked
- **Storage.** Disks are identified by filesystem UUID and mounted with systemd units
  rather than `/etc/fstab` ([ADR-0013](docs/decisions/0013-storage-identity-and-mounting.md)).
  Nine operations in `hostd`, nine endpoints in `core`, a dashboard screen, and the ability
  to give a disk to an application — which then refuses to start without it rather than
  writing to the system disk
- `make vm-test-storage`: a real disk, hot-plugged over QEMU's monitor and pulled out
  without warning. It checks that the disk is found again after being unplugged even though
  the kernel gave it a different name, that a managed mount survives a reboot, and that
  **not even root can write into the mount point while the disk is absent**
- CI now checks `api/openapi.yaml` against the routes `core` actually serves, in both
  directions. A specification that has drifted is worse than none: it reads
  authoritatively and is wrong

### Changed

- The Docker Engine API version is negotiated rather than pinned. Pinning was chosen so
  an upgraded Docker could not change a response shape underneath a root process, and
  that reasoning was wrong about how the Engine works: it does not negotiate downwards,
  it refuses anything below its own floor, and that floor rises. Docker 29 rejects the
  pinned v1.43 outright. A pinned client is one that stops working when the user upgrades
  Docker, on an appliance whose promise is that it keeps working
- An image already on the machine satisfies an install when the registry is unreachable.
  The image is pinned to a version or a digest, so the local copy is the same bytes;
  refusing would make Homebase useless in exactly the situation a local server is for
- **Restarting `hostd` no longer destroys its socket.** systemd removes a
  `RuntimeDirectory` when the service stops, and the socket the *socket unit* owns lives
  inside `hostd`'s — so a momentary stop deleted `/run/homebase/hostd.sock` while
  `homebase-hostd.socket` carried on reporting itself active and listening on a path that
  no longer existed. Nothing could connect again, nothing said so, and every upgrade
  restarts `hostd`. Found when the new `homebase-apps` package started restarting it to
  reload the catalogue; the package test now restarts `hostd` deliberately and checks the
  socket is still there and still works
- An application somebody stopped is no longer reported as having crashed. Docker keeps no
  record of who stopped a container, and the exit code cannot stand in for one — a program
  terminated by `SIGTERM` chooses its own, and `traefik/whoami` chooses 2. Homebase does the
  stopping, so `hostd` records it, in a root-owned state directory `core` cannot rewrite
- The event stream clears its write deadline. `core` sets a 60-second `WriteTimeout`, which
  is exactly what kills a connection meant to stay open for hours — silently, and looking
  from the browser like a server with nothing to say
- A failed Docker version negotiation is no longer remembered. `sync.Once` cached the
  failure as readily as the success, so a `hostd` asked for something before Docker had
  finished starting would have refused every application operation for the rest of its life
- `core` no longer logs a client that went away as an error. Every poll cancelled by a
  navigation, and every request in flight when the machine reboots, was an error-level
  entry — and a journal full of entries nobody can act on is how people learn to scroll
  past the ones they can
- `hostd` gives the service account ownership of every directory it creates under the
  application data root, not only the leaf. `os.MkdirAll` creates intermediates as root,
  which left `/srv/homebase/apps/<id>` unreadable by `core` — so it could not have backed
  the data up, and would have failed silently. Found by the VM test on its first run
- The schema's environment-variable names allow mixed case. It demanded all capitals,
  which is a convention rather than a rule, and Jellyfin ships `JELLYFIN_PublishedServerUrl`
- Secret scanning runs the gitleaks **binary**, pinned by version and verified by
  checksum, rather than `gitleaks/gitleaks-action`. The action is closed-source,
  commercially licensed, and contacts a third-party service for licence validation on
  every run — a poor trade for checking our own hygiene
- Labels are applied through `gh api` rather than `gh label`, which does not exist before
  gh 2.6. The scripts now work on whatever version a contributor happens to have
- The setup script also enables GitHub Pages, the dependency graph, Dependabot alerts,
  and requires workflow approval for **all** external contributors rather than only
  first-time ones. All four were previously documented as manual steps that GitHub has
  no API for, which was wrong

### Removed

- Documentation site deployment, until Milestone 6. `mkdocs build --strict` still runs on
  every pull request; publishing waits for an audience that will not clone the repository.
  A permanently failing workflow is how people learn to ignore failing workflows
- The `gomod` and `npm` Dependabot ecosystems, until the first `go.mod` and `package.json`
  exist. Dependabot does not ignore an ecosystem whose manifest is missing — it fails

[Unreleased]: https://github.com/HusnuOkanCakir/homebase/commits/main
