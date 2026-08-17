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
- **Backups can run on a schedule** — daily or weekly, carried here from Milestone 5. A
  systemd timer rather than a ticker inside `core`: `core` is restarted by every update and
  stopped whenever somebody is working on the machine, and a scheduler that only runs while
  the thing it is part of is running is one that stops without saying so
- `Persistent=true`, which is the setting that makes this work on the hardware Homebase is
  for. A laptop in a cupboard is asleep or unplugged at three in the morning more often than
  not; without it the backup is skipped silently, every night, until the day somebody needs it
- The schedule is a word — `daily`, `weekly`, `off` — mapped to a calendar expression by a
  fixed table. Nothing from a request is ever written into a unit file, because a unit file
  is a way to run things
- **The destination is checked when the schedule is set**, not at three in the morning. A
  schedule pointing at a disk that cannot hold a backup is a promise that fails in the dark,
  weeks later, to somebody who was told it was working
- `backup.get_schedule` reports whether systemd will *actually* run it, read from systemd
  rather than from the file — a schedule recorded on disk and never enabled is exactly the
  failure worth making visible — along with when it next fires and how the last one went
- **Updates are looked for on their own**, once a day, enabled on every installation. The
  unit does nothing until a channel is configured, so this costs nothing on a machine that
  has never been pointed at one — and it means a server nobody touches again still finds out
  that a security fix exists. Once a day rather than hourly, because checking more often
  would only tell the archive how many servers exist and how often each is switched on,
  which a local-first product has no business collecting about its own users
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
- `make vm-test-update` (310s): a real archive over HTTP, a real machine, and the checks
  the design exists for. **An archive tampered with and re-signed by somebody else's key is
  refused**, and the version inserted into it is never offered. **A package Homebase does not
  ship is not installable from Homebase's origin** — `Signed-By` binds a key to a source, not
  to package names, so without the origin pin one compromised key would replace anything on
  the machine
- **The backup schedule in the dashboard**, with the last run's outcome beside it. A schedule
  is a promise made once and kept nightly, and the way it fails is silently — so anything
  that shows the promise has to show whether it is being kept. `enabled` comes from systemd
  rather than from what was asked for, and a schedule pointing at a disk that is not
  connected says so by name
- **A release workflow.** `release.yml` turns a tag into a signed archive; `promote.yml`
  moves a tested artifact between channels. The build job holds no secrets and no
  environment, which is what makes its Sigstore provenance attestations worth having — the
  strongest claim about where an artifact came from has no long-lived secret behind it. Its
  dependency caches are switched off, and only there: a poisoned cache costs CI a wrong test
  result and would cost a release a signed artifact installed as root
- The publish job's **environment is the channel**, so the manual approval belongs to
  `stable` rather than to a workflow file somebody could edit in a pull request
- `build-repo.py verify` reads a finished archive back the way apt will — `gpgv` against the
  exported keyring, `Release` against the index, the index against every file in the pool —
  and shares no code with the writer, so an archive that is self-consistent and wrong fails
  here rather than shipping
- `scripts/release.py` decides what a tag releases, and **no tag reaches stable**: `v0.2.0`
  is refused with a message saying to tag a beta and promote it. It also converts the semver
  prerelease to `0.2.0~alpha.1`, because `-` is not special to dpkg and every machine would
  otherwise treat the alpha as newer than the release it precedes
- `tests/unit/test_repo.py`, in `ci/contracts` on every pull request: builds a real signed
  archive from four empty `.deb` files and a throwaway key, then breaks it five ways — a
  rebuilt package, an edited index, the wrong signing key, a missing package, an expired
  index — and checks each break is refused. No Go, no Node, no VM
- **Recovery, from the browser.** A diagnostic file, a repair, and a factory reset, under
  *Something's wrong* — named for what somebody is thinking rather than for what it does
- The diagnostic file **says what it does not contain**, on the screen, at the moment
  somebody is deciding whether to send it to a stranger. The VM test plants recognisable
  strings in the database directory, the configuration and the user's files, and greps the
  finished bundle for them: a support tool that quietly contains the password database is a
  disclosure with a download button
- What it collects is a fixed list of commands with fixed arguments, and the download takes
  **no filename from the caller** — core serves the newest file in one directory, so a
  traversal has nothing to traverse
- **Repair is a fixed list, not a diagnosis**: finish an interrupted package transaction,
  put back directories and their ownership, start and enable the services. Nothing is
  deleted, which is what makes it safe to offer to somebody who does not know what is wrong,
  and **changing nothing is reported as a result** rather than as success — being sent away
  believing a broken server was repaired is worse than being told nothing was found
- It is proven on the machine that has just lost power mid-`dpkg`, which is the only
  genuinely half-upgraded machine in the suite, and pressed twice, because somebody who does
  not know what is wrong will press it twice
- **A factory reset keeps your files by default**, and `keep_data` absent means keep — a
  plain boolean would have made forgetting the field mean "delete everything". It takes the
  server's own name typed by hand, removes the certificate so the machine gets a new
  identity, and deliberately keeps the update channel, because a machine that forgets where
  its security fixes come from is worse than one that remembers

- **Wi-Fi setup from the dashboard**, carried from Milestone 7 and now proven rather than
  deferred again. `mac80211_hwsim` puts simulated radios on the real `mac80211` stack, so
  `make vm-test-wifi` runs `hostapd` with WPA2 as the house router and has the server join
  from a second radio — a real association and a real four-way handshake, with only the radio
  simulated. What it cannot cover is drivers, firmware and roaming, which still need laptops
- **A wrong password changes nothing, and says so.** The previous configuration is put back
  and re-applied *before* the error returns, so the caller waits for the rollback and the
  message can honestly say the server is connected exactly as it was. This is the whole
  reason the feature was held back: every other operation in Homebase fails by not working,
  and this one can fail by making the machine unreachable from the browser configuring it
- The Ethernet configuration is never read or written. Homebase writes one netplan file, and
  wireless takes a worse route metric — so a machine with both prefers the cable, and a
  failed attempt over a cable costs nothing. Checked by comparing the wired address either
  side of a failure, and the routing table after a success
- **The settings file is JSON**, because a netplan file is YAML and JSON is valid YAML. It is
  produced by an encoder rather than by formatting, so a network name containing a quote or a
  newline is escaped by construction — in a file that decides what the machine connects to
- The passphrase appears in none of: the wireless status, a scan, the audit log, or a
  diagnostic file. Checked in the VM against a real one that was really used
- **An operation can now declare which of its fields are secret**, and the audit log records
  those as `[redacted]`. The log holds the parameters of every privileged call, append-only
  and kept indefinitely, and that was safe on the strength of an invariant: hostd deals in
  references — an application id, a disk id — never in values anybody would mind seeing.
  Wi-Fi is the first genuine exception, because netplan needs the passphrase itself. The
  declaration lives on the operation, appears in `--describe`, and is enforced in both
  directions by `scripts/check_operations.py`
- The field is redacted rather than removed: "there was a passphrase and it is not recorded"
  and "there was no passphrase" are different facts, and somebody reconstructing an incident
  needs the first

- **Secure Boot.** A Homebase machine boots, installs and runs with the firmware enforcing
  signatures and Microsoft's keys enrolled — which is how every laptop bought in the last
  decade ships, and where a failure would happen before there is anything to read a log from.
  `make vm-test-secureboot` uses OVMF's Secure Boot build with the `.ms` variable store and
  SMM on, so the key store is protected the way it is on real hardware. The first assertion
  is that the firmware is *actually* enforcing — `mokutil`, the kernel's `SecureBoot`
  variable and `SetupMode` — because a machine booted with the empty store sits in setup
  mode, boots anything, and would pass every other assertion while proving nothing
- **The machine can say it is too hot.** An old laptop in a cupboard with a dusty fan
  throttles, slows, and eventually shuts off, which from outside is indistinguishable from
  Homebase being broken. The hottest zone rather than an average, thresholds deliberately
  high — warm at 80 °C, hot at 90, because processors are designed to run at 80 and warning
  at 60 teaches people to ignore the warning — and **no sensors means no reading, never
  zero**. Homebase does not control fans and does not pretend to; it says so, which is the
  difference between opening a cupboard door and buying a new laptop

- **`homebasectl` drives the server.** `system`, `apps`, `storage`, `backup`, `update`,
  `network`, `repair` and `diagnostics` — an ordinary client of the same HTTP API the
  dashboard uses, with the same permission checks, job records and events. As root it could
  open hostd's socket directly and deliberately does not: a second path to a privileged
  operation is a second place for the checks to be wrong
- **Authentication by being on the machine.** Running as root it reads the database and
  mints a ten-minute session — which is what root can do anyway, with `sqlite3`, less
  carefully. No token to create, store, rotate or leak, and `sudo homebasectl apps` simply
  works. An unprivileged caller needs `HOMEBASE_TOKEN` instead, which is deliberately the
  less convenient path
- `--json` everywhere, printing core's answer unmodified. That is the interface to build on;
  a CLI whose JSON is its own invention drifts from the API it claims to expose
- Exit codes a script can branch on: `1` failed, `2` used wrongly, `3` the server is not
  answering. A script that cannot tell "that operation failed" from "Homebase is down" will
  eventually treat both the same way
- Anything slow waits and reports how it ended rather than returning a job number, so the
  polling loop is written once here instead of once per caller
- **The Wi-Fi password is never an argument** — `HOMEBASE_WIFI_PASSWORD`, or a prompt with
  echo off. An argument is in the shell history and in `ps` output for every user on the
  machine for as long as the command runs
- **The destructive commands**: `backup restore`, `storage format`, `storage attach`,
  `storage detach` and `factory-reset` — with a confirmation designed for a shell rather
  than copied from the browser. In a form field, "type the id to confirm" works because the
  field is empty and the id is on screen; at a shell the id is already in the command that
  listed it and the up arrow re-runs the last thing. Three things replace it: a **preview**
  printed from the server before anything happens, a **terminal required** unless `--confirm`
  is passed explicitly, and a confirmation that is **the thing's own name** rather than a
  word. There is no `--yes`, because a flag meaning "do it anyway" ends up in every
  invocation within a week

- **Remote access, over self-hosted WireGuard**
  ([ADR-0019](docs/decisions/0019-remote-access-is-self-hosted-wireguard.md)). No account, no
  subscription, no coordination server — the first principle in the README is that nobody can
  switch Homebase off or start charging for it, and that principle decides this one almost on
  its own. `homebasectl vpn setup`, `add-device`, `remove-device`, with a QR code a phone can
  scan
- **A device's key is shown once and stored nowhere.** The server generates both halves — it
  must, because a phone joins by scanning a code that has to contain the key — and keeps only
  the public one. A lost configuration cannot be re-shown; remove the device and add it
  again. Every comparable tool keeps them on the server, which is convenient until somebody
  gets into the server and leaves with every device's identity
- **Reachability is a completed handshake, not a probe.** "Is my router forwarding the port?"
  cannot be answered from inside the house without asking somebody else's service. So it is
  not asked: a handshake proves the name resolved, the router forwarded and the key was
  accepted, with evidence rather than a guess
- Only the house's traffic goes over the tunnel. A full tunnel would route a phone's entire
  internet through a domestic upload link and a machine in a cupboard
- Setting up again keeps the server's key and every device. Regenerating it would silently
  invalidate everything already handed out, which somebody discovers on holiday
- `make vm-test-vpn` (138s): two machines, one with no Homebase on it, which completes a
  handshake, reaches the dashboard over the tunnel, and stops reaching it the moment its key
  is removed. The private key it was handed is grepped for across `/etc`, `/var/lib` and the
  audit log, and is in none of them

- **Dynamic DNS**, so a home connection whose address changes stays reachable. The provider
  is a word from a fixed table, never a URL — a URL from the caller would be a way to make
  the machine fetch an arbitrary address as root. The token is declared secret, so it is
  redacted from the audit log
- **A name that stopped updating says so.** Every dynamic DNS client updates a name; the
  failure that matters is the one where it stopped three weeks ago, because the result is a
  server nobody can reach that looks exactly like one that is fine
- **`homebasectl wake`**, which talks to nothing — a magic packet is a UDP broadcast any
  process can send, so routing it through the privilege boundary would add an audit record
  and a permission check to something with no privilege in it. Useful over the VPN: the
  desktop at home, started from a train
- The server now reports its own hardware address and whether its card will accept a wake-up
  packet — the one fact about waking it that has to be known *before* it sleeps, because
  nothing on a sleeping machine can answer

Four found by installing on the first real laptop, an ASUS with a spinning disk
and Windows on it:

- **The installer died before it started.** Probing seven Windows partitions on a
  5400 rpm drive took 91 seconds against subiquity's 90-second timeout. The seed
  now clears the target disk in `early-commands`, which run before the probe —
  and refuses to guess when there is more than one candidate disk, because a slow
  probe is a worse experience while wiping the wrong disk is a different category
  of thing
- **`installer devices` refused a stick that would have worked.** A 4 GB floor
  picked by guessing, against a 3.43 GB requirement the writer had been computing
  correctly all along
- **The system disk was reported as mounted at `/var/tmp`**, because the mount
  table kept whichever entry came last and `PrivateTmp=yes` on hostd's unit gives
  it a private `/var/tmp` on the root device
- **Remote access opens its own port, and can be switched off.** `vpn.setup` had
  always named `vpn.disable` as its rollback and nothing implemented it — so
  there was a way to configure a service reachable from the internet and none to
  shut it. Neither did anything touch the firewall, which denies inbound by
  default, so the tunnel ran behind a closed port and looked from a phone exactly
  like a wrong key
- **One firewall helper rather than one per feature.** The decision about who may
  reach a port — the house only, or the whole internet — is worth having in a
  single reviewable place. Wireguard is the only thing in Homebase that asks for
  the second, and it is the one service whose purpose is being reachable from
  outside
- **The fan is reported, and so is who is driving it.** A loud laptop has two
  completely different problems behind it — a fan somebody pinned, or a heatsink
  full of dust — and from across a room they are the same sound. Reporting only:
  on the first real laptop, full load reached 89 °C with the fan still climbing,
  past the 84 °C its processor throttles at, and a manual setting on a machine
  like that is a way to cook one that is already struggling
- **The network configuration no longer names an interface.** subiquity writes the
  name it saw at install time, and that name is not a property of the card — the
  kernel derives it from the PCI slot. On the first real laptop a wireless card
  was not detected on one boot, the ethernet moved from slot 5 to slot 4 and was
  renamed, and a configuration naming the old name produced a server that booted
  perfectly, brought up no interface, obtained no address, and could not be
  reached at all. Matched on the kind of device now, and `homebasectl network`
  says so when a configuration asks for a card that is not there
- **Applications made of more than one container.** A manifest declares
  supporting containers — a database, a cache — and each joins a private network
  of the application's own, publishes no port on any interface, and cannot ask
  for one. Started before the application and stopped after it; removed with it,
  network and all
- **qBittorrent**, downloading into a folder the media server reads. Pointed at
  the same storage location as the shared folders, so a finished file is renamed
  into place rather than copied — a 40 GB film moved between disks is minutes and
  twice the space
- **A manifest can say what is left to do**, shown once after installing and on
  the application's own screen. For several applications that is the difference
  between working and usable: one that is running, reachable, and asking for a
  password nobody was given is indistinguishable from one that is broken
- **Two applications can no longer claim the same port.** Each manifest is valid
  alone, so the collision only exists across the catalogue — and without the
  check the second one fails at container creation while the symptom is that the
  *first* stopped working
- **Processor, memory and network on the dashboard**, beside the temperature and
  fan, as charts over a day, a week or a month. Counters are recorded as running
  totals and differenced on the way out, so a rate can be recomputed years later
  at whatever resolution somebody asks for — and a counter that has gone
  backwards is a reboot, not negative traffic
- **A record of how hot it has been**, sampled every five minutes into a plain
  CSV at `/var/log/homebase/thermal.csv`, with `homebasectl system history` and a
  chart on the dashboard. One reading tells you almost nothing: 58 °C is fine, or
  it is the start of an afternoon that ends in thermal shutdown, and the
  difference is entirely in what the last week looked like

Five more found by watching one person try to use it, which is a different
activity from testing it:

- **The share login refused the name it was created with.** The Unix account is
  namespaced, which is what stops a file-sharing password from also being a
  login, and is not something anybody should have to type. Samba's username map
  translates; the authentication box coming back for ever was the first thing
  the first user hit
- **There was no way to open an installed application.** The address existed and
  nothing offered it. The dashboard has an Open button now, `homebasectl apps
  open` exists, and install, start and restart print the address when they finish
- **`apps stop` and `apps restart` had never worked.** Both need the name
  confirmed and only `uninstall` sent it
- **Changing an application's disk never took effect.** A container's folders are
  fixed when it is built, so restarting one keeps the folders it was built with —
  and the message promised a restart would apply it. There was a test holding
  that claim in place
- **Pointing an application at a shared folder took the folder away from the file
  server**, because user-selected storage was handed to the application's own
  account. It belongs to the service group, and the container joins that group
- **`homebase.local` sometimes resolved to a Docker bridge address**, because
  avahi answered on every interface it could see. A machine asking for the server
  by name got back something unroutable, or its own bridge
- **`ls` on a shared folder said "Permission denied"** to the person who had just
  been told its path

- **Folders on the network, over SMB.** `homebasectl share add backup internal` publishes a
  folder that Windows, macOS and Linux all mount without installing anything. The file
  server is fetched when the first folder is shared rather than with Homebase, the port is
  opened to private address ranges only and closed again when the last share goes, and the
  accounts have no shell and no password on the machine — a password saved in a Windows
  dialog for years is not a credential for anything that administers the server

Three more found by installing an application and trying to use it:

- **Applications required a disk that was not in the machine.** A server with a
  1 TB drive could not run one without an external disk beside it. The server's
  own disk is a storage location now — chosen by name, never fallen back to,
  never a backup destination, and it cannot be removed or erased
- **Nothing could reach an installed application.** Containers bound to
  127.0.0.1 on a port Docker chose, on the reasoning that Homebase proxies them.
  There is no such proxy, and nothing reported the port. A manifest now declares
  whether an application is reachable from the network, and may only do so if it
  has its own accounts
- **`homebasectl apps logs` had never worked**, and there was no way to give an
  application a disk from a terminal at all
- **Wake-on-LAN was reported as unsupported on hardware that supports it**, for
  the same reason, and is now read over ethtool's netlink interface — a family
  hostd is permitted. `homebasectl network wake-on-lan <card>` switches it on and
  hostd reapplies it at every boot; `homebasectl wake` now names the firmware
  settings that stop it working, because on the machine this was found on both
  software halves were correct and it still would not start
- **The internet check had never worked on any installation.** hostd is forbidden
  `AF_INET` by its own unit, so it could not dial and returned false everywhere —
  including on a machine downloading Ubuntu updates while it said so. Moved to
  core, which is allowed a socket, and verified on the machine that found it

### Fixed

Three bugs of the same shape, each shipped in the commit before the test that caught it, and
each invisible to anything that did not use the feature the way a machine will. `hostd`
writes as `root`; the unprivileged half reads as `homebase`; everything that writes and every
test that runs is root.

- **Every scheduled backup would have reported success having copied nothing.**
  `/etc/homebase/backup-schedule.conf` was written `root:root 0640`, and `backup-run` — which
  runs as `homebase` — treated an unreadable schedule as "nothing configured" and exited `0`.
  The file is `root:homebase` now, and an unreadable one is a loud failure: the unit's
  `ConditionPathExists` already covers "never set up", so by the time the script runs, not
  being able to read it means the permissions are wrong
- **`next_run` was always empty.** `NextElapseUSecRealtime` is named after microseconds and
  current systemd prints `Thu 2026-08-13 03:00:00 CEST`; the parse failed and returned the
  empty string, which looks exactly like "no next run", so nothing looked wrong. Both forms
  are read now
- **Turning backups off discarded the disk**, while the comment above it said the choice was
  kept on purpose. "Off" is sent without a destination — a caller turning something off has
  no reason to repeat where it pointed — and taking the request at face value threw it away
- **The diagnostic file could not be downloaded.** Its directory was `root:root 0750`, so the
  file inside being readable made no difference: core could not list the directory, and the
  download answered `404` with the file sitting there

**`--json` printed the CLI's struct, not the server's answer.** Those differ whenever core
knows a field `homebasectl` does not, and the difference is silent: the field is simply
absent, and a script relying on it breaks with nothing to read. Found when `vpn status
--json` stopped reporting dynamic DNS state the server was returning perfectly well. The same
shape appeared twice more in one afternoon — `hostclient.NetworkInterface` had no
`wake_on_lan`, so the value vanished between hostd and core.

**A secret prompt with no terminal blocked for ever.** `ssh host homebasectl vpn dns …` has
no TTY, so the read never returned and the command hung until something killed it — worse
than an error, because it looks like it is working. It now says which environment variable to
use instead.

And a fourth of the same family, plus the one that makes it interesting. `/etc/netplan` was
read-only for `hostd` under `ProtectSystem=strict`, so joining a network could not write its
settings — the same shape as the `/etc/apt` bug above. But it was reported as
`wifi.did_not_join`, with a message telling somebody to check their password, **and the
wrong-password test passed on it**: the attempt was refused, nothing changed, and no file was
left behind. Every assertion held; none of them were about a password. There are now two
error codes — a settings file that cannot be written is a broken installation, a network that
will not let the machine in is almost always a wrong password — and the test asserts which
one it got.

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
