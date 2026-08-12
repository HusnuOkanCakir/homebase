#!/usr/bin/env python3
"""Build an archive, break it five ways, and check each break is caught.

`build-repo.py verify` is the last gate before an archive becomes what a
household's server installs as root. A gate that has never been shown to refuse
anything is not a gate, so this test is mostly about the refusals.

Nothing here needs Go, Node, a VM, or Homebase's real packages. The four `.deb`
files are built empty, from a scratch directory, because what is under test is
the archive around them: the signature, the chain from Release to the index to
the pool, and the four packages having to move together.

Run with `make test-repo`, or directly.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
BUILD_REPO = REPO_ROOT / "scripts" / "build-repo.py"

PACKAGES = ("homebase-hostd", "homebase-core", "homebase-apps", "homebase-dashboard")
VERSION = "0.9.0~test"

RED, GREEN, BLUE, RESET = "\033[31m", "\033[32m", "\033[1;34m", "\033[0m"

failures: list[str] = []


def step(message: str) -> None:
    print(f"\n{BLUE}==> {message}{RESET}")


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        print(f"{GREEN}  ✓ {description}{RESET}")
    else:
        print(f"{RED}  ✗ {description}{RESET}")
        if detail:
            print(f"    {detail.strip()[:600]}")
        failures.append(description)


def need(*tools: str) -> None:
    missing = [t for t in tools if shutil.which(t) is None]
    if missing:
        print(f"{RED}  ✗ missing: {', '.join(missing)}{RESET}")
        print("    Install gnupg, dpkg-dev and apt-utils.")
        sys.exit(1)


def build_repo(*args: str, env: dict[str, str], repo: Path) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(BUILD_REPO), "--repo", str(repo), *args],
        capture_output=True, text=True, env={**os.environ, **env})


def make_key(home: Path, name: str, email: str) -> str:
    """A throwaway signing key. Never a real one — this test breaks things."""
    home.mkdir(parents=True, exist_ok=True)
    home.chmod(0o700)
    env = {**os.environ, "GNUPGHOME": str(home)}
    subprocess.run(
        ["gpg", "--batch", "--quiet", "--passphrase", "", "--pinentry-mode", "loopback",
         "--quick-generate-key", f"{name} <{email}>", "ed25519", "sign", "never"],
        check=True, capture_output=True, env=env)
    listed = subprocess.run(["gpg", "--list-keys", "--with-colons", email],
                            capture_output=True, text=True, check=True, env=env)
    for line in listed.stdout.splitlines():
        if line.startswith("fpr:"):
            return line.split(":")[9]
    raise RuntimeError(f"no key for {email}")


def build_packages(into: Path) -> None:
    """Four empty .debs, named and versioned like the real ones."""
    into.mkdir(parents=True, exist_ok=True)
    for name in PACKAGES:
        architecture = "amd64" if name in ("homebase-hostd", "homebase-core") else "all"
        tree = into / f"{name}.tree"
        (tree / "DEBIAN").mkdir(parents=True, exist_ok=True)
        (tree / "DEBIAN" / "control").write_text(
            f"Package: {name}\n"
            f"Version: {VERSION}\n"
            f"Architecture: {architecture}\n"
            f"Maintainer: Homebase <test@homebase.invalid>\n"
            f"Description: a package that exists only to be indexed\n")
        subprocess.run(
            ["dpkg-deb", "--build", "--root-owner-group", str(tree),
             str(into / f"{name}_{VERSION}_{architecture}.deb")],
            check=True, capture_output=True)
        shutil.rmtree(tree)


def refuses(repo: Path, env: dict, what: str, expected: str, keyring: Path | None = None) -> None:
    """Assert verify fails, and fails for the stated reason."""
    args = ["verify", "--channel", "development"]
    if keyring is not None:
        args += ["--keyring", str(keyring)]
    result = build_repo(*args, env=env, repo=repo)
    output = result.stdout + result.stderr

    check(result.returncode != 0, what,
          "It was accepted. This is the archive a machine would install as root.\n"
          + output)
    if result.returncode != 0:
        check(expected in output, f"  and says why: {expected!r}", output)


def main() -> int:
    need("gpg", "gpgv", "dpkg-deb", "apt-ftparchive")

    with tempfile.TemporaryDirectory(prefix="homebase-repo-") as workspace:
        work = Path(workspace)
        gnupg = work / "gnupg"

        step("A signed archive")
        key = make_key(gnupg, "Homebase Test Archive", "archive@homebase.invalid")
        attacker = make_key(gnupg, "Somebody Else", "attacker@homebase.invalid")
        env = {"GNUPGHOME": str(gnupg)}

        packages = work / "packages"
        build_packages(packages)

        good = work / "repo"
        published = build_repo("publish", "--version", VERSION,
                               "--channel", "development",
                               "--packages", str(packages), "--key", key,
                               env=env, repo=good)
        if published.returncode != 0:
            print(published.stdout + published.stderr)
            return 1
        check(True, f"{VERSION} published to development")

        result = build_repo("verify", "--channel", "development", "--version", VERSION,
                            env=env, repo=good)
        check(result.returncode == 0, "a good archive verifies",
              result.stdout + result.stderr)

        # --- and now the refusals -------------------------------------------------

        def fresh() -> Path:
            broken = work / "broken"
            shutil.rmtree(broken, ignore_errors=True)
            shutil.copytree(good, broken)
            return broken

        step("An archive nobody should be able to publish")

        # A package rebuilt under a version that was already indexed. The one
        # promotion is designed to catch, checked again on the finished archive.
        broken = fresh()
        artifact = next(broken.glob("pool/**/homebase-core_*.deb"))
        artifact.write_bytes(artifact.read_bytes() + b"different")
        refuses(broken, env, "a package rebuilt under a published version is refused",
                "does not hash to what the index says")

        # An index edited by hand. Release names its SHA-256, so this is the
        # link that catches anything changed after signing.
        broken = fresh()
        index = broken / "dists/development/main/binary-amd64/Packages"
        index.write_text(index.read_text().replace(f"Version: {VERSION}", "Version: 9.9.9"))
        refuses(broken, env, "an index edited after signing is refused",
                "does not match what Release says it is")

        # Signed, correctly, by the wrong key. The signature verifying is not
        # the question — whose signature it is, is.
        broken = fresh()
        build_repo("sign", "--channel", "development", "--key", attacker,
                   env=env, repo=broken)
        refuses(broken, env, "an archive signed by another key is refused",
                "is not signed by the key",
                keyring=good / "homebase-archive-keyring.gpg")

        # Three packages out of four. They depend on each other with
        # (= version), so this is an update every machine would refuse — better
        # found here than by every household at once.
        broken = fresh()
        kept = [s for s in index_of(broken).split("\n\n")
                if "Package: homebase-apps" not in s]
        (broken / "dists/development/main/binary-amd64/Packages").write_text("\n\n".join(kept))
        build_repo("sign", "--channel", "development", "--key", key, env=env, repo=broken)
        refuses(broken, env, "a channel missing one of the four packages is refused",
                "does not offer homebase-apps")

        # An expired index is one apt will not read, so publishing it ships a
        # release nobody can install.
        broken = fresh()
        release = broken / "dists/development/Release"
        release.write_text(release.read_text().replace(
            release.read_text().split("Valid-Until: ")[1].split("\n")[0],
            "Mon, 01 Jan 2001 00:00:00 +0000"))
        build_repo_sign_release(broken, key, env)
        refuses(broken, env, "an expired index is refused", "expired")

        step("Asking for a version the channel does not have")
        result = build_repo("verify", "--channel", "development", "--version", "1.2.3",
                            env=env, repo=good)
        check(result.returncode != 0,
              "verifying the wrong version is refused",
              result.stdout + result.stderr)

        check_what_a_tag_releases(work)

    print()
    if failures:
        print(f"{RED}  ✗ FAIL — {len(failures)} check(s) failed{RESET}")
        return 1
    print(f"{GREEN}  ✓ PASS — the archive verifier refuses every archive it should{RESET}")
    return 0


def check_what_a_tag_releases(work: Path) -> None:
    """The tag decides the version and the channel, and some tags must not exist."""
    step("What a tag releases")

    changelog = work / "CHANGELOG.md"
    changelog.write_text("# Changelog\n\n## [Unreleased]\n\n## [0.2.0-alpha.1]\n\nThings.\n")

    def plan(tag: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            [sys.executable, str(REPO_ROOT / "scripts" / "release.py"), "plan",
             "--tag", tag, "--changelog", str(changelog), "--github-output", ""],
            capture_output=True, text=True)

    result = plan("v0.2.0-alpha.1")
    check(result.returncode == 0, "a prerelease tag is a release",
          result.stdout + result.stderr)

    # The conversion that stops every machine treating the alpha as newer than
    # the release it precedes. `-` is not special to dpkg; `~` sorts before
    # nothing at all.
    check("version: 0.2.0~alpha.1" in result.stdout,
          "and its Debian version uses a tilde, so it sorts before 0.2.0",
          result.stdout)
    check("channel: alpha" in result.stdout, "and it goes to alpha", result.stdout)

    if shutil.which("dpkg"):
        ordered = subprocess.run(
            ["dpkg", "--compare-versions", "0.2.0~alpha.1", "lt", "0.2.0"]).returncode == 0
        check(ordered, "and dpkg agrees that 0.2.0~alpha.1 comes before 0.2.0")

    # Stable is reached by promoting something already tested, never by tagging.
    result = plan("v0.2.0")
    check(result.returncode != 0, "a tag cannot publish straight to stable",
          result.stdout + result.stderr)
    check("promot" in (result.stdout + result.stderr),
          "  and says to promote a beta instead", result.stdout + result.stderr)

    result = plan("v0.9.9-alpha.1")
    check(result.returncode != 0, "a version with no changelog entry is refused",
          result.stdout + result.stderr)

    for tag in ("0.2.0-alpha.1", "v0.2", "vlatest", "v0.2.0-gamma.1", "v0.2.0-alpha"):
        result = plan(tag)
        check(result.returncode != 0, f"{tag!r} is not a release tag",
              result.stdout + result.stderr)


def index_of(repo: Path) -> str:
    return (repo / "dists/development/main/binary-amd64/Packages").read_text()


def build_repo_sign_release(repo: Path, key: str, env: dict) -> None:
    """Re-sign Release in place, without rewriting it.

    `build-repo.py sign` regenerates Release from the index, which would undo the
    edit being tested. This signs the file as it stands — which is exactly what
    somebody with the key and a stale index would produce.
    """
    directory = repo / "dists/development"
    for output in ("InRelease", "Release.gpg"):
        (directory / output).unlink(missing_ok=True)
    common = ["gpg", "--batch", "--yes", "--pinentry-mode", "loopback",
              "--local-user", key, "--digest-algo", "SHA256"]
    full = {**os.environ, **env}
    subprocess.run(common + ["--clearsign", "--output", str(directory / "InRelease"),
                             str(directory / "Release")],
                   check=True, capture_output=True, env=full)
    subprocess.run(common + ["--armor", "--detach-sign",
                             "--output", str(directory / "Release.gpg"),
                             str(directory / "Release")],
                   check=True, capture_output=True, env=full)


if __name__ == "__main__":
    sys.exit(main())
