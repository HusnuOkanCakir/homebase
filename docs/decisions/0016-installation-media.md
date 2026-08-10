# ADR-0016 — The official Ubuntu ISO, unmodified, with a seed beside it

- **Status:** Accepted
- **Date:** 2026-08-10
- **Milestone:** 6 — Installer and first-use
- **Related:** [ADR-0010](0010-vm-lab-qemu-cloud-image.md), [ADR-0006](0006-privilege-split.md),
  [ADR-0015](0015-password-recovery.md)

## Context

This is the milestone that makes Homebase installable, and the installer is the only
component that destroys data by design. Everything else in this project fails by refusing to
work. This one fails by erasing the disk with somebody's photographs on it.

The target machine is the laptop in the cupboard: eight years old, Windows still on it, no
longer used for anything. The person installing has never opened a terminal, and the
roadmap's exit condition is deliberately phrased around that — *starting from a
Windows-occupied disk, the installer produces a working server that reaches the dashboard
and installs an application, with no Linux commands.*

Two things about that sentence do the work. **Windows-occupied**, because a blank disk is
the easy case and not the one people have. And **no Linux commands**, because an installer
that ends at a shell prompt has not installed anything as far as this user is concerned.

[ADR-0010](0010-vm-lab-qemu-cloud-image.md) already settled where this gets tested — real
ISO installation, in `tests/installer/`, against blank and Windows-occupied fixtures — and
recorded why the development lab does not use it: five to fifteen minutes per run against
twenty seconds. That cost is worth paying exactly once, here, for the thing it actually
tests.

What remains open is what the installation media *is*.

## Decision

**The official Ubuntu Server LTS ISO, downloaded, verified, and written unmodified. The
autoinstall configuration travels beside it on a second partition, not inside it.**

`homebasectl installer create` produces a USB stick with two partitions:

1. The Ubuntu Server ISO, written byte for byte as it was published.
2. A small volume labelled `CIDATA`, holding `user-data`, `meta-data` and Homebase's own
   `.deb` packages — the NoCloud datasource Ubuntu's installer looks for on its own.

The packages travel on the media rather than being fetched, so a machine with no working
network still ends up with a server on it. An installer that needs the internet to finish
is an installer that fails in the house it was bought for.

### Why the image is not repacked

Repacking is the common approach: unpack the ISO, drop `autoinstall.yaml` at its root,
rebuild it with `xorriso`. It works, and it was rejected for three reasons.

**A repacked image cannot be checked against anything.** Canonical publishes a SHA256 and
signs it. The moment the bytes are rebuilt, that signature describes a file nobody has any
more, and the honest answer to "is this image what Ubuntu published?" becomes "no, but trust
us". For the one component that erases disks, being able to verify the base image against
its publisher is worth more than the convenience.

**It makes us responsible for the boot path.** Rebuilding a hybrid UEFI/BIOS ISO correctly
is finicky, and getting it subtly wrong produces media that boots on the machine it was
tested on and not on the user's. Leaving the image alone means the boot path is Canonical's,
exercised by millions of installations.

**It needs tooling on the user's computer.** The controller runs on Windows, macOS and
Linux; `xorriso` is not present on two of those. Writing bytes and creating a small labelled
volume is something every platform can already do.

### What the autoinstall does, and does not, decide

The seed is deliberately thin. It sets up the machine and installs Homebase, and then it
stops.

- **Whole disk, always.** No resizing, no dual boot, no preserving a Windows partition.
- **The seed never names a device.** See below.
- **No user accounts beyond the one the installer needs.** The Homebase administrator is
  created in the browser, at first use, along with the recovery code
  ([ADR-0015](0015-password-recovery.md)). The installer does not invent a password, and
  there is no default credential to change afterwards.

### The installation is not silent, and that is Ubuntu's doing

Leaving the image alone has one consequence that shows up on the user's screen, and it is
worth being plain about because it is the most visible thing about the whole design.

Ubuntu's installer refuses to run an unattended installation it was handed by a datasource
unless `autoinstall` appears on the kernel command line. Putting it there means editing the
bootloader configuration *inside* the ISO, which is exactly the repacking this decision
rejects. So it stops, once, and asks:

```
Continue with autoinstall? (yes|no)
```

**That prompt is kept, and treated as the confirmation step.** It is the last moment before
the disk is erased, it requires a deliberate word rather than a keypress, and it is Ubuntu's
own safety feature rather than one Homebase would have to be trusted to have written
correctly.

It is also not good enough, and the honest way to hold that is to say so rather than to
pretend the alternative was better. The prompt appears under a screenful of installer log
output, it is Ubuntu's wording rather than ours, and — the real gap — **it does not say
which disk is about to be erased or what is on it.** A user who booted this on the wrong
machine gets no warning that they are about to lose something.

Two things reduce that exposure now, and a third fixes it later:

- The media is written by `homebasectl installer create`, which says plainly, before it
  writes anything, that booting this stick erases the whole disk of the machine it is booted
  on. The warning happens where there is room for it.
- The user guide's installation page is written around the prompt rather than around it
  being absent.
- A Homebase-branded first stage that enumerates disks, reports "this one looks like it has
  Windows on it", and asks in our words is what a custom image buys — and is the strongest
  argument for building one, recorded here so that the reason is not lost the next time the
  question comes up.

What is *not* done is telling the seed which device to install onto. A seed that names
`/dev/sda` is right on the machine it was written for and wrong on the next one, and the
consequence of being wrong is not a failed install but a destroyed disk. The layout is
`direct` onto the largest suitable disk, and the machine this is booted on is expected to be
one being given over to Homebase entirely.

## Consequences

### What this costs

**Two volumes is more than one file.** `homebasectl installer create` has to partition and
format, not merely copy — more code, and code that writes to block devices. It inherits the
installer's caution: enumerate removable devices, show size and model, refuse anything that
looks like a system disk, and require the device to be named before writing.

**The seed is visible and editable.** Anyone can mount the second partition and read the
autoinstall configuration. This is fine — it holds no secrets, by construction, and the
recovery code is generated on the machine at first use rather than baked into media that
might be handed around.

**Ubuntu's installer is Ubuntu's.** Its bugs and its wording are not ours, and a
point-release can change behaviour underneath us. The installer test pins a release and
verifies its checksum, so an upgrade is a deliberate change with a test run behind it rather
than something that happens on a Tuesday.

**Roughly 2.6 GB has to be downloaded** before media can be made. The controller caches it
and verifies before reuse.

### What was rejected

**A fully custom image built with `livecd-rootfs`.** Complete control, and the right answer
eventually — it would let the first-boot experience be ours from the first frame. Rejected
now because it makes Homebase responsible for the entire boot and installation path before
there is any evidence the product is worth installing, and because a broken custom image
fails on hardware we cannot reproduce.

**Repacking the ISO with `autoinstall.yaml` inside**, as above.

**Preserving Windows and dual-booting.** Attractive, because it makes the decision
reversible for the user. Rejected: partition resizing on a disk that may be failing, in a
machine that may lose power, is the single most dangerous thing this project could do — and
the user who wants their laptop back has a backup of the Windows they replaced, or they do
not want it back at all. Whole-disk is honest about what is happening.

**Naming the target disk in the seed.** Faster and fully unattended, and wrong for the
reason above: a stale device name here erases the wrong disk.
