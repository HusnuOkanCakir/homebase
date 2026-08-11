# ADR-0018 — Updates are a signed APT repository, not an image

- **Status:** Accepted
- **Date:** 2026-08-11
- **Milestone:** 8 — Updates and recovery
- **Related:** [ADR-0006](0006-privilege-split.md), [ADR-0016](0016-installation-media.md),
  [update security](../security/update-security.md)

## Context

An automatic update system is a remote code execution channel the user has agreed to.
Compromising it reaches every installation at once, which makes it a higher-impact target
than `hostd` — `hostd` compromises one machine.

[Update security](../security/update-security.md) was written in Milestone 0 and already
fixes the requirements: artifacts are signed and verified before installation, downgrades are
refused unless asked for, signing keys are unreachable from ordinary CI, the *same* artifact
is promoted between channels rather than rebuilt, an interrupted update leaves a bootable
machine, and recovery works when the update server is unreachable or hostile.

What was never decided is the mechanism. Homebase today is four `.deb` packages installed on
Ubuntu Server by [ADR-0016](0016-installation-media.md)'s autoinstall seed. The machine they
run on is an eight-year-old laptop in a cupboard, and losing power part-way through an update
is expected rather than exceptional.

## Decision

**Homebase publishes a signed APT repository, and snapshots its own state around the
transaction.**

### Verification is apt's, not ours

The repository is an ordinary Debian archive: a `Release` file listing the SHA-256 digest of
every index, with a detached OpenPGP signature over it. `hostd` does not verify anything
itself — it runs `apt-get`, and apt refuses to install from an archive whose `Release`
signature does not verify against the key it was told to expect.

The key ships in `homebase-hostd` as a keyring under `/usr/share/keyrings/`, and the source
entry names it with `signed-by=`, so this key is trusted for the Homebase repository and
nothing else on the machine.

`signed-by=` binds a key to a source; it does not bind it to package names. A repository
Homebase controls could still offer a package called `openssh-server` and win on version
number. So the source is also pinned in `/etc/apt/preferences.d/`, restricting the Homebase
origin to `homebase-*` packages. Without that pin, compromising Homebase's signing key would
mean replacing any package on the system rather than only Homebase's own.

### Channels are suites, so promotion moves nothing

`development`, `alpha`, `beta` and `stable` are four suites in one repository. A machine
subscribes to exactly one. Promotion re-indexes the same `.deb` file into a new suite — the
bytes are identical, the signature still verifies, and stable ships precisely what beta
tested. A rebuild between channels would ship something nobody ran.

Changing channel is a Homebase operation, not a file somebody edits.

### Everything is fetched before anything is applied

`apt-get -d` downloads and verifies the whole set first. An update that fails while
downloading — no space, no network, a bad signature — has changed nothing at all, which
removes the largest slice of the interruption window without any cleverness.

### The snapshot covers what apt does not own

Before `dpkg` runs, `hostd` snapshots the SQLite database and `/etc/homebase`. These are the
things a failed upgrade can leave inconsistent with the code, because a migration has run
against them.

**Application data in `/srv/homebase` is deliberately not snapshotted.** It is measured in
hundreds of gigabytes, no package touches it, and copying it would turn a thirty-second
update into an hour-long one — while filling the disk it is trying to protect. Its safety
comes from nothing in the update writing to it, which is a stronger property than a copy.

### Failure rolls back, and rollback is a reinstall

After apt returns, `hostd` runs a health check. If it fails, the previous versions are
reinstalled from `.deb` files still in apt's cache — no network needed, which matters because
one of the ways an update fails is by breaking the network — and the snapshot is restored.

### Downgrades are refused unless somebody asks

`hostd` refuses a target version lower than what is installed unless the caller is explicitly
rolling back. An attacker able to serve old-but-validly-signed artifacts can otherwise push a
machine back to a version with a known hole, and every signature involved is genuine.

## Alternatives considered

### A/B image updates

Two root partitions, write the inactive one, switch the bootloader, reboot. This is what
serious appliances do — RAUC, Mender, ChromeOS — and atomicity stops being something to be
careful about and becomes a property of the partition switch.

Rejected for this milestone, for two reasons of different weight.

The first is cost: it requires the installer to lay down two root partitions, which means
abandoning Ubuntu's stock `direct` layout and taking ownership of the partition scheme and
the bootloader — the two things [ADR-0016](0016-installation-media.md) specifically declined
to own, on a machine we cannot reproduce when it fails to boot. It also doubles the root
filesystem's disk cost on hardware chosen for being old.

The second matters more, because it means A/B would not deliver what it appears to.
**Homebase's state is not on the image.** The SQLite database, the container images, the
per-application uid allocations and the mount units all live outside it. An A/B switch that
reverts the code while leaving a database a newer version already migrated is not a rollback;
it is a downgrade into an inconsistent state, which is the failure this milestone exists to
prevent. Whatever the delivery mechanism, the state snapshot above still has to be built. A/B
would sit on top of it, not replace it.

Recorded as the right answer if Homebase ever builds its own image, which
[ADR-0016](0016-installation-media.md) already names as a future possibility.

### A bespoke updater: signed tarball, verified by us

Attractive because it is self-contained and owes nothing to Debian. Rejected because it means
writing signature verification, and `hostd` has no non-standard-library dependencies by
policy — Go's standard library has no OpenPGP, so this would be ed25519 over a hand-rolled
metadata format. That is inventing both a package manager and a trust format, on the
highest-impact attack surface in the project, to replace one that has been attacked and fixed
in public for twenty-five years.

### `unattended-upgrades`

Already present on Ubuntu and would work. Rejected as *the* mechanism because it has no
health check, no rollback and no way to report progress into a job — an update that silently
breaks the dashboard at 3am is exactly the failure the user cannot diagnose.

It stays for a different job: **Ubuntu's own security updates are not Homebase's updates**,
and a server that only patches when Homebase releases is a server running an unpatched
`openssl` between releases. The two paths remain separate, with separate cadences.

## Consequences

### What this costs

**`dpkg` is not atomic, and this is the honest weak point.** Power loss during unpack leaves a
package half-configured, and the machine needs `dpkg --configure -a` to finish. That is
detected on start and run automatically. The exit condition still holds — the machine boots,
because the packages being replaced are not the boot path, and application data is untouched
because nothing in the update writes to it — but "recovers cleanly" here means "Debian's
recovery path, which is decades old", not "atomic". An A/B image is what buys atomicity, and
that is the trade being made.

**Homebase has to host a repository and hold a signing key.** Both are new operational
responsibilities, and the key is now the most valuable secret in the project.

**Updates depend on apt behaving.** A broken `sources.list.d` entry or a held package can
block updates in ways whose error messages are apt's rather than ours, and those messages are
not written for this audience. Translating them is work this milestone owns.

### Security impact

This adds a code execution channel that did not exist: a signed repository whose contents
become root-installed packages. It is bounded by three things — apt's signature verification
against a pinned keyring, an origin pin restricting it to `homebase-*` packages, and refusal
of downgrades.

The signing key is not reachable from ordinary CI, and `hostd` gains no generic execution
path: `update.check` and `update.apply` are named typed operations that run specific commands
with fixed arguments, exactly like `system.rename` runs `hostnamectl`.

A signature failure is a security event, not a transient error. It is reported to the user,
written to the audit log, and **not retried** — "update failed, retrying" is the wrong
response to a bad signature.

### What would make us revisit this

- **Homebase building its own image**, at which point A/B becomes available for the cost of
  a partition table it already owns, and this record should be superseded.
- **A power-loss test that leaves an unbootable machine.** The interruption matrix is what
  decides whether this is fit to ship; if `dpkg` is where it fails, the trade above was wrong.
- **The origin pin proving unmaintainable** as the package set grows, which would mean the
  compromise blast radius is wider than stated here.
