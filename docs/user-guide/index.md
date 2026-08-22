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
| [Sharing files](sharing.md) | A folder on the server as a drive on your laptop, and the address to type |
| [Reaching it from anywhere](remote-access.md) | Wireguard, the one setting on your router, and adding a device |
| [Using Wi-Fi](wifi.md) | Joining a network, and why a cable is better for a server |

## Finding your way around

The dashboard has seven sections, and they are meant to be read as a sentence
about what you want:

| | |
|---|---|
| **Home** | What is wrong, your applications as things to press, and how the machine is |
| **Apps** | Installing and removing them |
| **Files** | Sharing folders onto your network |
| **Storage** | Disks, and backups onto them |
| **Network** | How the server is connected, Wi-Fi, and reaching it from outside |
| **Settings** | The server's name, updates, your recovery code, and switching it off |
| **Help** | When something is not working |

Pressing an application on **Home** opens the application itself. The small
**Details** link under it opens Homebase's page about it, which is where you stop
it, change its storage, or read its log.

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
