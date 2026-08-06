#!/usr/bin/env bash
#
# Create the Homebase issue and pull-request labels.
#
# Idempotent: safe to re-run. Existing labels are updated to match this file, which
# makes this script the source of truth rather than whatever accumulated in the UI.
#
#   ./scripts/setup-labels.sh
#
# Requires the GitHub CLI, authenticated with repo admin rights.

set -euo pipefail

REPO="${HOMEBASE_REPO:-HusnuOkanCakir/homebase}"

if ! command -v gh >/dev/null 2>&1; then
  echo "error: the GitHub CLI (gh) is not installed." >&2
  echo "       https://cli.github.com/ — or apply the labels by hand from" >&2
  echo "       docs/development/repository.md" >&2
  exit 1
fi

if ! gh auth status >/dev/null 2>&1; then
  echo "error: not authenticated. Run: gh auth login" >&2
  exit 1
fi

echo "Applying labels to ${REPO}"

# name|colour|description
#
# Colour carries meaning, so that a crowded issue list is readable at a glance:
#   blue   area      — which part of the system
#   purple type      — what kind of work
#   red    priority  — how urgent
#   grey   status    — where it is in the process
#   orange risk      — changes how it must be reviewed
LABELS=(
  # --- area ---------------------------------------------------------------
  "area/core|1d76db|The unprivileged core service"
  "area/hostd|1d76db|The privileged host service"
  "area/dashboard|1d76db|The web interface"
  "area/installer|1d76db|Server installation media and first boot"
  "area/controller|1d76db|The installer USB creator"
  "area/storage|1d76db|Disks, mounts and managed storage"
  "area/network|1d76db|Networking, discovery and remote access"
  "area/apps|1d76db|Application lifecycle and the catalogue"
  "area/backup|1d76db|Backup and restore"
  "area/docs|1d76db|Documentation"
  "area/ci|1d76db|CI, tooling and release plumbing"

  # --- type ---------------------------------------------------------------
  "type/feature|8b5cf6|New capability"
  "type/bug|8b5cf6|Something behaves incorrectly"
  "type/security|8b5cf6|Security hardening or a boundary change"
  "type/docs|8b5cf6|Documentation work"
  "type/test|8b5cf6|Test coverage"
  "type/chore|8b5cf6|Tooling, dependencies, plumbing"
  "type/epic|8b5cf6|Milestone-sized, tracks smaller issues"
  "type/research|8b5cf6|Investigation before a decision"

  # --- priority -----------------------------------------------------------
  "priority/critical|b60205|Data loss, security, or nothing works"
  "priority/high|d93f0b|Blocks a milestone"
  "priority/normal|fbca04|Ordinary"
  "priority/low|fef2c0|Worth doing eventually"

  # --- status -------------------------------------------------------------
  "status/needs-triage|ededed|Not yet assessed"
  "status/needs-design|ededed|Needs a decision before it can be built"
  "status/blocked|ededed|Waiting on something else"
  "status/ready|ededed|Understood and ready to pick up"
  "status/in-progress|ededed|Someone is working on it"

  # --- risk ---------------------------------------------------------------
  # These three change how a pull request is reviewed rather than merely
  # describing it. See CONTRIBUTING.md.
  "risk/destructive|e11d21|Can erase or overwrite user data"
  "risk/migration|e11d21|Changes schema, on-disk format, or the upgrade path"
  "risk/security|e11d21|Touches the privilege boundary, auth, or the update path"

  # --- housekeeping -------------------------------------------------------
  "good first issue|7057ff|Small and well-defined; a good place to start"
  "help wanted|008672|Input or hardware we do not have would be welcome"
  "duplicate|cfd3d7|Already tracked elsewhere"
  "wontfix|cfd3d7|Deliberately not doing this"
)

created=0
for entry in "${LABELS[@]}"; do
  IFS='|' read -r name colour description <<<"${entry}"
  # --force updates an existing label rather than failing, which is what makes
  # this file the source of truth.
  gh label create "${name}" \
    --repo "${REPO}" \
    --color "${colour}" \
    --description "${description}" \
    --force >/dev/null
  printf '  %-28s %s\n' "${name}" "${description}"
  created=$((created + 1))
done

echo
echo "${created} labels applied."
echo
echo "GitHub's default labels (bug, enhancement, question, invalid) are left alone."
echo "Delete them by hand if you want only the taxonomy above:"
echo "  gh label delete bug --repo ${REPO} --yes"
