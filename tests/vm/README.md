# tests/vm/ — the disposable VM lab

Throwaway Ubuntu VMs for testing what cannot be tested honestly anywhere else: systemd
units, real disks, real reboots.

```sh
make vm-create     # create and boot one (~20s after the first run)
make vm-ssh        # shell into it
make vm-test       # the full end-to-end check
make vm-test-hostd # hostd under real systemd
make vm-destroy    # remove it
```

## How it works

Raw QEMU, UEFI, Ubuntu cloud images, no libvirt and no root —
[ADR-0010](../../docs/decisions/0010-vm-lab-qemu-cloud-image.md).

1. The Ubuntu cloud image (~600 MB) is downloaded once and **checksum-verified** against
   Ubuntu's published `SHA256SUMS`. It is the base of every test VM, so an unverified image
   would compromise every result derived from it.
2. Each VM is a **copy-on-write qcow2 overlay** on that base — a few MB and under a second,
   rather than a 600 MB copy.
3. **cloud-init** configures it at first boot: hostname, the `homebase` user, and a
   **freshly generated throwaway SSH key**. Never your own key: a harness that installs your
   personal key into every disposable machine eventually installs it somewhere you did not
   intend.
4. QEMU boots it under **UEFI (OVMF)**, because Homebase ships UEFI-only and a lab booting
   legacy BIOS would be testing a machine we do not ship.
5. Networking is **user-mode with port forwarding** — no bridge, no daemon, nothing needing
   root. SSH plus the API and dashboard ports are forwarded to localhost.
6. The harness then waits for **systemd to finish starting**, not merely for SSH to answer.
   SSH is reachable several seconds before the system is ready, and a test that begins there
   races cloud-init — producing flaky failures that get blamed on the code under test.

## Prerequisites

```sh
sudo apt install qemu-system-x86 qemu-utils cloud-image-utils ovmf
sudo usermod -aG kvm "$USER"    # then log out and back in
```

Roughly 40 GB of free disk: the cached base image, plus an overlay per VM that grows as the
VM writes.

`./scripts/bootstrap-dev.sh` checks all of this and tells you what is missing.

## Layout

```text
tests/vm/
├── vmctl.py            # the harness
├── test_lifecycle.py   # the Milestone 1 exit condition, executable
├── cache/              # gitignored — base image, checksum stamp
└── run/<name>/         # gitignored — per-VM overlay, key, console log, state
```

`cache/` is shared and worth keeping. `run/` is disposable; **destroying a VM deletes it
entirely**, which is the point.

Set `HOMEBASE_VM_DIR` to put both somewhere else — an external disk, for instance.

## What `make vm-test` proves

The Milestone 1 exit condition, as an executable test: create a clean VM, install a systemd
service, reboot, verify the service came back, export logs, destroy the machine.

Two assertions in it are worth knowing about, because both catch a test that would otherwise
pass while proving nothing:

**The reboot is verified by `boot_id`.** SSH reconnecting is not evidence of a reboot — it
is equally consistent with the machine never going down. The test compares
`/proc/sys/kernel/random/boot_id` before and after and fails if it is unchanged.

**The service must record exactly two boots.** Not "at least one". A service that is still
running because it never stopped, and a service that correctly restarted, look identical if
you only check that it is active.

The test destroys its VM on the way out **including when it fails**, after collecting
diagnostics. A failing test that leaves a 20 GB disk image behind is a test people stop
running.

## When something goes wrong

**It will not boot.** `tests/vm/run/<name>/console.log` is the serial console, captured from
the first instruction. It is the only diagnostic available when a VM fails before SSH, which
is exactly when you need one.

**Reproduce by hand.** `tests/vm/run/<name>/qemu.cmd` holds the exact QEMU command line.
Run it directly, swapping `-serial file:...` for `-serial mon:stdio` to watch the console
live.

**A leaked QEMU process.** If the harness is killed uncleanly, `make vm-status` shows what
it believes exists and `make vm-destroy-all` clears it. Overlays are what consume disk;
destroy VMs you are not using.

**Everything is slow.** Check `/dev/kvm` is accessible. Without it QEMU falls back to
emulation, which is roughly ten times slower and feels like a hang rather than an error.

## Scope

This lab tests Homebase running **on** a machine. It does not test **installing** Homebase —
the VMs come from cloud images, not from our installer. That is Milestone 6's job, in
[`tests/installer/`](../installer/), against blank and Windows-occupied disk fixtures.

The gap is deliberate and has a cost: these VMs are close to, but not identical to, a real
installation. [ADR-0010](../../docs/decisions/0010-vm-lab-qemu-cloud-image.md) records why
that trade was made and what would make us revisit it.
