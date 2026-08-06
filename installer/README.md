# installer/ — server installation media

Ubuntu Server LTS autoinstall configuration and the post-install hooks that turn a bare
machine into a Homebase server. Lands in Milestone 6.

Responsibilities:

- Detect hardware and enumerate **every** disk, including the one holding an existing
  Windows installation
- Require explicit confirmation of the target device before touching it
- Whole-disk wipe, partition, install Ubuntu Server (x86-64, UEFI)
- Install `core` and `hostd`, configure the firewall and laptop power behaviour
- Generate the pairing identity and recovery code, then reboot into first-use setup

## Why this directory gets the most review

This is the only component that destroys data by design. A bug in `internal/jobs` produces a
failed job; a bug here erases somebody's photographs. Changes require the
`risk/destructive` label, and the confirmation flow may not be simplified for convenience.

Installer changes are validated by the installer VM CI job against a Windows-style disk
fixture — never merged on the strength of a local test alone.
