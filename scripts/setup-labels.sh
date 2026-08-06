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

# Uses `gh api` rather than `gh label`, which did not exist until gh 2.6 — the
# version shipped with Ubuntu 22.04 is 2.4. `gh api` is a generic REST client and
# has been stable for years, so this works on whatever gh a contributor happens to
# have.
#
# Idempotence: POST creates, and a 422 means the label already exists, so PATCH
# updates it to match this file.
apply_label() {
  local name="$1" colour="$2" description="$3" encoded status

  encoded=$(python3 -c 'import sys,urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "${name}")

  if gh api "repos/${REPO}/labels" --method POST --silent \
      -f name="${name}" -f color="${colour}" -f description="${description}" 2>/dev/null
  then
    status="created"
  elif gh api "repos/${REPO}/labels/${encoded}" --method PATCH --silent \
      -f new_name="${name}" -f color="${colour}" -f description="${description}" 2>/dev/null
  then
    status="updated"
  else
    echo "  ! failed: ${name}" >&2
    return 1
  fi

  printf '  %-8s %-28s %s\n' "${status}" "${name}" "${description}"
}

applied=0
failed=0
for entry in "${LABELS[@]}"; do
  IFS='|' read -r name colour description <<<"${entry}"
  if apply_label "${name}" "${colour}" "${description}"; then
    applied=$((applied + 1))
  else
    failed=$((failed + 1))
  fi
done

echo
echo "${applied} labels applied, ${failed} failed."
echo
echo "GitHub's default labels (bug, enhancement, question, invalid) are left alone."
echo "Delete them if you want only the taxonomy above:"
echo "  gh api repos/${REPO}/labels/bug --method DELETE"
