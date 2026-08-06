# Homebase

Turn an old laptop into a home server you can actually manage.

Homebase installs a complete server operating system onto a spare machine, then gets out of
the way behind a local web dashboard. You install applications, attach storage, and
configure backups without ever opening a terminal or learning Linux.

> **Status: pre-alpha, Milestone 0.**
> There is no installable release yet, and nothing here should be pointed at data you care
> about. This repository currently contains the architecture, contracts and project
> machinery that the implementation will be built against. See the [roadmap](ROADMAP.md).

## What it is

- **Local-first.** Everything works on your own network. No account, no cloud dependency,
  no telemetry.
- **Whole-disk install.** Boot a USB stick, pick a disk, wait. The result is a server, not a
  desktop with extras.
- **Applications, not containers.** Curated, tested app manifests — you choose "Jellyfin",
  not an image tag and a volume mount.
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
