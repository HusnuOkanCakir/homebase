# Homebase

Turn an old laptop into a home server you can actually manage.

Homebase installs a complete server operating system onto a spare machine, then gets out of
the way behind a local web dashboard. You install applications, attach storage, and
configure backups without ever opening a terminal or learning Linux.

> **Status: pre-alpha. Milestones 0–3 complete.**
> There is no installable release yet, and nothing here should be pointed at data you care
> about. What works: setting up an administrator, reading live system information,
> restarting the machine, and installing, running and removing applications from a small
> catalogue — all from a browser. What does not exist yet: an installer, storage
> management, and backups. Nothing here backs anything up, so nothing here should hold the
> only copy of anything. See the [roadmap](ROADMAP.md).

## Try it

There is no installer yet, so the way to see Homebase working is to run it from source.
You need **Go 1.23+**, **Node 20+** and **Git**:

```sh
git clone https://github.com/HusnuOkanCakir/homebase.git
cd homebase
make run
```

Then open **<http://127.0.0.1:8080>**. It will ask you to create an administrator — any
name, and a password of twelve characters or more — and then show you live information
about the machine you are sitting at. `make run-fresh` starts over with an empty database.

Under **Applications** you can install something from the catalogue. That needs Docker on
your machine; without it the list still appears and says it cannot see the container
runtime, which is deliberate — "Homebase cannot look" and "there is nothing there" are
different answers and must not look the same.

That is a development instance, not an installation. Both services run as you rather than
as `root` and the `homebase` account, so the privilege boundary is not the real one.
Application data goes under `./run/` instead of `/srv/homebase`, and restarting the server
is refused on purpose — it would restart *your* machine.

To see the real thing, install the Debian packages on a throwaway virtual machine. This
needs QEMU with KVM and about 40 GB of free disk:

```sh
sudo apt install qemu-system-x86 qemu-utils cloud-image-utils ovmf
make vm-test-packages
```

That builds the packages, installs them on a clean Ubuntu machine, creates an
administrator, upgrades in place, reboots, and finally purges them — checking at every step
that the account and the user's files are still there.

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
