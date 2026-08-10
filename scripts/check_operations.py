#!/usr/bin/env python3
"""Check that hostd's privileged operations still declare what they should.

`go run ./cmd/hostd --describe` prints the operation registry: every privileged
thing this build of Homebase can do, with its risk level and whether it requires
confirmation. That registry is the security surface — the Stage 2 policy engine
will read it to decide what an AI operator may propose, and the documentation is
generated from it.

The properties below are the ones where a quiet change is dangerous. A
confirmation requirement that can be downgraded during a refactor is not a
confirmation requirement, and nothing else in the build would notice: the code
would compile, the tests would pass, and a destructive operation would simply
stop asking.

So the expected values are written down here, away from the code that declares
them. Changing one means changing this file in the same commit, which is the
point — it makes the change visible in review rather than incidental.

Usage:
    go run ./cmd/hostd --describe > operations.json
    python3 scripts/check_operations.py operations.json
"""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

# Operation → (risk, confirmation).
#
# Not every operation is listed. These are the ones where being wrong costs a
# user something they cannot get back, plus the read-only pair as a control: if
# reading the system ever starts demanding confirmation, something is confused.
EXPECTED = {
    "system.reboot": ("high", "explicit"),
    "app.remove_data": ("critical", "explicit"),
    "app.uninstall": ("medium", "required"),
    "app.stop": ("medium", "required"),
    "app.restart": ("medium", "required"),
    "app.install": ("low", "none"),
    "app.list": ("read", "none"),
    "system.get_info": ("read", "none"),
    # Storage. format is the only operation that can destroy data Homebase never
    # created, and remove_location must never quietly become destructive: it
    # unmounts a disk and leaves everything on it alone.
    "storage.format": ("critical", "explicit"),
    "storage.remove_location": ("medium", "required"),
    "storage.unmount": ("medium", "required"),
    "storage.list_disks": ("read", "none"),
    # Backup. restore is the third operation that destroys data irreversibly,
    # and the only one where what it overwrites is usually what somebody is
    # trying to save. preview must stay read-only: it is what a user is shown
    # before agreeing, and an operation that changed something while previewing
    # would make the preview a lie.
    "backup.restore": ("critical", "explicit"),
    "backup.delete": ("medium", "required"),
    "backup.preview": ("read", "none"),
    "backup.verify": ("read", "none"),
    "backup.create": ("low", "none"),
}


def load(source: str | None) -> dict:
    """Read the registry, from a file or by asking hostd for it."""
    if source:
        return json.loads(Path(source).read_text())

    result = subprocess.run(
        ["go", "run", "./cmd/hostd", "--describe"],
        capture_output=True, text=True,
        cwd=Path(__file__).resolve().parents[1],
    )
    if result.returncode != 0:
        print("could not read the operation registry:", file=sys.stderr)
        print(result.stderr.strip()[-800:], file=sys.stderr)
        sys.exit(2)
    return json.loads(result.stdout)


def main() -> int:
    registry = load(sys.argv[1] if len(sys.argv) > 1 else None)
    declared = {op["name"]: op for op in registry["operations"]}

    problems: list[str] = []

    for name, (risk, confirmation) in sorted(EXPECTED.items()):
        operation = declared.get(name)
        if operation is None:
            problems.append(f"{name} is no longer declared")
            continue
        if operation["risk"] != risk:
            problems.append(
                f"{name}: risk is {operation['risk']!r}, expected {risk!r}")
        if operation["confirmation"] != confirmation:
            problems.append(
                f"{name}: confirmation is {operation['confirmation']!r}, "
                f"expected {confirmation!r}")

    # A rule rather than a list, so it also covers operations added later. If
    # something is critical it destroys data irreversibly, and the user has to
    # have said so in a way they could not have done by reflex.
    for operation in declared.values():
        if operation["risk"] == "critical" and operation["confirmation"] != "explicit":
            problems.append(
                f"{operation['name']} is critical but asks only for "
                f"{operation['confirmation']!r} confirmation")

    # Likewise: nothing that only reads should be asking permission. Friction on
    # a safe action teaches people to click through the friction on an unsafe
    # one, which is how a confirmation dialogue stops working.
    for operation in declared.values():
        if operation["risk"] == "read" and operation["confirmation"] != "none":
            problems.append(
                f"{operation['name']} only reads but demands "
                f"{operation['confirmation']!r} confirmation")

    if problems:
        print("The privilege declarations changed in a way that needs a human:\n")
        for problem in problems:
            print(f"  {problem}")
        print("\nIf this is deliberate, update scripts/check_operations.py in the")
        print("same commit and say why in the pull request.")
        return 1

    print(f"{len(declared)} operations declared; "
          f"{len(EXPECTED)} checked against their expected risk and confirmation.")
    for name in sorted(EXPECTED):
        operation = declared[name]
        print(f"  {name:<22} risk={operation['risk']:<9} "
              f"confirm={operation['confirmation']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
