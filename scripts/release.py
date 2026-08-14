#!/usr/bin/env python3
"""Work out what a tag releases, and refuse the tags that should not exist.

Three decisions live here rather than in a workflow file, because each of them
is the kind of thing that is wrong once and then shipped.

**A tag cannot reach stable.** `docs/release/process.md` says stable is only ever
reached by promoting an artifact that was already tested somewhere else. That is
a rule about what people can do in a hurry at eleven at night, so it is enforced
by a program rather than written down: there is no tag that publishes to stable.

**A semver prerelease is not a Debian version.** `0.2.0-alpha.1` sorts *after*
`0.2.0` in Debian ordering, because `-` is not special and `a` beats the empty
string. Every machine would then treat the alpha as newer than the release it
precedes, and refuse the real thing as a downgrade. `~` is the character that
sorts before nothing at all, so the Debian version is `0.2.0~alpha.1`.

**A release with no changelog entry is not a release.** The version has to exist
in `CHANGELOG.md` before it exists as an artifact — which is step 2 of the
documented process, and this is what makes skipping it fail loudly rather than
quietly.

    release.py plan --tag v0.2.0-alpha.1
"""

from __future__ import annotations

import argparse
import os
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]

# The channels a tag may publish to. `stable` is deliberately absent.
TAG_CHANNELS = {"dev": "development", "alpha": "alpha", "beta": "beta"}

# v<major>.<minor>.<patch>[-<identifier>.<n>]
TAG = re.compile(r"^v(\d+)\.(\d+)\.(\d+)(?:-([a-z]+)\.(\d+))?$")

RED, GREEN = "\033[31m", "\033[32m"
RESET = "\033[0m"


class ReleaseError(Exception):
    pass


def plan(tag: str, changelog: Path) -> dict[str, str]:
    match = TAG.match(tag)
    if not match:
        raise ReleaseError(
            f"{tag!r} is not a release tag.\n"
            f"    Expected v<major>.<minor>.<patch> with an optional "
            f"-alpha.N, -beta.N or -dev.N.")

    major, minor, patch, identifier, number = match.groups()
    semver = f"{major}.{minor}.{patch}" + (f"-{identifier}.{number}" if identifier else "")

    if identifier is None:
        raise ReleaseError(
            f"{tag} would publish a stable release directly.\n"
            f"    Stable is only ever reached by promoting an artifact that has "
            f"already been through beta — see docs/release/process.md. Tag "
            f"v{major}.{minor}.{patch}-beta.1 and promote it.")

    channel = TAG_CHANNELS.get(identifier)
    if channel is None:
        raise ReleaseError(
            f"{tag} names a channel Homebase does not publish: {identifier!r}.\n"
            f"    The choices are dev, alpha and beta.")

    # `~` rather than `-`: see the note at the top. This is the one place the two
    # version formats are converted, so there is one place to be wrong.
    debian = f"{major}.{minor}.{patch}~{identifier}.{number}"

    entry = f"## [{semver}]"
    if entry not in changelog.read_text(encoding="utf-8"):
        raise ReleaseError(
            f"CHANGELOG.md has no {entry} section.\n"
            f"    Move Unreleased into {semver} before tagging. A release with "
            f"nothing written about it is one nobody can decide whether to install.")

    return {"version": debian, "semver": semver, "channel": channel, "tag": tag}


def main() -> int:
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("plan", help="what a tag releases, and where")
    p.add_argument("--tag", required=True)
    p.add_argument("--changelog", default=str(REPO_ROOT / "CHANGELOG.md"))
    # GitHub Actions reads job outputs out of a file rather than out of stdout.
    p.add_argument("--github-output", default=os.environ.get("GITHUB_OUTPUT", ""))

    args = parser.parse_args()
    try:
        decided = plan(args.tag, Path(args.changelog))
    except ReleaseError as exc:
        print(f"{RED}  ✗ {exc}{RESET}", file=sys.stderr)
        return 1

    for key, value in decided.items():
        print(f"{GREEN}  ✓ {key}: {value}{RESET}")

    if args.github_output:
        with open(args.github_output, "a", encoding="utf-8") as handle:
            for key, value in decided.items():
                handle.write(f"{key}={value}\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
