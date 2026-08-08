#!/usr/bin/env python3
"""Validate the JSON Schemas and every fixture against them.

Three assertions, in increasing order of usefulness:

1. Each schema is itself a valid JSON Schema 2020-12 document
2. Each `valid-*` fixture is accepted
3. Each `invalid-*` fixture is rejected **by the constraint it was written to test**

The third is the point. Asserting only that an invalid fixture is rejected is a weak
test: a fixture that becomes invalid for an unrelated reason — a typo, a renamed field —
would still pass while the constraint it was protecting quietly stopped working.
schemas/examples/expectations.json names the expected path and keyword for each.

Run via `make validate` or directly.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

try:
    from jsonschema import Draft202012Validator
except ImportError:  # pragma: no cover
    print(
        "jsonschema is not installed. Run `make bootstrap` first.",
        file=sys.stderr,
    )
    sys.exit(2)

SCHEMAS_DIR = Path("schemas")
EXAMPLES_DIR = SCHEMAS_DIR / "examples"
EXPECTATIONS = EXAMPLES_DIR / "expectations.json"


def load_json(path: Path) -> object:
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def pointer(error) -> str:
    """JSON pointer to the value an error is about."""
    return "".join(f"/{part}" for part in error.absolute_path)


class Results:
    def __init__(self) -> None:
        self.passed = 0
        self.failures: list[str] = []

    def ok(self, what: str) -> None:
        self.passed += 1
        print(f"  ok    {what}")

    def fail(self, what: str, why: str) -> None:
        self.failures.append(f"{what}: {why}")
        print(f"  FAIL  {what}")
        for line in why.splitlines():
            print(f"          {line}")


def check_group(name: str, spec: dict, results: Results) -> None:
    schema_path = SCHEMAS_DIR / spec["schema"]
    print(f"\n{name} ({schema_path})")

    schema = load_json(schema_path)

    # 1. The schema must itself be well-formed.
    try:
        Draft202012Validator.check_schema(schema)
        results.ok(f"{schema_path.name} is a valid JSON Schema")
    except Exception as exc:
        results.fail(f"{schema_path.name} is a valid JSON Schema", str(exc))
        return

    validator = Draft202012Validator(schema)
    fixtures = EXAMPLES_DIR / name

    # 2. Valid fixtures must be accepted.
    for filename, why in spec.get("valid", {}).items():
        path = fixtures / filename
        if not path.exists():
            results.fail(filename, "fixture is listed in expectations.json but missing")
            continue

        errors = sorted(validator.iter_errors(load_json(path)), key=str)
        if errors:
            detail = "\n".join(
                f"at {pointer(e) or '<root>'}: {e.message}" for e in errors[:3]
            )
            results.fail(filename, f"expected valid, but:\n{detail}\n({why})")
        else:
            results.ok(f"{filename} is accepted")

    # 3. Invalid fixtures must be rejected by the *expected* constraint.
    for filename, expectation in spec.get("invalid", {}).items():
        path = fixtures / filename
        if not path.exists():
            results.fail(filename, "fixture is listed in expectations.json but missing")
            continue

        errors = list(validator.iter_errors(load_json(path)))

        if not errors:
            results.fail(
                filename,
                f"expected rejection, but the schema accepted it.\n{expectation['why']}",
            )
            continue

        want_path = expectation["path"]
        want_keyword = expectation["keyword"]

        matched = any(
            pointer(e) == want_path and e.validator == want_keyword for e in errors
        )

        if matched:
            results.ok(f"{filename} is rejected by {want_keyword} at {want_path or '<root>'}")
        else:
            got = "\n".join(
                f"{e.validator} at {pointer(e) or '<root>'}: {e.message}"
                for e in errors[:5]
            )
            results.fail(
                filename,
                "rejected, but not by the constraint this fixture tests.\n"
                f"expected: {want_keyword} at {want_path or '<root>'}\n"
                f"actual:\n{got}\n"
                f"({expectation['why']})",
            )


def check_catalogue(results: Results) -> None:
    """Every shipped manifest must satisfy the schema.

    The fixtures prove the schema works; this proves the catalogue does. A
    manifest that ships invalid is an application that silently does not appear
    on somebody's server, and "Jellyfin is missing" is a much harder thing to
    diagnose than "Jellyfin's manifest is invalid, here is why".

    This check earned its place immediately: it caught the Jellyfin manifest on
    the first run, because the schema demanded all-caps environment variable
    names and Jellyfin genuinely uses JELLYFIN_PublishedServerUrl. The schema was
    the thing that was wrong.
    """
    catalogue = Path("app-store")
    manifests = sorted(catalogue.glob("*.json"))

    print(f"\ncatalogue ({catalogue})")

    if not manifests:
        results.ok("no manifests yet")
        return

    schema = load_json(SCHEMAS_DIR / "app-manifest.schema.json")
    validator = Draft202012Validator(schema)

    for path in manifests:
        errors = sorted(validator.iter_errors(load_json(path)), key=str)
        if errors:
            detail = "\n".join(
                f"at {pointer(e) or '<root>'}: {e.message}" for e in errors[:3]
            )
            results.fail(path.name, f"does not satisfy the schema:\n{detail}")
            continue

        # The id is a directory name and an API path segment; hostd rejects a
        # manifest whose id disagrees with its filename, so catching it here
        # saves finding out on a user's machine.
        manifest = load_json(path)
        expected = path.stem
        if manifest.get("id") != expected:
            results.fail(path.name, f"id is {manifest.get('id')!r}, expected {expected!r}")
        else:
            results.ok(f"{path.name} is valid")


def main() -> int:
    if not EXPECTATIONS.exists():
        print(f"{EXPECTATIONS} not found.", file=sys.stderr)
        return 2

    expectations = load_json(EXPECTATIONS)
    results = Results()

    for name, spec in expectations.items():
        if name.startswith("_"):
            continue  # Commentary.
        check_group(name, spec, results)

    check_catalogue(results)

    print()
    if results.failures:
        print(
            f"{len(results.failures)} contract check(s) failed, "
            f"{results.passed} passed.",
            file=sys.stderr,
        )
        return 1

    print(f"Contracts: {results.passed} checks passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
