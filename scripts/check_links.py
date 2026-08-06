#!/usr/bin/env python3
"""Verify that every relative Markdown link points at something that exists.

Broken internal links are the failure mode that actually bites: someone renames
docs/security/threat-model.md, and six other pages quietly stop working. External
link rot is a different problem with a different cadence — it is checked on a
schedule, not on every pull request, because a third-party outage should never
block a merge.

Checks relative links and images, plus anchors within the repository. Skips
external URLs, mailto:, and pure fragments.
"""

from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path
from urllib.parse import unquote

# [text](target) and ![alt](target), ignoring an optional "title" after the target.
LINK_RE = re.compile(r"!?\[[^\]]*\]\(\s*<?([^)>\s]+)>?(?:\s+\"[^\"]*\")?\s*\)")

# [text]: target  — reference-style link definitions.
REFDEF_RE = re.compile(r"^\s*\[[^\]]+\]:\s*(\S+)", re.MULTILINE)

SKIP_PREFIXES = ("http://", "https://", "mailto:", "tel:", "#", "data:")

# Fenced code blocks contain example links that are not meant to resolve.
FENCE_RE = re.compile(r"^```.*?^```", re.MULTILINE | re.DOTALL)


def markdown_files() -> list[Path]:
    out = subprocess.run(
        ["git", "ls-files", "-z", "*.md"],
        capture_output=True, check=True, text=True,
    ).stdout
    return [Path(p) for p in out.split("\0") if p]


def strip_code(text: str) -> str:
    """Blank out fenced blocks, preserving line count for accurate numbering."""
    def blank(match: re.Match[str]) -> str:
        return "\n" * match.group(0).count("\n")
    return FENCE_RE.sub(blank, text)


def heading_anchors(path: Path) -> set[str]:
    """Approximate GitHub/MkDocs heading slugs for a Markdown file."""
    anchors: set[str] = set()
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.startswith("#"):
            continue
        title = line.lstrip("#").strip()
        slug = re.sub(r"[^\w\s-]", "", title.lower())
        anchors.add(re.sub(r"[\s_]+", "-", slug).strip("-"))
    return anchors


def main() -> int:
    repo = Path(".").resolve()
    failures = 0

    for path in markdown_files():
        text = strip_code(path.read_text(encoding="utf-8"))
        targets = LINK_RE.findall(text) + REFDEF_RE.findall(text)

        for target in targets:
            if target.startswith(SKIP_PREFIXES):
                continue

            file_part, _, anchor = target.partition("#")
            if not file_part:
                continue

            resolved = (path.parent / unquote(file_part)).resolve()

            try:
                resolved.relative_to(repo)
            except ValueError:
                print(f"{path}: link escapes the repository -> {target}")
                failures += 1
                continue

            if not resolved.exists():
                print(f"{path}: broken link -> {target}")
                failures += 1
                continue

            if anchor and resolved.suffix == ".md":
                if anchor.lower() not in heading_anchors(resolved):
                    print(f"{path}: no such heading -> {target}")
                    failures += 1

    if failures:
        print(f"\n{failures} broken internal link(s).", file=sys.stderr)
        return 1

    print("Links: all internal Markdown links resolve.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
