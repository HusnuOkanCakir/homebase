#!/usr/bin/env python3
"""The Milestone 2 vertical slice: dashboard → API → privileged operation → hardware.

Runs core and hostd together in a VM and walks the journey the milestone is
defined by: create an administrator, sign in, read accurate system information,
restart the machine, and have everything come back by itself.

The assertion this test exists for is the last one. A reboot job cannot observe
its own success — the connection dies with the machine — so core resolves it on
the next start by comparing the kernel's boot id. Nothing but a real reboot on a
real machine can tell us whether that works.

Run with `make vm-test-core`.
"""

from __future__ import annotations

import json
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from vmctl import (  # noqa: E402
    VM,
    VMError,
    collect_logs,
    copy_to,
    create,
    destroy,
    fail,
    info,
    ok,
    ssh,
    start,
    step,
    wait_for_boot_complete,
    wait_for_ssh,
    write_file,
)

REPO_ROOT = Path(__file__).resolve().parents[2]
VM_NAME = "homebase-core"
# HTTPS on the ordinary port, exactly as a browser reaches it.
#
# This said `http://127.0.0.1:8080` for five milestones and stopped being true in
# Milestone 7, when core moved to 443 with a self-signed certificate — and nobody
# noticed, because this suite was not re-run. The rule that came out of that is
# in docs/development/testing.md: a change to how the server is reached is a
# change to every suite that reaches it.
API = "https://127.0.0.1/api/v1"
PASSWORD = "a-sufficiently-long-password"


class TestFailure(Exception):
    pass


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        ok(description)
    else:
        raise TestFailure(f"{description}\n    {detail}" if detail else description)


def build() -> dict[str, Path]:
    step("Building core and hostd")
    out = REPO_ROOT / "bin"
    out.mkdir(parents=True, exist_ok=True)

    import os
    env = {**os.environ, "CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64"}

    result = subprocess.run(
        ["go", "build", "-trimpath", "-o", str(out) + "/", "./cmd/core", "./cmd/hostd"],
        cwd=REPO_ROOT, capture_output=True, text=True, env=env,
    )
    if result.returncode != 0:
        raise VMError("go build failed", result.stderr.strip()[:800])

    binaries = {"core": out / "core", "hostd": out / "hostd"}
    for name, path in binaries.items():
        ok(f"{name} ({path.stat().st_size // 1024} KB)")
    return binaries


def api(vm: VM, path: str, method: str = "GET", body: str | None = None,
        cookie_jar: str = "/tmp/homebase-cookies") -> tuple[int, str]:
    """Call core's API from inside the guest, keeping cookies between calls.

    --insecure stands in for the "proceed once" a person clicks over the
    server's own certificate. It is the right thing here and would be the wrong
    thing in a test about the certificate — see test_network.py, which checks the
    fingerprint rather than skipping it.
    """
    cmd = ["curl", "--silent", "--show-error", "--insecure",
           "-c", cookie_jar, "-b", cookie_jar,
           "-o", "/dev/stdout", "-w", "\\n%{http_code}",
           "-X", method]
    if body is not None:
        cmd += ["-H", "Content-Type: application/json", "-d", body]
    cmd.append(f"{API}{path}")

    result = ssh(vm, cmd, check=False)
    output = result.stdout.strip()
    if not output:
        return 0, result.stderr.strip()

    parts = output.rsplit("\n", 1)
    if len(parts) == 2:
        return int(parts[1]), parts[0]
    return int(output), ""


def install(vm: VM, binaries: dict[str, Path]) -> None:
    step("Installing core and hostd")

    ssh(vm, ["sudo", "groupadd", "--system", "--force", "homebase"])
    ssh(vm, ["sudo", "useradd", "--system", "--gid", "homebase",
             "--home-dir", "/var/lib/homebase", "--shell", "/usr/sbin/nologin",
             "homebase"], check=False)

    ssh(vm, ["sudo", "mkdir", "-p", "/usr/libexec/homebase", "/etc/homebase",
             "/var/lib/homebase", "/srv/homebase", "/var/log/homebase"])
    ssh(vm, ["sudo", "chown", "homebase:homebase",
             "/var/lib/homebase", "/srv/homebase", "/var/log/homebase"])

    for name, path in binaries.items():
        copy_to(vm, path, f"/usr/libexec/homebase/{name}", mode="0755")

    for unit in ("homebase-hostd.service", "homebase-hostd.socket", "homebase-core.service"):
        write_file(vm, f"/etc/systemd/system/{unit}",
                   (REPO_ROOT / "packaging" / "systemd" / unit).read_text())

    ssh(vm, ["sudo", "systemctl", "daemon-reload"])
    ssh(vm, ["sudo", "systemctl", "enable", "--now", "homebase-hostd.socket"])
    ssh(vm, ["sudo", "systemctl", "enable", "--now", "homebase-core.service"])

    for _ in range(30):
        state = ssh(vm, ["systemctl", "is-active", "homebase-core.service"],
                    check=False).stdout.strip()
        if state == "active":
            ok("homebase-core.service is active")
            break
        if state == "failed":
            logs = ssh(vm, ["sudo", "journalctl", "-u", "homebase-core", "--no-pager", "-n", "40"],
                       check=False).stdout
            raise TestFailure(f"core failed to start\n{logs}")
        time.sleep(1)
    else:
        raise TestFailure("core never became active")

    # core must be unprivileged. The entire design rests on this, and it is a
    # single word in a unit file away from not being true.
    user = ssh(vm, ["ps", "-o", "user=", "-C", "core"], check=False).stdout.strip()
    check(user == "homebase", f"core runs as {user or '<not found>'}, not root")


def verify_setup_flow(vm: VM) -> None:
    step("First-run setup")

    status, body = api(vm, "/setup")
    check(status == 200, f"setup status returned {status}", body)
    check(json.loads(body)["needs_setup"] is True, "a fresh server reports needing setup")

    # Before there is an administrator, nothing else is reachable.
    status, _ = api(vm, "/system")
    check(status == 401, f"system information is refused before sign-in ({status})")

    status, body = api(vm, "/setup", "POST",
                       json.dumps({"username": "okan", "password": PASSWORD}))
    check(status == 201, f"administrator created ({status})", body)

    status, body = api(vm, "/setup", "POST",
                       json.dumps({"username": "attacker", "password": PASSWORD}))
    check(status == 409, f"a second administrator is refused ({status})", body)
    check(
        json.loads(body)["error"]["code"] == "setup.already_complete",
        "with setup.already_complete — a server cannot be claimed twice",
    )


def verify_system_information(vm: VM) -> None:
    step("Reading system information through the whole stack")

    status, body = api(vm, "/system")
    check(status == 200, f"system returned {status}", body)

    facts = json.loads(body)
    check(facts["hostname"] == VM_NAME, f"hostname = {facts['hostname']}")
    check(bool(facts["kernel"]), f"kernel = {facts['kernel']}")
    check(facts["virtualised"] is True, "correctly reports running in a VM")
    check(facts["cpu"]["threads"] >= 1, f"cpu = {facts['cpu']['model']}")
    check(facts["memory"]["total_bytes"] > 0,
          f"memory = {facts['memory']['total_bytes'] // 1048576} MB total")
    check(
        0 < facts["memory"]["available_bytes"] <= facts["memory"]["total_bytes"],
        "available memory is within total",
    )
    check(facts["uptime_seconds"] > 0, f"uptime = {facts['uptime_seconds']}s")

    # This data came from /proc, through hostd's typed operation, over the Unix
    # socket, through core, to an HTTP client. That path is the milestone.
    ok("the value travelled /proc → hostd → socket → core → HTTP")


def verify_health(vm: VM) -> None:
    step("Health")
    status, body = api(vm, "/health")
    check(status == 200, f"health returned {status}", body)
    payload = json.loads(body)
    check(payload["status"] == "ok", f"status = {payload['status']}")
    check(payload["hostd_reachable"] is True, "core can reach hostd")


def verify_reboot_confirmation(vm: VM) -> None:
    step("Reboot refuses to proceed without naming the machine")

    status, body = api(vm, "/system/reboot", "POST", json.dumps({"confirm": "wrong-name"}))
    check(status == 428, f"a mismatched confirmation returns {status}", body)
    check(
        json.loads(body)["error"]["code"] == "system.confirmation_required",
        "with system.confirmation_required",
    )


def verify_reboot_and_job_resolution(vm: VM) -> str:
    """Restart through the API and return the job id."""
    step("Restarting the server through the API")

    before = ssh(vm, ["cat", "/proc/sys/kernel/random/boot_id"]).stdout.strip()

    status, body = api(vm, "/system/reboot", "POST",
                       json.dumps({"confirm": VM_NAME, "reason": "core integration test"}))
    check(status == 202, f"reboot accepted with {status} and a job", body)

    job = json.loads(body)
    job_id = job["job_id"]
    check(job["state"] in ("queued", "running"), f"job {job_id} is {job['state']}")
    check(job["operation"] == "system.reboot", "the job names the operation")

    time.sleep(5)
    wait_for_ssh(vm)
    wait_for_boot_complete(vm)

    after = ssh(vm, ["cat", "/proc/sys/kernel/random/boot_id"]).stdout.strip()
    check(before != after, f"the machine really rebooted ({before[:8]}… → {after[:8]}…)")

    return job_id


def verify_job_resolved_after_reboot(vm: VM, job_id: str) -> None:
    """The assertion this whole test exists for.

    Nothing observed the reboot finishing. core has to work it out on the next
    start from the boot id, and the alternative — assuming success — would make
    every job report a lie.
    """
    step("The reboot job resolves itself on the next start")

    for _ in range(30):
        state = ssh(vm, ["systemctl", "is-active", "homebase-core.service"],
                    check=False).stdout.strip()
        if state == "active":
            break
        time.sleep(1)
    check(state == "active", f"core came back by itself ({state})")

    # Sign in again: the session cookie jar in /tmp did not survive the reboot,
    # which is itself worth knowing.
    status, _ = api(vm, "/auth/login", "POST",
                    json.dumps({"username": "okan", "password": PASSWORD}))
    check(status == 200, f"signing in again returned {status}")

    status, body = api(vm, f"/jobs/{job_id}")
    check(status == 200, f"the job still exists after the reboot ({status})", body)

    job = json.loads(body)
    check(
        job["state"] == "succeeded",
        f"the reboot job resolved to {job['state']}, want succeeded",
        "core compares the kernel's boot id recorded when the job started with "
        "the current one. A different id is evidence the machine went down and "
        "came back, which is exactly what a reboot means by success.",
    )
    check(job["finished_at"] is not None, "the job has a finish time")
    check(
        job["message"] is not None and job["message"] != "",
        f"and an explanation: {job.get('message')!r}",
    )


def verify_no_stuck_jobs(vm: VM) -> None:
    step("Nothing is left stuck")

    status, body = api(vm, "/jobs")
    check(status == 200, f"jobs listed ({status})", body)

    items = json.loads(body)["items"]
    check(len(items) >= 1, f"{len(items)} job(s) recorded")

    stuck = [j for j in items if j["state"] in ("queued", "running", "cancelling")]
    check(
        not stuck,
        "no job is left showing progress with nothing behind it",
        f"stuck: {[(j['job_id'], j['state']) for j in stuck]}",
    )


def verify_state_survived(vm: VM) -> None:
    step("State survived the restart")

    status, body = api(vm, "/setup")
    check(
        json.loads(body)["needs_setup"] is False,
        "the administrator still exists after the reboot",
    )

    perms = ssh(vm, ["sudo", "stat", "-c", "%a %U", "/var/lib/homebase/homebase.db"]).stdout.strip()
    check(perms.endswith("homebase"), f"the database is owned by the service account ({perms})")


def main() -> int:
    started = time.time()
    print()
    step("Homebase core vertical slice")
    info("setup → sign in → read system → reboot → job resolves → state intact")
    print()

    vm: VM | None = None
    try:
        binaries = build()

        vm = create(VM_NAME, force=True)
        start(vm)
        wait_for_ssh(vm)
        wait_for_boot_complete(vm)

        install(vm, binaries)
        verify_health(vm)
        verify_setup_flow(vm)
        verify_system_information(vm)
        verify_reboot_confirmation(vm)

        job_id = verify_reboot_and_job_resolution(vm)
        verify_job_resolved_after_reboot(vm, job_id)
        verify_no_stuck_jobs(vm)
        verify_state_survived(vm)

        elapsed = int(time.time() - started)
        print()
        ok(f"PASS — the vertical slice works end to end ({elapsed}s)")
        return 0

    except (TestFailure, VMError) as exc:
        print()
        fail("FAIL", str(exc))
        if isinstance(exc, VMError) and exc.hint:
            info(exc.hint)
        if vm:
            try:
                for unit in ("homebase-core", "homebase-hostd"):
                    journal = ssh(vm, ["sudo", "journalctl", "-u", unit,
                                       "--no-pager", "-n", "30"], check=False).stdout
                    if journal.strip():
                        print(f"\n  --- {unit} ---")
                        for line in journal.strip().splitlines()[-20:]:
                            print(f"  {line}")
                destination = collect_logs(vm)
                info(f"Diagnostics saved to {destination}")
            except Exception:
                info("Could not collect diagnostics")
        return 1

    except KeyboardInterrupt:
        print("\nInterrupted.")
        return 130

    finally:
        if vm:
            print()
            destroy(VM_NAME)


if __name__ == "__main__":
    sys.exit(main())
