#!/usr/bin/env python3
"""The Milestone 1 exit condition, as an executable test.

    One command creates a clean VM, installs a service, reboots, verifies health,
    exports logs and destroys the machine.

Run with `make vm-test`.

The service installed here is deliberately trivial — a systemd unit that writes a
file. Milestone 1 is proving the *harness*, not Homebase. What it must demonstrate
is that a service installed in a VM is still running after a reboot, because
every milestone after this one depends on being able to test exactly that.

The test destroys its VM on the way out, including when it fails. A failing test
that leaves a 20 GB disk image behind is a test people stop running.
"""

from __future__ import annotations

import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from vmctl import (  # noqa: E402
    VM,
    VMError,
    collect_logs,
    create,
    destroy,
    fail,
    info,
    ok,
    reboot,
    ssh,
    start,
    step,
    wait_for_ssh,
)

VM_NAME = "homebase-test"

# A service that records each boot. Trivial on purpose: if this survives a reboot,
# the harness can test things that matter.
UNIT = """[Unit]
Description=Homebase VM harness probe
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'echo "booted $(date -Is)" >> /var/lib/homebase-probe/boots'

[Install]
WantedBy=multi-user.target
"""


class TestFailure(Exception):
    pass


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        ok(description)
    else:
        raise TestFailure(f"{description}\n    {detail}" if detail else description)


def install_probe(vm: VM) -> None:
    step("Installing a systemd service")

    ssh(vm, ["sudo", "mkdir", "-p", "/var/lib/homebase-probe"])

    # Written through a single-quoted printf: the unit file contains newlines and
    # '%' would otherwise be interpreted, and passing it as one argv element keeps
    # it away from two layers of shell quoting (local, then remote).
    encoded = UNIT.replace("'", "'\\''")
    ssh(vm, [
        "sudo", "sh", "-c",
        f"printf '%s' '{encoded}' > /etc/systemd/system/homebase-probe.service",
    ])

    ssh(vm, ["sudo", "systemctl", "daemon-reload"])
    ssh(vm, ["sudo", "systemctl", "enable", "--now", "homebase-probe.service"])
    ok("homebase-probe.service installed and enabled")


def verify_healthy(vm: VM, expected_boots: int) -> None:
    step(f"Verifying health (expecting {expected_boots} boot record(s))")

    state = ssh(vm, ["systemctl", "is-enabled", "homebase-probe.service"]).stdout.strip()
    check(state == "enabled", f"service is enabled ({state})")

    active = ssh(vm, ["systemctl", "is-active", "homebase-probe.service"]).stdout.strip()
    check(active == "active", f"service is active ({active})")

    boots = ssh(vm, ["sudo", "cat", "/var/lib/homebase-probe/boots"]).stdout.strip()
    lines = [ln for ln in boots.splitlines() if ln.strip()]
    check(
        len(lines) == expected_boots,
        f"recorded {len(lines)} boot(s), expected {expected_boots}",
        f"file contents:\n{boots}",
    )

    failed = ssh(vm, ["systemctl", "--failed", "--no-legend"], check=False).stdout.strip()
    check(not failed, "no failed units", failed)


def main() -> int:
    started = time.time()
    print()
    step("Homebase VM lifecycle test")
    info("create → install service → reboot → verify → export logs → destroy")
    print()

    vm: VM | None = None
    try:
        vm = create(VM_NAME, force=True)
        start(vm)
        wait_for_ssh(vm)

        step("Confirming the guest is what we asked for")
        release = ssh(vm, ["lsb_release", "-ds"], check=False).stdout.strip()
        check(bool(release), f"guest reports: {release}")

        # Homebase ships UEFI-only, so a lab that quietly booted BIOS would be
        # testing a machine we do not ship.
        efi = ssh(vm, ["test", "-d", "/sys/firmware/efi"], check=False)
        check(efi.returncode == 0, "booted under UEFI, not legacy BIOS")

        install_probe(vm)
        verify_healthy(vm, expected_boots=1)

        reboot(vm)
        verify_healthy(vm, expected_boots=2)

        destination = collect_logs(vm)
        check(
            (destination / "console.log").exists(),
            f"logs exported to {destination}",
        )

        elapsed = int(time.time() - started)
        print()
        ok(f"PASS — the harness works end to end ({elapsed}s)")
        return 0

    except (TestFailure, VMError) as exc:
        print()
        fail("FAIL", str(exc))
        if isinstance(exc, VMError) and exc.hint:
            info(exc.hint)
        if vm:
            try:
                destination = collect_logs(vm)
                info(f"Diagnostics saved to {destination} before teardown")
            except Exception:
                info("Could not collect logs from the failed VM")
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
