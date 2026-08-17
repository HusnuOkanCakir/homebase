# Roadmap

Homebase is built in two stages. **Stage 1** is a home server that installs itself onto an
old laptop and is then run from a terminal. **Stage 2** adds a local AI operator on top of
it, and is not yet committed to.

**Who this is for:** somebody comfortable with Linux who wants the tedious and dangerous
parts done properly — installing, backing up, updating, recovering — without doing them by
hand on every machine, and without a stack of shell scripts nobody has tested. Not somebody
who has never opened a terminal; that was the earlier premise and it was wrong about its own
audience.

Stage 1 must be genuinely good on its own. If the AI never ships, what remains should still
be worth running — and the AI, when it arrives, is a client of the same APIs the dashboard
uses, never a privileged part of the system.

**Current position: Milestones 0–8 and 10–12 complete. Milestone 9 in progress; 13–15 planned.** A USB stick turns a Windows
laptop into a working server, a new server says what to do next, and it is then reachable by
name over HTTPS from any device in the house. It backs itself up every night, looks for
updates on its own, applies one and puts the previous version back if it does not work, and
survives having its power cut mid-`dpkg` — and when something does go wrong it can produce a
file safe to send to somebody, repair itself, or start again without losing anybody's
photographs.

It also joins a wireless network now, refusing a wrong password without changing anything;
it boots with Secure Boot enforcing, as every laptop ships; and it says how hot it is
getting, because an old machine in a cupboard that is cooking looks from the outside exactly
like one that is broken.

**It now runs on a real laptop.** An ASUS with a spinning disk and Windows on it, installed
from the USB stick, reachable by name over HTTPS, reporting its own temperature. That
afternoon found four bugs no VM could have — the worst being an internet check that had
never worked on any installation, because the process it lived in is forbidden from opening
a socket. They are recorded in Milestone 9 below, because how they were missed matters more
than what they were.

What is still missing is *more* hardware: one laptop is a data point, not a milestone.
Driver quirks, other Wi-Fi adapters, USB disks that misbehave.

Files are on the network now, over SMB: a folder on the server is a drive on a laptop,
which is the half a browser cannot be.

**The gap that is left is the catalogue.** Three applications, one of which exists
to prove installation works. Everything needed to add more is built and proven —
Milestones 13 to 15 are the content, and the two places real applications do not
fit the model: several of them need more than one container, and Home Assistant
needs privileges no other manifest is allowed.

Nothing has been released. The release machinery exists and has never been run for real, and
there is no host for the update archive yet.

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
- [→] Scheduling — carried to Milestone 8 with the update timers, which needed the same
      machinery. **Shipped there**: nightly or weekly, on a systemd timer that catches up a
      run the machine was switched off for

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
- [→] A graphical controller — split out to Milestone 10, and **since dropped**: it
      existed for an audience this project turned out not to have

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
- [→] Wi-Fi setup — **moved to Milestone 9**, where wireless hardware could be tested
      against. It shipped there, against simulated radios on the real `mac80211` stack
- [→] Optional private remote access — **now Milestone 11**, as self-hosted Wireguard

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
network*.

The premise turned out to be half wrong, which is worth recording. QEMU has no wireless
device, but the *kernel* does: `mac80211_hwsim` puts simulated radios on the real `mac80211`
stack, so `wpa_supplicant`, `netplan` and `iw` cannot tell the difference. That is enough to
test every line of Homebase's own code and every claim it makes to the user. It is not
enough to test drivers, firmware or roaming, which still need laptops — so the deferral was
right about *what* needed hardware and wrong about *all of it* needing hardware. Shipped in
Milestone 9.

**Private remote access was deferred past Stage 1 here, and that was reversed after
Milestone 9.** The reasoning was that it "cannot be tested without an account on somebody
else's service, and depending on a third-party coordination network needs a decision record
of its own" — and every word of that is about Tailscale-shaped designs. It is not true of
plain Wireguard, which is self-hosted, needs no account, and can be tested with two machines
in the VM lab. The deferral was sound reasoning about a design that was not chosen. It is
now **Milestone 11**.

### Milestone 8 — Updates and recovery ✅

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
- [x] `update.apply` — download and verify everything first, snapshot what apt does not own,
      apply, health check, and put the previous version back if it fails. Downgrades are
      refused unless somebody is rolling back
- [x] **Rollback, proven by forcing it.** A release that installs cleanly and then does not
      work — core's binary replaced inside a real package — is detected by the health check
      and the previous version is put back, with the database snapshot restored
- [x] **The interruption matrix.** Power is cut with QMP `system_reset`, during downloading
      and again *with dpkg running*. The machine boots both times with its data intact
- [x] An Updates screen and the API behind it, so none of this needs a terminal — including
      that all four routes refuse an unauthenticated caller, which is worth exercising on a
      surface that can install root-level code
- [x] **SBOMs**, in CycloneDX, read from the linked binary rather than from `go.mod` — and
      `homebase-hostd`'s must be empty, so the build fails if the privileged service ever
      acquires third-party code
- [x] **A release workflow** — `release.yml` turns a tag into a signed archive and
      `promote.yml` moves a tested artifact between channels behind a manual approval. The
      build job holds no secrets, which is what makes its provenance attestations worth
      having; the publish job's *environment is the channel*, so the gate belongs to
      `stable` rather than to a workflow file somebody could edit in a pull request
- [x] **Backup scheduling** — a systemd timer, not a ticker inside core. `Persistent=true`
      is the setting that makes it work on the machine Homebase runs on: a laptop in a
      cupboard is asleep at three in the morning more often than not, and without it the
      run is skipped silently, every night, until somebody needs it
- [x] **The update check runs on its own**, once a day, catching up if the machine was off.
      Not hourly: nothing it does installs anything, and checking more often would only tell
      the archive how many servers exist and when each is switched on
- [x] The backup schedule in the dashboard, with the last run's outcome beside it
- [x] **Recovery** — a diagnostic bundle, repair, and factory reset (credential reset
      shipped in Milestone 5). Reinstalling to fix a broken install is what repair does;
      there is no separate button for it, because "reinstall the packages" is not a sentence
      this product's user should have to read

**Done when:** interrupting an update at any stage leaves a bootable machine with intact
application data. ✅

Proven by cutting power with QMP `system_reset` rather than by killing a process, because a
killed process flushes its writes and a power cut does not. Triggered on a running `dpkg`
rather than on the stage that precedes it: the stage is recorded a moment before apt is
invoked, and a reset fired on the stage alone landed at the edge of the dangerous window and
left the machine untouched — a real result, but not the one being claimed.

With dpkg genuinely mid-write, the machine boots, the file in `/srv/homebase` is intact, and
`update.status` reports `interrupted`. That last part is the loop closing: the field was
built in this milestone's first commit for exactly this state.

**And the remedy is now a button.** `dpkg --configure -a` is what the error message has
named since that first commit, and naming a terminal command to somebody who bought an
appliance is a remedy they do not have. `system.repair` runs it, and the interruption test
is where it is proven — on the only genuinely half-upgraded machine in the suite, rather
than on one that was never broken. It is run twice, because somebody who does not know
what is wrong will press it twice, and the second run has to be a quiet no-op.

**Three bugs were found by writing tests that use the thing rather than describe it**, all
of them the same shape: `hostd` writes as `root`, the unprivileged half reads as `homebase`,
and nothing notices because everything that writes and every test that runs is root.

- The backup schedule was written `root:root 0640`, and `backup-run` treated an unreadable
  schedule as "nothing configured" and exited 0. Every scheduled backup would have succeeded,
  nightly, having copied nothing, with the dashboard reporting that the last one worked.
- `next_run` was always empty: `NextElapseUSecRealtime` is named after microseconds and
  current systemd prints `Thu 2026-08-13 03:00:00 CEST`. The parse failed and returned the
  empty string, which looks exactly like "no next run".
- The diagnostics directory was `root:root 0750`, so the file inside being readable made no
  difference — core could not list the directory, and the download answered 404 with the
  file sitting there.

Each was caught by a test that starts the service the way systemd will, or downloads the
file the way a browser will. None would have been caught by a test that asserted against
what was written.

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

**The release workflow is built and has never been run for real**, which is stated here
rather than left to be discovered. There is no host for the archive yet — a release produces
a signed, verified repository as a build artifact, and serving it is a decision Milestone 9
has to make. What does run is everything that needs neither a key nor a host, on every pull
request: `tests/unit/test_repo.py` builds a real signed archive from four empty `.deb` files
and a throwaway key, then breaks it five ways and checks each break is refused. The only
untested part of a real release is the key material and the environment protection rules,
neither of which lives in the repository.

**The diagnostic bundle's claim about itself is checked against itself.** It tells the user
it contains no password, no recovery code and none of their files — and the VM test plants
recognisable strings in each of those places and then greps the finished bundle for them.
A support tool that quietly contains the password database is not an unhelpful support tool;
it is a disclosure with a download button.

### Milestone 9 — Hardware alpha — in progress

- [x] **Wi-Fi setup from the dashboard** — moved here from Milestone 7, and now proven
      against simulated radios rather than deferred again
- [x] **Secure Boot** — a Homebase machine boots and runs with the firmware enforcing
      signatures and Microsoft's keys enrolled, which is how every laptop ships
- [x] **Thermal reporting** — the machine says how hot it is, and says nothing rather than
      zero when it cannot tell
- [x] **The first real laptop**, which found four bugs in an afternoon — see below
- [ ] Intel and AMD laptops, with and without TPM, various Wi-Fi adapters
- [ ] Lid-close behaviour and sleep prevention, on hardware with a lid
- [ ] Power-loss recovery on real disks, Wi-Fi reconnection, USB disk handling

**Done when:** three different laptops complete install → first boot → app install → reboot
→ backup → restore, with no manual Linux commands at any point.

#### What one real laptop found in an afternoon

An ASUS with a 5400 rpm 1 TB drive and Windows on it. Everything below was found
by installing Homebase on it and typing four commands.

**The installer died before it started.** Probing seven Windows partitions on a
slow spinning disk took 91 seconds against subiquity's 90-second timeout, and the
install ended in a Python traceback and a root shell. The seed now clears the
target disk in `early-commands`, which run before the probe — it destroys nothing
the install was not about to destroy, and it refuses to guess when there is more
than one candidate disk. No VM could have produced this: the lab's disks are
qcow2 images on an SSD with no rotational latency and at most two partitions.

**`installer devices` refused a USB stick that would have worked.** The floor was
4 GB, chosen by guessing; a nominal 4 GB stick holds 3.88 GB and the image needs
3.43 GB. The writer had computed the real figure all along. The floor is now a
sanity check rather than a pretend requirement.

**The system disk was reported as mounted at `/var/tmp`.** The mount table kept
whichever entry came last, and `PrivateTmp=yes` on hostd's own unit gives it a
private `/var/tmp` backed by the root device. `/` now wins outright.

**The internet check had never worked. On any installation. Ever.**
`homebase-hostd.service` sets `RestrictAddressFamilies=AF_UNIX AF_NETLINK` — a
restriction added deliberately and defended at length — and the check lived
inside hostd, so it could not open a socket and returned false on every machine
that ever ran it, including one downloading Ubuntu updates while it said so.

Nothing caught it for four milestones, and that is the part worth recording. The
unit tests injected a fake dialler: they exercised the logic perfectly and never
asked whether the process could execute it. The VM suite asserted `online is
False`, and only after taking the interface down — passing every time for a
reason that had nothing to do with the interface. **A check that can only answer
"no" passes every test that only asks when the answer should be no.**

It is in core now, which is allowed a socket, and verified on the machine that
found it. A second lesson came free: the first diagnosis was that the network
blocked TCP/53. It does not — that server reaches `1.1.1.1:53` perfectly well. A
cause inferred from a symptom, wrong, and the real cause was a line written by
the same hand months earlier.

**Wake-on-LAN was reported as unsupported on hardware that supports it.** Same
root cause: reading the setting needs `SIOCETHTOOL` on an `AF_INET` socket, so
the answer was "cannot tell" on every machine that has ever run Homebase.

It is read over ethtool's netlink interface now — a family hostd *is* permitted,
which is why the fix was to speak the right protocol rather than to widen the
sandbox. The laptop reports its wired card as wakeable and its wireless card as
not, both correct, and `homebasectl network wake-on-lan enp5s0` switches it on
and keeps it on: hostd reapplies it at every boot, because the setting lives in
the card and the driver resets it.

The rest of it is a lesson about how little the software half is worth. Both
halves Homebase controls were correct and the machine still would not start,
because the firmware cuts power to the network card when the machine is off —
ASUS calls it "Power Off Energy Saving" and its own help text admits it stops
wake-up working. Nothing on a running machine can read that setting. So the
product cannot fix it and should not pretend to; what it can do is name it, and
`homebasectl wake` now does, along with the ERP and Deep Sleep settings that do
the same thing under other names.

#### Using it found what testing it had not

Installing an application and trying to watch something found three things that
every test had passed over.

**Applications required a disk that was not in the machine.** Storage locations
were filesystems added by UUID, so the only way to get one was to plug something
in — a server with a 1 TB drive could not run a media application without an
external disk sitting beside it. The server's own disk is now a location like
any other, chosen by name. What the original design was protecting against is
kept: an application must never *fall back* to the system disk when the disk it
was given is missing. That is about silence, and choosing it deliberately is not
silent. It cannot be a backup destination, and it cannot be removed or erased.

**Nothing could reach an installed application.** Containers were bound to
127.0.0.1 on a port Docker chose, on the reasoning that applications are reached
through Homebase, which applies authentication. Homebase has no such proxy.
Nothing reported the port either. So an application would install, start, pass
its health check, and sit at an address no part of the product would tell anybody
— a media server nobody could watch anything on.

The VM test asked Docker for the port and connected from inside the machine. It
proved the container serves HTTP and nothing whatever about anybody reaching it.

A manifest now says whether an application is reachable from the network, and may
only say so if it authenticates its own users — a per-application decision in a
root-owned file, reviewed in a diff, refused outright for the ports the server
itself uses. Jellyfin and File Browser have their own accounts and are published;
the test application does not and is not.

**There was no way to give an application a disk from a terminal**, and
`homebasectl apps logs` had never worked — it decoded the line *count* as the log
text and failed on every application with a Go type error. Both existed as API
endpoints. Neither had ever been run against a server.

#### A name is not a property of a card

The worst outage this project has had, and it took an evening with a keyboard to
diagnose.

The server was woken with a magic packet, booted, and could not be reached. No
address, no ARP entry, no mDNS — a sweep of all 254 addresses on the network found
six devices and none of them was the server. From outside it was indistinguishable
from a machine that had not started.

It had started perfectly. Its **wireless card was not detected on that one boot**,
which moved the ethernet controller from PCI slot 5 to slot 4, and Ubuntu's
predictable naming renamed it `enp4s0`. The network configuration named `enp5s0`,
so nothing was brought up. The card came back with its old name on the next boot;
nothing was broken and nothing needed replacing.

**Homebase wrote that configuration.** The installer left subiquity's
`50-cloud-init.yaml`, which names whatever interface existed during
installation — and that name comes from the slot order, so it changes when the
hardware enumeration does. Adding a card, removing one, or a card failing to be
detected once is enough.

Fixed by matching on the kind of device rather than the name, and
`homebasectl network` now says plainly when a configuration asks for a card the
machine does not have — which is the message that would have turned an evening
into a minute.

It is the second time this week that a name derived from device enumeration has
been wrong in a way that looked like broken hardware. The GPU render node was the
first: every guide says `renderD128` and on this machine that is the NVIDIA card,
because the Intel one enumerated second. **Anything the kernel numbers by
discovery order is a fact about one boot, not about the machine.**

#### Secure Boot, which is the one that would have failed silently

Every laptop bought in the last decade has Secure Boot on and Microsoft's keys enrolled from
the factory. If a Homebase installation will not boot under that, the milestone fails at step
one on every machine it is for — and it fails *before there is anything to read a log from*,
which is the worst way for anything to fail.

`make vm-test-secureboot` boots with OVMF's Secure Boot build and the `.ms` variable store,
which has Microsoft's PK, KEK and db already enrolled, with SMM on so the store is protected
the way it is on real hardware. Ubuntu's `shimx64.efi` is signed by Microsoft's UEFI CA, so
a correct installation boots and an incorrect one does not.

The first assertion is that the firmware is *actually enforcing* — `mokutil`, the kernel's
`SecureBoot` EFI variable, and `SetupMode` all checked, because a machine booted with the
plain variable store has no keys, sits in setup mode, boots anything, and would pass every
other assertion while proving nothing.

It found nothing wrong with Homebase, which is the answer worth having: the machine boots,
installs, runs, and boots again with signatures enforced. It did find a bug in the test —
`/boot/efi` is mounted `0700 root`, and `2>/dev/null` hid the permission error, so the check
failed on a machine that was perfectly fine.

**SMM is not optional**, and the comment in `vmctl.py` says why. The variable store holding
the keys has to be writable by the firmware and by nothing else; without SMM an operating
system can write to it directly, so the firmware would enforce signatures against a key list
anything on the machine could replace.

#### The machine can say it is too hot

An old laptop, lid shut, in a cupboard, with eight years of dust in its fan. It throttles,
gets slower, and eventually shuts itself off — and from outside that is indistinguishable
from "Homebase is broken".

Nothing acts on the temperature: Homebase does not control fans and should not pretend to.
What it can do is say so, which is the difference between somebody opening a cupboard door
and somebody buying a new laptop.

Three decisions, and the middle one is the one that usually goes wrong:

- **The hottest zone, not an average.** One component at 95 °C with three at 40 °C is a
  problem, and the average hides it
- **The thresholds are deliberately high** — warm at 80 °C, hot at 90. Processors are
  designed to run at 80 and throttle in the nineties, so warning at 60 would teach people to
  ignore the warning, which is the failure mode of every temperature indicator ever shipped.
  A machine at an ordinary temperature says nothing at all
- **No sensors means no reading, never zero.** Every VM is in that state and so is some real
  hardware; a machine claiming 0 °C would look wonderfully cool. The same rule as the
  battery, and the VM test is the right place to check it because a VM genuinely has none

#### Wi-Fi, and what a simulated radio does and does not prove

Wi-Fi was held out of Milestone 7 because **the failure mode is a server that can no longer
be reached to fix it** — every other operation in Homebase fails by not working — and there
was no wireless hardware to get it wrong on. It is here because there turned out to be, near
enough.

`mac80211_hwsim` is the kernel's simulated wireless driver. It creates real `wlanN`
interfaces on the real `mac80211` stack, so `wpa_supplicant`, `netplan` and `iw` behave
exactly as they do on a card. `make vm-test-wifi` builds two radios, runs `hostapd` with
WPA2 on one as the house router and `dnsmasq` behind it, and has the server join from the
other. The association and the four-way handshake are real; only the radio is not.

**What that proves** is every line of Homebase's own code, and the claims that matter:

- a wrong password changes nothing, and says so — the previous configuration is put back and
  re-applied *before* the error returns, so the message can honestly say the server is
  connected exactly as it was
- the Ethernet configuration is never touched, checked by comparing the wired address before
  and after a failed attempt
- the cable still wins afterwards, checked against the routing table rather than against the
  file that was written
- the passphrase is in none of: the status, a scan, the audit log, or a diagnostic file
- an SSID with a newline or a quote in it cannot change the shape of the settings file

**What it does not prove** is the half this milestone still owes, and none of it can be
faked: driver quirks and firmware that will not load, cards that vanish on resume, regulatory
domains that make a channel disappear, roaming between two access points with the same name,
and the specific misery of a Realtek adapter. Those need laptops.

Three decisions carry the safety, and each is in the code rather than in a warning:

**netplan, not NetworkManager.** netplan is what an Ubuntu Server install already uses, and
running two things that both believe they own the network is a well-known way to lose it.

**The settings file is JSON.** A netplan file is YAML and JSON is valid YAML, so it is
produced by `encoding/json` rather than by formatting — which means a network name containing
a quote or a newline is escaped by construction, in a file that decides what the machine
connects to. Hand-formatting somebody else's string into that file is the kind of quoting
that is right until the day it is not.

**Wireless is written to one file, and the cable's is never read.** That is what makes a
failed attempt unable to strand a machine that is plugged in.

The test found two bugs, and the second is the interesting one. `/etc/netplan` was read-only
for `hostd` under `ProtectSystem=strict`, so the write failed — the same shape as the
`/etc/apt` bug in Milestone 8. But the failure was reported as `wifi.did_not_join`, with a
message saying to check the password, **and the wrong-password test passed on it**: it was
refused, nothing changed, no file was left behind. Every assertion held and none of them
were about a password. There are now two error codes, and the test asserts the failure came
from joining rather than from writing.

### Milestone 10 — The whole product from a terminal — in progress

`homebasectl` today has three commands: `installer`, `recovery-code`, `list-accounts`.
Everything else — applications, storage, backups, updates, wireless, repair — is reachable
only through the HTTP API or the dashboard. For somebody who lives in a terminal that is the
wrong way round.

- [x] A command surface over the API: `system`, `apps`, `storage`, `backup`, `update`,
      `network`, `repair`, `diagnostics`
- [x] **Authentication by being on the machine.** Running as root it reads the database and
      mints a short-lived session, which is what root can do anyway; otherwise it wants a
      token. No setup, nothing to rotate, and `sudo homebasectl apps` simply works
- [x] `--json` on everything, printing core's answer unmodified rather than something
      reshaped here — a CLI whose JSON is its own invention drifts from the API it claims
      to expose
- [x] Exit codes that distinguish failure (1) from misuse (2) from the server not answering
      at all (3)
- [x] **The destructive ones**: restoring a backup, formatting, attaching and detaching a
      disk, factory reset — with a confirmation designed for a shell rather than copied from
      the browser
- [ ] Changing the update channel, and removing an application's data

**Done when:** every operation the dashboard can perform can be performed from a terminal,
over SSH, on the machine itself — and a shell script can drive an install end to end.

It comes first among what is left because it is the surface everything after it lands on.
Building the VPN or file sharing before it would mean building each twice.

**It is an API client, not a second way in.** As root it could open `hostd`'s socket
directly, and does not: a second path to a privileged operation is a second place for the
permission checks, the job records and the events to be wrong. The two commands that *do*
read the database directly — `recovery-code` and `list-accounts` — exist for a server nobody
can sign in to, which is the whole point of them (ADR-0015).

**Passwords are never arguments.** The Wi-Fi password comes from the environment or a
prompt with echo off. An argument is in the shell history and in `ps` output for every user
on the machine for as long as the command runs, which for joining a network is up to a
minute.

**The confirmation the dashboard uses does not survive the move to a terminal.** In a form
field, "type the backup id to confirm" works: the id is on the screen and the field is empty,
so typing it means having read it. At a shell it means almost nothing — the id is already in
the command that listed it, one press of the up arrow re-runs whatever was done last, and a
`--yes` flag becomes muscle memory within a week. Copying the browser's confirmation across
would have looked like safety without being any.

Three things replace it. **The preview**, which is the part that actually stops a mistake: the
server is asked what would happen and the answer is printed, specifically — this many files
replaced, from this machine, or this is what is on the disk now. A wrong choice usually looks
wrong once described. **A terminal is required**, because a script can run a command by
accident in a way nobody can click a button by accident, so the unattended path has to be
asked for. And **the confirmation is a value rather than a word** — the backup's id, the
disk's device, the server's name, which cannot be replayed against a different one. There is
no `--yes`.

The VM test found two things on its first two runs, both about the shape of a CLI rather
than about Homebase. `homebasectl apps --json` was read as a subcommand called `--json` and
refused — the first thing anybody types. And shared flags did not work *before* the
subcommand, although the help had always listed them under "Options" as though they were
global: `--address` first exited 2 as an unknown command. A documented flag that is refused
is worse than one that does not exist.

### Milestone 11 — Reachable from anywhere — in progress

The half a home server is actually for. Everything so far assumes the same house.

- [x] [ADR-0019](docs/decisions/0019-remote-access-is-self-hosted-wireguard.md) — remote
      access is **self-hosted Wireguard**, not an overlay network somebody else operates
- [x] `homebasectl vpn setup`, `add-device`, `remove-device`, `status` — with a QR code for a
      phone, because typing a Wireguard key by hand is nobody's evening
- [x] A clear account of what has to be done on the router, said at the moment of setting up
      rather than left to be discovered when the first device fails to connect
- [x] Dynamic DNS, so a home connection whose address changes stays reachable — a fixed
      table of providers, never a URL from the caller, and the token declared secret so it is
      redacted from the audit log
- [x] Wake-on-LAN: `homebasectl wake`, and the server reporting whether it can be woken
- [ ] A screen for it on the dashboard

**Done when:** a device outside the house reaches the server by name, over Wireguard, with
no third-party account involved. **The tunnel half is done** — `make vm-test-vpn` (138s), two
machines, one of which has no Homebase on it and knows nothing about the server except the
configuration it was handed. It completes a handshake, reaches the dashboard over the tunnel,
and stops reaching it the moment its key is taken away.

What is left is a screen on the dashboard. The tunnel, the name that follows a changing home
address, and waking a sleeping machine all work from a terminal.

#### A name that stopped updating says so

The reporting is the point rather than the updating. Every dynamic DNS client updates a
name; the failure that matters is the one where it stopped three weeks ago — because the
result is a server nobody can reach, and it is indistinguishable from a server that is fine
unless something says otherwise. `homebasectl vpn dns` reports whether the last update
worked, and the VPN status folds it in, because they fail together.

The provider is a **word from a fixed table**, never a URL. A URL from the caller would be a
way to make the machine fetch an arbitrary address as root — the generic execution path
ADR-0006 exists to prevent, wearing a different hat. The token is declared `Secret` on the
operation, so it is redacted from the audit log; that machinery exists because the Wi-Fi
passphrase needed it first, and `scripts/check_operations.py` refused the commit until the
new secret was listed there too.

#### Waking, in both directions

`homebasectl wake` sends a magic packet and talks to nothing — not core, not hostd, not the
database. A wake-up packet is a UDP broadcast any process can send, so routing it through the
privilege boundary would add an audit record and a permission check to something with no
privilege in it. It is useful over the VPN: on a train, wanting the desktop at home to start.

Waking *the server* cannot be done from the server, so what Homebase can do is say, before it
sleeps, whether it could be: `homebasectl network` reports the hardware address and whether
the card will accept a packet.

#### Three fields eaten in the middle, and the one that mattered

`--json` was printing the struct `homebasectl` decoded into rather than what the server said.
Those differ whenever core knows a field the CLI does not, and the difference is silent — the
field is simply absent, and a script relying on it breaks with nothing to read anywhere. It
now prints the response.

The same shape twice more: `hostclient.NetworkInterface` had no `wake_on_lan`, so the value
hostd reported vanished between hostd and core. And a secret prompt with no terminal blocked
for ever rather than failing — every script, and every `ssh host homebasectl …`, which is how
this CLI is meant to be used.

#### The key is shown once and stored nowhere

The server generates both halves — it has to, because a phone joins by scanning a QR code
and that code must contain the key — and then keeps only the public one. A configuration that
is lost cannot be re-shown; the device is removed and added again.

Every comparable tool keeps client configurations on the server, which is convenient right
up to the day somebody gets into the server and leaves with every device's identity. This is
the second time the project has chosen "shown once" over "stored for convenience", after the
recovery code in [ADR-0015](docs/decisions/0015-password-recovery.md).

The VM test checks it by grepping the whole of `/etc` and `/var/lib` for the private key it
was handed, and the audit log as well.

#### Reachability is a handshake, not a probe

"Is my router forwarding the port?" cannot be answered from inside the house. Answering it
properly means asking a service on the outside to try — which is the dependency this whole
record exists to avoid.

So Homebase does not ask. It reports whether any device has ever completed a handshake, which
proves the same thing with evidence: the name resolved, the router forwarded, the key was
accepted. Until then it says nothing has connected yet and that the port is the likely
reason.

#### What the lab cannot simulate

Both machines are on one segment, standing in for a correctly forwarded port. The router is
not simulated and neither is carrier-grade NAT, which is the case where this design simply
does not work and the documentation says so.

The first run failed at the handshake, and the reason is worth recording: the test picked the
server's *Docker bridge* address, 172.17.0.1, because the shared segment has no DHCP and
nothing had assigned an address on it. WireGuard was working perfectly and the endpoint was a
network the other machine could not reach.

**Wireguard rather than Tailscale**, and the trade is worth stating plainly. Wireguard needs
a forwarded UDP port and therefore a router somebody can configure, and it does not work
behind carrier-grade NAT. What it buys is that nobody can switch it off or start charging
for it — the first principle in the README — and that it can be tested without an account
on anybody's service. The ADR will record what would make us revisit it, which is CGNAT
becoming common enough to matter.

**Dynamic DNS is the one outside dependency**, and it is small and replaceable: a name that
points at a changing address. The design keeps the provider a setting rather than a
hard-coded assumption.

### Milestone 12 — Files on the network ✅

- [x] SMB shares over Samba, so Windows, macOS and Linux can all mount them
- [x] Shares defined against managed storage locations — including the server's own disk,
      which is now a location like any other rather than a thing to be protected from
- [x] Accounts that cannot log in: no shell, no password on the machine, and a namespaced
      name, so the password saved in a Windows dialog for years is not a credential for
      anything that administers the server
- [x] The file server is installed when the first folder is shared, not with Homebase. A
      machine nobody asked to share anything has no SMB server on it
- [x] Samba is off, and the port closed, whenever nothing is shared

**Done when:** a machine on the same network opens a share. ✅ — verified against the ASUS
from another laptop: SMB2 negotiated, NTLMv2 accepted, the share opened, a wrong password
refused, and the account rejected by ssh.

**Neither machine had an SMB client and neither could install one**, so the test is a
70-line SMB2 client written for the purpose. That is worth recording rather than
apologising for: the alternative was to declare it working because the port was open and
`testparm` was happy, which is the exact shape of every other bug in this document.

Three things had to be got out of the privilege boundary's way, and each is a place the
sandbox did its job:

- `useradd` needs `/etc/passwd` and `/etc/shadow`, which hostd may not write. A root service
  that can write the credential store can add a root login, so the account is created by a
  fixed unit instead — and the password never goes near it, because setting one writes only
  to Samba's own database, which hostd may write directly. So the secret goes from the
  caller to `smbpasswd`'s standard input and never reaches a disk or a command line.
- `ufw` writes `/etc/ufw` and reloads the packet filter, neither of which hostd may do. Same
  answer, and the same reasoning: a root service that can rewrite the firewall can open the
  machine to the internet.
- `chmod` refused the set-group-id bit, as root, because `RestrictSUIDSGID=yes` is set.
  Samba's `force group` does the same job better, and the restriction stays.

#### What one person using it found in an evening

Milestone 12 was finished, tested from another machine, and documented. Then
somebody opened a file manager and none of it worked. Every one of these is a
gap between *working* and *usable*, which is a distinction no test in this
repository was measuring.

**The login refused the name it was created with.** The account is namespaced —
`hbshare-okan` — which is what stops a file-sharing password from also being an
ssh login. It is not something anybody should have to type, and the first person
to try typed the name they had just chosen and got an authentication box that
came back for ever with no explanation. Samba has a username map for exactly
this. The account keeps the prefix; the map translates.

**There was no way to open an installed application.** The address existed by
then and nothing offered it: no button, no command, nothing printed after an
install. A media server nobody can find is not better than one that will not
start.

**`apps stop` and `apps restart` had never worked**, because both require the
name confirmed and only `uninstall` sent it. The same shape as `apps logs`
decoding the wrong field: an endpoint with a caller nobody had run.

**Changing an application's disk never took effect.** A container's bind mounts
are fixed when it is created, so restarting one keeps the folders it was built
with — and the message promised that a restart would apply it. A test asserted
that message, which is how a false claim survives.

**Pointing an application at a shared folder took the folder away from the file
server**, because user-selected storage was handed to the application's own
account recursively. Right for data an application owns; wrong for a folder
somebody else writes into.

**`homebase.local` sometimes resolved to `172.17.0.1`** — a Docker bridge. avahi
answers on every interface it can see, so a laptop asking for the server by name
got back an address that was unroutable, or its own bridge. It appeared as ssh
reporting a host key for an address nobody recognised.

**`ls` on a shared folder said "Permission denied"** to the person who had just
been told its path, because `/srv/homebase` was `0750`. The tempting fix — adding
the administrator to the `homebase` group — is the wrong one: that group owns the
hostd socket, so it is the privilege boundary.

The lesson is not any one of these. It is that **the distance between "verified
working" and "a person can use it" was seven bugs wide**, and every one of them
was found in under an hour by somebody trying to do a real thing.

#### Heat, on a machine chosen for being disposable

Milestone 9 is about hardware, and the hardware turned out to have something to
say. The first real laptop throttles: sustained load takes it to 86 °C against an
84 °C limit, while its own fan controller stops at two thirds and lets the
processor slow down rather than make noise. That is the right trade for a laptop
on a desk and the wrong one for a server transcoding a film in a cupboard.

Homebase reports the fan now — speed, how hard it is driven, and **who is driving
it**, which is the field that matters. A fan somebody pinned years ago and a fan
working correctly on a hot machine sound identical from a doorway, and one is
fixed in seconds while the other needs a heatsink cleaning.

It does not *control* the fan, and that is a decision rather than a gap. On a
machine already at 89 °C with the fan still climbing, a manual setting is a way
to cook a computer that is struggling — and the `asus_wmi` driver refuses to
report a speed at all while under manual control, so it cannot even say what was
done.

**A correction, from watching the same machine a day later.** This originally
recorded that a manual setting was not cleanly reversible — that switching back
to automatic left the fan fast, and that it took a reboot. That was wrong, and
wrong in the way this document keeps warning about: a cause inferred from a
symptom, measured once, minutes after a stress test.

What the fan actually does is decay very slowly. From full it takes about ten
minutes to come back down, and it does so on its own. Every earlier reading that
looked like a stuck fan was taken shortly after something CPU-heavy — a package
build, an install, the boot itself — and was simply on the way down.

It also hunts. At 44 °C, held flat with no load, it fell to 2400 rpm and climbed
back to 4200 over the following two minutes. That is the controller oscillating
across a step boundary with no hysteresis, which is a firmware quirk and not
something software here can fix. It is worth recording because it is the reason a
single fan reading means so little, and therefore the reason the history exists.

- [x] Temperature and fan recorded every five minutes, to a plain CSV
- [x] `homebasectl system history`, with a chart that works over ssh
- [x] The same chart on the dashboard, drawn as inline SVG — a charting library
      would be the largest dependency in the product, for its simplest picture
- [ ] A boost mode: raising a fan is always safe and on this machine would stop
      the throttling, which is the opposite of what fan-control tools are usually
      for. It needs a temperature watchdog that hands control back on its own

### Milestone 13 — A catalogue worth having

Three applications, one of which exists only to prove installation works, is a
demo. The architecture for adding them is finished and proven — a manifest is a
reviewable file and `hostd` owns what can run ([ADR-0012](docs/decisions/0012-hostd-owns-the-catalogue.md)) —
so this milestone is the content, and the places where real applications do not
fit the model.

- [x] **qBittorrent** — completes the media loop that already half exists. The
      download folder and the media folder are storage locations on the same
      filesystem, so a completed file is renamed into place rather than copied
- [x] **A manifest can say what is left to do.** qBittorrent invents a new
      password on every start until somebody sets one, and it says so in its log;
      File Browser prints one once; Jellyfin has a setup wizard. All three were
      installable and unusable without a sentence saying which
- [x] **Two applications cannot claim the same port**, which nothing checked —
      each manifest is valid alone and the collision only exists across the
      catalogue
- [ ] **Immich** — photographs off a phone and onto hardware its owner controls.
      The most-wanted self-hosted application there is, and the one that makes a
      server worth the electricity. Needs a database container, which the manifest
      schema does not yet describe
- [ ] **Paperless-ngx** — documents, searchable. Also multi-container
- [ ] **Nextcloud** — deliberately last of these four. It overlaps with what SMB
      already does, needs a database and a cache, and is the heaviest thing in the
      catalogue by a distance
- [x] **Multi-container applications** in the manifest schema. A manifest
      declares supporting containers; each joins a private network of the
      application's own, publishes no port on any interface, and there is
      deliberately no field with which to ask for one — a database a manifest
      *could* publish is a database somebody publishes. Proven with Postgres and
      Redis: both running, reachable by the application under the names it
      expects, and invisible to the machine (`ss` finds nothing listening).
      Uninstalling removes the set and the network with it
- [ ] **A per-application initial password**, generated and reported once. File
      Browser ships `admin`/`admin`; qBittorrent ships `admin`/`adminadmin`. An
      application published onto the network with a documented default password is
      an open door, and the manifest must be able to say so

#### The wall the multi-container work hit

Building it found something larger than it, and it is not about databases.

**A great many popular images cannot run under Homebase's container model.**
Paperless was the first one tried. Its entrypoint starts as root, `chown`s the
directories it was given, and drops to its own account with `gosu`. Under
Homebase every application runs as a uid of its own with all capabilities dropped
and `no-new-privileges`, so all three steps fail:

```text
chown: changing ownership of '/config/data': Operation not permitted
error: failed switching to "paperless": operation not permitted
```

That pattern is not unusual — it is what every linuxserver.io image does, and
many others. So the choice is real and has to be made deliberately rather than
one manifest at a time:

- Grant `CAP_CHOWN`, `CAP_DAC_OVERRIDE`, `CAP_FOWNER`, `CAP_SETUID` and
  `CAP_SETGID` to applications that declare they need them. The container still
  cannot escape, but it can rewrite ownership across its bind mounts and become
  any user inside itself — which is most of what the model was protecting.
- Or keep the model and accept a catalogue restricted to images that support
  running as an arbitrary user, which excludes a large part of what people
  actually want to run.

Neither is obviously right, and it is exactly the decision Milestone 14 exists to
make for Home Assistant — so it is the same milestone's problem, arriving early
and from a different direction. **Paperless is not in the catalogue** and will
not be until this is settled: shipping an application that installs and
crash-loops is worse than not shipping it.

**Done when:** installing any application in the catalogue produces something
reachable, with no default credentials left in it, and a folder somebody can put
files into from their own computer.

### Milestone 14 — Home Assistant, and what it costs

Home Assistant is the application that does not fit, and it is worth its own
milestone rather than a line in the one above — because making it work means
deciding what a manifest is allowed to ask for.

It wants the host network namespace, so it can find devices by mDNS and SSDP. It
wants USB devices passed through, for Zigbee and Z-Wave adapters. Both are
things every other manifest is forbidden, and both are the difference between the
application working and not existing.

- [ ] `host_network` in a manifest, which the schema already describes as needing
      written justification, actually used and actually reviewed
- [ ] USB device pass-through, named per device rather than as a class
- [ ] A statement in the application's own screen about what it was granted, so
      that "this one is different" is visible to the person installing it rather
      than only to whoever read the manifest

**Done when:** Home Assistant runs, finds a device on the network, and the
dashboard says plainly what it was allowed to do that other applications are not.

### Milestone 15 — The media loop closes itself ✅

- [x] Prowlarr, Radarr, Sonarr and Jellyseerr as manifests
- [x] Applications that declare they need to reach another application, so the
      addresses are Homebase's to arrange rather than a user's to look up
- [x] The whole flow on one storage location, so a completed download becomes a
      film in the library by being linked rather than copied

**The capability question got answered by hitting it three times.** An image may
now declare that it cannot run as an arbitrary user, which grants uid 0 and five
named capabilities and requires a written reason. Every linuxserver.io entrypoint
needs it; the alternative was a catalogue that excluded most of what people want.
What bounds it is that the bind mounts are the only paths the container can reach,
and that `PUID`/`PGID` mean the elevation lasts only as long as the entrypoint —
verified on the ASUS, where the Radarr process itself runs as an unprivileged uid
Homebase chose.

**Reaching another application needed inventing, because every obvious route
fails.** Containers on Docker's default bridge cannot resolve each other at all.
The server's own `.local` name does not resolve inside a container. And the bridge
gateway is the host, where the firewall drops it — which is correct, since a rule
permitting containers to reach the host's published ports would permit every
container to reach every one of them. So a manifest names the applications it must
reach and Homebase puts them on a shared network.

**An upgrade served two applications from two different versions of hostd.** It is
socket-activated, so replacing the binary leaves any running process alone, and
whether a request gets the old code or the new one depends on when the previous
one exited. Two applications installed a minute apart came out configured
differently, with nothing to say why. The package now stops the service so the
next connection starts the new binary.

#### A catalogue is a tested combination, not a set of latest versions

Sonarr refused qBittorrent with "Authentication Failure" against credentials that
were provably correct — a login from inside Sonarr's own container returned
success while Sonarr's own test did not.

qBittorrent 5.1 changed its login reply from `200 OK` with the body `Ok.` to an
empty `204`. Sonarr checks the body. So the newest version of each, both correct
on their own, do not work together — and the error names the username.

**Nothing in the manifest schema could have caught that**, because neither
manifest is wrong. It is a property of the pair. Which is the argument for a
curated catalogue over a list of images: the value is not the manifests, it is
that the versions in them have been run together. qBittorrent is pinned to 5.0.5
with the reason written where the next person to bump it will read it.

The general rule now recorded: **pin a version because it was tested with the
others, and treat a version bump as a change to the combination.**

### Milestone 15 — the original plan

qBittorrent alone means finding a file, adding it, waiting, and moving it into
the right folder by hand. The applications that automate that — Prowlarr, Radarr,
Sonarr, Bazarr — are four more manifests and one hard problem: they talk to each
other, and each needs the others' addresses and API keys.

- [ ] Prowlarr, Radarr, Sonarr, Bazarr as manifests
- [ ] Applications that can declare they need to reach another application, so
      that the addresses are Homebase's to fill in rather than a user's to look up
- [ ] The whole flow on one storage location, so a completed download becomes a
      film in the library by being renamed rather than copied

**Done when:** something asked for arrives in Jellyfin without anybody moving a
file.

**Deliberately not in this milestone:** a VPN client. Running a download client
through somebody else's VPN is the usual arrangement and Homebase has a Wireguard
*server*, not a client. Those are different things and conflating them would put
the server's own remote access and a third-party tunnel in one configuration
file.

### Stage 1 definition of done

Someone comfortable with Linux can: write the installer USB with one command, plug it into
an old laptop and walk away, then reach the machine over SSH or a browser and — from a
terminal — install and use an app, attach external storage, configure and verify a backup,
update and restart safely, recover from a simulated failure, export diagnostics safe to
send to somebody, share files onto the network, and reach the whole thing from outside the
house over a VPN nobody else operates. Without ever granting the dashboard or the CLI root
access to do any of it.

**What is outstanding:** nothing on that list. It was finished by Milestone 12.

What remains is Milestone 9 — more hardware, and time on the one machine there is
— and the fact that a definition of done written around *capabilities* was
satisfied by a product with three applications in it, one of which is a test
fixture. Milestones 13 to 15 exist because "can install and use an app" and "has
apps worth using" turned out to be different claims.

**This definition changed after Milestone 9.** It used to read "a non-technical person…
without ever opening a terminal", and Milestone 10 used to be a graphical USB writer for
Windows and macOS. That was written for an audience this project does not have. The
audience is somebody who is happy in a terminal and wants the tedious parts — installing,
backing up, updating, recovering — to be done properly and once.

What survives the change is almost everything, because the hard parts were never about the
interface: the privilege boundary, the signed updates with rollback, the backups that
restore onto a different machine, the recovery tools. What goes is the graphical
stick-maker, which existed only to serve the audience that has been dropped — and it was
the largest single piece of remaining work in Stage 1.

The dashboard stays. It is built, tested, and works, and deleting working code to satisfy a
preference would be a poor trade. It is no longer what decides whether a milestone is done:
new work lands as a `hostd` operation, an API route and a `homebasectl` command, and gets a
screen when a screen is cheap.

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
