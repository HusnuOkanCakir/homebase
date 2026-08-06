# Branching and commits

Trunk-based development. `main` is always releasable and is the only long-lived branch. See
[ADR-0007](../decisions/0007-trunk-based-development.md) for why, including the alternatives
that were rejected.

## Branches

Branch from `main`, prefixed by intent, one logical change each, short-lived, deleted on
merge.

| Prefix | For | Example |
|---|---|---|
| `feat/` | New capability | `feat/app-installation` |
| `fix/` | Bug fix | `fix/backup-restore-permissions` |
| `docs/` | Documentation only | `docs/clarify-storage-layout` |
| `test/` | Tests only | `test/network-failure-paths` |
| `refactor/` | Behaviour-preserving | `refactor/job-runner` |
| `perf/` | Performance | `perf/reduce-poll-frequency` |
| `security/` | Hardening | `security/hostd-socket-permissions` |
| `chore/` | Tooling, plumbing | `chore/bump-dependencies` |
| `hotfix/` | Urgent, against a release branch | `hotfix/0.3.1-update-rollback` |

### One logical change

The rule that does the most work. Do not mix a refactor with the feature that motivated it —
land the refactor first, separately.

Not process for its own sake: a reviewer cannot spot a behaviour change hidden among a
thousand moved lines, and neither can the author. When something breaks a month later, "which
change did this?" needs to have an answer.

## Commits

[Conventional Commits](https://www.conventionalcommits.org/). Because merges are squashed,
**the pull request title is what lands on `main`** — that is the one that must be right.

```text
<type>(<scope>): <imperative summary>
```

**Types:** `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `security`, `chore`, `build`,
`ci`, `revert`

**Scopes:** `core`, `hostd`, `dashboard`, `installer`, `controller`, `storage`, `network`,
`apps`, `backup`, `api`, `schemas`, `packaging`, `docs`, `ci`, `repo`

```text
feat(core): add system inventory endpoint
fix(storage): preserve USB mount across reboot
security(hostd): restrict socket to the homebase group
docs(installer): describe the whole-disk erase warning
ci: pin actions to commit SHAs
```

Write the summary as an instruction — "add", not "adds" or "added". It completes the sentence
"if applied, this commit will…".

### Breaking changes

```text
feat(api)!: return job envelope from all mutating endpoints

BREAKING CHANGE: POST /system/reboot now returns 202 with a job id
instead of 204. Clients must poll GET /jobs/{id} for completion.
```

The footer must say what a consumer has to do, not merely that something changed.

### Bodies

The subject says what. The body says **why**, and what a reader would otherwise have to
reconstruct: what else was tried, what constraint forced this shape, what is deliberately not
handled.

A one-line commit is fine for an obvious change. Anything involving a judgement call deserves
the paragraph explaining it — the diff can be read from the code, the reasoning cannot.

### Sign-off

```sh
git commit -s
```

Required on every commit — the [DCO](https://developercertificate.org/) assertion that you
have the right to submit the work.

## Pull requests

1. Branch from an up-to-date `main`
2. Make the change; run `make check`
3. Push and open a pull request; title it as a Conventional Commit
4. Fill in the template — security and data-migration impact are prose, not checkboxes
5. Apply any `risk/` labels that fit
6. Green CI, resolved conversations, branch current with `main`
7. **Squash merge**

### Keeping current

Rebase rather than merge, so the branch stays linear:

```sh
git fetch origin
git rebase origin/main
git push --force-with-lease
```

`--force-with-lease` rather than `--force`: it refuses if someone else has pushed to your
branch in the meantime.

## Releases

Tagged from `main`:

```text
v0.1.0-alpha.1
v0.2.0-beta.1
v0.2.0
v0.2.1
```

`release/0.x` branches are created only when a release actually needs a fix that cannot wait
for the next one. Pre-1.0 there are no backports, so this has not happened yet.

Hotfixes branch from the release branch and are **immediately merged back into `main`** — a
fix that exists only on a release branch will be lost at the next release.

See [release process](../release/process.md).
