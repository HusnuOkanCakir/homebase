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
    qmp,
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

# A release that installs cleanly and then does not work, and the good one that
# follows it.
BROKEN = "0.9.1"
RECOVERED = "0.9.2"
LATEST = "0.9.3"


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
    # hostd's envelope is the result object itself. An earlier version of this
    # helper unwrapped a "result" key when one was present, which worked for
    # every operation until one returned a payload with a field of that name —
    # and then returned the string "ok" where a dict was expected.
    return body


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



def create_data(vm: VM) -> None:
    """Something on the disk that has to survive being updated over."""
    step("Putting something on the server first")
    ssh(vm, ["sudo", "sh", "-c",
             "mkdir -p /srv/homebase && echo 'a photograph' > /srv/homebase/important.txt "
             "&& chown -R homebase:homebase /srv/homebase"])
    ok("a file in /srv/homebase")


def verify_applying_the_update(vm: VM) -> None:
    """The milestone's centre: actually install it, and stay working.

    Applying cannot be a synchronous call, and that is structural rather than a
    matter of taste. The update replaces homebase-hostd and restarts it, so the
    process holding the request open is the process being replaced. hostd starts
    the unit detached and the caller polls — which also means a dashboard whose
    connection died during the restart can reconnect and find out how it went.
    """
    step("Applying the update")

    started = must(vm, "update.apply", confirmed=True)
    check(started.get("started") is True, "the update was accepted and started",
          json.dumps(started))

    progress = wait_for_update_to_finish(vm)

    check(progress.get("result") == "ok",
          f"it finished, and reports {progress.get('result')}",
          json.dumps(progress, indent=4))
    check(progress.get("stage") == "done",
          f"having reached the last stage ({progress.get('stage')})")
    check(progress.get("from") == OLD and progress.get("to") == NEW,
          f"and says what it moved between ({progress.get('from')} → {progress.get('to')})")

    status = must(vm, "update.status")
    check(status.get("version") == NEW,
          f"the machine now runs {status.get('version')}")
    check(status.get("consistent") is True,
          "with all four packages agreeing",
          json.dumps(status.get("components")))
    check(status.get("interrupted") is False, "and nothing left half-applied")

    # The health check inside the update already asked core to answer. Asking
    # again from outside is not duplication: the first is what decides whether
    # to roll back, and this is whether the decision was right.
    code = ssh(vm, ["curl", "--silent", "--insecure", "--max-time", "15",
                    "-o", "/dev/null", "-w", "%{http_code}",
                    "https://127.0.0.1/api/v1/health"], check=False).stdout.strip()
    check(code == "200", f"and the dashboard answers ({code})")

    kept = ssh(vm, ["sudo", "cat", "/srv/homebase/important.txt"], check=False).stdout.strip()
    check(kept == "a photograph",
          f"and the file that was there before the update still is ({kept!r})")



def publish_broken(archive: Archive, version: str) -> None:
    """Publish a version that installs cleanly and then does not work.

    The realistic failure. A release that fails to *install* is caught by apt
    and never reaches the health check; the one rollback exists for is a release
    that installs perfectly and leaves the server unusable, which nothing before
    the health check can notice.

    Built normally, then core's binary is replaced inside the package — so
    everything else about it is a real Homebase release, including the
    dependencies and the maintainer scripts that run on the way in.
    """
    step(f"Publishing {version}, which installs and then does not work")
    archive.build(version)

    deb = REPO_ROOT / "dist" / f"homebase-core_{version}_amd64.deb"
    work = archive.root / "broken"
    if work.exists():
        shutil.rmtree(work)

    subprocess.run(["dpkg-deb", "-R", str(deb), str(work)],
                   capture_output=True, text=True, check=True)
    binary = work / "usr" / "libexec" / "homebase" / "core"
    binary.write_text("#!/bin/sh\nexit 1\n")
    binary.chmod(0o755)
    subprocess.run(["dpkg-deb", "--build", "--root-owner-group", str(work), str(deb)],
                   capture_output=True, text=True, check=True)

    ok(f"homebase-core {version} will start and immediately exit")
    archive.publish(version)


def wait_for_update_to_finish(vm: VM, timeout: int = 900) -> dict:
    """Poll until the update reports an outcome.

    Failures to reach hostd are expected rather than exceptional: it is being
    restarted underneath these calls. That the answer outlives the process
    reporting it is the reason progress is written to a file at all.
    """
    deadline = time.time() + timeout
    progress: dict = {}
    while time.time() < deadline:
        time.sleep(5)
        try:
            progress = must(vm, "update.progress")
        except (TestFailure, VMError):
            continue
        if progress.get("result"):
            return progress
    raise TestFailure(f"the update never finished: {json.dumps(progress)}")


def verify_a_broken_release_is_rolled_back(vm: VM, archive: Archive) -> None:
    """The property rollback exists for, forced to happen.

    Until this test, rollback was code rather than a proven property: the
    snapshot was taken and the branch was written, but nothing had ever made
    the health check fail, so nothing had ever run it.
    """
    step("A release that installs and then does not work")

    publish_broken(archive, BROKEN)

    must(vm, "update.apply", confirmed=True)
    progress = wait_for_update_to_finish(vm)

    check(progress.get("result") == "failed",
          f"the update reports that it failed ({progress.get('result')})",
          json.dumps(progress, indent=4))
    check(progress.get("stage") == "rolled-back",
          f"having put the machine back ({progress.get('stage')})",
          json.dumps(progress, indent=4))
    # Which of the three health questions caught it is not fixed: core's binary
    # exits immediately, so systemd notices before the dashboard is ever asked.
    # What matters is that the report names the thing that failed rather than
    # saying the update went wrong.
    detail = progress.get("detail") or ""
    check("homebase-core" in detail or "dashboard" in detail,
          f"and says what was wrong ({detail[:140]})")
    check(f"put back on {NEW}" in detail,
          "and that it put the previous version back",
          detail[:200])

    status = must(vm, "update.status")
    check(status.get("version") == NEW,
          f"the server is running {status.get('version')} again, not {BROKEN}")
    check(status.get("consistent") is True,
          "with all four packages agreeing",
          json.dumps(status.get("components")))

    code = ssh(vm, ["curl", "--silent", "--insecure", "--max-time", "15",
                    "-o", "/dev/null", "-w", "%{http_code}",
                    "https://127.0.0.1/api/v1/health"], check=False).stdout.strip()
    check(code == "200", f"and the dashboard answers again ({code})")

    kept = ssh(vm, ["sudo", "cat", "/srv/homebase/important.txt"], check=False).stdout.strip()
    check(kept == "a photograph", f"and the file is still there ({kept!r})")


def current_stage(vm: VM) -> str:
    out = ssh(vm, ["sudo", "cat", "/var/lib/homebase/apply"], check=False).stdout
    for line in out.splitlines():
        if line.startswith("stage="):
            return line.split("=", 1)[1].strip()
    return ""


def wait_inside_for_stage(vm: VM, stage: str, timeout: int = 300) -> None:
    """Block until the update inside the VM reaches a stage.

    The waiting happens *in* the guest rather than by polling over SSH. A round
    trip is a couple of hundred milliseconds and `applying` can be over in a few
    seconds, so polling from outside decides which stage gets interrupted by
    accident — which would make this test quietly assert something easier than
    it claims.
    """
    # For `applying`, waiting for the stage is not enough. The stage is written
    # just before apt is invoked, and apt spends a moment resolving before dpkg
    # touches anything — so a reset triggered on the stage alone lands at the
    # edge of the dangerous window rather than inside it, and the machine comes
    # back untouched. That is a real result, but it is not the one this claims
    # to test. Waiting for a running dpkg puts the power cut where the promise
    # is actually made.
    trigger = ("pgrep -x dpkg >/dev/null"
               if stage == "applying"
               else f"grep -q '^stage={stage}$' /var/lib/homebase/apply 2>/dev/null")

    watch = (f"end=$(( $(date +%s) + {timeout} )); "
             f"while [ $(date +%s) -lt $end ]; do "
             f"  if {trigger}; then exit 0; fi; "
             f"  grep -qE '^stage=(done|rolled-back)$' /var/lib/homebase/apply 2>/dev/null && exit 2; "
             f"  sleep 0.05; "
             f"done; exit 1")

    result = ssh(vm, ["sudo", "sh", "-c", watch], check=False, timeout=timeout + 60)
    if result.returncode == 2:
        raise TestFailure(f"the update finished before reaching '{stage}'")
    if result.returncode != 0:
        raise TestFailure(f"the update never reached '{stage}' "
                          f"(it is at {current_stage(vm)!r})")


def verify_power_loss_mid_update(vm: VM, archive: Archive, version: str, stage: str) -> None:
    """Milestone 8's exit condition.

    Not a clean shutdown and not a killed process — `system_reset` through QMP,
    which is the guest losing power with its caches dirty. This runs on a laptop
    in a cupboard; power loss during an update is expected rather than
    exceptional, and the promise is a machine that still boots with its data
    intact.

    Run once per dangerous stage rather than once at whichever stage happens to
    be caught. `downloading` is the easy case and passes by design — nothing has
    been changed yet. `applying` is the one the promise is actually about: dpkg
    is part way through replacing the running system.
    """
    step(f"Losing power during '{stage}'")

    archive.build(version)
    archive.publish(version)

    must(vm, "update.apply", confirmed=True)
    wait_inside_for_stage(vm, stage)

    qmp(vm, "system_reset")
    ok(f"power cut during '{stage}'"
       + (" — with dpkg running" if stage == "applying" else ""))

    wait_for_ssh(vm)
    wait_for_boot_complete(vm)
    ok("the machine boots again")

    kept = ssh(vm, ["sudo", "cat", "/srv/homebase/important.txt"], check=False).stdout.strip()
    check(kept == "a photograph",
          f"and the application data is intact ({kept!r})",
          "This is the exit condition. Everything else on this page is detail.")

    # Whatever state it came back in, it has to describe that state correctly.
    # A machine that reports itself healthy while dpkg has work outstanding is
    # one nobody can help.
    status = must(vm, "update.status")
    info(f"after the power cut: version={status.get('version')} "
         f"consistent={status.get('consistent')} interrupted={status.get('interrupted')}")

    if status.get("interrupted") or not status.get("consistent"):
        ok("it admits the update did not finish")

        # And the way out is a button, not a command.
        #
        # `dpkg --configure -a` is what the error message has always named, and
        # naming a terminal command to somebody who bought an appliance is a
        # remedy they do not have. `system.repair` runs it, and this is the only
        # place in the suite where a genuinely half-upgraded machine exists to
        # try it on — so it is tried here rather than against a machine that was
        # never broken.
        repaired = must(vm, "system.repair", timeout=600)
        info(repaired.get("message", ""))
        check(repaired.get("changed", 0) >= 1,
              f"repair found something to fix ({repaired.get('changed')})",
              json.dumps(repaired, indent=4))

        finished = [s for s in repaired.get("steps", [])
                    if "unfinished" in s.get("what", "") and s.get("done")]
        check(bool(finished),
              "and what it fixed was the unfinished update",
              json.dumps(repaired.get("steps"), indent=4))

        time.sleep(5)
        status = must(vm, "update.status")
        check(status.get("interrupted") is False and status.get("consistent") is True,
              "and afterwards the machine is whole again",
              json.dumps(status, indent=4))

        # Twice, because somebody who does not know what is wrong will press it
        # twice. The second run must be a quiet no-op rather than a second
        # attempt at a transaction that has already been completed.
        again = must(vm, "system.repair", timeout=600)
        check(again.get("changed") == 0 and again.get("healthy") is True,
              f"and pressing repair again does nothing ({again.get('changed')} changed)",
              json.dumps(again, indent=4))
    else:
        ok("it came back complete, and says so")

    code = ssh(vm, ["curl", "--silent", "--insecure", "--max-time", "20",
                    "-o", "/dev/null", "-w", "%{http_code}",
                    "https://127.0.0.1/api/v1/health"], check=False).stdout.strip()
    check(code == "200", f"and the dashboard answers ({code})")



# --- talking to core, the way the dashboard does ------------------------------

PASSWORD = "a-password-nobody-would-guess"


def core_api(vm: VM, path: str, method: str = "GET", body: str | None = None) -> tuple[int, str]:
    """Call core's HTTP API from inside the VM.

    Not named `http`: that shadows the stdlib module this file imports for the
    archive server, and the failure is an AttributeError three hundred lines
    away from the cause."""
    cmd = ["curl", "--silent", "--show-error", "--insecure",
           "-c", "/tmp/update-cookies", "-b", "/tmp/update-cookies",
           "-o", "/dev/stdout", "-w", "\\n%{http_code}", "-X", method,
           "--max-time", "200"]
    if body is not None:
        cmd += ["-H", "Content-Type: application/json", "-d", body]
    cmd.append(f"https://127.0.0.1/api/v1{path}")

    result = ssh(vm, cmd, check=False, timeout=260)
    output = result.stdout.strip()
    if not output:
        return 0, result.stderr.strip()
    parts = output.rsplit("\n", 1)
    return (int(parts[1]), parts[0]) if len(parts) == 2 else (int(output), "")


def verify_the_api_exposes_all_of_this(vm: VM) -> None:
    """The routes the dashboard actually calls.

    Everything above this talks to hostd over its socket, which is how the
    privileged half is tested but not how anybody uses Homebase. These are the
    routes a browser reaches, behind a session and a permission check — and a
    permission check that has never been exercised is a permission check nobody
    knows the shape of.
    """
    step("The same thing, through the API a browser uses")

    status, body = core_api(vm, "/setup", "POST",
                        json.dumps({"username": "alex", "password": PASSWORD}))
    check(status == 201, f"an administrator is created ({status})", body[:300])

    status, body = core_api(vm, "/system/update")
    check(status == 200, f"GET /system/update ({status})", body[:300])
    reported = json.loads(body)
    check(reported.get("version") == NEW,
          f"and reports {reported.get('version')} through core, as it does through hostd")

    status, body = core_api(vm, "/system/update/check", "POST")
    check(status == 200, f"POST /system/update/check ({status})", body[:300])
    check(json.loads(body).get("reachable") is True,
          "and reaches the archive", body[:300])

    status, body = core_api(vm, "/system/update/progress")
    check(status == 200, f"GET /system/update/progress ({status})", body[:300])
    check(json.loads(body).get("result") == "failed",
          "and still remembers the rolled-back update from earlier",
          body[:300])

    # Signed out, the same routes must say nothing at all. The update surface
    # can install root-level code on this machine; it is the last place an
    # unauthenticated caller should get an answer.
    ssh(vm, ["rm", "-f", "/tmp/update-cookies"], check=False)
    for path, method in (("/system/update", "GET"),
                         ("/system/update/check", "POST"),
                         ("/system/update/apply", "POST"),
                         ("/system/update/channel", "POST")):
        status, _ = core_api(vm, path, method,
                         '{"channel": "development"}' if "channel" in path else None)
        check(status == 401, f"{method} {path} needs a session ({status})")


def verify_a_diagnostic_file_is_safe_to_send(vm: VM) -> None:
    """The claim the diagnostic file makes about itself, checked against it.

    Everything else about a support bundle is a matter of taste. This is not: it
    is written to be sent to a stranger, it says at the top what it does not
    contain, and if that sentence is wrong the tool that was meant to get
    somebody help has handed out their password database instead.

    So the file is read, on the machine, and searched for the things it promises
    are not in it — using values planted earlier in this test, which is the only
    way to tell "not present" from "not looked for properly".
    """
    step("A diagnostic file, and whether it is safe to send")

    # Something secret, in each of the places the bundle promises not to read
    # from. Recognisable strings rather than real secrets: a grep for "password"
    # would match the word in the file's own header and prove nothing.
    ssh(vm, ["sudo", "sh", "-c",
             "printf 'SECRETINDATABASE\\n' > /var/lib/homebase/planted.txt"])
    ssh(vm, ["sudo", "sh", "-c",
             "printf 'SECRETINCONFIG\\n' >> /etc/homebase/homebase.yaml"])
    ssh(vm, ["sudo", "sh", "-c",
             "printf 'SECRETINUSERDATA\\n' > /srv/homebase/private.txt"])

    result = must(vm, "system.diagnostics", timeout=200)
    path = result.get("path", "")
    check(path.startswith("/var/lib/homebase/diagnostics/"),
          f"the file is written where hostd owns ({path})")
    check(result.get("bytes", 0) > 500, f"and has something in it ({result.get('bytes')} bytes)")
    check(len(result.get("excludes", [])) > 0,
          "and says what it does not contain", json.dumps(result, indent=4))

    contents = ssh(vm, ["sudo", "cat", path]).stdout

    for planted, where in (("SECRETINDATABASE", "/var/lib/homebase"),
                           ("SECRETINCONFIG", "/etc/homebase"),
                           ("SECRETINUSERDATA", "/srv/homebase")):
        check(planted not in contents,
              f"nothing from {where} is in it",
              "The bundle says it does not contain this. It does.")

    # And it has to be useful, or nobody will make one. These are the things
    # somebody debugging asks for first.
    for expected, what in (("homebase-hostd", "which versions are installed"),
                           ("=== journal.txt", "the journal"),
                           ("=== space.txt", "free space"),
                           ("=== dpkg.txt", "whether a package transaction is unfinished")):
        check(expected in contents, f"and it contains {what}")

    # Through the API, which is the only way a person gets it off the machine.
    status, body = core_api(vm, "/auth/login", "POST",
                            json.dumps({"username": "alex", "password": PASSWORD}))
    check(status == 200, f"signed in ({status})", body[:200])

    status, body = core_api(vm, "/system/diagnostics/download")
    check(status == 200, f"the browser can download it ({status})")
    check("Homebase diagnostics" in body,
          "and gets the file rather than a description of it", body[:200])

    ssh(vm, ["rm", "-f", "/tmp/update-cookies"], check=False)
    status, _ = core_api(vm, "/system/diagnostics/download")
    check(status == 401,
          f"a caller with no account cannot download it ({status})")


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

    # The four packages Homebase does ship are still offered by it. A pin that
    # refused everything from this origin would pass the check above and break
    # the product, and the two are indistinguishable without asking.
    result = must(vm, "update.check")
    check(result.get("reachable") is True and result.get("available") == NEW,
          f"while Homebase's own packages are still offered ({result.get('available')})",
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
        create_data(vm)
        verify_applying_the_update(vm)
        verify_the_archive_cannot_replace_other_packages(vm, archive)
        verify_a_broken_release_is_rolled_back(vm, archive)
        verify_the_api_exposes_all_of_this(vm)

        # Once per stage that matters. The first is the easy case and passes
        # by construction; the second is the one the milestone promises.
        verify_power_loss_mid_update(vm, archive, RECOVERED, "downloading")
        verify_power_loss_mid_update(vm, archive, LATEST, "applying")

        # Last, on a machine that has genuinely been through two power cuts —
        # which is the machine a diagnostic file is for.
        verify_a_diagnostic_file_is_safe_to_send(vm)

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
