# ADR-0009: MkDocs Material and a Python-only Milestone 0 toolchain

- **Status:** Accepted
- **Date:** 2026-08-06

## Context

Milestone 0 ships no product code — it establishes contracts, documentation and CI. What it
needs is a documentation site, Markdown and YAML linting, JSON Schema validation, OpenAPI
validation and workflow-security analysis.

The obvious tools for several of these are JavaScript: Docusaurus, markdownlint, Spectral.
But the development machine this project starts on has **Node 12**, which none of them
support, and Node 20 is not needed by anything until the dashboard arrives at Milestone 2.

There is a choice here between upgrading the toolchain now to use the conventional tools, or
choosing tools that work with what is already present.

## Decision

Milestone 0 tooling is **Python-only**. Python 3.11 is already installed; nothing in this
milestone requires Node or Go.

| Need | Tool |
|---|---|
| Documentation site | `mkdocs` + `mkdocs-material` |
| Markdown linting | `pymarkdownlnt` |
| YAML linting | `yamllint` |
| JSON Schema validation | `check-jsonschema` |
| OpenAPI validation | `openapi-spec-validator` |
| Workflow security | `zizmor` |
| Hygiene and link checking | Local scripts in `scripts/` |

Everything installs from one pinned `requirements-dev.txt` into one virtualenv. `make
bootstrap` is the whole setup.

## Alternatives considered

### Docusaurus for the documentation site

React-based, so it would share a toolchain with the dashboard, and it is better for a
combined marketing-and-documentation site later.

Rejected for Milestone 0 because it would force a Node upgrade before a single documentation
page could be written, and because what this milestone actually needs — architecture
documents, ADRs, a threat model — is what MkDocs Material is best at. Navigation, search,
admonitions and Mermaid diagrams work out of the box with no build configuration.

The dashboard will bring Node 20 in Milestone 2. Revisiting then is cheap; the documents are
Markdown either way.

### markdownlint and Spectral, with a Node upgrade now

The more standard tools, and Spectral in particular is better than
`openapi-spec-validator` — it does style and consistency rules, not just schema validity.

Rejected because it front-loads a toolchain upgrade for no Milestone 0 benefit, and because
it would make the local development story worse: a contributor wanting to fix a typo would
need Node 20, a `package.json` and `node_modules` for a repository containing nothing but
Markdown. One `pip install` is a lower barrier.

Spectral becomes worth adding at Milestone 2, when Node is present anyway and the OpenAPI
document is large enough for style rules to matter.

### Docker-based tooling

Run each linter in a container, avoiding local installation entirely. Rejected as slower for
the edit-check loop, and as an odd requirement for editing Markdown. Docker is present here
but should not be a prerequisite for documentation work.

### Third-party GitHub Actions for each check

Let CI use `markdownlint-action`, `lychee-action` and similar without local installation.

Rejected on two grounds. It would break the rule that `make check` and CI run identical
tools — a lint failure that only reproduces in CI is a bad experience and an easy way for the
two to drift apart permanently. And each action is execution rights in CI granted to a third
party; repository hygiene is a poor reason to extend that trust.

## Consequences

### What this makes easier

- `make bootstrap && make check` works on a clean machine with only Python 3.11 and Git
- Local and CI runs are identical by construction — same tools, same pinned versions
- One `requirements-dev.txt` and one Dependabot ecosystem for all of it
- Contributing a documentation fix requires no JavaScript toolchain
- Fewer third-party actions in CI, and correspondingly less to trust

### What this makes harder

- `pymarkdownlnt` is less widely used than `markdownlint`, so its rule identifiers and
  configuration are less familiar and less well documented
- `openapi-spec-validator` checks validity, not style. Nothing currently catches an
  inconsistent OpenAPI document that is technically well-formed
- MkDocs Material's theme customisation is more limited than a React-based site's
- Two documentation toolchains may briefly coexist if Docusaurus is adopted later
- Python tooling on the appliance is still explicitly excluded — this decision covers
  development machines only ([ADR-0002](0002-implementation-languages.md))

### Security impact

Mildly positive. Fewer third-party GitHub Actions means a smaller CI supply chain, and pinned
Python dependencies are covered by Dependabot and `dependency-review` in the same way
everything else is.

`zizmor` deserves specific mention: it analyses the workflows themselves for unpinned
actions, injection-prone template expressions in `run:` blocks, and over-broad permissions. It
is the check that stops CI hardening from decaying as workflows are edited.

Hashes are not yet pinned in `requirements-dev.txt`. That is a gap, and belongs with the rest
of the supply-chain work in Milestone 8.

### What would make us revisit this

- Milestone 2 bringing Node 20 for the dashboard, at which point Spectral for OpenAPI style
  rules becomes nearly free
- The documentation site outgrowing MkDocs Material — most likely if a marketing site with
  interactive components is wanted alongside it
- `pymarkdownlnt` proving unreliable or unmaintained
- **The MkDocs 2.0 situation resolving, in either direction.** Material for MkDocs currently
  warns that MkDocs 2.0 removes the plugin system and rewrites theming with no migration
  path, and that it is presently unlicensed. We are pinned to `mkdocs==1.6.1` and unaffected
  today, but this is an upstream fork risk in a dependency the whole documentation site rests
  on. If the ecosystem splits, reassess rather than follow by default
