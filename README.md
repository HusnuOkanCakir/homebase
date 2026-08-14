# Homebase

Turn an old laptop into a home server, from one command and a USB stick.

Homebase installs a complete server operating system onto a spare machine, unattended, and
gives you back something you administer from a terminal — or from a local web dashboard if
you would rather. Applications, storage, backups, signed updates with automatic rollback,
and a way back in when it breaks.

**Who it is for:** somebody comfortable with Linux who wants the tedious and dangerous parts
done properly and once, rather than as a pile of shell scripts nobody has tested on a machine
that already holds their photographs.

> **Status: pre-alpha. Milestones 0–8 complete, 9 in progress.**
> There is no release yet, and nothing here should be pointed at data you care about.
>
> **What works.** `homebasectl installer create` writes a USB stick that installs Ubuntu and
> Homebase onto a laptop unattended — including over a disk with Windows on it. The server
> is then reachable at `https://its-name.local` from anywhere in the house, encrypted, with
> no port number and no IP address to remember, and it can tell the difference between
> having no network and having no internet. It joins a Wi-Fi network and refuses a wrong
> password without changing anything. It boots with Secure Boot enforcing, as laptops ship.
>
> Applications install from a small catalogue and keep their data on a disk you choose.
> Backups run every night onto a second disk, restore onto a *different* machine, and can be
> read without Homebase installed. Updates are signed, check themselves daily, health-check
> after applying and put the previous version back if it fails — and survive having the
> power cut mid-`dpkg`. When something breaks it can produce a diagnostic file safe to send
> to a stranger, repair itself, or start again without deleting anybody's photographs.
>
> **What does not exist yet.** Reaching the server from *outside* the house, and sharing
> files onto the network — the two halves a home server is most used for. And `homebasectl`
> covers only a fraction of what the dashboard can do, which for this audience is the wrong
> way round. Those are Milestones 10, 11 and 12.
>
> See the [roadmap](ROADMAP.md).

## Building the server, start to finish

Eight commands, from a blank USB stick to a server running your media library, backing
itself up every night and keeping itself patched.

### 1. Write the stick

On any Linux machine, with the USB drive plugged in:

```sh
homebasectl installer devices          # what it is allowed to write to, and why
sudo homebasectl installer create --device /dev/sdX
```

`devices` refuses anything it should not touch — the disk this computer is running from,
anything mounted, anything not removable — and says which rule stopped it. Ubuntu's ISO is
downloaded and checked against its published signature; it is never repacked
([ADR-0016](docs/decisions/0016-installation-media.md)).

### 2. Install

Plug the stick into the old laptop and boot from it. Ubuntu asks once whether to continue —
that prompt is Ubuntu's own, and it does not say which disk it is about to erase, so be sure
which machine you are at. Everything after it is unattended: partitioning, Ubuntu, Homebase,
the firewall, the service accounts, the lid-close behaviour.

It reboots into a screen showing the server's name and address. That takes about fifteen
minutes and needs nothing typed.

### 3. Find it

```sh
ssh you@homebase.local
```

The name comes from mDNS, so there is no IP address to hunt for and no DHCP reservation to
make. If your network eats mDNS, the screen on the laptop shows the address instead.

### 4. Create the administrator

```sh
sudo homebasectl setup okan
```

It asks for a password and prints a **recovery code**. Write that down somewhere other than
the server. It is shown once, it is stored the way a password is, and it is the way back in
if the password is ever forgotten — it also travels with your backups, so it still works on
a machine rebuilt from one.

### 5. Attach a disk

Plug in the drive your files will live on:

```sh
sudo homebasectl storage disks         # what is plugged in
```

Formatting and attaching still needs the dashboard at `https://homebase.local` — those
commands are the part of Milestone 10 not yet written, because "which disk do I erase" needs
a confirmation designed for a shell rather than copied from a form.

### 6. Install what you want to run

```sh
sudo homebasectl apps                  # the catalogue
sudo homebasectl apps install jellyfin
sudo homebasectl apps logs jellyfin
```

Jellyfin's data goes on the disk you attached, not the system disk. The container runs with
every capability dropped, no new privileges, and its port bound to localhost.

### 7. Turn on backups

```sh
sudo homebasectl backup schedule daily backups
```

Every night at about three, onto the disk called `backups`. If the machine is asleep at the
time — a laptop in a cupboard usually is — it backs up when it next wakes. Run it with no
arguments to see the schedule, whether systemd is actually running it, and how the last one
went.

```sh
sudo homebasectl backup now backups    # and one right now
```

A backup is plain files with a JSON manifest, readable without Homebase, and it restores
onto a *different* machine ([ADR-0014](docs/decisions/0014-backups-are-readable-without-homebase.md)).

### 8. Keep it patched

```sh
sudo homebasectl update check
sudo homebasectl update apply
```

It already checks daily on its own. Applying downloads and verifies everything first,
snapshots the database, health-checks afterwards, and puts the previous version back if the
check fails. Cutting the power mid-update leaves a machine that boots, says it was
interrupted, and is put right by `sudo homebasectl repair`.

### When something goes wrong

```sh
sudo homebasectl repair                # fix what a power cut left broken
sudo homebasectl diagnostics           # a file safe to send to somebody
```

`diagnostics` prints what the file does *not* contain — no passwords, no recovery code,
none of your files — because the question "is this safe to send?" is asked at the moment of
sending.

Full reference: [From a terminal](docs/user-guide/terminal.md). Everything above is also on
the dashboard at `https://homebase.local`, if you would rather click.

## Try it without a laptop

Making the stick is one command on a Linux machine. See
[Installing Homebase](docs/user-guide/installing.md) for the real thing.

Short of finding a spare laptop, there are two ways to see Homebase working, and they answer
different questions.

### On a throwaway virtual machine — the closest thing to the real product

This installs Homebase from its own Debian packages onto a clean Ubuntu machine, plugs two
blank 2 GB disks into it, and leaves it running for you to use. Needs QEMU with KVM and
about 40 GB of free disk:

```sh
sudo apt install qemu-system-x86 qemu-utils cloud-image-utils ovmf
git clone https://github.com/HusnuOkanCakir/homebase.git
cd homebase
make vm-run
```

It prints a URL when it is ready — open it, create an administrator, and write down the
recovery code it gives you. From there you can install an application and use it, prepare
one blank disk and give it to File Browser, back the whole server up onto the other, and
restart the machine and watch the page notice.

Two disks rather than one because Homebase refuses to put a backup on a disk an application
keeps its files on — a copy on the same disk protects against deleting a file by accident
and against nothing else.

This is the real thing: `hostd` runs as root, `core` runs as the unprivileged `homebase`
account, the socket between them is `root:homebase 0660`, and restarting the server restarts
the VM rather than your laptop.

```sh
make vm-run-destroy    # when you are finished
```

### On your own machine — quicker, for developing

Runs both services as you, against a scratch directory under `./run/`. You need **Go 1.23+**,
**Node 20+** and **Git**:

```sh
make run
```

Then open **<http://127.0.0.1:8080>**. It will ask you to create an administrator — any
name, and a password of twelve characters or more — then show you a recovery code to write
down, and then show you live information about the machine you are sitting at.
`make run-fresh` starts over with an empty database.

The recovery code is the only way back into a server whose password has been forgotten:
there is no email to send a link to and no account with anybody. It is shown once and kept
as an argon2id hash. `sudo homebasectl recovery-code` issues a fresh one from the machine
itself, for when the paper is gone too — see
[ADR-0015](docs/decisions/0015-password-recovery.md).

Under **Applications** you can install something from the catalogue. That needs Docker on
your machine; without it the list still appears and says it cannot see the container
runtime, which is deliberate — "Homebase cannot look" and "there is nothing there" are
different answers and must not look the same.

That is a development instance, not an installation. Both services run as you rather than as
`root` and the `homebase` account, so the privilege boundary is not the real one. Application
data goes under `./run/` instead of `/srv/homebase`, restarting the server is refused on
purpose — it would restart *your* machine — and storage is largely untestable, because
managing disks needs root.

### Everything else

```sh
./scripts/bootstrap-dev.sh   # what this machine has, and what it is missing
make help                    # every target
make check                   # docs, contracts, workflow security
make go-test dash-lint       # the code
make hostd-describe          # every privileged operation this build can perform
```

The tests worth knowing about run against real virtual machines, because that is the only
place a reboot is a reboot:

| | |
|---|---|
| `make vm-test` | The harness itself: create, install a service, reboot, verify, destroy |
| `make vm-test-hostd` | `hostd` under real systemd — socket permissions, sandbox, audit log |
| `make vm-test-core` | The API slice: setup, sign in, read the machine, restart it |
| `make vm-test-dashboard` | The whole journey in a browser, including a real reboot |
| `make vm-test-apps` | Install an application, use it, reboot, remove it — the data must survive |
| `make vm-test-storage` | Add a real USB disk, reboot, pull it out, plug it back in — nothing may corrupt |
| `make vm-test-backup` | Back up one machine, destroy it, restore onto a different one |
| `make vm-test-packages` | Install, upgrade, reboot and purge the `.deb`s |

## What it is

- **Local-first.** Everything works on your own network. No account, no cloud dependency,
  no telemetry.
- **Whole-disk install.** Boot a USB stick, pick a disk, wait. The result is a server, not a
  desktop with extras.
- **Applications, not containers.** Curated, tested app manifests — you choose "Jellyfin",
  not an image tag and a volume mount. The trade is deliberate and it is a real one: you
  can install what Homebase ships and nothing else
  ([ADR-0012](docs/decisions/0012-hostd-owns-the-catalogue.md)).
- **Recoverable by design.** Every meaningful change is a job that can be previewed,
  verified and rolled back. Backups and restore are core features, not add-ons.

## What it is not

- A public-internet hosting platform. Homebase targets your home network, with optional
  private remote access. It does not help you expose services to the world.
- A NAS with RAID and storage pools. One internal disk plus optional USB storage, initially.
- A general-purpose container host. If you want arbitrary `docker run`, use Docker.

## Architecture in one paragraph

Three components, split along a privilege boundary. **`core`** runs unprivileged and owns
the API, authentication, jobs, application metadata and audit history. **`hostd`** is a
small root service that accepts only a fixed set of typed operations over a Unix socket —
it has no generic shell endpoint, and that is a permanent constraint, not a current
limitation. The **dashboard** is a browser application that talks only to the `core` API.
Every privileged action therefore travels the same audited, reversible path.

That boundary exists because of what comes later: a **local AI operator** (Stage 2) that
administers the server through exactly the same API the dashboard uses, with a policy engine
between the model and anything that can change the system. Read
[ADR-0006](docs/decisions/0006-privilege-split.md) before proposing anything that crosses it.

## Documentation

| | |
|---|---|
| [Architecture overview](docs/architecture/overview.md) | How the pieces fit together |
| [Decision records](docs/decisions/) | Why things are the way they are |
| [Threat model](docs/security/threat-model.md) | What it defends against, and what it does not |
| [Getting started](docs/development/getting-started.md) | Set up a development environment |
| [Contributing](CONTRIBUTING.md) | Branching, commits, review |
| [Roadmap](ROADMAP.md) | Milestones and current position |

## Development

Milestone 0 needs only Python 3.11+ and Git — no Go, no Node, no VM:

```sh
make bootstrap   # create .venv and install docs/lint/validation tooling
make check       # run exactly what CI runs
make docs        # serve the documentation site on :8000
```

Go, Node 20+ and QEMU/KVM become prerequisites in Milestones 1–2. See
[getting started](docs/development/getting-started.md).

## Security

Please report vulnerabilities privately — see [SECURITY.md](SECURITY.md). Do not open a
public issue for a security problem.

## Licence

[Apache License 2.0](LICENSE). See [NOTICE](NOTICE) for attribution requirements.
