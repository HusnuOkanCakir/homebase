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

**Current position: Milestones 0–8 complete. Milestone 9 in progress; 10–12 rescoped.** A USB stick turns a Windows
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

What is still missing is hardware. Everything above is proven in VMs — with real firmware,
a real `mac80211` stack and a real WPA2 handshake, but emulated disks and no lid. Milestone 9
finishes on real laptops, which is where driver quirks, lid-close behaviour, thermal
throttling and USB disks that misbehave all arrive at once. Making the installer stick also
still takes one command on a Linux machine; the graphical tool for Windows and macOS is
Milestone 10, and it is what Stage 1 is still missing.

What it cannot do yet is the half a home server is actually for: **be reached from outside
the house**, and **share files onto the network**. Both are now milestones rather than
deferrals. And the whole of it is still driven through a browser or the HTTP API —
`homebasectl` has three commands, which for this audience is the wrong way round.

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
- [ ] Intel and AMD laptops, with and without TPM, various Wi-Fi adapters
- [ ] Lid-close behaviour and sleep prevention, on hardware with a lid
- [ ] Power-loss recovery on real disks, Wi-Fi reconnection, USB disk handling

**Done when:** three different laptops complete install → first boot → app install → reboot
→ backup → restore, with no manual Linux commands at any point.

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
- [ ] The operations still missing: restoring a backup, formatting and attaching a disk,
      changing the update channel, factory reset — the destructive ones, which need their
      confirmations thought about at a terminal rather than copied from the browser

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

### Milestone 12 — Files on the network

- [ ] SMB shares over Samba, so Windows, macOS and Linux can all mount them
- [ ] Shares defined against managed storage locations, so a share cannot be pointed at the
      system disk by accident
- [ ] Accounts and permissions that follow Homebase's, rather than a second set nobody
      remembers
- [ ] A test that mounts a share from another machine and reads a file, because a share that
      exists and cannot be mounted is not a share

**Done when:** a laptop on the same network mounts a share, writes a file, and the file is
in the backup.

### Stage 1 definition of done

Someone comfortable with Linux can: write the installer USB with one command, plug it into
an old laptop and walk away, then reach the machine over SSH or a browser and — from a
terminal — install and use an app, attach external storage, configure and verify a backup,
update and restart safely, recover from a simulated failure, export diagnostics safe to
send to somebody, share files onto the network, and reach the whole thing from outside the
house over a VPN nobody else operates. Without ever granting the dashboard or the CLI root
access to do any of it.

**What is outstanding:** the terminal surface (Milestone 10), remote access (11) and file
sharing (12). Everything else on that list works today.

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
