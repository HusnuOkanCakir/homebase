#!/usr/bin/env python3
"""Repository hygiene checks.

Enforces the parts of .editorconfig that matter most, plus a size guard, without
requiring a Node or Go toolchain. Milestone 0 is deliberately Python-only; see
requirements-dev.txt.

Deliberately narrow: this checks encoding, line endings, trailing whitespace,
final newlines and file size. It is not a formatter and will not become one.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

# Anything larger than this is almost certainly a disk image, an ISO or a
# dependency tree that escaped .gitignore. Committing one is painful to undo,
# because it stays in the history forever.
MAX_FILE_BYTES = 1_000_000

# Verbatim third-party text and binaries are exempt from formatting rules.
EXEMPT_EXACT = {"LICENSE"}
BINARY_SUFFIXES = {
    ".png", ".jpg", ".jpeg", ".gif", ".ico", ".pdf", ".woff", ".woff2",
    ".qcow2", ".img", ".iso", ".deb", ".gz", ".xz", ".zst", ".zip",
}

# Markdown uses two trailing spaces as a hard line break, so trailing-whitespace
# enforcement there would fight the syntax. .editorconfig makes the same exception.
NO_TRAILING_WS_CHECK = {".md"}


def tracked_files() -> list[Path]:
    out = subprocess.run(
        ["git", "ls-files", "-z"],
        capture_output=True, check=True, text=True,
    ).stdout
    return [Path(p) for p in out.split("\0") if p]


def check(path: Path) -> list[str]:
    """Return a list of problems with one file."""
    problems: list[str] = []

    if str(path) in EXEMPT_EXACT or path.suffix.lower() in BINARY_SUFFIXES:
        return problems

    raw = path.read_bytes()

    size = len(raw)
    if size > MAX_FILE_BYTES:
        problems.append(
            f"{size:,} bytes exceeds the {MAX_FILE_BYTES:,} byte limit — "
            f"should this be committed at all?"
        )
        return problems  # No point linting something that large.

    if not raw:
        return problems  # Empty files (.gitkeep) are fine.

    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError as exc:
        problems.append(f"not valid UTF-8: {exc}")
        return problems

    if "\r\n" in text:
        problems.append("contains CRLF line endings; the repository is LF only")

    if not text.endswith("\n"):
        problems.append("missing final newline")
    elif text.endswith("\n\n"):
        problems.append("ends with a blank line")

    if path.suffix.lower() not in NO_TRAILING_WS_CHECK:
        offenders = [
            i for i, line in enumerate(text.split("\n"), start=1)
            if line != line.rstrip()
        ]
        if offenders:
            shown = ", ".join(str(n) for n in offenders[:5])
            more = f" (+{len(offenders) - 5} more)" if len(offenders) > 5 else ""
            problems.append(f"trailing whitespace on line(s) {shown}{more}")

    if "\t" in text and path.suffix in {".yml", ".yaml"}:
        problems.append("contains a tab; YAML must be indented with spaces")

    return problems


def main() -> int:
    failures = 0
    for path in tracked_files():
        if not path.is_file():
            continue  # Deleted or a submodule.
        for problem in check(path):
            print(f"{path}: {problem}")
            failures += 1

    if failures:
        print(f"\n{failures} hygiene problem(s) found.", file=sys.stderr)
        return 1

    print("Hygiene: all tracked files clean.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
