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
  the kernel gave it a different name, that a managed mount survives a reboot, that **not
  even root can write into the mount point while the disk is absent**, and that an
  application whose disk is gone refuses to start rather than running without its files
- `make vm-run`: Homebase installed from its own packages onto a throwaway VM and left
  running, with a blank disk plugged in. Every VM target before it created a machine,
  asserted something and destroyed it — there was no way to simply use the thing
- Disks filling up are noticed before applications start failing to write. A server that
  runs out of space does not announce it: applications fail in whatever way each of them
  fails, and the common cause is visible only to somebody who thinks to look
- **Backup and restore.** A backup is plain files with a JSON manifest, readable without
  Homebase ([ADR-0014](docs/decisions/0014-backups-are-readable-without-homebase.md)). Six
  operations in `hostd`, six endpoints in `core`, a dashboard screen built around restoring
  rather than around backing up, and a README inside every backup written for somebody
  whose server has died and who is looking at the disk on a borrowed computer
- The database is exported with `VACUUM INTO`, never copied. A live SQLite database has a
  write-ahead log beside it, and copying the main file gives something stale or corrupt —
  usually stale, which is worse, because it restores successfully and is quietly missing the
  last week
- Homebase refuses to back up onto a disk an application keeps its files on, and says which
  application and why. A copy on the same disk protects against deleting a file by accident
  and against nothing else
- Restoring is a merge, not a mirror: nothing the backup does not contain is deleted, and a
  file whose checksum no longer matches is skipped rather than written over a good one
- `make vm-test-backup`: two machines. One is set up, backed up onto a USB disk and
  destroyed; a second is created from scratch and the backup restored onto it. It reads a
  backed-up file with `cat`, without Homebase doing the reading
- **An installer** ([ADR-0016](docs/decisions/0016-installation-media.md)).
  `homebasectl installer create` writes a USB stick: Canonical's Ubuntu Server image byte for
  byte, with the autoinstall configuration and Homebase's own packages on a partition
  appended after it. The image is never repacked, so the boot path stays the one Ubuntu
  publishes and the media can still be checked against its publisher
- The packages travel on the stick rather than coming from the network, so a house with no
  working internet still ends up with a server. Applications still need the internet, because
  a container image comes from a registry however the machine was installed
- `homebasectl installer devices` lists what may be written to and, more usefully, refuses
  what may not: anything holding a mounted filesystem, anything not removable, anything too
  small. Writing to a drive asks for its name to be typed first
- The installed machine behaves like a server rather than a laptop: a firewall that allows
  the dashboard and nothing else, a closed lid that does not switch it off, and no suspending
  itself while nobody is looking
- Its own screen says where to browse to, worked out when the message is shown rather than
  written in at install time — a screen confidently showing the wrong address is worse than
  one showing none
- A **getting-started list** on a newly claimed server: what is worth doing, and why. It
  reads its state from what the server actually reports rather than remembering what was
  clicked, so a disk that gets removed brings its step back
- **Servers can be given a name.** `system.rename` asks systemd-hostnamed rather than
  writing `/etc/hostname` itself, so the privileged service never needs write access to
  `/etc`. Renaming is offered on the getting-started list while the machine still has the
  name the installer gave it, and under **This server** for ever
- **Password recovery** ([ADR-0015](docs/decisions/0015-password-recovery.md)). A recovery
  code shown once at first-run setup: 125 bits, five groups of five, from an alphabet with
  no `I`, `L`, `O` or `U` because it is copied off paper by hand. Stored as an argon2id
  hash, single-use, and using it signs out every session
- A signed-in administrator can see whether a code exists and issue a fresh one, under
  **Security**. The code itself is never shown twice, which is what lets it be stored the
  way a password is
- `homebasectl recovery-code`, for when the paper is gone too. It deliberately does not set
  passwords: typing one at a terminal leaves it in scrollback, and the browser already does
  that job correctly
- A recovery code travels with a backup, so the paper written at setup opens the machine
  rebuilt from the disk. Without it, restoring after being locked out restores the lock —
  which is what made this urgent rather than merely missing
- Sign-in, first-run setup and recovery are rate limited per address, and argon2 is bounded
  to four concurrent computations. Each verification reserves 64 MiB by design and none of
  the three needs a credential to reach, so an unbounded one is memory exhaustion by
  anonymous request. Successful attempts are refunded: rationing correct sign-ins would
  punish the household rather than the attacker
- CI now checks `api/openapi.yaml` against the routes `core` actually serves, in both
  directions. A specification that has drifted is worse than none: it reads
  authoritatively and is wrong
- **A server is reachable by name, over HTTPS, on the ordinary ports**
  ([ADR-0017](docs/decisions/0017-local-https-and-discovery.md)). `https://attic.local`,
  with no port number in it, from any device in the house. `core` generates its own
  ECDSA P-256 certificate at first start, valid for ten years, and binds 80 and 443 with
  `CAP_NET_BIND_SERVICE` — one narrow capability, not root and not a privileged proxy.
  Port 80 answers nothing but a `307` to the same name on 443, so the dashboard has exactly
  one origin and one set of cookies
- The machine prints its certificate's fingerprint **on its own screen**, beside the address
  to browse to, and logs it so it can be read out over the phone. The browser warns once per
  device, which is the honest cost of a server that has no public name to get a certificate
  for; the fingerprint is what makes that warning something to check rather than something to
  dismiss
- Names come from mDNS, with `avahi-daemon` a dependency of `homebase-core`. `mdns_works` is
  reported only when the responder is actually running, checked rather than assumed — a name
  the network cannot resolve sends somebody to type an address that will never load
- `network.status` and `GET /network` — addresses, router, resolvers, and whether the
  internet answers, read live from the kernel on every request. Reachability is tested by
  opening a TCP connection to two well-known resolvers **by address**, because ICMP is
  blocked on plenty of networks and resolving a name first would make a broken resolver look
  like a broken connection
- **A Network screen that says which of three faults it is.** *The server has no address*,
  *the server is fine and the internet is not*, and *nothing here is wrong* are
  indistinguishable from a browser that will not load, and somebody without that distinction
  restarts their router for an hour over a problem with their phone's Wi-Fi. `reachable` and
  `online` are separate fields for that reason
- `make vm-test-network` — the first test here that involves two machines, on one network
  segment joined by a QEMU multicast socket rather than a bridge, since bridges need root and
  [ADR-0010](docs/decisions/0010-vm-lab-qemu-cloud-image.md) says the lab does not have it.
  The second machine resolves the name, loads the dashboard over HTTPS, checks the
  certificate covers that name and that plain HTTP redirects to it. Then the server's route
  out is taken away and it has to report itself offline while still saying it is reachable
- `make check` runs the Go tests. It stopped at formatting and vet, which is how a commit
  with a failing test got through: the gate did not cover the thing that had changed
- **The browser journey runs over HTTPS**, at the port a household uses, with the
  "proceed once" a person clicks granted explicitly. The loopback plain-HTTP origin that
  concealed the `Secure` cookie bug is no longer available to conceal the next one
- `update.status` and `GET /system/update` — what version this machine is running, whether
  its four packages agree, and whether dpkg left a transaction unfinished. Reporting comes
  before changing: the interruption matrix asserts against this, and a broken update is
  diagnosed with it ([ADR-0018](docs/decisions/0018-updates-are-a-signed-apt-repository.md))
- The package suite stages a half-applied upgrade — two packages moved, two left behind —
  and asserts Homebase reports it. Every package still reads as fully installed and nothing
  has failed, which is why a single version string would call it healthy
- **A signed APT repository, and a machine that updates from it.**
  `scripts/build-repo.py` publishes a Debian archive with a suite per channel, signs the
  `Release` that names every index's SHA-256, and **promotes without rebuilding** — the
  index entry records the checksum the source channel tested, so an artifact rebuilt under
  the same version is refused rather than shipped to stable
- `update.configure` and `update.check`. The channel is validated against four literal words
  before it reaches apt's configuration, because that file decides what code the machine runs
  as root. `reachable` is reported separately from "no update available": they are the same
  silence and very different facts
- **`hostd` does not run apt.** Its unit sets `RestrictAddressFamilies=AF_UNIX AF_NETLINK`,
  so the root service cannot open a network socket — deliberately. Rather than widen that,
  the work that reaches the network happens in `homebase-update-check.service`, a fixed
  unit the package installs and `hostd` starts through systemd. Nothing from a request
  becomes part of a unit name or a command line
- **`update.apply`** — everything is downloaded and verified before anything is applied, so
  a failure while fetching changes nothing at all. Then the database and `/etc/homebase` are
  snapshotted (`VACUUM INTO`, not a copy of a live database), the packages are installed, and
  the machine is asked three questions: are all four at the version we meant, did the
  services come back, and does the dashboard actually answer. A failure puts the previous
  version back and restores the snapshot. `/srv/homebase` is deliberately not snapshotted —
  no package touches it, and copying hundreds of gigabytes would fill the disk it is meant
  to protect
- **Applying an update is asynchronous, because it has to be.** The update replaces
  `homebase-hostd` and restarts it, so the process holding the request open is the process
  being replaced. `update.apply` starts the unit detached; `update.progress` reads the stage
  the unit records as it goes — which also lets a dashboard disconnected by the restart
  reconnect and find out how it went
- An update older than what is installed is refused. An attacker able to serve an
  old-but-validly-signed index can otherwise push a machine back to a version with a known
  hole, with every signature involved genuine
- `publish` keeps the version it replaces in the channel's index. Rollback is
  `apt-get install homebase-core=<previous>`, and apt can only install a version its index
  lists — an archive indexing one version per suite is one you cannot roll back from
- **Milestone 8's exit condition is met**: power is cut with QMP `system_reset` while `dpkg`
  is mid-write, and the machine boots with its application data intact, reports itself
  `interrupted`, and is finished by the `dpkg --configure -a` its own error message names.
  A killed process flushes its writes and a power cut does not, which is why this uses a
  reset rather than a signal
- Rollback is proven by forcing it. A release that installs cleanly and then does not
  work — core's binary replaced inside an otherwise real package — fails the health check,
  and the previous version and the database snapshot are put back
- **An Updates screen**, and the API behind it: `GET /system/update`, `/system/update/progress`,
  `POST /system/update/check`, `/system/update/channel` and `/system/update/apply`. The screen
  is shaped by the server going away part way through: an update restarts the services that
  serve the page, so a failed request during one is the expected middle of a *successful*
  update. It polls, treats that failure as normal, and reads the outcome back from the server
  rather than remembering what it started
- The stages are translated rather than passed through. "applying" is accurate and tells a
  person nothing; what they want to know is whether it is safe to walk away, and the answer
  differs between downloading and installing
- **Bills of materials**, in CycloneDX, written by `make packages` and published beside the
  artifact they describe. Read from the linked binary with `go version -m` rather than from
  `go.mod`: one lists what was asked for, the other what arrived, and an SBOM that overstates
  an artifact turns every advisory into a false alarm
- The build now **fails if `homebase-hostd` gains a third-party dependency**, checked against
  the binary rather than against `go.mod` — which is where something arriving through a
  transitive path would show up. Its bill of materials is empty, and that emptiness is a
  claim being made rather than a file nobody wrote
- SBOMs are deterministic: the same inputs produce the same bytes, because ADR-0018 promotes
  artifacts by comparing checksums and a document that changed on every build would be one
  more thing that could not be compared
- `make vm-test-update` (303s): a real archive over HTTP, a real machine, and the checks
  the design exists for. **An archive tampered with and re-signed by somebody else's key is
  refused**, and the version inserted into it is never offered. **A package Homebase does not
  ship is not installable from Homebase's origin** — `Signed-By` binds a key to a source, not
  to package names, so without the origin pin one compromised key would replace anything on
  the machine

### Changed

- **Restarting `hostd` no longer leaves it unable to start.** Its unit hard-required
  `/etc/homebase` in `ReadWritePaths`, which `homebase-core`'s maintainer script creates — so
  installing `homebase-hostd` on its own gave a privileged service that exited
  `226/NAMESPACE` before running a line of Go, and then retried every two seconds, 673 times,
  because nothing limited it. The paths are optional now, hostd's own package creates what it
  needs, and the unit gives up after five attempts and stays failed where `systemctl status`
  can explain it
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
- **Signing in works on a real installation.** The session cookie was marked `Secure`, and
  browsers refuse a `Secure` cookie from a non-secure origin — except on `localhost`, which
  is the only origin every browser test in this repository ever used. So a server reached at
  `http://192.168.1.50:8080` silently discarded the session and answered `401` straight after
  a correct password, while the suite stayed green. The attribute now follows whether the
  request actually arrived over TLS, and since this milestone it always does
- **An application that is crash-looping is no longer reported as running.** Docker reports a
  restarting container as `Running: true`, and Homebase repeated it. File Browser had been
  installed and called healthy since Milestone 4 while it panicked in a loop, and the storage
  test asserted that exact word — so the suite agreed with it. Restarting is now reported as
  failed, and the test asserts the application is still up a minute later with no restarts
- **Jellyfin starts on a machine with no graphics card.** Device passthrough demanded every
  device in the manifest, so `/dev/dri` — absent on any headless machine, including every VM
  this is tested in — failed the container with `error gathering device information`. Missing
  optional devices are skipped
- **Applications can write their own data.** Containers run with `CapDrop: ALL`, which
  removes `CAP_DAC_OVERRIDE`, so a container running as a non-root user could not write to
  the directory Homebase had made for it. Each application now gets a stable uid and gid of
  its own, allocated by `hostd` and recorded on disk, and owns the directories it writes to —
  and one application still cannot read another's files
- **Installing an application that needs a disk says so before downloading it.** The storage
  requirement was checked after the image was pulled, so somebody without a disk attached
  waited for several hundred megabytes to arrive before being told it could not be installed
- `homebase-hostd` declares its dependency on `sqlite3`. It was missing, and invisible,
  because a VM test had been made to pass by installing the package itself — which is the
  same failure as the two above: the test was run against a machine that was not the user's
- **A signature failure is no longer reported as "up to date".** `apt-get update` exits `0`
  when a source fails, on the assumption that other sources will cover it — but Homebase
  refreshes exactly one source, so a repository signed by the wrong key was reported as
  reachable and current. `APT::Update::Error-Mode=any` corrects it. Found by the test that
  tampers with the archive on purpose, in the code that reacts to verification rather than
  in verification itself, which is where a correct-archive test can never look
- Disks smaller than 1 GiB are not offered as storage. They are boot partitions, EFI system
  partitions and card readers with nothing in them, and offering them invites somebody to
  give an application a disk that cannot hold anything

### Removed

- Documentation site deployment, until Milestone 6 — **restored in Milestone 6**, now that
  somebody who has installed a server from a USB stick cannot be told to browse to
  `docs/user-guide/backup.md` on GitHub. `mkdocs build --strict` ran on every pull request
  throughout, because validation and publication are separate concerns
- The `gomod` and `npm` Dependabot ecosystems, until the first `go.mod` and `package.json`
  exist. Dependabot does not ignore an ecosystem whose manifest is missing — it fails

[Unreleased]: https://github.com/HusnuOkanCakir/homebase/commits/main
