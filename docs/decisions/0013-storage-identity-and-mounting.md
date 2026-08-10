# ADR-0013 — Disks are identified by filesystem UUID and mounted by systemd, never by fstab

- **Status:** Accepted
- **Date:** 2026-08-09
- **Milestone:** 4 — Storage
- **Supersedes:** nothing
- **Related:** [ADR-0006](0006-privilege-split.md), [ADR-0012](0012-hostd-owns-the-catalogue.md)

## Context

Milestone 4 lets a user attach a disk and give it to an application: a USB drive becomes
where Jellyfin keeps films. The roadmap gives storage its own milestone for one reason —
**storage mistakes are the ones that destroy data** — and two questions have to be settled
before any code is written, because both are almost impossible to change afterwards.

**How is a disk identified?** Whatever answer is chosen ends up written into an
application's configuration, into a mount unit, and into a user's expectations.

**How is it mounted?** Whatever answer is chosen runs on every boot of a machine that has no
monitor attached and whose owner does not have a rescue USB stick.

The target machine makes both sharper than they sound. It is a laptop in a cupboard. Disks
are plugged and unplugged by hand, in whatever order somebody happens to reach for them, by
a person who will not be reading a console.

## Decision

### Disks are identified by filesystem UUID

A managed location records the filesystem's UUID. Never `/dev/sdb1`.

Device paths are assigned in discovery order, so they change. Plug in a phone before the
media drive and yesterday's `/dev/sdb1` is today's `/dev/sdc1` — and an application pointed
at `/dev/sdb1` is now pointed at somebody's phone. That failure is silent, and depending on
what happens next it is either confusing or destructive.

A filesystem UUID is written inside the filesystem itself. It survives being unplugged,
moved to a different port, moved to a different machine, and having other disks plugged in
first. It changes when the filesystem is recreated, which is exactly when Homebase should
consider it a different disk.

Where a UUID is genuinely absent — some exFAT and FAT volumes have only a short serial —
the volume is listed and can be mounted manually, but **cannot be assigned to an
application**. An assignment that cannot be resolved reliably is worse than no assignment.

### Mounting is done with systemd mount units, never `/etc/fstab`

`hostd` writes a `.mount` unit per managed location and enables it. It never edits
`/etc/fstab`.

A malformed or unsatisfiable `fstab` entry stops the boot and drops the machine to an
emergency shell. On a server with a monitor and a keyboard that is an annoyance. On a laptop
in a cupboard, belonging to somebody who was promised they would never need a terminal, it
is a brick — and the thing that caused it is a disk they unplugged.

systemd mount units are the same mechanism `fstab` is translated into anyway, with two
properties that matter here:

- `nofail` — an absent disk fails one unit rather than the boot. This is the whole argument.
- They are files in `/etc/systemd/system/`, written and removed by name, so Homebase can
  manage its own entries without parsing and rewriting a file that other things also edit.

Every unit Homebase writes carries `nofail` and a short `x-systemd.device-timeout`. There is
no configuration to turn that off. A disk that is not there must never be able to stop the
machine from starting.

### Nothing is ever formatted or mounted without being named

`storage.format` requires the caller to name the disk's identity, in the same way
`app.remove_data` requires the application's id and `system.reboot` requires the hostname.

**Homebase never selects a disk on the user's behalf**, in any operation, for any reason —
including when there is only one candidate. "The obvious disk" is how somebody's photographs
get erased by a machine that was sure.

### The mountpoint is inert when nothing is mounted on it

A managed location's mountpoint directory is owned by `root` and mode `0555` when empty.

This is the disconnected-disk problem, and it is the one that quietly destroys the most.
When a disk is unplugged, the mountpoint reverts to being an ordinary empty directory on the
root filesystem. An application still writing to it writes to the system disk instead —
filling it, and creating files that vanish from view the moment the disk is reconnected and
shadows them. The user sees an application that lost their data and a server out of space,
and nothing anywhere reports an error.

Making the empty mountpoint unwritable turns that silent corruption into an immediate,
attributable write failure.

### An application whose storage is absent does not run

If a user-selected location is not mounted, the application does not start. If it disappears
while the application is running, Homebase stops the application and records why.

Running a media server whose media is missing produces an application that appears broken,
an empty library, and — for applications that rebuild an index from what they can see — a
database that has to be repaired afterwards. Refusing is worse in the moment and better
every time after it.

## Alternatives considered

### Identify by device path

Rejected above: device paths are assigned in discovery order and change. The failure is
silent and can be destructive.

### Identify by partition label

Better than a device path, and readable, but labels are not unique — two disks can carry
the same one, and nothing prevents it. A collision resolves to whichever was seen first,
which is the device-path problem with extra steps.

Labels are still shown to the user, because "Seagate Backup" is what somebody recognises,
and a UUID is not. Displayed by label, resolved by UUID.

### Identify by disk serial number

Identifies the *hardware* rather than the *filesystem*, which sounds stronger and is wrong
for this. Reformatting a disk would keep the identity while destroying everything the
identity referred to, so an application would carry on pointing at a location whose contents
are gone. Homebase should treat a recreated filesystem as a new disk, and a UUID does.

### Write `/etc/fstab` entries with `nofail`

`nofail` addresses the boot-failure argument, and this was the closest alternative.

Rejected on the other one: `fstab` is a single file that the distribution, the installer and
a future administrator all also edit. Managing entries in it means parsing and rewriting
somebody else's file, and getting that wrong once produces the unbootable machine that
`nofail` was there to prevent. Separate files that can be written and deleted whole have no
such failure mode.

### Mount on demand with `autofs` or `systemd.automount`

Attractive for a disk that is often absent: the mount happens on first access.

Rejected because it makes "is the disk connected?" unanswerable without touching the
mountpoint, and Homebase needs that answer to decide whether an application may run. It also
turns an absent disk into a *delay* at the point of first access rather than a clear state,
which is precisely the ambiguity this milestone is trying to remove.

### Let applications write to a bare mountpoint

That is the default behaviour, and it is what the `0555` decision exists to prevent. Rejected
as the failure with the worst ratio of damage to visibility in the whole milestone.

## Consequences

### What this makes easier

- A disk can be unplugged and reconnected in a different port, or after other disks, and is
  still the same location to Homebase
- A machine with a missing disk still boots, still serves the dashboard, and can explain
  what is wrong
- Reformatting a disk is correctly treated as a new location rather than the old one
- An application never silently writes its data somewhere other than where the user put it

### What this makes harder

- A volume with no UUID cannot be assigned to an application. Some FAT and exFAT volumes are
  affected, and the honest answer to that user is "reformat it", which is not the answer
  anybody wants
- `hostd` grows again: block-device discovery, mount unit generation, filesystem creation.
  The same argument against [ADR-0012](0012-hostd-owns-the-catalogue.md) applies here with
  more force, and it is the strongest argument against this decision
- Homebase owns files in `/etc/systemd/system/`, so uninstalling has to remove them. A
  package that leaves a mount unit behind leaves a machine referring to a disk nothing
  manages any more
- Refusing to start an application whose disk is absent will read as Homebase being broken
  to somebody who does not realise they unplugged something. The interface has to say which
  disk and where it was

### Security impact

`core` gains no new privilege. Storage operations follow the ADR-0006 pattern: `core` sends
an identifier, `hostd` performs the work.

The parameters here are more dangerous than an application id, because a mount is one of the
few things that can make an attacker-chosen path appear inside a container. So:

- `core` sends a **location id or a filesystem UUID**, never a device path and never a
  mountpoint. `hostd` resolves the identifier to a device itself
- Mountpoints are constructed by `hostd` under a fixed root, never supplied
- Every managed location is mounted `nosuid,nodev`. A removable disk is untrusted input: it
  can be prepared on another machine, and a setuid binary on a USB stick is a local root
  exploit that arrives by post
- `storage.format` is `critical` and requires explicit confirmation naming the disk. It is
  the second operation in Homebase that destroys data irreversibly, after
  `app.remove_data`, and the first that can destroy data Homebase never created

### What would make us revisit this

- Multi-disk arrangements — pooling, mirroring, anything spanning devices — which the
  roadmap deliberately excludes and which would change what "a location" means
- Encrypted volumes, where the identity of the *unlocked* filesystem differs from the
  identity of the thing the user plugged in
- Network storage, which has no filesystem UUID at all and would need a second identity
  scheme rather than a stretch of this one
