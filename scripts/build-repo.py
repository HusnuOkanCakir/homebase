#!/usr/bin/env python3
"""Build and sign the APT repository Homebase updates from.

ADR-0018 decided the mechanism: a signed Debian archive, with apt doing the
verification rather than code written here. This is what produces it.

The property this script exists to protect is **promotion moves the artifact, it
does not rebuild it**. A rebuild between beta and stable produces a different
artifact from the one that was tested, which means stable ships something nobody
ran. So `promote` never touches a `.deb`: it copies the index entry the source
suite already recorded, after checking the file on disk still hashes to what that
entry says. A rebuilt package is caught rather than promoted.

Each suite lists exactly one version of each package — the one that channel is
currently on. Older versions stay in the pool, because a rollback needs them and
because deleting the artifact somebody is running is not something to do quietly.

    build-repo.py publish --version 0.1.0 --channel development --key <fingerprint>
    build-repo.py promote --version 0.1.0 --from beta --to stable --key <fingerprint>
    build-repo.py keyring --key <fingerprint> --out homebase-archive-keyring.gpg
"""

from __future__ import annotations

import argparse
import gzip
import hashlib
import shutil
import subprocess
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
DEFAULT_REPO = REPO_ROOT / "dist" / "repo"

CHANNELS = ("development", "alpha", "beta", "stable")

# The architecture line in Release. Two of the four packages are `all`; they are
# listed in binary-amd64 alongside the native ones rather than in a binary-all of
# their own, because a machine only ever fetches the index for its own
# architecture and a second index would be one more thing to keep signed.
ARCHITECTURE = "amd64"
COMPONENT = "main"

# How long an index is good for.
#
# From docs/security/update-security.md: an attacker who can serve a stale but
# validly-signed snapshot can hide the existence of a security fix indefinitely.
# Expiry bounds that — it does not eliminate it, and the doc says so. Seven days
# is long enough that a household offline for a week still updates cleanly, and
# short enough that hiding a fix means holding the network for a week.
VALID_DAYS = 7


class RepoError(Exception):
    pass


def run(cmd: list[str], **kwargs) -> subprocess.CompletedProcess:
    result = subprocess.run(cmd, capture_output=True, text=True, **kwargs)
    if result.returncode != 0:
        raise RepoError(f"{cmd[0]} failed: {(result.stderr or result.stdout).strip()[:600]}")
    return result


def step(message: str) -> None:
    print(f"\033[1;34m==> {message}\033[0m")


def ok(message: str) -> None:
    print(f"\033[32m  ✓ {message}\033[0m")


# --- reading and writing Debian control stanzas ----------------------------


def parse_stanzas(text: str) -> list[dict[str, str]]:
    """Parse a Packages file into stanzas.

    Continuation lines (starting with a space) belong to the field above, and
    are kept verbatim — a Description spread over several lines is normal, and
    rewriting it would change the checksum apt verifies.
    """
    stanzas: list[dict[str, str]] = []
    current: dict[str, str] = {}
    field = ""

    for line in text.splitlines():
        if not line.strip():
            if current:
                stanzas.append(current)
                current, field = {}, ""
            continue
        if line[0] in " \t" and field:
            current[field] += "\n" + line
            continue
        key, _, value = line.partition(":")
        field = key.strip()
        current[field] = value.strip()

    if current:
        stanzas.append(current)
    return stanzas


def render_stanzas(stanzas: list[dict[str, str]]) -> str:
    out = []
    for stanza in stanzas:
        for key, value in stanza.items():
            out.append(f"{key}: {value}")
        out.append("")
    return "\n".join(out) + "\n" if out else ""


# --- the pool ---------------------------------------------------------------


def pool_path(repo: Path, deb: Path) -> Path:
    """Where a package file lives in the pool.

    The conventional Debian layout, `pool/main/<first letter>/<source>/`. It is
    followed because tools and humans both expect it, not because apt requires
    it — apt only reads the Filename field.
    """
    name = deb.name.split("_")[0]
    return repo / "pool" / COMPONENT / name[0] / name / deb.name


def add_to_pool(repo: Path, debs: list[Path]) -> None:
    for deb in debs:
        target = pool_path(repo, deb)
        target.parent.mkdir(parents=True, exist_ok=True)

        if target.exists() and sha256(target) != sha256(deb):
            # The same filename with different bytes. Either a rebuild of a
            # version already published, or two different things claiming to be
            # one version — and both mean somebody could be running bytes that
            # were never tested under a name that says they were.
            raise RepoError(
                f"{deb.name} is already in the pool with different contents.\n"
                f"    A published version is immutable. Build a new version "
                f"rather than replacing one that machines may already have.")

        shutil.copy2(deb, target)

        # The bill of materials travels beside the artifact it describes, in the
        # pool rather than in the index. Anyone holding a .deb can then fetch the
        # SBOM for exactly those bytes — which is the only version of the
        # question worth answering, since "what is in Homebase 0.9.0?" depends on
        # which 0.9.0.
        sbom = deb.parent / (deb.name.split("_")[0] + "_" + deb.name.split("_")[1] + ".cdx.json")
        if sbom.exists():
            shutil.copy2(sbom, target.parent / sbom.name)


def scan_pool(repo: Path) -> list[dict[str, str]]:
    """Ask apt-ftparchive to describe everything in the pool.

    Its output is used rather than hand-built control stanzas: it extracts the
    fields dpkg actually recorded, and computes the checksums apt will verify.
    Writing those by hand would mean two implementations of the same format,
    one of which is only exercised here.
    """
    if not shutil.which("apt-ftparchive"):
        raise RepoError("apt-ftparchive is missing — install apt-utils")

    result = run(["apt-ftparchive", "packages", "pool"], cwd=repo)
    return parse_stanzas(result.stdout)


# --- suites -----------------------------------------------------------------


def suite_dir(repo: Path, channel: str) -> Path:
    return repo / "dists" / channel


def packages_path(repo: Path, channel: str) -> Path:
    return suite_dir(repo, channel) / COMPONENT / f"binary-{ARCHITECTURE}" / "Packages"


def read_suite(repo: Path, channel: str) -> list[dict[str, str]]:
    path = packages_path(repo, channel)
    if not path.exists():
        return []
    return parse_stanzas(path.read_text())


def write_suite(repo: Path, channel: str, stanzas: list[dict[str, str]], key: str) -> None:
    packages = packages_path(repo, channel)
    packages.parent.mkdir(parents=True, exist_ok=True)

    body = render_stanzas(stanzas)
    packages.write_bytes(body.encode())

    # Both forms, because apt asks for the compressed one first and falls back.
    # mtime=0 so that publishing the same content twice produces the same bytes:
    # a timestamp in here would make every rebuild look like a change.
    with gzip.GzipFile(str(packages) + ".gz", "wb", mtime=0) as out:
        out.write(body.encode())

    write_release(repo, channel, key)


def newer(a: str, b: str) -> bool:
    """Whether a is a higher version than b, as dpkg orders them.

    dpkg does the comparison rather than Python. Debian ordering has epochs and
    tildes that sort before the empty string, and a string compare gets it
    wrong in ways that would quietly index the wrong version to roll back to.
    """
    return subprocess.run(["dpkg", "--compare-versions", a, "gt", b]).returncode == 0


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def write_release(repo: Path, channel: str, key: str) -> None:
    """Write and sign the Release file.

    This is the whole trust chain in one file: it names the SHA-256 of every
    index, and the signature covers it. Everything else verifies because its
    hash is written here and this file is signed.
    """
    now = datetime.now(timezone.utc)
    directory = suite_dir(repo, channel)

    lines = [
        "Origin: Homebase",
        "Label: Homebase",
        f"Suite: {channel}",
        f"Codename: {channel}",
        f"Architectures: {ARCHITECTURE}",
        f"Components: {COMPONENT}",
        "Description: Homebase — a home server you can actually manage",
        f"Date: {now.strftime('%a, %d %b %Y %H:%M:%S %z')}",
        f"Valid-Until: {(now + timedelta(days=VALID_DAYS)).strftime('%a, %d %b %Y %H:%M:%S %z')}",
        "SHA256:",
    ]

    for path in sorted(directory.rglob("Packages*")):
        relative = path.relative_to(directory)
        lines.append(f" {sha256(path)} {path.stat().st_size} {relative}")

    release = directory / "Release"
    release.write_text("\n".join(lines) + "\n")

    sign(release, key)


def sign(release: Path, key: str) -> None:
    """Sign a Release file, both ways apt accepts.

    InRelease is the signature inline, which is what apt fetches first: one
    request rather than two, and no window in which the index and its signature
    can be fetched from different states of the archive. Release.gpg is the
    detached form, kept because older clients ask for it.
    """
    inline = release.parent / "InRelease"
    detached = release.parent / "Release.gpg"
    inline.unlink(missing_ok=True)
    detached.unlink(missing_ok=True)

    common = ["gpg", "--batch", "--yes", "--pinentry-mode", "loopback",
              "--local-user", key, "--digest-algo", "SHA256"]

    run(common + ["--clearsign", "--output", str(inline), str(release)])
    run(common + ["--armor", "--detach-sign", "--output", str(detached), str(release)])


def export_keyring(key: str, out: Path) -> None:
    """Export the public key in the form `signed-by=` wants.

    Binary rather than armoured: apt accepts both, and the binary form is what
    goes in /usr/share/keyrings, which is the directory that exists so a key can
    be trusted for one source instead of for every package on the machine.
    """
    out.parent.mkdir(parents=True, exist_ok=True)
    result = subprocess.run(["gpg", "--batch", "--yes", "--export", key],
                            capture_output=True)
    if result.returncode != 0 or not result.stdout:
        raise RepoError(f"could not export {key}: {result.stderr.decode()[:400]}")
    out.write_bytes(result.stdout)


# --- commands ---------------------------------------------------------------


def publish(args: argparse.Namespace) -> int:
    repo = Path(args.repo)
    debs = sorted(Path(args.packages).glob(f"*_{args.version}_*.deb"))
    if not debs:
        raise RepoError(f"no packages for version {args.version} in {args.packages}")

    step(f"Publishing {args.version} to {args.channel}")
    add_to_pool(repo, debs)
    for deb in debs:
        ok(deb.name)

    wanted = [s for s in scan_pool(repo) if s.get("Version") == args.version]
    if len(wanted) != len(debs):
        raise RepoError(f"the pool describes {len(wanted)} packages at {args.version}, "
                        f"expected {len(debs)}")

    # The version being replaced stays in the index.
    #
    # Rollback is `apt-get install homebase-core=<previous>`, and apt can only
    # install a version its index lists. An archive that indexes one version per
    # suite is an archive you cannot roll back from without the network and a
    # different channel — which is exactly the situation rollback exists for.
    # Two versions, not a history: keeping every release indexed would make the
    # index grow without bound for the sake of a downgrade nobody performs.
    previous = [s for s in read_suite(repo, args.channel)
                if s.get("Version") != args.version]
    keep: dict[str, dict[str, str]] = {}
    for stanza in previous:
        current = keep.get(stanza["Package"])
        if current is None or newer(stanza["Version"], current["Version"]):
            keep[stanza["Package"]] = stanza
    for stanza in keep.values():
        if (repo / stanza["Filename"]).exists():
            wanted.append(stanza)
            ok(f"{stanza['Package']} {stanza['Version']} stays indexed, to roll back to")

    write_suite(repo, args.channel, wanted, args.key)
    export_keyring(args.key, repo / "homebase-archive-keyring.gpg")

    ok(f"{args.channel} now offers {args.version}, signed")
    return 0


def promote(args: argparse.Namespace) -> int:
    repo = Path(args.repo)

    step(f"Promoting {args.version}: {args.source} → {args.target}")

    stanzas = [s for s in read_suite(repo, args.source) if s.get("Version") == args.version]
    if not stanzas:
        raise RepoError(f"{args.source} does not offer {args.version} — nothing to promote")

    # The check that makes this a promotion rather than a republication. The
    # index entry records the SHA-256 of the file the source channel tested; if
    # the file on disk no longer matches, something was rebuilt underneath a
    # version that had already been through a channel, and promoting it would
    # ship bytes nobody ran.
    for stanza in stanzas:
        artifact = repo / stanza["Filename"]
        if not artifact.exists():
            raise RepoError(f"{stanza['Filename']} is not in the pool any more")
        if sha256(artifact) != stanza.get("SHA256"):
            raise RepoError(
                f"{stanza['Package']} {args.version} has been rebuilt since "
                f"{args.source} indexed it.\n"
                f"    Promotion moves the artifact that was tested. This one is "
                f"different bytes under the same version.")
        ok(f"{stanza['Package']} is the same artifact {args.source} has")

    # The stanzas are copied verbatim, so the target index describes byte for
    # byte what the source index described. Nothing is regenerated.
    write_suite(repo, args.target, stanzas, args.key)
    export_keyring(args.key, repo / "homebase-archive-keyring.gpg")

    ok(f"{args.target} now offers {args.version} — the same artifact, re-indexed")
    return 0


def resign(args: argparse.Namespace) -> int:
    """Re-checksum and re-sign a suite whose index was changed underneath us.

    Needed because Release names the SHA-256 of every index: editing an index
    without coming back through here leaves a signature covering a file that no
    longer exists, which apt correctly refuses. That refusal is a feature, and
    the update test breaks a signature on purpose to prove it happens.
    """
    repo = Path(args.repo)
    if not packages_path(repo, args.channel).exists():
        raise RepoError(f"{args.channel} has no index to sign")

    step(f"Re-signing {args.channel}")
    write_release(repo, args.channel, args.key)
    ok(f"{args.channel} is signed again")
    return 0


def keyring(args: argparse.Namespace) -> int:
    export_keyring(args.key, Path(args.out))
    ok(f"exported {args.key} to {args.out}")
    return 0


# The four packages that make up a Homebase release. A channel offering three of
# them is an archive that will half-upgrade a machine: they depend on each other
# with `(= version)`, so apt would refuse and the user would be told their server
# has an update it cannot install.
RELEASE_PACKAGES = (
    "homebase-hostd", "homebase-core", "homebase-apps", "homebase-dashboard",
)


def verify(args: argparse.Namespace) -> int:
    """Read the archive back the way a machine will, and refuse a bad one.

    This does not share code with the writer on purpose. Everything above builds
    the archive; this opens the finished thing, checks the signature against an
    exported key, and follows the chain the way apt does — Release names the
    hash of the index, the index names the hash of every package. A bug in the
    writer that produced a self-consistent but wrong archive would pass every
    check made while writing it, and fail here.

    It is the last gate before an archive becomes what a household's server
    installs as root, so it runs before publishing, not after.
    """
    repo = Path(args.repo)
    directory = suite_dir(repo, args.channel)
    failures: list[str] = []

    step(f"Verifying {args.channel}")

    inrelease = directory / "InRelease"
    release = directory / "Release"
    if not inrelease.exists() or not release.exists():
        raise RepoError(f"{args.channel} has no signed index at {directory}")

    # The signature first, because nothing below it means anything without it.
    # gpgv rather than gpg: it takes a keyring and verifies against exactly that,
    # with no path by which the ambient keyring can satisfy a signature the
    # shipped key would not.
    keyring_path = Path(args.keyring) if args.keyring else repo / "homebase-archive-keyring.gpg"
    if not keyring_path.exists():
        raise RepoError(f"no keyring at {keyring_path} to verify against")

    for name, cmd in (
        ("InRelease", ["gpgv", "--keyring", str(keyring_path), str(inrelease)]),
        ("Release.gpg", ["gpgv", "--keyring", str(keyring_path),
                         str(directory / "Release.gpg"), str(release)]),
    ):
        result = subprocess.run(cmd, capture_output=True)
        if result.returncode != 0:
            failures.append(f"{name} is not signed by the key in {keyring_path.name}: "
                            + result.stderr.decode(errors="replace").strip()[:300])
        else:
            ok(f"{name} verifies against the shipped key")

    fields = parse_stanzas(release.read_text())
    header = fields[0] if fields else {}

    if header.get("Origin") != "Homebase":
        failures.append(f"Origin is {header.get('Origin')!r}, and the pin on every "
                        f"machine matches o=Homebase")
    if header.get("Suite") != args.channel:
        failures.append(f"Suite says {header.get('Suite')!r} in the {args.channel} index")

    # An expired index is refused by apt, which means a release published with
    # one is a release nobody can install.
    until = header.get("Valid-Until", "")
    try:
        expires = datetime.strptime(until, "%a, %d %b %Y %H:%M:%S %z")
        remaining = expires - datetime.now(timezone.utc)
        if remaining.total_seconds() <= 0:
            failures.append(f"the index expired at {until}")
        else:
            ok(f"the index is good for {remaining.days} more days")
    except ValueError:
        failures.append(f"Valid-Until is not a date apt can read: {until!r}")

    # Release names the hash of every index. Checked here rather than trusted,
    # because this is the link between the one signed file and everything else.
    recorded = {}
    for line in header.get("SHA256", "").splitlines():
        parts = line.split()
        if len(parts) == 3:
            recorded[parts[2]] = (parts[0], int(parts[1]))

    for name in (f"{COMPONENT}/binary-{ARCHITECTURE}/Packages",
                 f"{COMPONENT}/binary-{ARCHITECTURE}/Packages.gz"):
        path = directory / name
        if not path.exists():
            failures.append(f"{name} is named in Release and is not there")
            continue
        if name not in recorded:
            failures.append(f"{name} exists and Release does not name it, so apt "
                            f"would fetch an unverified index")
            continue
        digest, size = recorded[name]
        if sha256(path) != digest or path.stat().st_size != size:
            failures.append(f"{name} does not match what Release says it is")
        else:
            ok(f"{name} matches its entry in Release")

    # And the index names the hash of every package.
    stanzas = read_suite(repo, args.channel)
    offered: dict[str, str] = {}
    for stanza in stanzas:
        artifact = repo / stanza.get("Filename", "")
        if not stanza.get("Filename") or not artifact.exists():
            failures.append(f"{stanza.get('Package')} {stanza.get('Version')} is indexed "
                            f"and its file is not in the pool")
            continue
        if sha256(artifact) != stanza.get("SHA256"):
            failures.append(f"{stanza['Filename']} does not hash to what the index says")
            continue
        current = offered.get(stanza["Package"])
        if current is None or newer(stanza["Version"], current):
            offered[stanza["Package"]] = stanza["Version"]

    if not failures:
        ok(f"all {len(stanzas)} indexed packages are present and match their hashes")

    missing = [name for name in RELEASE_PACKAGES if name not in offered]
    if missing:
        failures.append("the channel does not offer " + ", ".join(missing) +
                        " — they depend on each other with (= version), so apt "
                        "would refuse the upgrade on every machine")

    versions = set(offered.get(name) for name in RELEASE_PACKAGES if name in offered)
    if len(versions) > 1:
        failures.append("the channel offers a mixed set: " +
                        ", ".join(f"{n}={offered[n]}" for n in RELEASE_PACKAGES if n in offered))
    elif versions and not missing:
        ok(f"{args.channel} offers {versions.pop()}, all four packages")

    if args.version and args.version not in {offered.get(n) for n in RELEASE_PACKAGES}:
        failures.append(f"asked to verify {args.version}; the channel offers "
                        + ", ".join(sorted({v for v in offered.values()})))

    if failures:
        raise RepoError(f"{args.channel} is not fit to publish:\n    "
                        + "\n    ".join(failures))

    ok(f"{args.channel} verifies")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--repo", default=str(DEFAULT_REPO),
                        help="where the repository lives")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("publish", help="add a built version to a channel")
    p.add_argument("--version", required=True)
    p.add_argument("--channel", required=True, choices=CHANNELS)
    p.add_argument("--packages", default=str(REPO_ROOT / "dist"))
    p.add_argument("--key", required=True, help="signing key fingerprint or id")
    p.set_defaults(func=publish)

    p = sub.add_parser("promote", help="move a tested artifact to another channel")
    p.add_argument("--version", required=True)
    p.add_argument("--from", dest="source", required=True, choices=CHANNELS)
    p.add_argument("--to", dest="target", required=True, choices=CHANNELS)
    p.add_argument("--key", required=True)
    p.set_defaults(func=promote)

    p = sub.add_parser("sign", help="re-sign a suite after its index changed")
    p.add_argument("--channel", required=True, choices=CHANNELS)
    p.add_argument("--key", required=True)
    p.set_defaults(func=resign)

    p = sub.add_parser("keyring", help="export the public key apt should trust")
    p.add_argument("--key", required=True)
    p.add_argument("--out", required=True)
    p.set_defaults(func=keyring)

    p = sub.add_parser("verify", help="read a channel back the way a machine will")
    p.add_argument("--channel", required=True, choices=CHANNELS)
    p.add_argument("--keyring", default="",
                   help="the exported public key to verify against "
                        "(default: the one in the repository)")
    p.add_argument("--version", default="",
                   help="fail unless this is the version the channel offers")
    p.set_defaults(func=verify)

    args = parser.parse_args()
    try:
        return args.func(args)
    except RepoError as exc:
        print(f"\033[31m  ✗ {exc}\033[0m", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
