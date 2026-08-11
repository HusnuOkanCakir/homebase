# Roadmap

Homebase is built in two stages. **Stage 1** is a home server that a non-technical person
can install and run. **Stage 2** adds a local AI operator on top of it.

Stage 1 must be genuinely good on its own. If the AI never ships, what remains should still
be worth running — and the AI, when it arrives, is a client of the same APIs the dashboard
uses, never a privileged part of the system.

**Current position: Milestones 0–7 complete. Milestone 8 next.** A USB stick turns a
Windows laptop into a working server, a new server says what to do next, and it is then
reachable by name over HTTPS from any device in the house. Making that stick still takes one
command on a Linux machine — the graphical tool for doing it on Windows and macOS is
Milestone 10, and it is what Stage 1 is still missing. The server also still needs a network
cable; Wi-Fi arrives with the hardware it has to be tested on, in Milestone 9.

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

**One deliverable deliberately deferred:** publishing the documentation site. The site built
under `mkdocs build --strict` on every pull request, but deployment to GitHub Pages stayed
off until Milestone 6 — see [`.github/workflows/docs.yml`](.github/workflows/docs.yml).
Until there was an installer, the audience for the documentation was people reading it in
this repository, where it already worked. **Switched on in Milestone 6**, which is when
somebody who has installed a server from a USB stick stops being a hypothetical reader.

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
- [x] Password recovery, brought forward from Milestones 6 and 8 — backups made the gap
      worse rather than better
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

Password recovery arrived here rather than in Milestone 6, because finishing backups made
the hole in it obvious: a backup restores the password hash faithfully, so somebody
restoring *because* they were locked out restores the account they cannot sign into.
Backups protect against a lost machine, not a lost password, and treating those as one
problem is what left a forgotten password meaning a lost server for five milestones.

[ADR-0015](docs/decisions/0015-password-recovery.md): **a recovery code the user holds, and
a console command behind it.** 125 bits on a piece of paper, stored as an argon2id hash,
single-use, and it travels with the backup — so the paper written at setup opens the machine
rebuilt from the disk. `sudo homebasectl recovery-code` is the last resort for when the
paper is gone too.

It also forced the auth endpoints to be rate limited. Sign-in, setup and recovery each
verify an argon2id hash, which reserves 64 MiB by design, and all three are reachable
without credentials — an unbounded one is memory exhaustion that needs no account.

### Milestone 6 — Installer and first-use ✅

- [x] `homebasectl installer create`, and `installer devices` to say what may be written to
- [x] Ubuntu autoinstall: whole-disk install onto a Windows-occupied disk
- [x] Firewall, laptop power behaviour, and a screen that says where to browse to
- [x] First-use flow: a getting-started list that says what is worth doing and why, and
      naming the server (the administrator and the recovery code arrived early, in
      Milestone 5)
- [→] A graphical controller — **split out to Milestone 10**

**Done when:** starting from a Windows-occupied disk, the installer produces a working
server that reaches the dashboard and installs an application — with no Linux commands. ✅

Proven by `make vm-test-installer`, which boots the media `homebasectl installer create`
writes — one USB drive, Ubuntu's image byte for byte with a Homebase partition appended
after it — onto a disk carrying a real GPT with Windows' partition types and an NTFS
signature. It answers Ubuntu's confirmation prompt by pressing keys, waits for the machine
to come back up as the system it installed, then creates an administrator and installs an
application through the API.

[ADR-0016](docs/decisions/0016-installation-media.md): **Canonical's ISO is never
repacked.** The autoinstall configuration and Homebase's own packages travel beside it on a
partition of their own, so the boot path stays the one Ubuntu publishes and tests, and the
image can still be checked against its publisher. The cost is visible and written down:
Ubuntu stops once to ask whether to continue, and that prompt does not say which disk it is
about to erase.

The test found two bugs that nothing else could have. The console account could not run
`sudo`, so the recovery path from Milestone 5 would have failed on every real installation.
And an installed server listened on `127.0.0.1` — every machine-side check passing while the
one thing the product is for did not work, invisible because two other places each set the
address for their own good reasons.

First use is a checklist rather than a wizard. A wizard has to be finished, so it either
blocks somebody who has not bought a USB disk yet or teaches them that skipping is normal.
The list says what is worth doing and why, reads its state from what the server actually
reports rather than remembering what was clicked, and stops mentioning each thing once it is
true. Naming the server is the one step that happens on the list itself — and also under
**This server**, permanently, because a machine that can only be renamed during its first
week is one nobody can rename.

**The graphical controller is deliberately not here.** It writes to a raw block device on
somebody's *own* computer — the one with all their work on it — and its whole purpose is
Windows and macOS, neither of which can be tested from the machines this project is
developed and tested on.

The refusals are the product: `refusal()` is what stops a person erasing the disk their
photographs are on, and on Linux it is `lsblk` semantics with a test behind it. The Windows
and macOS equivalents share no logic with it and would have to be written from
documentation. Shipping that unverified is the one thing a tool like this must not do, so it
became Milestone 10 with the platform testing it
requires rather than an unchecked box here.

Until then, making the stick needs one command on a Linux machine. That is a real gap and it
is why Stage 1 is not done.

### Milestone 7 — Networking and private access ✅

- [x] Ethernet DHCP, local hostname, mDNS discovery, local HTTPS on the ordinary ports
- [x] Network diagnostics and an honest offline state
- [→] Wi-Fi setup — **moved to Milestone 9**, where there is real wireless hardware
- [→] Optional private remote access — **deferred past Stage 1**

Public internet exposure stays out of scope.

**Done when:** the server is reachable by name from another device and stays manageable
while the internet is down. ✅

Proven by `make vm-test-network`, which is the first test in this repository that involves
two machines. That is the whole point of it. Every other suite reaches Homebase through a
port forwarded to the host's loopback — the one origin browsers treat as trustworthy and the
one address that always works, neither of which is what a user has. mDNS is multicast and
cannot cross QEMU's NAT at all, so a shared network segment is not a convenience here; it is
the only arrangement in which the claim can be tested. The second machine resolves
`homebase-net.local`, loads the dashboard over HTTPS, checks the certificate covers that
name, and confirms plain HTTP redirects to it. Then the server's route to the internet is
taken away, and it has to report that it is offline while still saying it is reachable.

[ADR-0017](docs/decisions/0017-local-https-and-discovery.md): **the server signs its own
certificate and the user trusts it once.** A home server has no public name, so Let's Encrypt
cannot issue for it without a domain, a DNS API token stored on the appliance, and
publication of every household's server name in Certificate Transparency logs. The cost of
signing its own is a browser warning, and that cost is not hidden: the machine prints its own
fingerprint on its own screen, so the warning is something to check rather than something to
dismiss. The certificate lasts ten years, because a yearly renewal is how "check the
fingerprint" degrades into "click through the warning".

`core` binds 80 and 443 with `CAP_NET_BIND_SERVICE` — one narrow capability rather than root
or a privileged proxy in front — so the address is `https://attic.local` with no port number,
which is the difference between something a person can be told over the phone and something
they have to be sent in a message.

**This milestone existed because of a bug that a green test suite could not see.** The
session cookie was already marked `Secure`, and browsers refuse a `Secure` cookie from a
non-secure origin — except on `localhost`, which every browser test in this repository uses.
So a real installation reached at `http://192.168.1.50:8080` discarded the session and
answered `401` immediately after a correct password, while every test passed. The product did
not work in the one respect it exists for, and nothing failed. Recorded in
[testing.md](docs/development/testing.md).

**Wi-Fi moved to Milestone 9** rather than being written here. QEMU has no wireless device,
and a Wi-Fi setup flow that has never touched a real adapter is a guess about the one
operation whose failure mode is *the server is now unreachable and the way to fix it was the
network*. Milestone 9 is where the laptops and their various adapters are. Until then a
Homebase server needs a network cable, and that is a real gap: a thin laptop without an
Ethernet port cannot be one yet.

**Private remote access is deferred past Stage 1.** It is not in the Stage 1 definition of
done, it cannot be tested without an account on somebody else's service, and depending on a
third-party coordination network needs a decision record of its own rather than a checkbox
here — the first principle in the README is that nobody can switch Homebase off or start
charging for it.

### Milestone 8 — Updates and recovery — in progress

- [x] [ADR-0018](docs/decisions/0018-updates-are-a-signed-apt-repository.md) — the update
      mechanism decided: a signed APT repository, with Homebase's own state snapshotted
      around the transaction
- [x] `update.status` and `GET /system/update` — what version this machine runs, whether its
      components agree, and whether dpkg left a transaction unfinished
- [x] Channels: development, alpha, beta, stable — `scripts/build-repo.py` publishes and
      promotes, and **refuses to promote a rebuilt artifact**: the index entry records the
      SHA-256 the source channel tested, so different bytes under the same version are
      caught rather than shipped
- [x] A signed archive, and a machine that updates from it — `update.configure` and
      `update.check`, proven by `make vm-test-update` (146s)
- [ ] Signed release artifacts in CI, SBOMs, build attestations, downgrade protection
- [ ] Pre-update snapshot, health check after update, automatic and manual rollback
- [ ] Backup scheduling and update timers — the same machinery, deferred from Milestone 5
- [ ] Recovery: diagnostic bundle, service repair, reinstall preserving data, factory reset
      (credential reset shipped in Milestone 5)

**Done when:** interrupting an update at any stage leaves a bootable machine with intact
application data.

**`hostd` does not run apt, and that was not planned.** Its unit sets
`RestrictAddressFamilies=AF_UNIX AF_NETLINK` — the root service that manages the machine
cannot open a network socket at all. Giving it `AF_INET` so it could run `apt-get update`
would have traded a real structural property for a convenience, so the work that reaches the
network happens in fixed units the package installs and `hostd` starts through systemd. The
action is not a parameter and they are not template units: nothing from a request becomes
part of a unit name or a command line.

**Reporting comes before changing.** The interruption matrix asserts against `update.status`,
and a broken update is diagnosed with it — so it is built first, and it reports the set of
four packages rather than one version string. They depend on each other with `(= version)`,
so apt cannot produce a mixed set on purpose; only an interruption can. A machine in that
state usually still works, which is exactly why nothing else would notice.

**A/B image updates were considered and rejected**, with the reasoning recorded rather than
implied. They are what buys real atomicity, and they would require owning the partition
layout and the bootloader that [ADR-0016](docs/decisions/0016-installation-media.md)
deliberately left to Ubuntu. More importantly they would not deliver what they appear to:
Homebase's state is a SQLite database, container images and uid allocations that are not on
the image, so a partition switch that reverts the code while leaving a migrated database is a
downgrade into an inconsistent state rather than a rollback. The state snapshot has to be
built either way.

### Milestone 9 — Hardware alpha

- [ ] Intel and AMD laptops, with and without TPM, various Wi-Fi adapters
- [ ] UEFI and Secure Boot, lid-close behaviour, sleep prevention, thermal reporting
- [ ] Power-loss recovery, Wi-Fi reconnection, USB disk handling
- [ ] **Wi-Fi setup from the dashboard** — moved here from Milestone 7, because the failure
      mode of getting it wrong is a server that can no longer be reached to fix it, and the
      VM lab has no wireless hardware to get it wrong on

**Done when:** three different laptops complete install → first boot → app install → reboot
→ backup → restore, with no manual Linux commands at any point.

### Milestone 10 — The graphical stick-maker

The last thing standing between Homebase and its actual audience: a small application that
writes the installation media, run on the computer somebody already owns.

- [ ] A way to test on Windows and macOS — a VM lab for both, the way Milestone 1 built one
      for Linux. This comes first, because everything else here is untestable without it
- [ ] Device enumeration and the refusals, per platform: `\\.\PhysicalDrive` on Windows,
      `diskutil` and `/dev/rdisk` on macOS
- [ ] A graphical wrapper around the same media logic `homebasectl installer create` uses —
      one implementation, not three
- [ ] Signed and notarised builds, since an unsigned tool that asks for administrator rights
      to erase a disk is indistinguishable from malware, and correctly so

**Done when:** somebody who has never opened a terminal makes a working Homebase stick on
their own Windows or macOS computer, and the tool refuses to write to the disk that computer
runs from — proven on both, not reasoned about.

Numbered last because it cannot start until there is somewhere to run it. Ordered by
necessity rather than by importance: it is the piece Stage 1 cannot be called done without.

### Stage 1 definition of done

A non-technical person can: create the installer USB, install without a terminal, reach the
dashboard, install and use an app, attach external storage, configure and verify a backup,
update and restart safely, recover from a simulated failure, export understandable
diagnostics — and never grant the dashboard root access to do any of it.

**The first of those is the one still outstanding.** Everything after "install without a
terminal" works today; making the stick still takes one command on a Linux machine. That is
Milestone 10, and until it lands this list is a target rather than a description.

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
