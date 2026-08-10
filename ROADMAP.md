# Roadmap

Homebase is built in two stages. **Stage 1** is a home server that a non-technical person
can install and run. **Stage 2** adds a local AI operator on top of it.

Stage 1 must be genuinely good on its own. If the AI never ships, what remains should still
be worth running — and the AI, when it arrives, is a client of the same APIs the dashboard
uses, never a privileged part of the system.

**Current position: Milestones 0–5 complete. Milestone 6 next.**

---

## Stage 1 — the server

### Milestone 0 — Contracts and project machinery ✅

No product code. Establish what everything else will be built against.

- [x] Monorepo skeleton, Apache-2.0, editor and line-ending conventions
- [x] Governance: contributing guide, security policy, PR and issue templates, CODEOWNERS
- [x] Hardened CI: hygiene, docs, contracts, workflow-security, secret scanning
- [x] Branch protection and repository settings, scripted and documented
- [x] Architecture documentation and ADRs 0001–0009
- [x] Threat model and privilege-boundary documentation
- [x] `api/openapi.yaml` — the v1 API contract, ahead of implementation
- [x] `schemas/app-manifest.schema.json` — with valid *and* invalid fixtures in CI

**Done when:** CI is green on a public repository, `main` is protected, and the contracts
are machine-validated on every pull request. ✅

Branch protection was verified by the test that matters: a direct push to `main` is
rejected, and a pull request cannot merge until all five required checks pass. A ruleset
that exists but does not bite is worse than none, because it gets trusted.

**One deliverable deliberately deferred:** publishing the documentation site. The
site builds under `mkdocs build --strict` on every pull request, but deployment to GitHub
Pages is off until Milestone 6 — see [`.github/workflows/docs.yml`](.github/workflows/docs.yml).
Until there is an installable release, the audience for the documentation is people
reading it in this repository, where it already works.

### Milestone 1 — Disposable VM lab ✅

The first code written, because everything after it needs somewhere honest to be tested.

- [x] `make vm-create / vm-start / vm-ssh / vm-reboot / vm-reset / vm-test / vm-logs /
      vm-status / vm-destroy`
- [x] Automated Ubuntu boot, serial console capture, log export
- [x] Fixture: verified cloud base image, with copy-on-write overlays per VM

Requires QEMU/KVM, OVMF and roughly 40 GB of free disk. No libvirt and no root — see
[ADR-0010](docs/decisions/0010-vm-lab-qemu-cloud-image.md).

**Done when:** one command creates a clean VM, installs a service, reboots, verifies health,
exports logs and destroys the machine. ✅ — `make vm-test`, about 50 seconds.

**Two items moved to Milestone 6.** The blank-disk and Windows-occupied-disk fixtures were
listed here, but they exist to test *installation*, and ADR-0010 moved ISO-based installation
to Milestone 6 where the installer actually exists. Putting them here would have meant
building fixtures for a component that has not been written.

Go and Node 20 were also expected here; they are only needed once there is code to build, so
they move to Milestone 2 with it.

### Milestone 2 — Core vertical slice ✅

The smallest complete product: dashboard → API → privileged operation → hardware.

- [x] `hostd` (root): system inventory, resource usage, reboot — over a Unix socket, as
      named typed operations with no generic execution path
      ([ADR-0011](docs/decisions/0011-hostd-protocol.md))
- [x] `core` (unprivileged): `/health`, `/setup`, `/auth/*`, `/system`, `/system/reboot`,
      `/jobs`, `/jobs/{id}` — with SQLite state, argon2id passwords and sessions
- [x] systemd units for both, and `ci/go` running build, vet, race tests, a
      dependency guard and govulncheck
- [x] Dashboard: first-run admin setup, login, system overview, reboot with confirmation
- [x] `.deb` packaging, with `go`, `packages` and `dashboard` as **required** checks

**Done when:** a user opens the dashboard, creates an administrator, sees accurate system
information, reboots the machine, and everything comes back by itself. ✅

**That journey passes in a real browser against a real machine** —
`make vm-test-dashboard`, about 64 seconds: first-run setup, a refused second
administrator, sign-in, live system information read through
`/proc` → `hostd` → socket → `core` → HTTP → browser, a restart refused until the machine
is named, a genuine reboot, and the dashboard noticing the server come back.

The packages are verified the same way — `make vm-test-packages`, about 89 seconds:
installed on a clean machine, given a real administrator account and a real file,
upgraded in place, reinstalled, rebooted and finally purged, with the account and the
file intact at every step.

The browser and package suites are not in CI: both need KVM, which GitHub's hosted
runners do not provide. They move there when the VM lab has an ephemeral,
network-isolated self-hosted runner. CI does build the packages, install them, reinstall
them and purge them, which covers everything that does not need a VM.

**How a reboot job finishes.** Nothing can observe a reboot completing — the connection dies
with the machine. Assuming success would make every job report a value nobody checked, so
`core` records the kernel's `boot_id` when the job starts and settles it on the next boot: a
different id is evidence the machine went down and came back. Anything else left running is
marked failed with a message saying so, because a job showing "running, 65 %" with no process
behind it is indistinguishable, to a user, from one still working.

`/events` was deferred to Milestone 3, where there was something to raise events about. It
shipped there, along with a live stream of them.

### Milestone 3 — Applications ✅

- [x] Manifest validation, image pull, container creation, port allocation
- [x] Health checks; start, stop, restart, uninstall
- [x] Data preservation across uninstall and reinstall; per-app logs; version pinning
- [x] Catalogue: `hello-homebase`, Jellyfin, File Browser
- [x] `/events` and a live event stream, carried over from Milestone 2
- [x] Application screens in the dashboard

**Done when:** a user installs an app, uses it, reboots, finds it and its data intact, and
uninstalls it without collateral damage. ✅

Verified as written: in a browser, against a real machine, across a real reboot. The two
assertions that matter most are that the container comes back on its own afterwards, and
that a file written into the application's data directory is still there once the
application has been removed. A test that only checked the container was gone would pass
on an implementation that wiped the disk.

The load-bearing decision is [ADR-0012](docs/decisions/0012-hostd-owns-the-catalogue.md):
`hostd` reads root-owned manifests and builds the container itself, so `core` sends an
application id and has no vocabulary for describing a container. The VM test checks that
against `hostd`'s audit log rather than against the source, because the claim is about
what crosses the socket.

That costs something real, and the ADR says so: **a user cannot install an application
Homebase does not ship.** Widening the catalogue is a package change, which is what makes
the set of installable applications reviewable in a diff.

### Milestone 4 — Storage ✅

Its own milestone because storage mistakes are the ones that destroy data.

- [x] Disk discovery, model/size/filesystem display, mounting supported filesystems
- [x] Managed storage locations; assigning them to applications
- [x] Disconnected-disk handling; read-only reporting
- [x] Explicit confirmation before any format; never auto-select between disks
- [x] Space alerts, as events, before applications start failing to write
- [x] `make vm-run` — Homebase on a throwaway VM you can actually use

**Done when:** a USB disk can be added as Jellyfin's media storage, removed and reconnected
without corrupting anything. ✅

Verified against a real disk, hot-plugged over QEMU's monitor and **pulled out without
warning while mounted** — which is the case that destroys data, as opposed to unmounting,
which is the tidy one. The exit condition is checked for an application as well as for a
disk: File Browser installed onto the disk, the disk yanked, the application refusing to
start and naming the disk to plug back in, then reconnected with everything intact.

The load-bearing decision is
[ADR-0013](docs/decisions/0013-storage-identity-and-mounting.md): a disk is identified by
its filesystem UUID, and mounting is done with systemd units rather than `/etc/fstab`.

Both halves were settled by evidence rather than argument. A disk unplugged from a running
VM as `/dev/sda` came back as `/dev/sdb` — same filesystem, same UUID — so anything storing
a device path was by then pointing at nothing. And a malformed `fstab` entry stops the boot
and drops the machine to an emergency shell, which on a laptop in a cupboard is a brick
caused by a disk somebody unplugged. Every unit Homebase writes carries `nofail`.

### Milestone 5 — Backup and restore ✅

Before the installer ships, because real users start storing data immediately.

- [x] Configuration backup (settings, manifests, app config, database export)
- [x] Data backup (user-selected directories and volumes)
- [x] Integrity verification, restore preview, failure reporting
- [ ] Scheduling — you have to press the button. Carried to Milestone 8 with the
      update timers, which need the same machinery

**Done when:** a clean machine restores another machine's backup and comes up with its apps,
configuration and data. ✅

Proven with two machines. The first is set up, used, backed up onto a USB disk and
destroyed; the disk survives; a second machine is created from scratch and the backup is
restored onto it. Restoring onto the machine that made the backup would prove almost nothing
— the files are already there and half of what a restore has to reconstruct was never lost.

The load-bearing decision is
[ADR-0014](docs/decisions/0014-backups-are-readable-without-homebase.md): **a backup is
plain files with a JSON manifest, readable without Homebase.** No deduplication, no
compression, no incremental format, and a full copy every time — all real costs, accepted
because the alternative fails in exactly the situation a backup exists for. The machine that
broke is the machine the backup software was on.

The VM test reads a backed-up file with `cat`, on a machine, without Homebase doing the
reading.

### Milestone 6 — Installer and first-use ← *next*

- [ ] `homebasectl installer create`, then a graphical controller (Tauri)
- [ ] Ubuntu autoinstall: hardware detection, disk enumeration, Windows detection
- [ ] Target confirmation, whole-disk install, firewall, laptop power behaviour
- [ ] First-use flow: administrator, server name, storage, updates, backups, first app,
      recovery code

**Done when:** starting from a Windows-occupied disk, the installer produces a working
server that reaches the dashboard and installs an application — with no Linux commands.

### Milestone 7 — Networking and private access

- [ ] Ethernet DHCP, local hostname, mDNS discovery, local HTTPS
- [ ] Network diagnostics and an honest offline state
- [ ] Wi-Fi setup; optional private remote access (Tailscale)

Public internet exposure stays out of scope.

**Done when:** the server is reachable by name from another device and stays manageable
while the internet is down.

### Milestone 8 — Updates and recovery

- [ ] Channels: development, alpha, beta, stable — promoting the *same* artifact, never
      rebuilding between channels
- [ ] Signed packages, SBOMs, build attestations, downgrade protection
- [ ] Pre-update snapshot, health check after update, automatic and manual rollback
- [ ] Recovery: diagnostic bundle, credential reset, service repair, reinstall preserving
      data, factory reset

**Done when:** interrupting an update at any stage leaves a bootable machine with intact
application data.

### Milestone 9 — Hardware alpha

- [ ] Intel and AMD laptops, with and without TPM, various Wi-Fi adapters
- [ ] UEFI and Secure Boot, lid-close behaviour, sleep prevention, thermal reporting
- [ ] Power-loss recovery, Wi-Fi reconnection, USB disk handling

**Done when:** three different laptops complete install → first boot → app install → reboot
→ backup → restore, with no manual Linux commands at any point.

### Stage 1 definition of done

A non-technical person can: create the installer USB, install without a terminal, reach the
dashboard, install and use an app, attach external storage, configure and verify a backup,
update and restart safely, recover from a simulated failure, export understandable
diagnostics — and never grant the dashboard root access to do any of it.

---

## Stage 2 — the local AI operator

Only after Stage 1 is reliable. The model runs locally, works without internet, and
administers the server **through the same API the dashboard uses**.

The architecture is fixed in [ADR-0006](docs/decisions/0006-privilege-split.md):

> the model proposes an intention → a policy engine evaluates it → a typed capability
> performs it → everything is logged and reversible.

Rolled out by increasing autonomy, never all at once:

| | Capability |
|---|---|
| **2A** | Read-only: explains status, searches docs, runs safe diagnostics. Changes nothing. |
| **2B** | Proposes actions; the user confirms each one. |
| **2C** | An approved allowlist runs automatically — restart a crashed app, retry a backup, renew DHCP. |
| **2D** | Bounded rules: *"if Jellyfin fails two health checks, restart it once, then tell me."* |

Also in Stage 2: hardware profiling to pick a model and quantization per machine, an offline
recovery interface for when the server loses its network, and an evaluation suite that every
candidate model must pass — including resisting instructions hidden in content the server
ingested.

**Not planned:** giving the model a shell, unbounded autonomy, or any path to a privileged
operation that does not pass through the policy engine.

---

## Deliberately out of scope

Windows or macOS hosts · ARM and Raspberry Pi · RAID and storage pools · multi-server
clusters · public internet hosting · arbitrary third-party app stores · mobile applications.

Some of these become reasonable after Stage 1. None of them are worth destabilising it for.
