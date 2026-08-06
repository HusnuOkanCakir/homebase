# tests/ — the four levels

A Homebase feature is not finished when it works. It is finished when it works, fails
comprehensibly, survives a reboot, and can be rolled back.

| Directory | Level | Answers |
|---|---|---|
| `unit/` | Unit | Is the logic, parsing and validation correct? |
| `integration/` | Integration | Do `core`, `hostd`, SQLite and the container runtime agree? |
| `vm/` | VM | Does it work as real systemd services on real Linux, across a reboot? |
| `installer/` | Installer | Does a blank — or Windows-occupied — disk become a working server? |
| `upgrade/` | Upgrade | Does the previous release become this one without losing data? |
| `e2e/` | End-to-end | Can a user actually do this through the dashboard? |

Go and TypeScript unit tests live beside their source, as is idiomatic in both languages.
This directory holds the levels that need orchestration.

## The tests that matter most

Ordinary success-path tests are the easy half. The ones that earn their keep here are the
unpleasant ones, and they are required for any feature touching storage, installation,
updates or the privilege boundary:

- Power loss part-way through a write
- An update interrupted at each distinct stage
- A USB disk removed while mounted, then reconnected
- A disk that is full, then a disk that fills mid-operation
- A container image that fails its health check forever
- A restore onto a machine that is not the one backed up

## Milestone gating

Empty for now. `vm/` requires the QEMU harness (Milestone 1) and roughly 40 GB of free disk.
`installer/` and `upgrade/` require release artifacts to exist (Milestones 6 and 8).
