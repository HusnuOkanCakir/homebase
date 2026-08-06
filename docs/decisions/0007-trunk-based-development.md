# ADR-0007: Trunk-based development with squash merges

- **Status:** Accepted
- **Date:** 2026-08-06

## Context

Homebase starts as a solo project and hopes to attract contributors. It is public from the
first commit, which means the branching model is visible to anyone deciding whether to
contribute — and a process that looks heavier than the project warrants discourages people.

It also ships an appliance that installs itself by erasing a disk. `main` being broken is not
an inconvenience; if a broken commit reaches a release, it reaches machines holding data
their owners cannot replace.

## Decision

Trunk-based development:

- `main` is always releasable, and is the only long-lived branch
- Short-lived branches, one logical change each, merged by pull request
- **Squash merge only.** Merge commits and rebase merges are disabled
- The pull request title is a Conventional Commit and becomes the commit on `main`
- `release/0.x` branches are created only if a release ever needs maintaining separately
- Branch protection is not bypassable, including by administrators

## Alternatives considered

### Git Flow

`develop`, `release/*`, `feature/*`, `hotfix/*`, and `main` as a record of releases.

Rejected as solving a problem Homebase does not have. Git Flow exists for products with
parallel supported versions and scheduled releases. Homebase has one supported version — the
latest — and releases when a milestone is done.

The cost is concrete rather than theoretical: `develop` and `main` diverge, every change gets
merged twice, and there is a standing question of which branch a given fix belongs on. For a
solo project that is pure overhead; with contributors it is overhead plus a thing to explain.

### GitHub Flow with merge commits

Same branching model, preserving each branch's individual commits.

Rejected for history quality. During development a branch accumulates "fix typo", "actually
fix it", "address review" — commits that were useful in the moment and are noise afterwards.
Squashing means every commit on `main` is one complete, reviewed, tested change.

That matters most for the thing this project will need repeatedly: `git bisect`. Bisecting
across a history where individual commits are known-broken intermediate states is far worse
than bisecting across squashed changes, and "which change broke the installer?" is a question
we will be asking.

It also makes `git revert` reliable — one commit, one change, cleanly undoable.

### Long-lived release branches from the start

Rejected as premature. There has been no release, and pre-1.0 there are no backports —
[SECURITY.md](https://github.com/HusnuOkanCakir/homebase/blob/main/SECURITY.md) says so
explicitly. `release/0.x` gets created the first time a release actually needs a fix that
cannot wait for the next one.

## Consequences

### What this makes easier

- One branch to reason about; no question of where a change belongs
- Every commit on `main` is a complete reviewed change, so bisect and revert both work
- Changelog generation from Conventional Commit titles
- Small, frequent integration rather than large merges
- A short contributing guide, which lowers the barrier to a first pull request

### What this makes harder

- Individual development commits are lost at merge. Deliberate, but it means a branch's
  detailed history is not recoverable afterwards
- The pull request title matters more than any individual commit message, which contributors
  new to squash merging get wrong at first
- Large changes must genuinely be split into separately reviewable pieces, rather than merged
  as one branch with a readable internal history
- Without a staging branch, `main` is protected only by CI and review — which raises the
  stakes on CI actually being good

### The solo-project compromise

Branch protection requires a pull request and passing checks, but **zero approvals**, because
GitHub does not let an author approve their own pull request and a one-person project would
otherwise be unable to merge anything.

This is the weakest point in the process, and it is worth naming rather than glossing: right
now, "review" for most changes means the author reading their own diff in the pull request
view. That is not nothing — the pull request format catches things the editor does not — but
it is not review.

The required-approvals count moves to `1` the moment a second maintainer exists. The line to
change is marked in `scripts/setup-branch-protection.sh`.

### Security impact

Positive. Rules are not bypassable by administrators, so there is no path to `main` that
skips CI — including the secret scan and the workflow-security analysis. Linear history means
every change on `main` is attributable to one reviewed pull request.

The residual risk is the zero-approval rule above. Until there is a second maintainer, CI is
doing the work that review would otherwise do, which is why `ci/secrets` and `ci/workflows`
are required checks rather than advisory ones.

### What would make us revisit this

- A second maintainer joining — required approvals goes to 1 immediately
- Enough concurrent contributors that pull requests start invalidating each other, at which
  point a merge queue is the answer
- A release needing support while `main` has moved on, which creates `release/0.x` under the
  existing model rather than changing it
