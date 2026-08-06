#!/usr/bin/env bash
#
# Check that this machine can work on the current milestone, and say plainly what
# is missing for the next ones.
#
#   ./scripts/bootstrap-dev.sh
#
# Reports only. It installs nothing and changes nothing — a setup script that
# installs system packages on someone's machine without asking is a bad neighbour.

set -uo pipefail

ok=0
missing=0

green() { printf '  \033[32m✓\033[0m %s\n' "$1"; ok=$((ok + 1)); }
warn()  { printf '  \033[33m!\033[0m %s\n' "$1"; }
bad()   { printf '  \033[31m✗\033[0m %s\n' "$1"; missing=$((missing + 1)); }

version_of() { "$1" --version 2>&1 | head -1; }

echo "Homebase development environment"
echo
echo "Milestone 0 — contracts, CI and documentation"

# --- Python -----------------------------------------------------------------

if command -v python3 >/dev/null 2>&1; then
  py_minor=$(python3 -c 'import sys; print(sys.version_info[1])')
  py_major=$(python3 -c 'import sys; print(sys.version_info[0])')
  if [[ "${py_major}" -eq 3 && "${py_minor}" -ge 11 ]]; then
    green "python3 $(python3 -c 'import sys; print(".".join(map(str, sys.version_info[:3])))')"
  else
    bad "python3 is 3.${py_minor}; 3.11 or newer is needed"
  fi

  if python3 -c 'import venv' 2>/dev/null; then
    green "python3-venv"
  else
    bad "python3-venv is missing (Ubuntu: sudo apt install python3-venv)"
  fi
else
  bad "python3 is not installed"
fi

if command -v git >/dev/null 2>&1; then
  green "$(version_of git)"
else
  bad "git is not installed"
fi

# --- Disk -------------------------------------------------------------------

avail_kb=$(df -Pk . | awk 'NR==2 {print $4}')
avail_gb=$((avail_kb / 1024 / 1024))

if [[ "${avail_gb}" -ge 2 ]]; then
  green "disk: ${avail_gb} GB free"
elif [[ "${avail_gb}" -ge 1 ]]; then
  warn "disk: ${avail_gb} GB free — enough for the venv, but tight"
else
  bad "disk: ${avail_gb} GB free — the venv needs roughly 250 MB"
fi

# --- Later milestones -------------------------------------------------------

echo
echo "Milestone 1 — VM lab (not yet started)"

if [[ "${avail_gb}" -ge 40 ]]; then
  green "disk: ${avail_gb} GB free (40 GB needed for ISOs and qcow2 overlays)"
else
  warn "disk: ${avail_gb} GB free — the VM lab needs about 40 GB"
fi

if command -v qemu-system-x86_64 >/dev/null 2>&1; then
  green "$(version_of qemu-system-x86_64)"
else
  warn "qemu-system-x86_64 not installed (needed at Milestone 1)"
fi

if [[ -r /dev/kvm && -w /dev/kvm ]]; then
  green "/dev/kvm is accessible"
elif [[ -e /dev/kvm ]]; then
  warn "/dev/kvm exists but is not accessible — add yourself to the kvm group:"
  printf '      sudo usermod -aG kvm "$USER"   # then log out and back in\n'
else
  warn "/dev/kvm not present — hardware virtualisation may be disabled in firmware"
fi

echo
echo "Milestone 2 — core, hostd and the dashboard (not yet started)"

if command -v go >/dev/null 2>&1; then
  green "$(version_of go)"
else
  warn "go not installed (needed at Milestone 2, 1.23+)"
fi

if command -v node >/dev/null 2>&1; then
  node_major=$(node --version | sed 's/^v\([0-9]*\).*/\1/')
  if [[ "${node_major}" -ge 20 ]]; then
    green "node $(node --version)"
  else
    warn "node $(node --version) is too old — the dashboard needs 20 or newer"
  fi
else
  warn "node not installed (needed at Milestone 2, 20+)"
fi

echo
echo "Milestone 3 — applications (not yet started)"

if command -v docker >/dev/null 2>&1; then
  if docker info >/dev/null 2>&1; then
    green "$(version_of docker)"
  else
    warn "docker is installed but not reachable — is the daemon running, and are you in the docker group?"
  fi
else
  warn "docker not installed (needed at Milestone 3)"
fi

# --- Optional ---------------------------------------------------------------

echo
echo "Optional"

if command -v gh >/dev/null 2>&1; then
  green "$(version_of gh)"
else
  warn "gh not installed — scripts/setup-labels.sh and setup-branch-protection.sh need it."
  printf '      The web-UI equivalents are in docs/development/repository.md\n'
fi

# --- Summary ----------------------------------------------------------------

echo
if [[ "${missing}" -eq 0 ]]; then
  echo "Ready for Milestone 0. Next:"
  echo
  echo "  make bootstrap    # create .venv and install pinned tooling"
  echo "  make check        # run exactly what CI runs"
  echo "  make docs         # serve the documentation site on :8000"
  exit 0
fi

echo "${missing} required item(s) missing for Milestone 0."
echo "Warnings above are for later milestones and are not blocking today."
exit 1
