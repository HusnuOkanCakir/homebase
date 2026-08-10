# User guide

!!! warning "Mostly not yet written"

    Homebase is pre-alpha. There is no installable release, so most of what a user guide
    would guide you through does not exist.

    These pages are written alongside the features they describe — not afterwards — so each
    one appears when its milestone lands.

## Written

| Page | Covers |
|---|---|
| [Applications](applications.md) | Installing, running and removing applications |
| [Storage](storage.md) | Adding a disk, giving it to an application, unplugging safely |

## Planned

| Page | Covers | Milestone |
|---|---|---|
| Installation | Creating the USB stick, erasing the target laptop, first boot | 6 |
| First steps | Administrator setup, naming the server, finding it on your network | 6 |
| Backup | Configuring backups, verifying them, restoring | 5 |
| Networking | Wi-Fi, discovery, optional private remote access | 7 |
| Updates | Channels, applying updates, rolling back | 8 |
| Troubleshooting | When something is wrong and you want it working again | 8 |

See the [roadmap](https://github.com/HusnuOkanCakir/homebase/blob/main/ROADMAP.md).

## How these will be written

The intended reader has never used a terminal and does not know what a container is. That
constrains the writing more than it might sound:

- **No jargon without explanation.** Not "mount the volume" but "make the disk available to
  Jellyfin".
- **Say what will happen before it happens**, particularly where something is irreversible.
  The installation page's job is to make sure nobody erases the wrong disk.
- **Screenshots of the actual dashboard**, updated when it changes.
- **A troubleshooting section per page**, covering what actually goes wrong rather than what
  theoretically could.

If a page cannot be followed by someone who does not know what a daemon is, it is not
finished — regardless of whether it is accurate.

## In the meantime

- [Architecture overview](../architecture/overview.md) — how it works
- [Getting started](../development/getting-started.md) — developing on it
- [Threat model](../security/threat-model.md) — what it protects and what it does not
