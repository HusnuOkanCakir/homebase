# Getting started

## What you need right now

Milestone 0 contains no product code. To work on it you need:

- **Python 3.11+**
- **Git**

That is the whole list. Go, Node and QEMU arrive later, and this page says exactly when so
you are not installing things you cannot yet use.

```sh
git clone https://github.com/HusnuOkanCakir/homebase.git
cd homebase
make bootstrap
make check
```

`make bootstrap` creates `.venv` and installs the pinned tooling from `requirements-dev.txt`.
`make check` runs exactly what CI runs.

## What you will need later

| Milestone | Adds | Why |
|---|---|---|
| 1 — VM lab | QEMU/KVM, libvirt, **~40 GB free disk** | Booting real Ubuntu VMs for tests |
| 2 — Core slice | Go 1.23+, Node 20+ | `core`, `hostd`, the dashboard |
| 3 — Applications | Docker Engine | Container lifecycle |

Two warnings worth having in advance, because both have bitten this project already:

**Disk space.** The VM lab needs roughly 40 GB — an Ubuntu ISO, a base image, and a qcow2
overlay per test. The machine this project started on had 1.6 GB free, which is why
Milestone 1 is sequenced after Milestone 0 rather than before it.

**Node version.** The dashboard needs Node 20+. Node 12 is still the default on some
long-lived Ubuntu installations and will fail in confusing ways. Check with `node --version`
before Milestone 2, not during it.

## Everyday commands

```sh
make help          # list targets
make check        # everything CI runs
make docs          # serve the documentation site on :8000
make lint          # markdown, YAML, workflow security
make validate      # links, OpenAPI, JSON schemas
make clean
```

Run `make check` before pushing. It is the same tooling at the same pinned versions as CI, so
a local pass should mean a CI pass — if the two ever disagree, that is a bug worth reporting
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
