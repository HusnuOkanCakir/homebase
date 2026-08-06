# ADR-0010: Raw QEMU and cloud images for the VM lab

- **Status:** Accepted
- **Date:** 2026-08-06
- **Amends:** the roadmap's original "QEMU/KVM + libvirt" sketch

## Context

Milestone 1 builds the disposable VM lab. Everything after it — application lifecycle,
storage, backup, updates — is tested there, because those features touch real systemd
services, real disks and real reboots, and none of that can be honestly tested in a unit
test.

Two properties decide whether the lab gets used or worked around.

**It must be fast.** A developer runs this dozens of times a day. A harness that takes ten
minutes to produce a VM is a harness people stop running, and a test suite people stop
running is worse than no test suite, because it still claims coverage.

**It must need no privileged setup.** The same harness has to work on a contributor's laptop
and on an ephemeral CI runner. Every daemon, group membership or bridge interface required
up front is a step that can be missing, misconfigured or unavailable.

The roadmap originally sketched "QEMU/KVM + libvirt" and, separately, Ubuntu autoinstall.
Both deserve revisiting now that the requirements are concrete.

## Decision

**Raw QEMU, no libvirt.** `qemu-system-x86_64` invoked directly, with user-mode networking
and port forwarding. No daemon, no XML, no group membership beyond `kvm`.

**Ubuntu cloud images, not ISO installation.** A single cloud image is downloaded and
checksum-verified once; each VM is a copy-on-write qcow2 overlay on top of it, configured at
first boot by cloud-init.

**UEFI firmware (OVMF), not legacy BIOS.** Homebase targets UEFI x86-64.

**ISO-based installation is not abandoned** — it moves to Milestone 6 and
`tests/installer/`, where testing the installer is the actual point.

## Alternatives considered

### libvirt, as originally sketched

Better tooling for long-lived VMs, real bridged networking so a VM is reachable from the
LAN, and a management layer that handles state.

Rejected because every one of its advantages costs setup that must be reproduced on CI.
`libvirtd` must run, the user must be in the `libvirt` group, and a NAT bridge must be
configured — all of which need root, and each of which is a way for the harness to fail on
somebody's machine for reasons unrelated to Homebase.

Raw QEMU with `-netdev user,hostfwd=` gives SSH and forwarded ports, which is the entire
requirement. What it gives up — LAN reachability, ICMP inside the guest — is not needed
until mDNS discovery in Milestone 7, and that can be revisited then rather than paid for now.

### ISO installation with Ubuntu autoinstall

Faithful: it produces a machine installed the way a user's machine is installed, exercising
partitioning and the real first-boot path.

Rejected for Milestone 1 on speed. An unattended install takes five to fifteen minutes per
VM against roughly twenty seconds for a cloud-image overlay — a difference between a harness
run per test and a harness run when someone remembers. It also downloads a 2.6 GB ISO rather
than a 600 MB image.

The faithfulness argument is real, and it is answered by scope rather than by mechanism: the
installer is what Milestone 6 tests, in `tests/installer/`, against blank and Windows-occupied
disk fixtures. Milestone 1 is not testing installation; it is providing somewhere honest to
test everything else.

### Containers instead of VMs

Far faster and lighter. Rejected outright: Homebase manages systemd units, mounts disks,
configures networking and reboots the machine. A container cannot honestly test any of that,
and a lab that cannot test a reboot is not a lab for this project.

### Legacy BIOS boot

Simpler, and the QEMU default. Rejected because Homebase ships UEFI-only, and a lab booting
a different firmware path than production tests a machine we do not ship.

## Consequences

### What this makes easier

- VM creation in roughly twenty seconds, so tests can create and destroy per case
- No privileged setup: `kvm` group membership and the QEMU binaries are the whole
  prerequisite
- Identical behaviour on a laptop and an ephemeral CI runner
- Copy-on-write overlays are cheap, so a dirty VM is discarded rather than repaired
- Debuggable: the QEMU command line is printed, and can be run by hand

### What this makes harder

- **The dev VM is not produced by our installer.** Cloud images carry cloud-init and a
  slightly different package set from what the installer will produce. Milestone 1 tests run
  on a machine that is *close to*, not identical to, a real installation. This is the real
  cost of the decision, and `tests/installer/` is the answer to it
- User-mode networking is slower and gives the guest no LAN presence, so anything involving
  discovery, mDNS or multiple machines needs a different arrangement in Milestone 7
- No management layer, so VM lifecycle, process supervision and cleanup are ours to write
- A leaked QEMU process outlives a crashed harness unless cleanup is deliberate — the
  harness tracks PIDs and `make vm-destroy` exists for this

### Security impact

Small and positive. No privileged daemon, no root, and a VM reachable only through explicitly
forwarded localhost ports rather than present on the LAN.

Each VM gets a **freshly generated throwaway SSH key**, never the developer's. Test VMs are
disposable and their credentials must be too — a harness that installs your personal key
into every disposable machine is a harness that eventually installs it somewhere you did not
intend.

The cloud image is checksum-verified against Ubuntu's published `SHA256SUMS` before first
use. It is the base of every test VM, so an unverified image would compromise every test
result derived from it.

### What would make us revisit this

- Milestone 7's mDNS and discovery work needing real LAN presence, which user-mode
  networking cannot provide — likely answered by a bridged mode for those tests specifically
  rather than by adopting libvirt wholesale
- Multi-VM tests, where managing several QEMU processes by hand stops being pleasant
- Cloud-image and installed-system divergence causing a bug that Milestone 1 tests should
  have caught, which would argue for running some suites against installer output
