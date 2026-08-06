#!/usr/bin/env bash
#
# Apply the `main` ruleset and the repository settings that go with it.
#
# Idempotent: an existing "main protection" ruleset is updated in place rather than
# duplicated.
#
#   ./scripts/setup-branch-protection.sh
#
# Run this AFTER the first push. A protected branch cannot receive its initial
# commit cleanly, so the order is: create the repository, push main, then protect it.
#
# The equivalent web-UI checklist is in docs/development/repository.md, which is the
# source of truth if this script and the documentation ever disagree.

set -euo pipefail

REPO="${HOMEBASE_REPO:-HusnuOkanCakir/homebase}"

# ---------------------------------------------------------------------------
# Required approvals.
#
# Zero, because GitHub does not let an author approve their own pull request and
# a one-person project would otherwise be unable to merge anything.
#
# This is the weakest point in the process. Until a second maintainer exists, CI
# is doing the work review would otherwise do — which is why the secret scan and
# the workflow-security analysis are required checks rather than advisory.
#
# >>> CHANGE THIS TO 1 THE MOMENT A SECOND MAINTAINER JOINS. <<<
# ---------------------------------------------------------------------------
REQUIRED_APPROVALS=0

# Job names from .github/workflows/ci.yml. These must match exactly, or the
# ruleset will wait forever for a check that never reports.
REQUIRED_CHECKS=(hygiene docs contracts workflows secrets)

if ! command -v gh >/dev/null 2>&1; then
  echo "error: the GitHub CLI (gh) is not installed." >&2
  echo "       Apply the settings by hand instead — see docs/development/repository.md" >&2
  exit 1
fi

if ! gh auth status >/dev/null 2>&1; then
  echo "error: not authenticated. Run: gh auth login" >&2
  exit 1
fi

echo "Configuring ${REPO}"
echo

# --- Repository settings ----------------------------------------------------

echo "Repository settings"

gh api "repos/${REPO}" --method PATCH --silent \
  -F allow_squash_merge=true \
  -F allow_merge_commit=false \
  -F allow_rebase_merge=false \
  -f squash_merge_commit_title=PR_TITLE \
  -f squash_merge_commit_message=PR_BODY \
  -F delete_branch_on_merge=true \
  -F allow_auto_merge=true \
  -F allow_update_branch=true \
  -F has_issues=true \
  -F has_wiki=false \
  -F has_projects=false \
  -F web_commit_signoff_required=true

echo "  squash merging only; merge commits and rebase merging disabled"
echo "  squash commit title taken from the pull request title"
echo "  head branches deleted on merge"
echo "  sign-off required on web commits"

# Security features. Available on public repositories at no cost; a private
# repository without Advanced Security will reject some of these, hence the
# tolerant error handling.
if gh api "repos/${REPO}" --method PATCH --silent \
  -F 'security_and_analysis[secret_scanning][status]=enabled' \
  -F 'security_and_analysis[secret_scanning_push_protection][status]=enabled' 2>/dev/null
then
  echo "  secret scanning and push protection enabled"
else
  echo "  ! secret scanning could not be enabled automatically — enable it by hand"
  echo "    (Settings -> Code security). Push protection is what stops a secret"
  echo "    reaching a public repository, where it is scraped within minutes."
fi

# Private vulnerability reporting: the mechanism SECURITY.md tells people to use.
if gh api "repos/${REPO}/private-vulnerability-reporting" --method PUT --silent 2>/dev/null; then
  echo "  private vulnerability reporting enabled"
else
  echo "  ! private vulnerability reporting could not be enabled automatically"
fi

# Dependabot alerts, which also switch on the dependency graph. The graph is what
# actions/dependency-review-action reads; without it that job fails outright with
# "Dependency review is not supported on this repository", which reads like a
# broken workflow rather than a missing setting.
if gh api "repos/${REPO}/vulnerability-alerts" --method PUT --silent 2>/dev/null; then
  echo "  Dependabot alerts and dependency graph enabled"
else
  echo "  ! Dependabot alerts could not be enabled automatically"
fi

if gh api "repos/${REPO}/automated-security-fixes" --method PUT --silent 2>/dev/null; then
  echo "  Dependabot security updates enabled"
else
  echo "  ! Dependabot security updates could not be enabled automatically"
fi

# --- Actions permissions ----------------------------------------------------

echo
echo "Actions"

gh api "repos/${REPO}/actions/permissions/workflow" --method PUT --silent \
  -f default_workflow_permissions=read \
  -F can_approve_pull_request_reviews=false

echo "  default token permission: read"
echo "  Actions cannot approve pull requests"

# Fork pull requests run untrusted code. GitHub's default only holds workflows
# for *first-time* contributors; after one merged pull request an author's
# subsequent workflows run automatically. On a public repository that is a
# meaningful gap, so require approval for every external contributor, every time.
if gh api "repos/${REPO}/actions/permissions/fork-pr-contributor-approval" \
  --method PUT --silent -f approval_policy=all_external_contributors 2>/dev/null
then
  echo "  fork pull requests need approval from all external contributors"
else
  echo "  ! could not set the fork pull request approval policy — set it by hand"
  echo "    (Settings -> Actions -> General -> Fork pull request workflows)"
fi

# --- GitHub Pages -----------------------------------------------------------

echo
echo "Pages"

# The Docs workflow deploys here, and actions/configure-pages fails outright if
# Pages has never been enabled — so enabling it is part of setup, not an
# afterthought. 409 means it is already enabled, which is not an error.
pages_state=$(gh api "repos/${REPO}/pages" --jq .build_type 2>/dev/null || true)

if [[ "${pages_state}" == "workflow" ]]; then
  echo "  already enabled, source: GitHub Actions"
elif gh api "repos/${REPO}/pages" --method POST --silent -f build_type=workflow 2>/dev/null; then
  echo "  enabled, source: GitHub Actions"
else
  echo "  ! could not enable Pages — set it by hand"
  echo "    (Settings -> Pages -> Source: GitHub Actions)"
fi

# --- The main ruleset -------------------------------------------------------

echo
echo "Ruleset: main protection"

checks_json=$(printf '%s\n' "${REQUIRED_CHECKS[@]}" \
  | python3 -c 'import json,sys; print(json.dumps([{"context": c.strip()} for c in sys.stdin if c.strip()]))')

ruleset_json=$(python3 - "$REQUIRED_APPROVALS" "$checks_json" <<'PY'
import json, sys

approvals = int(sys.argv[1])
checks = json.loads(sys.argv[2])

print(json.dumps({
    "name": "main protection",
    "target": "branch",
    "enforcement": "active",
    # Empty bypass list: administrators are subject to the rules too. There is no
    # path to main that skips CI, and therefore none that skips the secret scan.
    "bypass_actors": [],
    "conditions": {"ref_name": {"include": ["refs/heads/main"], "exclude": []}},
    "rules": [
        {"type": "deletion"},
        {"type": "non_fast_forward"},
        {"type": "required_linear_history"},
        {
            "type": "pull_request",
            "parameters": {
                "required_approving_review_count": approvals,
                "dismiss_stale_reviews_on_push": True,
                "require_code_owner_review": True,
                "require_last_push_approval": False,
                "required_review_thread_resolution": True,
                "allowed_merge_methods": ["squash"],
            },
        },
        {
            "type": "required_status_checks",
            "parameters": {
                "strict_required_status_checks_policy": True,
                "required_status_checks": checks,
            },
        },
    ],
}))
PY
)

existing=$(gh api "repos/${REPO}/rulesets" --jq \
  '.[] | select(.name == "main protection") | .id' 2>/dev/null || true)

if [[ -n "${existing}" ]]; then
  echo "${ruleset_json}" | gh api "repos/${REPO}/rulesets/${existing}" \
    --method PUT --input - --silent
  echo "  updated existing ruleset (id ${existing})"
else
  echo "${ruleset_json}" | gh api "repos/${REPO}/rulesets" \
    --method POST --input - --silent
  echo "  created ruleset"
fi

echo "  direct pushes to main blocked"
echo "  force pushes and deletion blocked"
echo "  linear history required"
echo "  required approvals: ${REQUIRED_APPROVALS}"
echo "  code owner review required"
echo "  conversations must be resolved"
echo "  required checks: ${REQUIRED_CHECKS[*]}"
echo "  bypass list empty — administrators included"

# --- Verify -----------------------------------------------------------------

echo
echo "Verifying"

gh api "repos/${REPO}" --jq \
  '"  merge methods: squash=\(.allow_squash_merge) merge=\(.allow_merge_commit) rebase=\(.allow_rebase_merge)"'

gh api "repos/${REPO}/rulesets" --jq \
  '.[] | "  ruleset: \(.name) [\(.enforcement)]"'

cat <<'EOF'

Done.

Two negative tests are what actually prove this works — a ruleset that exists but
does not bite is worse than none, because it is trusted:

  # Direct push to main must be REJECTED
  git checkout main
  git commit -s --allow-empty -m "protection test"
  git push          # expect: rejected
  git reset --hard HEAD~1

  # A pull request with a failing check must be UNMERGEABLE, including for you

Optional, and not scripted because it is a judgement call rather than a setting:
  - Enable Discussions, if you want somewhere for questions that are not issues
EOF
