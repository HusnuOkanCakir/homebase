# Decision records

Architecture decision records exist so that a decision is made once, with its reasoning
attached, and can be revisited on the strength of new information rather than because
somebody forgot why it was made.

Each record states the context, the decision, what else was considered, and what it costs.
The consequences section is the one that earns its keep: every decision here has a real
downside, and writing it down is how we avoid pretending otherwise.

## Records

| # | Decision | Status |
|---|---|---|
| [0001](0001-monorepo.md) | Single repository for all components | Accepted |
| [0002](0002-implementation-languages.md) | Go for services, React and TypeScript for the dashboard | Accepted |
| [0003](0003-rest-openapi.md) | Versioned REST with OpenAPI as the single contract | Accepted |
| [0004](0004-sqlite.md) | SQLite for system state | Accepted |
| [0005](0005-container-runtime.md) | Docker Engine as the initial container runtime | Accepted |
| [0006](0006-privilege-split.md) | **Unprivileged core, minimal hostd, no generic execution** | Accepted |
| [0007](0007-trunk-based-development.md) | Trunk-based development with squash merges | Accepted |
| [0008](0008-apache-license.md) | Apache-2.0 licence | Accepted |
| [0009](0009-python-docs-toolchain.md) | MkDocs Material and a Python-only Milestone 0 toolchain | Accepted |
| [0010](0010-vm-lab-qemu-cloud-image.md) | Raw QEMU and cloud images for the VM lab | Accepted |

**[ADR-0006](0006-privilege-split.md) is the one to read.** The others describe how Homebase
is built; that one describes a boundary the project is not permitted to cross, and it is the
reason a local AI administrator can be added later without the result being an LLM with root.

## Writing one

Copy [`template.md`](template.md). A decision needs a record when it is expensive to reverse,
when it constrains future work, when it was contested, or when a reasonable person would
otherwise ask "why on earth is it done this way?".

Routine choices do not need one. A record for every library import devalues the ones that
matter.

## Status

| Status | Meaning |
|---|---|
| **Proposed** | Under discussion, not yet binding |
| **Accepted** | In force |
| **Superseded** | Replaced; links to its replacement |
| **Deprecated** | No longer applies, with nothing replacing it |

Accepted records are not edited to reflect a change of mind. A new record supersedes the old
one, and the old one stays — the history of what was believed, and when, is the point.
