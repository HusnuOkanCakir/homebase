#!/usr/bin/env python3
"""A real signed repository, and a machine that updates from it.

Milestone 8's mechanism, end to end: `scripts/build-repo.py` publishes a signed
APT archive, a machine is pointed at it, and it finds the newer version.

Two of the checks here are the point of the whole design rather than coverage of
it. **A tampered index must be refused** — that is the only reason the archive is
signed at all, and a test that never breaks a signature has not tested one.
**A package Homebase does not ship must not be installable from Homebase's
origin** — because `Signed-By` binds a key to a source, not to package names, so
without the pin a compromised signing key would replace anything on the machine
rather than only the four packages ADR-0018 intends.

Run with `make vm-test-update`.
"""

from __future__ import annotations

import gzip
import http.server
import json
import os
import shutil
import socket
import subprocess
import sys
import threading
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from vmctl import (  # noqa: E402
    VM,
    VMError,
    apt,
    collect_logs,
    create,
    destroy,
    fail,
    info,
    ok,
    ssh,
    start,
    step,
    upload,
    wait_for_boot_complete,
    wait_for_ssh,
)

REPO_ROOT = Path(__file__).resolve().parents[2]
VM_NAME = "homebase-update"
SOCKET = "/run/homebase/hostd.sock"
KEYRING = "/usr/share/keyrings/homebase-archive-keyring.gpg"

# The host, as seen from inside a QEMU user-mode network.
HOST_FROM_GUEST = "10.0.2.2"

OLD = "0.8.0"
NEW = "0.9.0"


class TestFailure(Exception):
    pass


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        ok(description)
    else:
        raise TestFailure(f"{description}\n    {detail}" if detail else description)


# --- talking to hostd ---------------------------------------------------------


def op(vm: VM, name: str, params: dict | None = None, confirmed: bool = False,
       timeout: int = 180) -> tuple[int, dict]:
    cmd = ["sudo", "-u", "homebase", "curl", "--silent", "--show-error",
           "--max-time", str(timeout), "--unix-socket", SOCKET,
           "-w", "\\n%{http_code}", "-X", "POST",
           "-H", "Content-Type: application/json",
           "-d", json.dumps(params or {})]
    if confirmed:
        cmd += ["-H", "X-Homebase-Confirmed: true"]
    cmd.append(f"http://hostd/v1/op/{name}")

    result = ssh(vm, cmd, check=False, timeout=timeout + 60)
    output = result.stdout.strip()
    if not output:
        raise TestFailure(f"{name} returned nothing: {result.stderr.strip()[:300]}")

    parts = output.rsplit("\n", 1)
    status = int(parts[1]) if len(parts) == 2 else int(output)
    body = parts[0] if len(parts) == 2 else ""
    try:
        return status, json.loads(body) if body else {}
    except json.JSONDecodeError as exc:
        raise TestFailure(f"{name} did not return JSON: {exc}\n    {body[:300]!r}") from exc


def must(vm: VM, name: str, params: dict | None = None, confirmed: bool = False,
         timeout: int = 180) -> dict:
    status, body = op(vm, name, params, confirmed, timeout)
    if status != 200:
        raise TestFailure(f"{name} returned {status}\n    {json.dumps(body, indent=4)}")
    return body.get("result", body)


# --- the repository -----------------------------------------------------------


class Archive:
    """A signed APT repository, served over HTTP from this machine."""

    def __init__(self, root: Path) -> None:
        self.root = root
        self.repo = root / "repo"
        self.gnupg = root / "gnupg"
        self.key = ""

        # A second key, belonging to nobody Homebase trusts. It exists so the
        # tamper test can produce a *correctly signed* archive that is signed by
        # the wrong hands — which is the shape of the attack, rather than an
        # unsigned archive that apt would refuse for a simpler reason.
        self.attacker_key = ""
        self.port = 0
        self._server: http.server.ThreadingHTTPServer | None = None

    def env(self) -> dict:
        return {**os.environ, "GNUPGHOME": str(self.gnupg)}

    def create_keys(self) -> None:
        step("Making a signing key, and one belonging to somebody else")
        self.gnupg.mkdir(parents=True, exist_ok=True)
        self.gnupg.chmod(0o700)

        # Throwaway keys, generated per run. The real one lives in a GitHub
        # environment that ordinary CI cannot reach; what is being tested here
        # is that verification happens, not which key does it.
        self.key = self._generate("Homebase Test Archive <test@homebase.invalid>")
        self.attacker_key = self._generate("Not Homebase <attacker@homebase.invalid>")
        ok(f"archive key {self.key[-16:]}, attacker key {self.attacker_key[-16:]}")

    def _generate(self, identity: str) -> str:
        run = subprocess.run(
            ["gpg", "--batch", "--yes", "--passphrase", "", "--pinentry-mode", "loopback",
             "--quick-generate-key", identity, "ed25519", "sign", "never"],
            capture_output=True, text=True, env=self.env(),
        )
        if run.returncode != 0:
            raise VMError(f"could not generate a key for {identity}", run.stderr[-400:])

        listed = subprocess.run(
            ["gpg", "--list-secret-keys", "--with-colons", identity],
            capture_output=True, text=True, env=self.env())
        for line in listed.stdout.splitlines():
            if line.startswith("fpr:"):
                return line.split(":")[9]
        raise VMError(f"no key after generating one for {identity}")

    def resign(self, key: str, channel: str = "stable") -> None:
        result = subprocess.run(
            [sys.executable, str(REPO_ROOT / "scripts" / "build-repo.py"),
             "--repo", str(self.repo), "sign", "--channel", channel, "--key", key],
            capture_output=True, text=True, env=self.env())
        if result.returncode != 0:
            raise VMError("re-signing failed", (result.stdout + result.stderr)[-400:])

    def build(self, version: str) -> None:
        step(f"Building packages ({version})")
        result = subprocess.run(["make", "packages", f"VERSION={version}"],
                                cwd=REPO_ROOT, capture_output=True, text=True)
        if result.returncode != 0:
            raise VMError("make packages failed", (result.stderr or result.stdout)[-600:])
        ok(f"four packages at {version}")

    def publish(self, version: str, channel: str = "stable") -> None:
        result = subprocess.run(
            [sys.executable, str(REPO_ROOT / "scripts" / "build-repo.py"),
             "--repo", str(self.repo), "publish",
             "--version", version, "--channel", channel, "--key", self.key],
            capture_output=True, text=True, env=self.env(),
        )
        if result.returncode != 0:
            raise VMError(f"publishing {version} failed",
                          (result.stdout + result.stderr)[-600:])
        ok(f"{channel} offers {version}, signed")

    def keyring(self) -> Path:
        out = self.root / "homebase-archive-keyring.gpg"
        result = subprocess.run(
            [sys.executable, str(REPO_ROOT / "scripts" / "build-repo.py"),
             "keyring", "--key", self.key, "--out", str(out)],
            capture_output=True, text=True, env=self.env(),
        )
        if result.returncode != 0:
            raise VMError("exporting the keyring failed", result.stderr[-400:])
        return out

    def serve(self) -> None:
        """Serve the archive, the way a real one would be served."""
        self.port = free_port()
        directory = str(self.root)

        class Handler(http.server.SimpleHTTPRequestHandler):
            def __init__(self, *args, **kwargs):
                super().__init__(*args, directory=directory, **kwargs)

            def log_message(self, *args):  # noqa: D102 - quiet
                pass

        self._server = http.server.ThreadingHTTPServer(("0.0.0.0", self.port), Handler)
        threading.Thread(target=self._server.serve_forever, daemon=True).start()
        ok(f"serving the archive on port {self.port}")

    def url(self) -> str:
        return f"http://{HOST_FROM_GUEST}:{self.port}/repo"

    def stop(self) -> None:
        if self._server is not None:
            self._server.shutdown()


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("", 0))
        return int(s.getsockname()[1])


# --- the machine --------------------------------------------------------------


def install_homebase(vm: VM, version: str) -> None:
    step(f"Installing Homebase {version}")

    result = apt(vm, "update -qq", timeout=600)
    if result.returncode != 0:
        raise TestFailure("apt-get update failed\n" + (result.stdout + result.stderr)[-400:])

    debs = sorted((REPO_ROOT / "dist").glob(f"*_{version}_*.deb"))
    for deb in debs:
        upload(vm, deb, f"/tmp/{deb.name}")
    names = " ".join(f"/tmp/{d.name}" for d in debs)

    result = apt(vm, f"install -y -qq {names}", timeout=1200)
    if result.returncode != 0:
        raise TestFailure("installing failed\n" + (result.stdout + result.stderr)[-600:])

    for _ in range(30):
        if ssh(vm, ["systemctl", "is-active", "homebase-core.service"],
               check=False).stdout.strip() == "active":
            break
        time.sleep(1)
    else:
        raise TestFailure("core did not start")
    ok(f"Homebase {version} is running")


def trust_the_archive(vm: VM, archive: Archive) -> None:
    step("Telling the machine which key to trust")

    # Overwriting the keyring the package shipped, rather than adding a second
    # one. The product has exactly one place a trusted key can live, and a test
    # that invented a second would be testing an arrangement no server has.
    keyring = archive.keyring()
    upload(vm, keyring, "/tmp/keyring.gpg")
    ssh(vm, ["sudo", "install", "-m", "0644", "/tmp/keyring.gpg", KEYRING])
    ok(f"{KEYRING} holds the test archive's key")


# --- the checks ---------------------------------------------------------------


def verify_configuring_a_channel(vm: VM, archive: Archive) -> None:
    step("Pointing the machine at the archive")

    status = must(vm, "update.status")
    check(status.get("channel") == "",
          "before anything is configured, it claims no channel")

    result = must(vm, "update.configure",
                  {"channel": "stable", "origin": archive.url()}, confirmed=True)
    check(result.get("reachable") is True,
          f"the archive answers and its signature verifies ({archive.url()})",
          result.get("detail", ""))

    status = must(vm, "update.status")
    check(status.get("channel") == "stable",
          f"and the machine now reports the stable channel ({status.get('channel')})")
    check(status.get("origin") == archive.url(),
          f"read back from apt's own file, not from a setting ({status.get('origin')})")


def verify_a_channel_that_does_not_exist_is_refused(vm: VM, archive: Archive) -> None:
    step("Channels Homebase does not publish")

    for channel in ("nightly", "stable\nSuites: evil", ""):
        status, body = op(vm, "update.configure",
                          {"channel": channel, "origin": archive.url()}, confirmed=True)
        check(status == 400,
              f"{channel!r} is refused ({status})",
              json.dumps(body)[:300])

    # And the refusal did not leave the machine pointed somewhere else on the
    # way. A validation that writes first and checks afterwards is not one.
    current = must(vm, "update.status")
    check(current.get("channel") == "stable",
          "and the machine is still on the channel it was")


def verify_finding_an_update(vm: VM, archive: Archive) -> None:
    step("Finding a newer version")

    result = must(vm, "update.check")
    check(result.get("reachable") is True, "the archive is reachable",
          result.get("detail", ""))
    check(result.get("update_available") is False,
          f"and there is nothing newer than {OLD} yet "
          f"(current {result.get('current')}, available {result.get('available')})")

    archive.build(NEW)
    archive.publish(NEW)

    result = must(vm, "update.check")
    check(result.get("update_available") is True,
          f"after publishing {NEW}, the machine finds it",
          json.dumps(result))
    check(result.get("available") == NEW,
          f"and names the version ({result.get('available')})")
    check(result.get("current") == OLD,
          f"while still running {result.get('current')}")


def verify_a_tampered_archive_is_refused(vm: VM, archive: Archive) -> None:
    """The only reason any of this is signed.

    A test suite that never breaks a signature has not tested signature
    verification; it has tested that a correct archive works, which is the case
    nobody is attacked by.
    """
    step("An archive somebody has tampered with")

    suite = archive.repo / "dists" / "stable"
    binary = suite / "main" / "binary-amd64"
    touched = [binary / "Packages", binary / "Packages.gz",
               suite / "Release", suite / "InRelease", suite / "Release.gpg"]
    original = {path: path.read_bytes() for path in touched if path.exists()}

    try:
        # Somebody who controls the archive, holding a key that is not
        # Homebase's. They edit the index and sign it, because an unsigned
        # archive is refused before anything is read.
        #
        # Two details here were found by this test failing, and both are the
        # difference between checking verification and assuming it.
        #
        # The index has to be altered in *both* forms. apt prefers the
        # compressed one, so tampering only with `Packages` left it fetching a
        # `Packages.gz` that matched the signed Release perfectly.
        #
        # And the signature file has to change. apt asks for InRelease
        # conditionally; an unchanged one is a cache hit, and it never refetches
        # the index at all — so the tampered file sat on the server, unread,
        # while the test declared verification working.
        tampered = original[binary / "Packages"].replace(
            b"Version: " + NEW.encode(), b"Version: 9.9.9")
        (binary / "Packages").write_bytes(tampered)
        with gzip.GzipFile(str(binary / "Packages.gz"), "wb", mtime=0) as out:
            out.write(tampered)

        archive.resign(archive.attacker_key)
        ok("the archive now offers 9.9.9, signed by somebody else's key")

        result = must(vm, "update.check")
        check(result.get("reachable") is False,
              "apt refuses the archive rather than reading it",
              json.dumps(result))
        check("9.9.9" not in json.dumps(result),
              "and the version somebody inserted is never offered")
        check(bool(result.get("detail")),
              f"and the machine says why ({(result.get('detail') or '')[:140]})")
    finally:
        for path, content in original.items():
            path.write_bytes(content)

    # And it recovers: a machine that refuses forever after one bad fetch would
    # be its own denial of service.
    result = must(vm, "update.check")
    check(result.get("reachable") is True,
          "and it works again once the archive is honest")


def verify_the_archive_cannot_replace_other_packages(vm: VM, archive: Archive) -> None:
    """`Signed-By` binds a key to a source, not to package names.

    Without the pin, a repository Homebase controls could offer a package called
    `openssh-server` and win on version number — so compromising one signing key
    would mean replacing anything on the machine. This is the check that the pin
    is doing its job.
    """
    step("A package Homebase does not ship, offered by Homebase's archive")

    # A package named after one Ubuntu ships, republished into Homebase's
    # archive at an implausibly high version. Nothing about it is malicious;
    # being able to install it at all is the flaw.
    add_decoy(archive)

    ssh(vm, ["sudo", "apt-get", "update", "-qq",
             "-o", "Dir::Etc::SourceList=/dev/null",
             "-o", "Dir::Etc::SourceParts=/etc/apt/sources.list.d"], check=False)

    policy = ssh(vm, ["apt-cache", "policy", "hello"], check=False).stdout
    check("9.9.9" not in candidate_line(policy),
          "apt will not take `hello` from Homebase's archive",
          f"apt-cache policy said:\n{policy[:600]}")

    simulated = ssh(vm, ["sudo", "apt-get", "install", "-s", "-y", "hello"], check=False)
    check("9.9.9" not in (simulated.stdout + simulated.stderr),
          "and would not install the version it offered",
          (simulated.stdout + simulated.stderr)[:400])

    # The four packages Homebase does ship are still installable from it —
    # a pin that refused everything would pass the check above and break the
    # product.
    result = must(vm, "update.check")
    check(result.get("update_available") is True,
          "while Homebase's own packages are still offered normally",
          json.dumps(result))


def candidate_line(policy: str) -> str:
    for line in policy.splitlines():
        if "Candidate:" in line:
            return line
    return policy


def add_decoy(archive: Archive) -> None:
    """Put a validly signed package Homebase does not ship into the archive.

    Signed correctly, deliberately. An unsigned tamper is the previous test;
    this one has to be a signature apt accepts, or it proves nothing about the
    pin — which is the only thing standing between a compromised signing key
    and every package on the machine.
    """
    staging = archive.root / "decoy" / "hello"
    (staging / "DEBIAN").mkdir(parents=True, exist_ok=True)
    (staging / "DEBIAN" / "control").write_text(
        "Package: hello\n"
        "Version: 9.9.9\n"
        "Architecture: all\n"
        "Maintainer: Homebase Test <test@homebase.invalid>\n"
        "Description: Stands in for any package an attacker would rather replace.\n")

    pool = archive.repo / "pool" / "main" / "h" / "hello"
    pool.mkdir(parents=True, exist_ok=True)
    built = subprocess.run(
        ["dpkg-deb", "--build", "--root-owner-group", str(staging), str(pool)],
        capture_output=True, text=True)
    if built.returncode != 0:
        raise TestFailure("building the decoy failed: " + built.stderr[-300:])

    scanned = subprocess.run(["apt-ftparchive", "packages", "pool"],
                             cwd=archive.repo, capture_output=True, text=True)
    if scanned.returncode != 0:
        raise TestFailure("apt-ftparchive failed: " + scanned.stderr[-300:])

    packages = archive.repo / "dists" / "stable" / "main" / "binary-amd64" / "Packages"
    packages.write_text(scanned.stdout)
    subprocess.run(["gzip", "-kf", str(packages)], check=True)

    archive.resign(archive.key)
    ok("the archive offers `hello` at 9.9.9, correctly signed")


# --- main ---------------------------------------------------------------------


def main() -> int:
    started = time.time()
    vm: VM | None = None
    archive = Archive(REPO_ROOT / "tests" / "vm" / "run" / "archive")

    print()
    step("Updating from a signed repository")
    info("publish, point a machine at it, find the newer version — and refuse")
    info("an archive that has been tampered with")
    print()

    try:
        if archive.root.exists():
            shutil.rmtree(archive.root)
        archive.root.mkdir(parents=True)

        archive.create_keys()
        archive.build(OLD)
        archive.publish(OLD)
        archive.serve()

        vm = create(VM_NAME, force=True)
        start(vm)
        wait_for_ssh(vm)
        wait_for_boot_complete(vm)

        install_homebase(vm, OLD)
        trust_the_archive(vm, archive)

        verify_configuring_a_channel(vm, archive)
        verify_a_channel_that_does_not_exist_is_refused(vm, archive)
        verify_finding_an_update(vm, archive)
        verify_a_tampered_archive_is_refused(vm, archive)
        verify_the_archive_cannot_replace_other_packages(vm, archive)

        elapsed = int(time.time() - started)
        print()
        ok(f"PASS — a machine updates from a signed archive, and refuses one that "
           f"has been tampered with ({elapsed}s)")
        return 0

    except (TestFailure, VMError) as exc:
        print()
        fail("FAIL", str(exc))
        if isinstance(exc, VMError) and exc.hint:
            info(exc.hint)
        if vm is not None:
            collect_logs(vm)
        return 1

    finally:
        archive.stop()
        if vm is not None:
            destroy(vm.name)


if __name__ == "__main__":
    sys.exit(main())
