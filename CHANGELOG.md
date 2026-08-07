# Changelog

All notable changes to Homebase are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versioning
follows [Semantic Versioning](https://semver.org/) — with one pre-1.0 caveat spelled out
below.

## Pre-1.0 versioning

Until 1.0.0, **minor releases may contain breaking changes**. Patch releases contain only
compatible fixes.

This is the usual pre-1.0 convention, but it is worth being explicit about what it does
*not* excuse: every release, breaking or not, must migrate existing installations without
destroying data. A user who installed 0.3.0 must be able to reach 0.4.0 with their photos,
their applications and their configuration intact. Breaking an API is acceptable before 1.0;
breaking somebody's server is not.

Each release records the minimum version it can upgrade from. See
[docs/release/versioning.md](docs/release/versioning.md).

## [Unreleased]

Milestone 0 — contracts and project machinery. No product code.

### Added

- Repository skeleton, Apache-2.0 licence, editor and line-ending conventions
- Contributing guide, security policy, code of conduct, roadmap
- Pull request and issue templates, CODEOWNERS
- Hardened CI: hygiene, docs, contracts, workflow-security and secret-scanning jobs,
  with every action pinned to a commit SHA and no secrets exposed to fork pull requests
- Python-only developer tooling — `make bootstrap` needs only Python 3.11 and Git
- Architecture documentation: components, services, jobs, data layout, API conventions
- Decision records 0001–0009, including **ADR-0006**, which fixes the privilege boundary:
  unprivileged `core`, minimal `hostd`, and no generic execution path in any form
- Threat model, privilege boundaries, update security, CI security, disclosure policy
- `api/openapi.yaml` — the v1 API contract, written ahead of its implementation
- `schemas/app-manifest.schema.json` and `schemas/error.schema.json`, with valid *and*
  invalid fixtures; each invalid fixture asserts rejection by a named constraint
- Repository automation for labels, branch protection and environment checks
- `.gitleaks.toml`, allowlisting individual illustrative strings in documentation and
  fixtures rather than exempting paths — exempting `docs/` would let a real credential
  pasted into an example go unnoticed

### Changed

- Secret scanning runs the gitleaks **binary**, pinned by version and verified by
  checksum, rather than `gitleaks/gitleaks-action`. The action is closed-source,
  commercially licensed, and contacts a third-party service for licence validation on
  every run — a poor trade for checking our own hygiene
- Labels are applied through `gh api` rather than `gh label`, which does not exist before
  gh 2.6. The scripts now work on whatever version a contributor happens to have
- The setup script also enables GitHub Pages, the dependency graph, Dependabot alerts,
  and requires workflow approval for **all** external contributors rather than only
  first-time ones. All four were previously documented as manual steps that GitHub has
  no API for, which was wrong

### Removed

- Documentation site deployment, until Milestone 6. `mkdocs build --strict` still runs on
  every pull request; publishing waits for an audience that will not clone the repository.
  A permanently failing workflow is how people learn to ignore failing workflows
- The `gomod` and `npm` Dependabot ecosystems, until the first `go.mod` and `package.json`
  exist. Dependabot does not ignore an ecosystem whose manifest is missing — it fails

[Unreleased]: https://github.com/HusnuOkanCakir/homebase/commits/main
