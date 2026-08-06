# Contributing to Homebase

Thank you for wanting to help. This document covers how work gets from an idea into `main`.

Homebase runs on machines holding the only copy of somebody's photographs, and it installs
itself by erasing a disk. That shapes the process below more than any style preference does:
the checks exist because of what a mistake costs here, not because of ceremony.

## Before you start

- **Open an issue first** for anything larger than a typo. It is cheaper to disagree about
  an approach in an issue than in a finished pull request.
- **Security problems do not go in issues.** See [SECURITY.md](SECURITY.md).
- Read [docs/architecture/overview.md](docs/architecture/overview.md) and
  [ADR-0006](docs/decisions/0006-privilege-split.md). ADR-0006 defines a boundary that
  changes are not permitted to cross, and it is the most common reason an otherwise good
  pull request is rejected.

## Development setup

Milestone 0 needs only Python 3.11+ and Git:

```sh
make bootstrap   # create .venv and install tooling
make check       # run exactly what CI runs
```

`make check` and CI run the same tools at the same pinned versions. If they disagree, that
is a bug — please report it. See
[docs/development/getting-started.md](docs/development/getting-started.md).

## Branching

Trunk-based. `main` is always releasable; there is no `develop` branch.

Branch from `main`, prefix by intent:

| Prefix | For |
|---|---|
| `feat/` | New capability |
| `fix/` | Bug fix |
| `docs/` | Documentation only |
| `test/` | Tests only |
| `refactor/` | Behaviour-preserving change |
| `perf/` | Performance |
| `security/` | Hardening, boundary changes |
| `chore/` | Tooling, dependencies, repo plumbing |
| `hotfix/` | Urgent fix against a release branch |

Examples: `feat/app-installation`, `fix/backup-restore-permissions`,
`security/hostd-socket-permissions`.

Rules:

- **One logical change per branch.** Do not mix a refactor with the feature that motivated
  it — land the refactor first, separately. Reviewers cannot spot a behaviour change hidden
  in a thousand moved lines, and neither can you.
- Keep branches short-lived. Days, not weeks.
- Branches are deleted automatically on merge.

## Commits

[Conventional Commits](https://www.conventionalcommits.org/). The **pull request title** is
what lands on `main`, because we squash-merge — so the PR title is the one that must be
right.

```text
<type>(<scope>): <imperative summary>
```

Types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `security`, `chore`, `build`, `ci`,
`revert`. Scopes: `core`, `hostd`, `dashboard`, `installer`, `controller`, `storage`,
`network`, `apps`, `backup`, `api`, `schemas`, `packaging`, `docs`, `ci`, `repo`.

```text
feat(core): add system inventory endpoint
fix(storage): preserve USB mount across reboot
security(hostd): restrict socket to the homebase group
docs(installer): describe the whole-disk erase warning
```

Breaking changes take a `!` and a `BREAKING CHANGE:` footer explaining the migration:

```text
feat(api)!: return job envelope from all mutating endpoints

BREAKING CHANGE: POST /system/reboot now returns 202 with a job id
instead of 204. Clients must poll GET /jobs/{id} for completion.
```

### Sign your commits off

Every commit needs a [Developer Certificate of Origin](https://developercertificate.org/)
sign-off, asserting you have the right to submit the work:

```sh
git commit -s
```

## Pull requests

Fill in the template honestly — particularly these two, which are prose, not checkboxes:

- **Security impact.** "None" is a perfectly good answer when it is true. It is the wrong
  answer when the change touches `hostd`, packaging, authentication, the update path, or
  anything that parses input from outside the machine.
- **Data and migration impact.** Anything touching storage, backups, the database schema or
  package upgrade scripts must say what happens to a user who already has data.

Then:

- CI must be green. All checks are required; none can be bypassed.
- Conversations must be resolved before merge.
- Your branch must be current with `main`.
- **Documentation ships in the same pull request as the change it describes.** Not a
  follow-up. Follow-ups do not happen.

### What reviewers will push back on

- A privileged operation in `hostd` that is more general than the task requires. Every
  operation should do one specific thing; "run this command" is not an operation.
- A change that makes a failure silent. Failing loudly with a comprehensible message beats
  succeeding ambiguously.
- An operation that cannot be undone, without a stated reason why not.
- Tests that only cover the success path, on a change touching storage, installation,
  updates or the privilege boundary.
- New user-facing strings that assume the reader knows what a container, a mount point or a
  daemon is.

## Testing

A feature is complete when it works, fails comprehensibly, survives a reboot, and can be
rolled back. See [tests/README.md](tests/README.md) and
[docs/development/testing.md](docs/development/testing.md).

Required before merge, where applicable to the change:

- [ ] Unit tests for logic, parsing and validation
- [ ] Integration tests where components interact
- [ ] Failure paths, not only success paths
- [ ] Reboot behaviour, if the change touches state on disk
- [ ] Existing user data survives an upgrade

## Labels that change the review

Three labels mean a pull request needs more than an ordinary read:

| Label | Applies when | Consequence |
|---|---|---|
| `risk/destructive` | Can erase or overwrite user data | Storage maintainer review; exercised in a VM against real data |
| `risk/migration` | Schema, on-disk format or upgrade path changes | Must pass the upgrade matrix from the previous release |
| `risk/security` | Privilege boundary, auth, update path, sockets | Security review; threat model updated if a boundary moved |

Apply them yourself if they fit. Maintainers will add them if you miss one.

## Code of conduct

By participating you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).

## Licence

Contributions are licensed under [Apache-2.0](LICENSE). Your DCO sign-off is how you confirm
you are entitled to submit them.
