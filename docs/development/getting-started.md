# Getting started

## What you need right now

It depends what you are working on, and this page says which so you are not installing
things you cannot yet use.

**Documentation, contracts, CI** — Python 3.11+ and Git. That is the whole list.

**The services and the dashboard** — additionally Go 1.23+, Node 20+, and QEMU with KVM to
run the tests. The tests are not optional here: `hostd` runs as root and `core` holds the
only copy of somebody's configuration, so both are verified in a real VM rather than only
in unit tests.

**Applications** — additionally Docker. Any version whose Engine API is 1.41 or newer;
`hostd` negotiates against whatever the machine has rather than pinning, so there is nothing
to match. `make run` will list the catalogue without Docker and say plainly that it cannot
see the container runtime, but it cannot install anything.

`./scripts/bootstrap-dev.sh` reports what this machine has and what it is missing, per
milestone. It installs nothing.

```sh
git clone https://github.com/HusnuOkanCakir/homebase.git
cd homebase
make bootstrap
make check
```

`make bootstrap` creates `.venv` and installs the pinned tooling from `requirements-dev.txt`.
`make check` runs exactly what CI runs.

## What each milestone adds

| Milestone | Adds | Why |
|---|---|---|
| 1 — VM lab ✅ | QEMU/KVM, OVMF, cloud-image-utils, **~40 GB free disk** | Booting real Ubuntu VMs for tests |
| 2 — Core slice ✅ | Go 1.23+, Node 20+ | `core`, `hostd`, the dashboard |
| 3 — Applications ✅ | Docker (Engine API 1.41+) | Container lifecycle |
| 4 — Storage ✅ | A spare USB disk, or a second qcow2 attached to a VM | Nothing about disks can be tested without one |
| 5 — Backup ✅ | `sqlite3`, and a second disk to back up *to* | Restore is the half that has to work |

Three things worth checking in advance rather than mid-task, because each has cost this
project time already:

**Disk space.** The VM lab needs roughly 40 GB — an Ubuntu ISO, a base image, and a qcow2
overlay per test. Run `make vm-destroy` after each run: the overlays are what creep, and
the base image should be the only long-lived artifact.

**Node version.** The dashboard needs Node 20+. Node 12 is still the default on some
long-lived Ubuntu installations, and upgrading it on one collides with the distribution's
own `libnode-dev`, which has to be removed first. Check with `node --version` before you
start, not once a build is failing.

**GitHub CLI version.** `gh` is optional, but the repository scripts use `gh api` rather
than newer subcommands so they work on old versions — Ubuntu 22.04 ships gh 2.4, which
predates `gh label` and `gh run list --branch`. If you extend those scripts, stay on
`gh api` for the same reason.

## Everyday commands

```sh
make help                    # list targets
make vm-run                  # Homebase on a throwaway VM, as a user would have it
make vm-run-destroy          # …and get rid of it
make run                     # run Homebase on this machine, in a browser
make run-fresh               # the same, discarding the existing account and state

make check                   # everything CI runs for docs and contracts
make go-lint go-test         # Go: gofmt, vet, race tests
make dash-lint               # dashboard: typecheck and lint
make docs                    # serve the documentation site on :8000

make packages                # build the .debs
make hostd-describe          # every privileged operation this build can perform
make hostd-check-operations  # …and that the destructive ones still ask permission

make vm-create               # a disposable VM
make vm-test                 # the harness itself
make vm-test-hostd           # hostd under real systemd
make vm-test-core            # the API vertical slice
make vm-test-dashboard       # the user journey, in a browser
make vm-test-apps            # install an app, reboot, remove it; data must survive
make vm-test-storage         # a real disk, unplugged mid-use; nothing may corrupt
make vm-test-backup          # back up one machine, restore onto a different one
make vm-test-packages        # install, upgrade, reboot and purge the .debs
make vm-destroy
```

### Two ways to run it, answering different questions

**`make vm-run`** installs the Debian packages onto a clean Ubuntu VM, plugs two blank 2 GB
disks into it, and leaves it running with a URL to open. It takes about five minutes the
first time. This is the one to use when the question is "does this work" — the privilege
boundary is real, `hostd` is root and `core` is not, and restarting the server restarts the
VM.

Two disks because Homebase refuses to back up onto a disk an application keeps files on, so
one spare disk gets you Storage or Backup but not both.

It is also the only way to exercise storage, which needs root to mount anything, and the only
way to try a restart without restarting your own machine.

**`make run`** starts both services against a scratch directory under `./run/`, in about two
seconds. This is the one to use while writing code. It is a development instance rather than
an installation: both run as you, application data goes under `./run/` instead of
`/srv/homebase`, restarting is refused on purpose, and storage mostly does not work.

A rule that has already caught things twice: **anything about permissions, mounting, systemd
or reboots is not tested until it has run in a VM.** Both bugs that made `hostd` unable to
start at all — a missing directory and a crash-loop with no limit — passed every test on a
developer machine.

The `vm-test-*` targets each create a VM, exercise it, and destroy it — including on
failure, after collecting diagnostics. A failing test that leaves a 20 GB disk image behind
is a test people stop running.

Run `make check` before pushing. It runs everything the pull-request checks run, Go
formatting and `go vet` included, at the same pinned versions as CI — so a local pass should
mean a CI pass. If the two ever disagree, that is a bug worth reporting
rather than working around ([ADR-0009](../decisions/0009-python-docs-toolchain.md)).

## Making a change

```sh
git checkout -b docs/clarify-storage-layout
# edit
make check
git commit -s        # -s is required: DCO sign-off
git push -u origin docs/clarify-storage-layout
```

Then open a pull request. The template asks for security impact and data-migration impact as
prose rather than checkboxes — "None" is a perfectly good answer when it is true, and a
required one to think about when it is not.

See [contributing](https://github.com/HusnuOkanCakir/homebase/blob/main/CONTRIBUTING.md).

## Before you write code

Two documents will save you a rejected pull request:

- [Architecture overview](../architecture/overview.md) — how the pieces fit
- [ADR-0006](../decisions/0006-privilege-split.md) — the privilege boundary, and the most
  common reason an otherwise good change is turned down

The short version: `core` is unprivileged, `hostd` accepts only named typed operations, and
nothing anywhere accepts a command to run. If a change seems to need `core` to have more
privilege, it needs a new `hostd` operation instead.

## Documentation

The site is [MkDocs Material](https://squidfunk.github.io/mkdocs-material/); pages are
Markdown under `docs/`.

```sh
make docs          # live reload on :8000
```

Adding a page means adding it to the `nav` in `mkdocs.yml` — `mkdocs build --strict` fails on
an orphaned page, which is deliberate. Mermaid diagrams work in fenced ```mermaid blocks.

**The site is not published yet.** Deployment to GitHub Pages is disabled until Milestone 6,
when the first installable release gives it an audience that will not clone the repository.
Until then the documentation is read here, and `make docs` renders it locally.

That does not make the strict build optional. Validating the site and publishing it are
separate concerns, and the strict build is what catches an orphaned page or a broken
reference long before anyone would notice it on a site. Write pages as though they were
published, because eventually they will be.

Documentation ships in the **same pull request** as the change it describes. Not a follow-up;
follow-ups do not happen.

## Troubleshooting

**`make bootstrap` fails building a wheel** — you likely need `python3-venv` and
`python3-dev`. On Ubuntu: `sudo apt install python3-venv python3-dev`.

**`make check` fails on MD013 (line length)** — prose wraps at 100; the limit is 130 to
accommodate tables that cannot be wrapped. Wrap the paragraph.

**`make check` reports a broken link** — internal Markdown links are checked, including
heading anchors. Relative paths are resolved from the file containing them, so a link from
`docs/security/` to an ADR needs `../decisions/`.

**zizmor fails after editing a workflow** — most often an action pinned to a tag rather than
a commit SHA. See [CI security](../security/ci-security.md).
