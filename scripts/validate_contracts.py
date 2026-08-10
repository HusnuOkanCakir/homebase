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
import re
import sys
from pathlib import Path

try:
    import yaml
    from jsonschema import Draft202012Validator
except ImportError:  # pragma: no cover
    print(
        "jsonschema and PyYAML are needed. Run `make bootstrap` first.",
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


def check_api_routes(results: Results) -> None:
    """Every route core serves must be described in the OpenAPI document, and
    every documented route must exist.

    An OpenAPI file that has drifted from the code is worse than no OpenAPI file:
    it is a contract that reads authoritatively and is wrong. The dashboard is
    generated against it by hand today and the Stage 2 operator will read it to
    learn what it can do, so a documented endpoint that does not exist is an
    operator confidently attempting something impossible.

    Path parameter names are ignored — the code says `{id}` where the document
    says `{app_id}`, and that difference is not drift.
    """
    print("\nAPI routes (api/openapi.yaml vs internal/api/)")

    spec_path = Path("api/openapi.yaml")
    if not spec_path.exists():
        results.fail("api/openapi.yaml", "not found")
        return

    spec = yaml.safe_load(spec_path.read_text())

    documented = set()
    for path, operations in (spec.get("paths") or {}).items():
        for method in operations:
            if method.lower() in {"get", "post", "put", "patch", "delete"}:
                documented.add((method.upper(), anonymise(path)))

    # mux.Handle("POST /api/v1/apps/{id}/install", …) and the HandleFunc form.
    route = re.compile(r'mux\.Handle(?:Func)?\(\s*"([A-Z]+) (/api/v1[^"]*)"')

    implemented = set()
    for source in sorted(Path("internal/api").glob("*.go")):
        if source.name.endswith("_test.go"):
            continue
        for method, path in route.findall(source.read_text()):
            implemented.add((method, anonymise(path[len("/api/v1"):] or "/")))

    if not implemented:
        results.fail("route discovery", "found no routes in internal/api/; the pattern is wrong")
        return

    # Routes deliberately outside the documented contract.
    exempt = {
        # First-run setup and sign-in are described in the docs rather than here
        # while the auth surface is still settling.
        ("GET", "/setup"), ("POST", "/setup"),
        ("POST", "/auth/login"), ("POST", "/auth/logout"), ("GET", "/auth/me"),
    }

    undocumented = sorted(implemented - documented - exempt)
    if undocumented:
        results.fail(
            "every served route is documented",
            "these exist in core but not in openapi.yaml:\n"
            + "\n".join(f"{m} /api/v1{p}" for m, p in undocumented),
        )
    else:
        results.ok(f"all {len(implemented)} served routes are documented")

    # Documented but absent. Contract-ahead-of-implementation is legitimate for
    # areas not built yet, so this lists only the ones claimed as implemented.
    built = ("/health", "/system", "/jobs", "/events", "/apps")
    missing = sorted(
        route for route in documented - implemented
        if any(route[1].startswith(prefix) for prefix in built)
    )
    if missing:
        results.fail(
            "every documented route in a built area exists",
            "these are documented but not served:\n"
            + "\n".join(f"{m} /api/v1{p}" for m, p in missing),
        )
    else:
        results.ok("no documented route in a built area is missing")


def anonymise(path: str) -> str:
    """Replace path parameter names, which the code and the document spell differently."""
    return re.sub(r"\{[^}]*\}", "{}", path)


def check_installer_seed(results: Results) -> None:
    """The autoinstall template must be YAML, once its placeholders are filled.

    Rendering happens in Go, and Go has no YAML parser here by choice. The
    installer VM test settles the question definitively by handing the result to
    Ubuntu, but it takes fifteen minutes — and an indentation slip in this file
    is a machine that stops halfway through an installation with a question on a
    screen nobody is watching. Worth catching in a second.
    """
    print("\nInstaller seed (internal/installer/user-data.yaml.in)")

    template = Path("internal/installer/user-data.yaml.in")
    if not template.exists():
        results.fail(str(template), "not found")
        return

    text = template.read_text()

    # The same substitutions homebasectl makes, with values shaped like the
    # real ones. Anything left over is a placeholder nobody fills in.
    filled = (
        text.replace("@HOSTNAME@", "homebase")
        .replace("@LOCALE@", "en_GB.UTF-8")
        .replace("@KEYBOARD@", "gb")
        .replace("@INSTALL_SSH@", "true")
        .replace("@AUTHORIZED_KEYS@", "\n      - ssh-ed25519 AAAAExample a@b")
        .replace("@SSH_FIREWALL@", "ufw allow 22/tcp")
        .replace("@SEED_LABEL@", "CIDATA")
        .replace("@VERSION@", "0.0.0")
        .replace("@UBUNTU_RELEASE@", "24.04")
    )

    leftover = re.findall(r"@[A-Z][A-Z0-9_]*@", filled)
    if leftover:
        results.fail(
            "every placeholder is filled",
            f"nothing fills {', '.join(sorted(set(leftover)))} — it would reach a "
            f"user's disk as literal text",
        )
        return

    try:
        parsed = yaml.safe_load(filled)
    except yaml.YAMLError as exc:
        results.fail("the rendered seed is valid YAML", str(exc)[:400])
        return

    autoinstall = (parsed or {}).get("autoinstall")
    if not isinstance(autoinstall, dict):
        results.fail("the seed has an autoinstall section", f"got {type(autoinstall)}")
        return

    results.ok("the rendered seed is valid YAML")

    for key in ("version", "identity", "storage", "late-commands", "shutdown"):
        if key not in autoinstall:
            results.fail(f"autoinstall has {key}", "missing")
        else:
            results.ok(f"autoinstall has {key}")


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
    check_api_routes(results)
    check_installer_seed(results)

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
