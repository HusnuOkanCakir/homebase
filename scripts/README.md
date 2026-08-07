# scripts/ — repository automation

| Script | Purpose | Needs |
|---|---|---|
| [`bootstrap-dev.sh`](bootstrap-dev.sh) | Report whether this machine can work on the current milestone | — |
| [`setup-labels.sh`](setup-labels.sh) | Create the issue and pull-request labels | `gh` |
| [`setup-branch-protection.sh`](setup-branch-protection.sh) | Apply the `main` ruleset and repository settings | `gh` |
| [`check_hygiene.py`](check_hygiene.py) | Encoding, line endings, whitespace, file size | — |
| [`check_links.py`](check_links.py) | Internal Markdown links, including heading anchors | — |
| [`validate_contracts.py`](validate_contracts.py) | JSON Schemas and their fixtures | `.venv` |

The three Python scripts are what `make check` runs, and what CI runs. They are local
scripts rather than third-party GitHub Actions deliberately: each is small, and repository
hygiene is a poor reason to grant a third party execution rights in CI
([ADR-0009](../docs/decisions/0009-python-docs-toolchain.md)).

## Conventions

**Report, do not install.** `bootstrap-dev.sh` tells you what is missing and how to get it.
It does not install system packages — a setup script that modifies someone's machine without
asking is a bad neighbour, and this one runs on machines that are also somebody's daily
driver.

**Idempotent.** The `gh` scripts can be re-run safely. `setup-labels.sh` creates a label,
and on the 422 that means it already exists, updates it instead — which is what makes this
file the source of truth rather than whatever accumulated in the web UI.

**`gh api`, not newer subcommands.** Ubuntu 22.04 ships gh 2.4, which predates `gh label`
and `gh run list --branch`. `gh api` is a generic REST client and has been stable for
years, so the scripts run on whatever version a contributor happens to have. Requiring a
CLI upgrade in order to run a setup script is a poor first impression; keep new code on
`gh api` for the same reason.

**Documented equivalents.** `gh` is not installed everywhere, and API shapes change. Every
setting these scripts apply is also written out as a web-UI checklist in
[docs/development/repository.md](../docs/development/repository.md) — and that checklist,
not the script, is the source of truth if the two ever disagree.

## Order, for a fresh repository

Branch protection is applied **after** the first push: a protected branch cannot receive its
initial commit cleanly.

```sh
./scripts/bootstrap-dev.sh          # check the machine
make bootstrap && make check        # verify locally

git remote add origin git@github.com:HusnuOkanCakir/homebase.git
git push -u origin main

./scripts/setup-labels.sh
./scripts/setup-branch-protection.sh
```

Then run the negative tests. A ruleset that exists but does not bite is worse than none,
because it is trusted:

```sh
git checkout main
git commit -s --allow-empty -m "protection test"
git push                             # expect: rejected
git reset --hard HEAD~1
```
