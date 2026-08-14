# User guide

!!! warning "Mostly not yet written"

    Homebase is pre-alpha. There is no installable release, so most of what a user guide
    would guide you through does not exist.

    These pages are written alongside the features they describe — not afterwards — so each
    one appears when its milestone lands.

## Written

| Page | Covers |
|---|---|
| [Installing Homebase](installing.md) | Making the USB stick, erasing the old laptop, finding the server afterwards |
| [First steps](first-steps.md) | Naming the server, and what is worth doing on a new one |
| [Applications](applications.md) | Installing, running and removing applications |
| [Storage](storage.md) | Adding a disk, giving it to an application, unplugging safely |
| [Backup and restore](backup.md) | Making a backup, checking it, and getting your files back |
| [Finding your server](network.md) | The address to open, the warning your browser shows once, and what to check when nothing loads |
| [If you forget your password](passwords.md) | Your recovery code, and how to get back into your own server |

## Planned

| Page | Covers | Milestone |
|---|---|---|
| Updates | Channels, applying updates, rolling back | 8 |
| Troubleshooting | When something is wrong and you want it working again | 8 |
| Wi-Fi | Setting up a server with no network cable | 9 |

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
