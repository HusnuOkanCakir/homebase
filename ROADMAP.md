# Roadmap

Homebase is built in two stages. **Stage 1** is a home server that a non-technical person
can install and run. **Stage 2** adds a local AI operator on top of it.

Stage 1 must be genuinely good on its own. If the AI never ships, what remains should still
be worth running — and the AI, when it arrives, is a client of the same APIs the dashboard
uses, never a privileged part of the system.

**Current position: Milestone 0 complete. Milestone 1 next.**

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

### Milestone 1 — Disposable VM lab ← *next*

The first code written, because everything after it needs somewhere honest to be tested.

- [ ] `make vm-create / vm-start / vm-reset / vm-test / vm-logs / vm-destroy`
- [ ] Automated Ubuntu Server boot, serial console capture, log export
- [ ] Fixtures: clean base image, blank disk, Windows-occupied disk

Requires QEMU/KVM, OVMF and roughly 40 GB of free disk for the cached base image and a
qcow2 overlay per test. No libvirt and no root — see
[ADR-0010](docs/decisions/0010-vm-lab-qemu-cloud-image.md). This is also where Go and
Node 20 are installed.

**Done when:** one command creates a clean VM, installs a service, reboots, verifies health,
exports logs and destroys the machine.

### Milestone 2 — Core vertical slice

The smallest complete product: dashboard → API → privileged operation → hardware.

- [ ] `core` (unprivileged): `/health`, `/system`, `/events`, `/jobs`, `/jobs/{id}`
- [ ] `hostd` (root): system inventory, resource usage, network info, reboot
- [ ] Dashboard: first-run admin setup, login, system overview, reboot with confirmation
- [ ] systemd units, `.deb` packaging, `ci/go` and `ci/dashboard` as required checks

**Done when:** a user opens the dashboard, creates an administrator, sees accurate system
information, reboots the machine, and everything comes back by itself.

### Milestone 3 — Applications

- [ ] Manifest validation, image pull, container creation, port allocation
- [ ] Health checks; start, stop, restart, uninstall
- [ ] Data preservation across uninstall and reinstall; per-app logs; version pinning
- [ ] Catalogue: `hello-homebase`, Jellyfin, File Browser

**Done when:** a user installs an app, uses it, reboots, finds it and its data intact, and
uninstalls it without collateral damage.

### Milestone 4 — Storage

Its own milestone because storage mistakes are the ones that destroy data.

- [ ] Disk discovery, model/size/filesystem display, mounting supported filesystems
- [ ] Managed storage locations; assigning them to applications
- [ ] Disconnected-disk handling, space alerts, read-only fallback
- [ ] Explicit confirmation before any format; never auto-select between disks

**Done when:** a USB disk can be added as Jellyfin's media storage, removed and reconnected
without corrupting anything.

### Milestone 5 — Backup and restore

Before the installer ships, because real users start storing data immediately.

- [ ] Configuration backup (settings, manifests, app config, database export)
- [ ] Data backup (user-selected directories and volumes)
- [ ] Scheduling, integrity verification, restore preview, failure reporting

**Done when:** a clean machine restores another machine's backup and comes up with its apps,
configuration and data.

### Milestone 6 — Installer and first-use

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
