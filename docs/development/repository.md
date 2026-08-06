# Repository setup

How the GitHub repository is configured. `scripts/setup-branch-protection.sh` and
`scripts/setup-labels.sh` apply this; the checklists below are the source of truth, so the
configuration can be verified or restored by hand when the scripts are unavailable.

```sh
gh auth login
./scripts/setup-labels.sh
./scripts/setup-branch-protection.sh
```

Both are idempotent.

## Repository settings

**Settings → General**

- [ ] Default branch: `main`
- [ ] **Squash merging only** — merge commits and rebase merging disabled
- [ ] Squash merge commit title: **pull request title**
- [ ] Automatically delete head branches on merge
- [ ] Always suggest updating pull request branches
- [ ] Issues: on · Discussions: on · Wiki: off · Projects: off

Squash-only is what keeps `main` at one-commit-per-change, which is what makes `git bisect`
and `git revert` behave ([ADR-0007](../decisions/0007-trunk-based-development.md)).

**Settings → Code security**

- [ ] Private vulnerability reporting: **on**
- [ ] Dependency graph: on
- [ ] Dependabot alerts and security updates: on
- [ ] Secret scanning: on
- [ ] **Push protection: on**

Push protection blocks a recognised secret before it reaches the repository. On a public
repository a pushed secret is scraped within minutes, so blocking the push is worth
considerably more than detecting it afterwards.

**Settings → Actions → General**

- [ ] Allow all actions (all are SHA-pinned, so mutability is not the exposure)
- [ ] Workflow permissions: **read repository contents**
- [ ] Do not allow GitHub Actions to create or approve pull requests
- [ ] Require approval for **all outside collaborators**

**Settings → Pages**

- [ ] Source: **GitHub Actions**

Enabled by the setup script, but the **Docs workflow that publishes to it is disabled
until Milestone 6** — see `.github/workflows/docs.yml`. The documentation is readable in
the repository until there is an audience that will not clone it.

`mkdocs build --strict` still runs on every pull request regardless. Validating the site
and publishing it are separate concerns.

## The `main` ruleset

**Settings → Rules → Rulesets → New branch ruleset**

- Name: `main protection` · Enforcement: **Active** · Target: `main`
- [ ] **Bypass list: empty** — including administrators

Rules:

- [ ] Restrict deletions
- [ ] Block force pushes
- [ ] Require linear history
- [ ] Require a pull request before merging
    - Required approvals: **0** — see below
    - [ ] Dismiss stale approvals on new commits
    - [ ] Require review from Code Owners
    - [ ] Require conversation resolution
- [ ] Require status checks to pass
    - [ ] Require branches to be up to date before merging
    - Checks: `hygiene`, `docs`, `contracts`, `workflows`, `secrets`

### Zero required approvals

GitHub does not allow an author to approve their own pull request, so a one-person project
with a non-zero requirement cannot merge anything.

This is the weakest point in the process and is worth naming rather than glossing over. Until
a second maintainer exists, CI is doing the work review would otherwise do — which is why the
secret scan and workflow-security analysis are required checks rather than advisory ones.

**Change to `1` the moment a second maintainer joins.** The line is marked in
`scripts/setup-branch-protection.sh`.

### Empty bypass list

Administrators are subject to the rules. There is no path to `main` that skips CI, which
means no path that skips the secret scan.

The escape hatch, if one is genuinely needed, is to disable the ruleset deliberately and
re-enable it — a visible, auditable act rather than a quiet exception.

## Labels

`scripts/setup-labels.sh` creates them.

| Prefix | Values |
|---|---|
| `area/` | `core`, `hostd`, `dashboard`, `installer`, `controller`, `storage`, `network`, `apps`, `backup`, `docs`, `ci` |
| `type/` | `feature`, `bug`, `security`, `docs`, `test`, `chore`, `epic`, `research` |
| `priority/` | `critical`, `high`, `normal`, `low` |
| `status/` | `needs-triage`, `needs-design`, `blocked`, `ready`, `in-progress` |
| `risk/` | `destructive`, `migration`, `security` |

The `risk/` labels change how a pull request is reviewed rather than merely describing it —
see [contributing](https://github.com/HusnuOkanCakir/homebase/blob/main/CONTRIBUTING.md).

## Verifying

```sh
gh api repos/HusnuOkanCakir/homebase --jq \
  '{squash: .allow_squash_merge, merge: .allow_merge_commit, rebase: .allow_rebase_merge}'

gh api repos/HusnuOkanCakir/homebase/rulesets

gh label list --limit 50
```

The negative tests are the ones that prove it works:

```sh
# Direct push to main — must be rejected
git checkout main && git commit -s --allow-empty -m "test" && git push
git reset --hard HEAD~1
```

A pull request with a failing check must also be unmergeable, including by an administrator.

## First-time setup order

Branch protection is applied **after** the first push. A protected branch cannot receive its
initial commit cleanly, so the order is: create the repository, push `main`, then protect it.

1. Create the repository — public, no README, no `.gitignore`, no licence (all exist here)
2. `git remote add origin git@github.com:HusnuOkanCakir/homebase.git`
3. `git push -u origin main`
4. `./scripts/setup-labels.sh`
5. `./scripts/setup-branch-protection.sh`
6. Apply the settings checklists above
7. Verify with the commands above, including the negative tests
