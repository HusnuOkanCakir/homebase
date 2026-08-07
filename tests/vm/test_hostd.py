#!/usr/bin/env python3
"""hostd under real systemd, in a real VM.

Unit tests cover the registry and the dispatch logic. They cannot cover the
things that decide whether the privilege boundary actually exists on a running
machine:

  - the socket's mode and group, which is what stops anything but core connecting
  - systemd's sandbox actually applying
  - an unprivileged user being refused by the kernel, not by our code
  - the service surviving the reboot it performed itself

Every one of those is a property of the deployment rather than of the Go, and
each would pass a unit test while being wrong in production.

Run with `make vm-test-hostd`.
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
VM_NAME = "homebase-hostd"
SOCKET = "/run/homebase/hostd.sock"


class TestFailure(Exception):
    pass


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        ok(description)
    else:
        raise TestFailure(f"{description}\n    {detail}" if detail else description)


def build_hostd() -> Path:
    step("Building hostd")
    out = REPO_ROOT / "bin" / "hostd"
    out.parent.mkdir(parents=True, exist_ok=True)

    result = subprocess.run(
        ["go", "build", "-trimpath", "-o", str(out), "./cmd/hostd"],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
        env={"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64", **_go_env()},
    )
    if result.returncode != 0:
        raise VMError("go build failed", result.stderr.strip()[:600])

    ok(f"built ({out.stat().st_size // 1024} KB, static)")
    return out


def _go_env() -> dict:
    import os
    return {k: v for k, v in os.environ.items()}


def curl(vm: VM, path: str, method: str = "GET", body: str | None = None,
         headers: dict | None = None, as_user: str = "homebase") -> tuple[int, str]:
    """Call hostd over the Unix socket from inside the guest.

    Deliberately curl rather than a bespoke client: it proves the protocol is
    ordinary HTTP that anyone can debug at 11pm, which was the argument for
    choosing it.
    """
    # No `-o /dev/stdout`. Under `sudo -u`, /dev/stdout resolves to
    # /proc/self/fd/1 — a pipe owned by the *login* user — and another user
    # cannot open it. curl writes the body to stdout by default anyway; the
    # redirect only ever added a way to fail.
    cmd = ["sudo", "-u", as_user, "curl", "--silent", "--show-error",
           "--unix-socket", SOCKET,
           "-w", "\\n%{http_code}", "-X", method]

    for key, value in (headers or {}).items():
        cmd += ["-H", f"{key}: {value}"]
    if body is not None:
        cmd += ["-H", "Content-Type: application/json", "-d", body]

    cmd.append(f"http://hostd{path}")

    result = ssh(vm, cmd, check=False)
    output = result.stdout.strip()
    if not output:
        return 0, result.stderr.strip()

    status_line = output.rsplit("\n", 1)
    if len(status_line) == 2:
        return int(status_line[1]), status_line[0]
    return int(output), ""


def parse_json(body: str, what: str) -> dict:
    """Decode a response, failing as a test failure rather than a traceback.

    A harness that crashes instead of failing tells you nothing about what the
    server actually said, which is the one thing you need.
    """
    try:
        return json.loads(body)
    except json.JSONDecodeError as exc:
        raise TestFailure(
            f"{what} did not return JSON: {exc}\n    body was: {body[:300]!r}"
        ) from exc


def install_hostd(vm: VM, binary: Path) -> None:
    step("Installing hostd as a systemd service")

    # The package will do this; here it is explicit so the test exercises the
    # same user and group the privilege boundary depends on.
    ssh(vm, ["sudo", "groupadd", "--system", "--force", "homebase"])
    ssh(vm, ["sudo", "useradd", "--system", "--gid", "homebase",
             "--home-dir", "/var/lib/homebase", "--shell", "/usr/sbin/nologin",
             "homebase"], check=False)

    ssh(vm, ["sudo", "mkdir", "-p", "/usr/libexec/homebase", "/etc/homebase",
             "/var/lib/homebase", "/srv/homebase", "/var/log/homebase"])

    copy_to(vm, binary, "/usr/libexec/homebase/hostd", mode="0755")

    for unit in ("homebase-hostd.service", "homebase-hostd.socket"):
        write_file(
            vm,
            f"/etc/systemd/system/{unit}",
            (REPO_ROOT / "packaging" / "systemd" / unit).read_text(),
        )

    ssh(vm, ["sudo", "systemctl", "daemon-reload"])
    ssh(vm, ["sudo", "systemctl", "enable", "--now", "homebase-hostd.socket"])
    ssh(vm, ["sudo", "systemctl", "start", "homebase-hostd.service"])

    # Type=notify: systemd waits for our readiness message before calling the
    # unit started, so reaching "active" at all proves sd_notify works.
    for _ in range(30):
        state = ssh(vm, ["systemctl", "is-active", "homebase-hostd.service"],
                    check=False).stdout.strip()
        if state == "active":
            ok("homebase-hostd.service is active")
            return
        if state == "failed":
            logs = ssh(vm, ["sudo", "journalctl", "-u", "homebase-hostd", "--no-pager", "-n", "30"],
                       check=False).stdout
            raise TestFailure(f"the service failed to start\n{logs}")
        time.sleep(1)

    raise TestFailure("the service never became active — sd_notify may not be working")


def verify_socket_permissions(vm: VM) -> None:
    step("Verifying the socket permissions")

    perms = ssh(vm, ["stat", "-c", "%a %U %G", SOCKET]).stdout.strip()
    check(
        perms == "660 root homebase",
        f"socket is {perms}",
        "Expected '660 root homebase'. This single line is the privilege "
        "boundary: 0666 would let anything on the machine reach a root service, "
        "without a line of Go changing.",
    )


def verify_read_operations(vm: VM) -> None:
    step("Calling the read-only operations")

    status, body = curl(vm, "/v1/health")
    check(status == 200, f"health returned {status}", body)

    status, body = curl(vm, "/v1/operations")
    check(status == 200, f"operations returned {status}", body)
    ops = {op["name"]: op for op in parse_json(body, "/v1/operations")["operations"]}
    check(
        {"system.get_info", "system.get_resources", "system.reboot"} <= set(ops),
        f"registry lists {len(ops)} operations",
    )
    check(
        ops["system.reboot"]["risk"] == "high"
        and ops["system.reboot"]["confirmation"] == "explicit",
        "system.reboot is declared high risk and needs explicit confirmation",
    )

    status, body = curl(vm, "/v1/op/system.get_info", "POST", "{}")
    check(status == 200, f"system.get_info returned {status}", body)
    facts = parse_json(body, "system.get_info")
    check(facts["hostname"] == VM_NAME, f"hostname = {facts['hostname']}")
    check(bool(facts["kernel"]), f"kernel = {facts['kernel']}")
    check(facts["uptime_seconds"] > 0, f"uptime = {facts['uptime_seconds']}s")
    check(facts["cpu"]["threads"] >= 1, f"cpu = {facts['cpu']['model']} ({facts['cpu']['threads']} threads)")
    check(facts["virtualised"] is True, "correctly reports running in a VM")

    status, body = curl(vm, "/v1/op/system.get_resources", "POST", "{}")
    check(status == 200, f"system.get_resources returned {status}", body)
    res = parse_json(body, "system.get_resources")
    check(res["memory"]["total_bytes"] > 0, f"memory total = {res['memory']['total_bytes'] // 1048576} MB")
    check(
        0 < res["memory"]["available_bytes"] <= res["memory"]["total_bytes"],
        "available memory is within total",
    )


def verify_rejections(vm: VM) -> None:
    step("Verifying what must be refused")

    # The kernel refuses this, not our code: nobody is not in the homebase group,
    # so connect(2) fails on the socket's mode before hostd sees anything.
    result = ssh(vm, ["sudo", "-u", "nobody", "curl", "--silent", "--unix-socket",
                      SOCKET, "http://hostd/v1/health"], check=False)
    check(
        result.returncode != 0,
        "a user outside the homebase group cannot connect at all",
        f"exit {result.returncode}: {result.stdout}{result.stderr}",
    )

    status, body = curl(vm, "/v1/op/system.does_not_exist", "POST", "{}")
    check(status == 404, f"an unknown operation returns {status}", body)
    check(parse_json(body, "the error")["error"]["code"] == "hostd.unknown_operation",
          "with the right error code")

    status, body = curl(vm, "/v1/op/system.reboot", "POST", '{"confirm":"anything"}')
    check(status == 428, f"reboot without confirmation returns {status}", body)

    status, body = curl(
        vm, "/v1/op/system.reboot", "POST", '{"confirm":"wrong-hostname"}',
        headers={"X-Homebase-Confirmed": "true"},
    )
    check(status == 428, f"reboot naming the wrong machine returns {status}", body)
    check(
        parse_json(body, "the error")["error"]["code"] == "system.confirmation_mismatch",
        "with confirmation_mismatch, so a confirmation cannot be replayed elsewhere",
    )

    status, body = curl(
        vm, "/v1/op/system.get_info", "POST", '{"unexpected":"field"}',
    )
    # 400, not 500: the request was built wrong. 500 would send whoever is
    # debugging to look for a bug in Homebase instead of in their call.
    check(status == 400, f"an unknown parameter returns {status}, want 400", body)

    status, body = curl(vm, "/v1/op/system.get_info", "GET")
    check(status == 405, f"GET on an operation returns {status}", body)


def verify_audit_log(vm: VM) -> None:
    step("Verifying the audit log")

    raw = ssh(vm, ["sudo", "cat", "/var/log/homebase/audit.log"]).stdout.strip()
    events = [json.loads(line) for line in raw.splitlines() if line.strip()]
    check(len(events) > 0, f"{len(events)} audit records written")

    by_op = {}
    for e in events:
        by_op.setdefault(e["operation"], []).append(e)

    check("system.get_info" in by_op, "reads are audited, not only writes")
    check(
        any(e["outcome"] == "rejected" for e in by_op.get("system.does_not_exist", [])),
        "an attempt to invoke an operation that does not exist is recorded",
    )

    # Attempt-then-result is what makes an interrupted operation visible. Without
    # the attempt record, the log is only trustworthy for actions that finished.
    phases = [e["phase"] for e in by_op.get("system.get_info", [])]
    check(
        phases[:2] == ["attempt", "result"],
        "the attempt is recorded before the operation runs",
        f"phases seen: {phases}",
    )

    check(
        all(e.get("peer_uid") is not None for e in events),
        "every record identifies the calling process",
    )

    # Needs sudo, which is itself the assertion: an audit log the service
    # account could rewrite would be decoration.
    perms = ssh(vm, ["sudo", "stat", "-c", "%a %U", "/var/log/homebase/audit.log"]).stdout.strip()
    check(perms.startswith("640 root"), f"audit log is {perms}, owned by root and not writable by anyone else")


def verify_reboot_and_recovery(vm: VM) -> None:
    """The operation actually reboots the machine, and hostd comes back."""
    step("Rebooting the machine through hostd")

    before = ssh(vm, ["cat", "/proc/sys/kernel/random/boot_id"]).stdout.strip()

    status, body = curl(
        vm, "/v1/op/system.reboot", "POST",
        json.dumps({"confirm": VM_NAME, "reason": "hostd integration test"}),
        headers={"X-Homebase-Confirmed": "true"},
    )
    check(status == 200, f"reboot accepted ({status})", body)

    time.sleep(5)
    wait_for_ssh(vm)
    wait_for_boot_complete(vm)

    after = ssh(vm, ["cat", "/proc/sys/kernel/random/boot_id"]).stdout.strip()
    check(before != after, f"the machine really rebooted ({before[:8]}… → {after[:8]}…)")

    # Socket-activated, so the service is deliberately *not* running yet: the
    # socket unit is enabled at boot and systemd starts hostd when something
    # connects. That is the property that lets core start first and have its
    # first request block rather than fail.
    state = ssh(vm, ["systemctl", "is-active", "homebase-hostd.service"], check=False).stdout.strip()
    check(state == "inactive", f"the service is idle after reboot, awaiting a connection ({state})")

    socket_state = ssh(vm, ["systemctl", "is-active", "homebase-hostd.socket"], check=False).stdout.strip()
    check(socket_state == "active", f"the socket is listening ({socket_state})")

    status, _ = curl(vm, "/v1/health")
    check(status == 200, "connecting starts hostd on demand and it serves")

    state = ssh(vm, ["systemctl", "is-active", "homebase-hostd.service"], check=False).stdout.strip()
    check(state == "active", f"the service is now running ({state})")

    # The audit log must survive the reboot it recorded — that is the entire
    # reason the attempt record is written before the action.
    raw = ssh(vm, ["sudo", "cat", "/var/log/homebase/audit.log"]).stdout
    check(
        "system.reboot" in raw,
        "the reboot it performed is still in the audit log afterwards",
    )


def verify_sandbox(vm: VM) -> None:
    step("Verifying systemd's sandbox is applied")

    props = ssh(vm, [
        "systemctl", "show", "homebase-hostd.service",
        "-p", "NoNewPrivileges", "-p", "ProtectSystem", "-p", "ProtectHome",
        "-p", "MemoryDenyWriteExecute", "-p", "RestrictNamespaces",
    ]).stdout

    settings = dict(
        line.split("=", 1) for line in props.strip().splitlines() if "=" in line
    )

    expected = {
        "NoNewPrivileges": "yes",
        "ProtectSystem": "strict",
        "ProtectHome": "yes",
        "MemoryDenyWriteExecute": "yes",
    }
    for key, want in expected.items():
        got = settings.get(key, "<unset>")
        check(
            got == want,
            f"{key}={got}",
            f"expected {want}. These are what make the privilege split "
            f"kernel-enforced rather than merely intended.",
        )


def main() -> int:
    started = time.time()
    print()
    step("hostd integration test")
    info("install → read ops → rejections → audit → reboot → recovery → sandbox")
    print()

    vm: VM | None = None
    try:
        binary = build_hostd()

        vm = create(VM_NAME, force=True)
        start(vm)
        wait_for_ssh(vm)
        wait_for_boot_complete(vm)

        install_hostd(vm, binary)
        verify_socket_permissions(vm)
        verify_sandbox(vm)
        verify_read_operations(vm)
        verify_rejections(vm)
        verify_audit_log(vm)
        verify_reboot_and_recovery(vm)

        elapsed = int(time.time() - started)
        print()
        ok(f"PASS — hostd works under real systemd ({elapsed}s)")
        return 0

    except (TestFailure, VMError) as exc:
        print()
        fail("FAIL", str(exc))
        if isinstance(exc, VMError) and exc.hint:
            info(exc.hint)
        if vm:
            try:
                journal = ssh(vm, ["sudo", "journalctl", "-u", "homebase-hostd",
                                   "--no-pager", "-n", "40"], check=False).stdout
                if journal.strip():
                    print("\n  --- hostd journal ---")
                    for line in journal.strip().splitlines()[-25:]:
                        print(f"  {line}")
                destination = collect_logs(vm)
                info(f"Diagnostics saved to {destination}")
            except Exception:
                info("Could not collect diagnostics from the failed VM")
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
