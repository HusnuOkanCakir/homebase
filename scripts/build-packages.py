#!/usr/bin/env python3
"""Build the Homebase Debian packages.

Three packages, staged and sealed with `dpkg-deb --build`:

    homebase-hostd      the privileged host service
    homebase-core       the unprivileged service
    homebase-dashboard  the web interface (architecture-independent)

Deliberately not debhelper. Not because debhelper is wrong — it is the right
tool for a package with anything complicated in it — but because these three
have no build system to invoke, no libraries to shlibdeps, and nothing to
compile at package time. What they do have is a privilege boundary expressed in
file ownership and modes, and that is worth being able to read in one file
rather than inferring from a dozen debhelper defaults.

It also means packages build with nothing installed but dpkg itself, which keeps
CI honest: `make packages` does the same thing on a laptop and on a runner.

Run with `make packages`.
"""

from __future__ import annotations

import argparse
import os
import shutil
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
BUILD = REPO_ROOT / "build"
OUT = REPO_ROOT / "dist"

MAINTAINER = "Husnu Okan Cakir <h.okancakir@gmail.com>"
HOMEPAGE = "https://github.com/HusnuOkanCakir/homebase"
SECTION = "admin"


def colour(code: str, text: str) -> str:
    return text if os.environ.get("NO_COLOR") else f"\033[{code}m{text}\033[0m"


def step(msg: str) -> None:
    print(colour("1;34", f"==> {msg}"), flush=True)


def ok(msg: str) -> None:
    print(colour("32", f"  ✓ {msg}"), flush=True)


def die(msg: str) -> None:
    print(colour("31", f"  ✗ {msg}"), file=sys.stderr)
    sys.exit(1)


def run(cmd: list[str], **kwargs) -> subprocess.CompletedProcess:
    result = subprocess.run(cmd, capture_output=True, text=True, **kwargs)
    if result.returncode != 0:
        die(f"{cmd[0]} failed:\n{(result.stderr or result.stdout).strip()[:800]}")
    return result


# --- Maintainer scripts ------------------------------------------------------
#
# Every one of these runs on a machine that already holds somebody's data, and
# runs again on every upgrade. Two rules follow, and they are why these are
# written out rather than generated:
#
#   Idempotent. `set -e` plus a command that fails the second time leaves the
#   package half-configured, which is a state nobody has code for.
#
#   Never destructive. Nothing here removes user data, on any path, including
#   purge. Debian policy says purge should remove what the package created; on a
#   machine holding the only copy of a family's photographs, that policy loses
#   to not deleting them. The deviation is deliberate and is announced to the
#   user, who can then remove the directories themselves.

HOSTD_POSTINST = """#!/bin/sh
set -e

# The homebase group is the privilege boundary: the hostd socket is
# root:homebase 0660, so membership is what decides who can ask for a
# privileged operation. It is created here rather than in homebase-core
# because the socket unit references it and hostd configures first.
if ! getent group homebase >/dev/null; then
    addgroup --system --quiet homebase
fi

if [ "$1" = "configure" ]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
    # The socket, not the service: hostd is socket-activated, so it starts on
    # the first connection and is not running the rest of the time.
    if [ -d /run/systemd/system ]; then
        systemctl enable --now homebase-hostd.socket >/dev/null 2>&1 || true
    fi
fi

exit 0
"""

HOSTD_PRERM = """#!/bin/sh
set -e

if [ -d /run/systemd/system ]; then
    systemctl stop homebase-hostd.service >/dev/null 2>&1 || true
    systemctl stop homebase-hostd.socket >/dev/null 2>&1 || true
fi

exit 0
"""

HOSTD_POSTRM = """#!/bin/sh
set -e

if [ "$1" = "remove" ] || [ "$1" = "purge" ]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

if [ "$1" = "purge" ]; then
    rm -f /var/log/homebase/audit.log

    # The homebase group is left in place. homebase-core may still be installed
    # and still own files with that group, and removing a group whose gid is
    # still referenced on disk is how files end up owned by a number.
    :
fi

exit 0
"""

CORE_POSTINST = """#!/bin/sh
set -e

if ! getent group homebase >/dev/null; then
    addgroup --system --quiet homebase
fi

# The unprivileged account everything above hostd runs as. No login shell and no
# home directory it can write outside: core is not meant to be a user anybody
# can become.
if ! getent passwd homebase >/dev/null; then
    adduser --system --quiet \\
        --ingroup homebase \\
        --home /var/lib/homebase \\
        --no-create-home \\
        --shell /usr/sbin/nologin \\
        homebase
else
    # An account by this name already exists, so this is an upgrade — or
    # somebody already had a user called homebase. adduser --system adopts it
    # either way and says nothing, which would silently run the service as a
    # person's interactive account, with their home directory and their shell.
    #
    # Not fatal: refusing here would break every upgrade to fix a case that is
    # rare. But it must not pass unremarked.
    existing_shell=$(getent passwd homebase | cut -d: -f7)
    case "$existing_shell" in
        */nologin|*/false)
            ;;
        *)
            echo "warning: the 'homebase' account already exists with shell" \\
                 "$existing_shell." >&2
            echo "         Homebase's services will run as it. If this is a" \\
                 "person's account rather than" >&2
            echo "         a service account, that is probably not what you" \\
                 "want." >&2
            ;;
    esac
fi

if [ "$1" = "configure" ]; then
    # Ownership here is the boundary, not housekeeping:
    #
    #   /etc/homebase   root-owned. core may read its configuration and may not
    #                   rewrite it — a configuration file core can change is one
    #                   an attacker who owns core can change.
    #   /var/lib        core's own state, including the database.
    #   /srv            user data. Created if absent; never chowned if it
    #                   already exists, because an upgrade must not reassign
    #                   ownership of files somebody already has.
    #   /var/log        both services write here; hostd writes its audit log as
    #                   root within it.
    install -d -o root     -g homebase -m 0750 /etc/homebase
    install -d -o homebase -g homebase -m 0750 /var/lib/homebase
    install -d -o homebase -g homebase -m 0750 /var/log/homebase

    if [ ! -d /srv/homebase ]; then
        install -d -o homebase -g homebase -m 0750 /srv/homebase
    fi

    systemctl daemon-reload >/dev/null 2>&1 || true
    if [ -d /run/systemd/system ]; then
        systemctl enable --now homebase-core.service >/dev/null 2>&1 || true
    fi
fi

exit 0
"""

CORE_PRERM = """#!/bin/sh
set -e

if [ -d /run/systemd/system ]; then
    systemctl stop homebase-core.service >/dev/null 2>&1 || true
fi

exit 0
"""

CORE_POSTRM = """#!/bin/sh
set -e

if [ "$1" = "remove" ] || [ "$1" = "purge" ]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

if [ "$1" = "purge" ]; then
    # Configuration and logs go. Data does not.
    #
    # Debian policy says purge removes everything the package created, and this
    # deliberately does not. /var/lib/homebase holds the account you sign in
    # with and every application's configuration; /srv/homebase holds the
    # photographs. A package removal is not a decision to destroy those, and
    # somebody who meant it can do it in one command — which is more than they
    # get if we were wrong.
    rm -rf /etc/homebase
    rm -rf /var/log/homebase

    if [ -d /var/lib/homebase ] || [ -d /srv/homebase ]; then
        echo ""
        echo "Homebase has been removed, but your data has been kept:"
        [ -d /var/lib/homebase ] && echo "  /var/lib/homebase   settings, accounts, application state"
        [ -d /srv/homebase ]     && echo "  /srv/homebase       your files"
        echo ""
        echo "Delete them yourself if you are sure:"
        echo "  sudo rm -rf /var/lib/homebase /srv/homebase"
        echo ""
    fi
fi

exit 0
"""

DASHBOARD_POSTINST = """#!/bin/sh
set -e

if [ "$1" = "configure" ]; then
    # core serves these files; restarting picks up the new build. Not a failure
    # if core is not installed or not running yet.
    if [ -d /run/systemd/system ]; then
        systemctl try-restart homebase-core.service >/dev/null 2>&1 || true
    fi
fi

exit 0
"""


# --- Package definitions -----------------------------------------------------


def build_hostd(version: str, binaries: Path) -> Path:
    root = BUILD / "homebase-hostd"
    reset(root)

    install_file(binaries / "hostd", root / "usr/libexec/homebase/hostd", 0o755)
    for unit in ("homebase-hostd.service", "homebase-hostd.socket"):
        install_file(
            REPO_ROOT / "packaging/systemd" / unit,
            root / "lib/systemd/system" / unit,
            0o644,
        )

    control(
        root,
        package="homebase-hostd",
        version=version,
        architecture="amd64",
        depends="systemd, adduser",
        description=(
            "Homebase privileged host service\n"
            " The only component of Homebase that runs as root. It accepts a fixed,\n"
            " compiled-in set of named operations over a Unix socket and has no generic\n"
            " execution path.\n"
            " .\n"
            " Everything else in Homebase is unprivileged and reaches the system only\n"
            " through this service."
        ),
    )

    script(root, "postinst", HOSTD_POSTINST)
    script(root, "prerm", HOSTD_PRERM)
    script(root, "postrm", HOSTD_POSTRM)

    return seal(root, "homebase-hostd", version, "amd64")


def build_core(version: str, binaries: Path) -> Path:
    root = BUILD / "homebase-core"
    reset(root)

    install_file(binaries / "core", root / "usr/libexec/homebase/core", 0o755)
    install_file(
        REPO_ROOT / "packaging/systemd/homebase-core.service",
        root / "lib/systemd/system/homebase-core.service",
        0o644,
    )

    control(
        root,
        package="homebase-core",
        version=version,
        architecture="amd64",
        depends=f"systemd, adduser, homebase-hostd (= {version})",
        description=(
            "Homebase core service\n"
            " The unprivileged service: HTTP API, authentication, jobs and state. Runs as\n"
            " the homebase user with no capabilities.\n"
            " .\n"
            " Every privileged action is performed by homebase-hostd on its behalf, as a\n"
            " named and audited operation."
        ),
    )

    script(root, "postinst", CORE_POSTINST)
    script(root, "prerm", CORE_PRERM)
    script(root, "postrm", CORE_POSTRM)

    return seal(root, "homebase-core", version, "amd64")


def build_dashboard(version: str, dist: Path) -> Path:
    root = BUILD / "homebase-dashboard"
    reset(root)

    target = root / "usr/share/homebase/dashboard"
    shutil.copytree(dist, target)
    for path in target.rglob("*"):
        path.chmod(0o755 if path.is_dir() else 0o644)

    control(
        root,
        package="homebase-dashboard",
        version=version,
        # No compiled code: the same files work everywhere.
        architecture="all",
        depends=f"homebase-core (= {version})",
        description=(
            "Homebase web interface\n"
            " The dashboard, as static files served by homebase-core from the same origin\n"
            " as the API.\n"
            " .\n"
            " It holds no privileges of its own and talks to nothing but the Homebase API."
        ),
    )

    script(root, "postinst", DASHBOARD_POSTINST)

    return seal(root, "homebase-dashboard", version, "all")


# --- Staging helpers ---------------------------------------------------------


def reset(root: Path) -> None:
    if root.exists():
        shutil.rmtree(root)
    (root / "DEBIAN").mkdir(parents=True)


def install_file(source: Path, target: Path, mode: int) -> None:
    if not source.exists():
        die(f"missing: {source}\n    Run `make go-build` and `make dash-build` first.")
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, target)
    target.chmod(mode)


def control(root: Path, *, package: str, version: str, architecture: str,
            depends: str, description: str) -> None:
    size = sum(f.stat().st_size for f in root.rglob("*") if f.is_file()) // 1024
    (root / "DEBIAN/control").write_text(
        f"""Package: {package}
Version: {version}
Section: {SECTION}
Priority: optional
Architecture: {architecture}
Depends: {depends}
Maintainer: {MAINTAINER}
Homepage: {HOMEPAGE}
Installed-Size: {size}
Description: {description}
"""
    )


def script(root: Path, name: str, body: str) -> None:
    path = root / "DEBIAN" / name
    path.write_text(body)
    path.chmod(0o755)

    # A maintainer script with a syntax error fails on the user's machine, part
    # way through, leaving the package half-configured. Checking here costs
    # nothing.
    run(["sh", "-n", str(path)])


def seal(root: Path, package: str, version: str, architecture: str) -> Path:
    # Directories inherit the builder's umask otherwise, which produced
    # group-writable 0775 directories in a package that ships a root service.
    # Harmless while the group is root, and exactly the kind of detail that
    # stops being harmless when somebody later changes the group.
    root.chmod(0o755)
    for directory in sorted(root.rglob("*")):
        if directory.is_dir() and directory.name != "DEBIAN":
            directory.chmod(0o755)

    OUT.mkdir(parents=True, exist_ok=True)
    output = OUT / f"{package}_{version}_{architecture}.deb"

    # fakeroot so the staged files are recorded as root-owned without this
    # script needing to be run as root.
    run(["fakeroot", "dpkg-deb", "--build", "--root-owner-group", str(root), str(output)])

    size = output.stat().st_size // 1024
    ok(f"{output.name} ({size} KB)")
    return output


def verify(packages: list[Path]) -> None:
    step("Verifying")

    for package in packages:
        contents = run(["dpkg-deb", "--contents", str(package)]).stdout

        # Nothing may ship setuid or setgid. A setuid binary in a package that
        # also installs a root service is how a privilege boundary becomes
        # decorative.
        for line in contents.splitlines():
            mode = line.split()[0] if line.split() else ""
            if "s" in mode[:10].replace("d", ""):
                die(f"{package.name} ships a setuid or setgid file:\n    {line}")

        # Files belong under the paths the data-layout document describes.
        allowed = ("./", "./usr/", "./lib/", "./etc/")
        for line in contents.splitlines():
            path = line.split()[-1]
            if not path.startswith(allowed):
                die(f"{package.name} installs outside the expected tree: {path}")

    ok("no setuid or setgid files")
    ok("nothing installed outside /usr, /lib or /etc")

    # A world- or group-writable directory in a package installing a root
    # service is a way for an unprivileged user to replace what root executes.
    for package in packages:
        for line in run(["dpkg-deb", "--contents", str(package)]).stdout.splitlines():
            fields = line.split()
            if not fields or not fields[0].startswith("d"):
                continue
            mode = fields[0]
            if mode[5] == "w" or mode[8] == "w":
                die(f"{package.name} ships a writable directory: {line}")
    ok("no group- or world-writable directories")

    # The runtime directories are created by the maintainer scripts with
    # explicit ownership, not shipped in the package — a directory shipped in a
    # .deb carries the mode it was staged with, and getting that wrong is
    # exactly the mistake that put core's database under root ownership before.
    for package in packages:
        contents = run(["dpkg-deb", "--contents", str(package)]).stdout
        for directory in ("./var/lib/homebase", "./srv/homebase"):
            if directory in contents:
                die(f"{package.name} ships {directory}; it must be created by postinst")
    ok("data directories are created by postinst, not shipped")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--version", default=os.environ.get("HOMEBASE_VERSION", "0.0.0~dev"))
    args = parser.parse_args()

    for tool in ("dpkg-deb", "fakeroot"):
        if not shutil.which(tool):
            die(f"{tool} is not installed.\n    sudo apt install dpkg-dev fakeroot")

    binaries = REPO_ROOT / "bin"
    dashboard = REPO_ROOT / "dashboard/dist"

    print()
    step(f"Building packages, version {args.version}")

    packages = [
        build_hostd(args.version, binaries),
        build_core(args.version, binaries),
        build_dashboard(args.version, dashboard),
    ]

    verify(packages)

    print()
    ok(f"{len(packages)} packages in {OUT.relative_to(REPO_ROOT)}/")
    return 0


if __name__ == "__main__":
    sys.exit(main())
