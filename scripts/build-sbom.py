#!/usr/bin/env python3
"""Write a CycloneDX bill of materials for each Homebase package.

Required by docs/security/update-security.md: every release publishes an SBOM,
because "is this machine affected by the vulnerability announced this morning?"
is a question somebody has to be able to answer without reading our source.

The source of truth is `go version -m <binary>` — the modules the linker
actually put in the binary, with the hashes the module proxy served. Not
`go.mod`, which lists what was asked for rather than what arrived, and not
`go list -m all`, which includes modules needed to *build* and test but never
linked. An SBOM that overstates what is in an artifact is nearly as unhelpful as
one that understates it: every advisory becomes a false alarm, and false alarms
are how people stop reading them.

The dashboard is different in kind. Its dependencies are bundled into static
files at build time, so what ships is a handful of JavaScript rather than a tree
of packages — but the provenance question is the same, so its production
dependencies are listed from the lockfile.

    build-sbom.py --version 0.9.0

Deterministic: given the same inputs it produces byte-identical output, because
ADR-0018 promotes artifacts between channels by checksum and an SBOM that
changed every time it was written would be one more thing that could not be
compared.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import subprocess
import sys
import uuid
from datetime import datetime, timezone
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
OUT = REPO_ROOT / "dist"

# Which binary ends up in which package. hostd is listed even though it is
# expected to have nothing in it — an empty components list is a claim, and a
# missing file is an oversight.
GO_PACKAGES = {
    "homebase-hostd": "hostd",
    "homebase-core": "core",
}


class SBOMError(Exception):
    pass


def ok(message: str) -> None:
    print(f"\033[32m  ✓ {message}\033[0m")


def step(message: str) -> None:
    print(f"\033[1;34m==> {message}\033[0m")


def linked_modules(binary: Path) -> list[dict]:
    """Read the modules the linker put into a binary.

    `go version -m` prints tab-separated records. The `dep` lines are the
    modules that were linked; `mod` is the main module and `build` lines are
    compiler settings, neither of which is a dependency.
    """
    if not binary.exists():
        raise SBOMError(f"{binary} has not been built — run `make go-build` first")

    result = subprocess.run(["go", "version", "-m", str(binary)],
                            capture_output=True, text=True)
    if result.returncode != 0:
        raise SBOMError(f"reading {binary.name}: {result.stderr.strip()[:300]}")

    modules = []
    for line in result.stdout.splitlines():
        fields = line.strip().split("\t")
        if len(fields) < 3 or fields[0] != "dep":
            continue

        component = {
            "type": "library",
            "name": fields[1],
            "version": fields[2],
            "purl": f"pkg:golang/{fields[1]}@{fields[2]}",
            "scope": "required",
        }

        # The go.sum hash, kept as a property rather than as a CycloneDX hash.
        # It is a dirhash over the module tree, not a digest of a file, so
        # recording it under `sha256` would be a lie that tooling would act on.
        if len(fields) > 3 and fields[3].startswith("h1:"):
            component["properties"] = [
                {"name": "go:mod:h1", "value": fields[3]},
            ]
        modules.append(component)

    return sorted(modules, key=lambda c: (c["name"], c["version"]))


def npm_production_dependencies() -> list[dict]:
    """The dashboard's runtime dependencies, from the lockfile.

    Only what ships. `dev: true` covers the build tools, the type definitions
    and the test runner, none of which reach a user's machine — listing them
    would put Playwright in the bill of materials for a web page.
    """
    lockfile = REPO_ROOT / "dashboard" / "package-lock.json"
    if not lockfile.exists():
        raise SBOMError("dashboard/package-lock.json is missing — run `npm ci` first")

    lock = json.loads(lockfile.read_text())
    components = []

    for path, entry in (lock.get("packages") or {}).items():
        if not path.startswith("node_modules/") or entry.get("dev"):
            continue
        name = path[len("node_modules/"):]
        version = entry.get("version")
        if not version:
            continue

        component = {
            "type": "library",
            "name": name,
            "version": version,
            "purl": f"pkg:npm/{name.replace('@', '%40', 1) if name.startswith('@') else name}@{version}",
            "scope": "required",
        }
        if entry.get("integrity"):
            component["properties"] = [
                {"name": "npm:integrity", "value": entry["integrity"]},
            ]
        components.append(component)

    return sorted(components, key=lambda c: (c["name"], c["version"]))


def document(package: str, version: str, components: list[dict]) -> dict:
    """Assemble a CycloneDX 1.5 document.

    The serial number is derived from the contents rather than generated, and
    the timestamp comes from SOURCE_DATE_EPOCH when it is set. Both exist so the
    same inputs produce the same bytes: ADR-0018 promotes an artifact between
    channels by comparing checksums, and a bill of materials that differed on
    every build would be one more thing nobody could compare.
    """
    body = json.dumps(components, sort_keys=True).encode()
    digest = hashlib.sha256(f"{package}@{version}".encode() + body).digest()

    epoch = os.environ.get("SOURCE_DATE_EPOCH")
    when = (datetime.fromtimestamp(int(epoch), tz=timezone.utc) if epoch
            else datetime.now(timezone.utc))

    return {
        "bomFormat": "CycloneDX",
        "specVersion": "1.5",
        "serialNumber": f"urn:uuid:{uuid.UUID(bytes=digest[:16], version=5)}",
        "version": 1,
        "metadata": {
            "timestamp": when.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "tools": [{
                "vendor": "Homebase",
                "name": "build-sbom.py",
                "version": "1",
            }],
            "component": {
                "type": "application",
                "name": package,
                "version": version,
                "purl": f"pkg:deb/homebase/{package}@{version}",
                "licenses": [{"license": {"id": "Apache-2.0"}}],
            },
        },
        "components": components,
    }


def write(package: str, version: str, components: list[dict]) -> Path:
    OUT.mkdir(parents=True, exist_ok=True)
    path = OUT / f"{package}_{version}.cdx.json"
    path.write_text(json.dumps(document(package, version, components),
                               indent=2, sort_keys=False) + "\n")
    return path


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--version", default=os.environ.get("HOMEBASE_VERSION", "0.0.0~dev"))
    parser.add_argument("--binaries", default=str(REPO_ROOT / "bin"))
    args = parser.parse_args()

    try:
        step(f"Bills of materials, version {args.version}")
        binaries = Path(args.binaries)

        for package, binary in GO_PACKAGES.items():
            components = linked_modules(binaries / binary)

            # hostd carries no third-party code, by policy (ADR-0002) and
            # because it is the only thing here that runs as root. CI already
            # checks go.mod; this checks the binary, which is the artifact
            # somebody actually runs. A dependency that arrived through a
            # transitive path would pass the first check and fail this one.
            if package == "homebase-hostd" and components:
                raise SBOMError(
                    "hostd has acquired third-party dependencies:\n    "
                    + "\n    ".join(f"{c['name']} {c['version']}" for c in components)
                    + "\n    The privileged service carries no third-party code. "
                      "See ADR-0002.")

            path = write(package, args.version, components)
            ok(f"{path.name} — {len(components)} "
               + ("components" if components else "components, as it must be"))

        dashboard = npm_production_dependencies()
        path = write("homebase-dashboard", args.version, dashboard)
        ok(f"{path.name} — {len(dashboard)} components")

        # The catalogue is JSON manifests. Nothing is linked into it, and an
        # SBOM listing the container images it *names* would describe software
        # that is downloaded later and may differ by then.
        path = write("homebase-apps", args.version, [])
        ok(f"{path.name} — no linked code")

        return 0

    except SBOMError as exc:
        print(f"\033[31m  ✗ {exc}\033[0m", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
