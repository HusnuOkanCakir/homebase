#!/usr/bin/env python3
"""Check which disk the installer would clear, before it clears one.

The seed wipes the target disk's partition table before subiquity probes it,
because on the first real laptop Homebase was installed on — a 5400 rpm 1 TB
drive with seven Windows partitions — probing took 91 seconds against a
90-second timeout and the install died with a Python traceback.

That wipe destroys nothing the install was not about to destroy. What it must
never do is pick the *wrong* disk, and it is shell embedded in a YAML file, which
is not somewhere bugs are easy to see. So the selection half is extracted from
the seed that actually ships and run against invented device tables, with the
destructive half replaced by an echo.

Reading it out of the template rather than copying it here is the point: a test
holding its own copy of the logic proves the copy works.

Run with `make test-seed`, or directly.
"""

from __future__ import annotations

import re
import subprocess
import sys
import tempfile
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
SEED = REPO_ROOT / "internal" / "installer" / "user-data.yaml.in"

RED, GREEN, BLUE, RESET = "\033[31m", "\033[32m", "\033[1;34m", "\033[0m"
failures: list[str] = []


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        print(f"{GREEN}  ✓ {description}{RESET}")
    else:
        print(f"{RED}  ✗ {description}{RESET}")
        if detail:
            print(f"    {detail.strip()[:500]}")
        failures.append(description)


def selection_script() -> str:
    """Pull the early-command out of the seed and defuse it.

    The destructive lines become an echo. Everything that decides *which* disk —
    which is the part worth testing — is the shipped text, unmodified.
    """
    text = SEED.read_text()

    block = re.search(r"  early-commands:\n    - \|\n(.*?)\n\n", text, re.S)
    if block is None:
        raise SystemExit("the seed has no early-commands block; this test is stale")

    body = "\n".join(line[6:] for line in block.group(1).split("\n"))

    # Replace the three destructive commands, and nothing else.
    defused = []
    for line in body.split("\n"):
        stripped = line.strip()
        if stripped.startswith(("wipefs ", "sgdisk ", "partprobe ")):
            continue
        if 'echo "homebase: clearing' in stripped:
            line = line.split("echo")[0] + 'echo "WIPE $target"'
        defused.append(line)
    return "\n".join(defused)


def run_against(script: str, findmnt: str, lsblk: str) -> str:
    """Run the selection with invented `findmnt` and `lsblk` output."""
    with tempfile.TemporaryDirectory() as workspace:
        fake = Path(workspace) / "bin"
        fake.mkdir()

        (fake / "findmnt").write_text(f"#!/bin/sh\nprintf '%s' '{findmnt}'\n")
        (fake / "lsblk").write_text(f"#!/bin/sh\ncat <<'INNER'\n{lsblk}\nINNER\n")
        for tool in ("findmnt", "lsblk"):
            (fake / tool).chmod(0o755)

        script_path = Path(workspace) / "selection.sh"
        script_path.write_text(script)

        import os
        result = subprocess.run(
            ["sh", str(script_path)], capture_output=True, text=True,
            env={**os.environ, "PATH": f"{fake}:{os.environ['PATH']}"})
        return (result.stdout + result.stderr).strip()


def main() -> int:
    script = selection_script()

    print(f"\n{BLUE}==> Which disk the installer would clear{RESET}")

    cases = [
        (
            "one internal disk and the installer's USB",
            "/dev/sdb1", "sda 0 disk\nsdb 1 disk\nsr0 1 rom",
            "WIPE /dev/sda",
        ),
        (
            "a USB stick that does not report itself as removable",
            "/dev/sdb1", "sda 0 disk\nsdb 0 disk\nsr0 1 rom",
            "WIPE /dev/sda",
        ),
        (
            "an NVMe machine",
            "/dev/sdb1", "nvme0n1 0 disk\nsdb 1 disk",
            "WIPE /dev/nvme0n1",
        ),
        (
            "booted from an NVMe medium, so the p1 suffix must be stripped",
            "/dev/nvme0n1p1", "nvme0n1 0 disk\nsda 0 disk",
            "WIPE /dev/sda",
        ),
    ]

    for name, findmnt, lsblk, expected in cases:
        out = run_against(script, findmnt, lsblk)
        check(expected in out, f"{name} → {expected}", out)

    print(f"\n{BLUE}==> And when it cannot be sure, it does nothing{RESET}")

    # The rule that matters. A machine with one disk is the machine Homebase is
    # for, and there the target is unambiguous. A machine with two is exactly
    # where a guess could destroy something somebody wanted.
    ambiguous = run_against(script, "/dev/sdc1", "sda 0 disk\nsdb 0 disk\nsdc 1 disk")
    check("WIPE" not in ambiguous,
          "two internal disks: nothing is wiped",
          f"It chose to wipe something: {ambiguous}\n    A slow probe is a worse "
          f"experience. Wiping the wrong disk is a different category of thing.")
    check("2 candidate" in ambiguous, "and it says why", ambiguous)

    empty = run_against(script, "/dev/sdb1", "sdb 1 disk")
    check("WIPE" not in empty, "no internal disk at all: nothing is wiped", empty)

    # The installer's own medium must survive, however it is described.
    for name, findmnt, lsblk in [
        ("removable", "/dev/sdb1", "sdb 1 disk"),
        ("not removable, but mounted at /cdrom", "/dev/sdb1", "sdb 0 disk"),
    ]:
        out = run_against(script, findmnt, lsblk)
        check("WIPE /dev/sdb" not in out,
              f"the installer's own medium is never wiped ({name})", out)

    print()
    if failures:
        print(f"{RED}  ✗ FAIL — {len(failures)} check(s) failed{RESET}")
        return 1
    print(f"{GREEN}  ✓ PASS — the seed clears the right disk, or none at all{RESET}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
