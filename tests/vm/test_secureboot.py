#!/usr/bin/env python3
"""Boot Homebase on a machine with Secure Boot enforcing, as laptops ship.

Every laptop bought in the last decade has Secure Boot on and Microsoft's keys
enrolled from the factory. If a Homebase installation will not boot under that,
Milestone 9 fails at step one on every machine it is meant for — and the failure
happens before there is anything to read a log from.

This is the one hardware property that can be tested honestly without hardware.
The firmware is real: OVMF's Secure Boot build, with the `.ms` variable store
that has Microsoft's PK, KEK and db already enrolled, and SMM enabled so the
store is protected the way it is on a real machine. Ubuntu's `shimx64.efi` is
signed by Microsoft's UEFI CA, so a correct installation boots and an incorrect
one does not.

What it does not cover is a laptop whose firmware is idiosyncratic, which is
most of them. It covers the part that is the same everywhere: the chain from
firmware to shim to GRUB to the kernel.

Run with `make vm-test-secureboot`.
"""

from __future__ import annotations

import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from vmctl import (  # noqa: E402
    VM,
    VMError,
    apt,
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
VM_NAME = "homebase-secureboot"


class TestFailure(Exception):
    pass


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        ok(description)
    else:
        raise TestFailure(f"{description}\n    {detail}" if detail else description)


def verify_the_firmware_is_actually_enforcing(vm: VM) -> None:
    """The check that stops this test proving nothing.

    A machine booted with the plain variable store has no keys, sits in setup
    mode, and boots absolutely anything. It would pass every assertion below
    while testing nothing at all — so the first thing to establish is that the
    firmware is enforcing, from the kernel's own view of it.
    """
    step("Is Secure Boot actually on?")

    result = apt(vm, "install -y -qq mokutil", timeout=600)
    if result.returncode != 0:
        raise TestFailure("installing mokutil failed\n" + (result.stdout + result.stderr)[-400:])

    state = ssh(vm, ["mokutil", "--sb-state"], check=False).stdout.strip()
    check("SecureBoot enabled" in state,
          f"the firmware reports Secure Boot enabled ({state!r})",
          "If this says disabled or setup mode, the machine booted with a "
          "variable store that has no keys in it, and everything below proves "
          "nothing.")

    # And the kernel agrees, from a different source: the EFI variable itself.
    # mokutil reads the same place, but a machine where these two disagree is
    # one worth stopping on.
    efi = ssh(vm, ["sh", "-c",
                   "od -An -t u1 /sys/firmware/efi/efivars/SecureBoot-* 2>/dev/null "
                   "| awk '{print $NF}'"], check=False).stdout.strip()
    check(efi == "1", f"and the kernel's own EFI variable says so ({efi!r})")

    # Setup mode is the state where the firmware has no platform key and will
    # enrol anything. It must be off.
    setup = ssh(vm, ["sh", "-c",
                     "od -An -t u1 /sys/firmware/efi/efivars/SetupMode-* 2>/dev/null "
                     "| awk '{print $NF}'"], check=False).stdout.strip()
    check(setup == "0", f"and the firmware is not in setup mode ({setup!r})")


def verify_it_booted_through_shim(vm: VM) -> None:
    """The chain the firmware actually verified."""
    step("What the machine booted through")

    # sudo, because the EFI partition is mounted 0700 root.
    #
    # Without it `ls` writes nothing to stdout and the assertion fails while the
    # machine is perfectly fine — which is exactly what happened the first time,
    # with `2>/dev/null` hiding the permission error that would have said so.
    listing = ssh(vm, ["sudo", "ls", "/boot/efi/EFI/ubuntu/"], check=False)
    if listing.returncode != 0:
        raise TestFailure("could not read the EFI partition: "
                          + (listing.stdout + listing.stderr).strip()[:300])
    installed = listing.stdout

    check("shimx64.efi" in installed,
          "the machine has Ubuntu's signed shim on its EFI partition",
          f"found: {installed.split()!r}\n    "
          "This is the binary Microsoft's key signs. Without it a Secure Boot "
          "machine has nothing it will accept.")
    check("grubx64.efi" in installed, "and the bootloader shim hands over to")

    # The packages that put them there, which is what an upgrade could break.
    packages = ssh(vm, ["sh", "-c",
                        "dpkg-query -W -f '${Package} ${Status}\n' "
                        "shim-signed grub-efi-amd64-signed 2>&1"], check=False).stdout
    check("shim-signed install ok installed" in packages,
          "installed from shim-signed, the Microsoft-signed package",
          packages.strip())
    check("grub-efi-amd64-signed install ok installed" in packages,
          "with a signed bootloader to match", packages.strip())


def build_packages(version: str) -> list[Path]:
    step(f"Building packages ({version})")
    result = subprocess.run(["make", "packages", f"VERSION={version}"],
                            cwd=REPO_ROOT, capture_output=True, text=True)
    if result.returncode != 0:
        raise VMError("building the packages failed:\n"
                      + (result.stdout + result.stderr)[-800:])
    packages = sorted((REPO_ROOT / "dist").glob(f"*_{version}_*.deb"))
    check(len(packages) == 4, f"four packages at {version} ({len(packages)})")
    return packages


def install_homebase(vm: VM, packages: list[Path]) -> None:
    step("Installing Homebase on a Secure Boot machine")

    for package in packages:
        copy_to(vm, package, f"/tmp/{package.name}")

    names = " ".join(f"/tmp/{p.name}" for p in packages)
    result = apt(vm, f"install -y -qq {names}", timeout=1200)
    if result.returncode != 0:
        raise TestFailure("installing failed\n" + (result.stdout + result.stderr)[-600:])

    for _ in range(60):
        if ssh(vm, ["systemctl", "is-active", "homebase-core.service"],
               check=False).stdout.strip() == "active":
            break
        time.sleep(1)
    else:
        journal = ssh(vm, ["sudo", "journalctl", "-u", "homebase-core",
                           "--no-pager", "-n", "25"], check=False).stdout
        raise TestFailure(f"core did not start\n{journal}")
    ok("Homebase is running")


def verify_homebase_works(vm: VM) -> None:
    """Nothing Homebase does should care about Secure Boot — checked, not assumed.

    It loads no kernel modules of its own and signs nothing, so there is no
    reason for it to fail here. "No reason to fail" is exactly the kind of claim
    that turns out to be wrong on the machine somebody actually owns.
    """
    step("Does Homebase work here?")

    code = ssh(vm, ["curl", "--silent", "--insecure", "--max-time", "20",
                    "-o", "/dev/null", "-w", "%{http_code}",
                    "https://127.0.0.1/api/v1/health"], check=False).stdout.strip()
    check(code == "200", f"the dashboard answers ({code})")

    out = ssh(vm, ["sudo", "curl", "--silent", "--max-time", "30",
                   "--unix-socket", "/run/homebase/hostd.sock",
                   "-w", "\\n%{http_code}", "-X", "POST",
                   "-H", "Content-Type: application/json", "-d", "{}",
                   "http://localhost/v1/op/system.get_info"], check=False).stdout.strip()
    check(out.endswith("200"), f"and the privileged service answers ({out[-3:]})")

    # The sandboxing is the part most likely to interact badly with a firmware
    # difference, because it is the part that touches the kernel hardest.
    failed = ssh(vm, ["systemctl", "list-units", "--state=failed", "--no-legend"],
                 check=False).stdout.strip()
    check("homebase" not in failed,
          "and no Homebase unit failed to start",
          f"failed units:\\n    {failed}")


def verify_it_survives_a_reboot(vm: VM) -> None:
    """A machine that boots once under Secure Boot and not twice is worse than
    one that never booted: it fails after somebody has put their photographs on
    it."""
    step("And again after a restart")

    ssh(vm, ["sudo", "systemctl", "reboot"], check=False)
    time.sleep(5)
    wait_for_ssh(vm)
    wait_for_boot_complete(vm)

    state = ssh(vm, ["mokutil", "--sb-state"], check=False).stdout.strip()
    check("SecureBoot enabled" in state, f"still enforcing after a reboot ({state!r})")

    for _ in range(60):
        if ssh(vm, ["systemctl", "is-active", "homebase-core.service"],
               check=False).stdout.strip() == "active":
            break
        time.sleep(1)
    check(ssh(vm, ["systemctl", "is-active", "homebase-core.service"],
              check=False).stdout.strip() == "active",
          "and Homebase started by itself")


def main() -> int:
    started = time.time()
    vm: VM | None = None

    print()
    step("Homebase with Secure Boot enforcing")
    info("real OVMF, Microsoft's keys enrolled, SMM on — the firmware every")
    info("laptop ships with")
    print()

    try:
        packages = build_packages("0.0.0~dev")

        vm = create(VM_NAME, force=True, secure_boot=True)
        start(vm)
        wait_for_ssh(vm)
        wait_for_boot_complete(vm)
        ok("the machine booted with the firmware enforcing signatures")

        apt(vm, "update -qq", timeout=600)

        verify_the_firmware_is_actually_enforcing(vm)
        verify_it_booted_through_shim(vm)
        install_homebase(vm, packages)
        verify_homebase_works(vm)
        verify_it_survives_a_reboot(vm)

        elapsed = int(time.time() - started)
        print()
        ok(f"PASS — Homebase installs and runs on a machine with Secure Boot "
           f"enforcing, as laptops ship ({elapsed}s)")
        return 0

    except (TestFailure, VMError) as error:
        print()
        fail(str(error))
        if isinstance(error, VMError) and error.hint:
            info(error.hint)
        if vm:
            try:
                console = vm.console_log
                if console.exists():
                    print("\n  --- the last of the serial console ---")
                    for line in console.read_text(errors="replace").splitlines()[-25:]:
                        print(f"  {line}")
                info(f"logs: {collect_logs(vm)}")
            except Exception:
                pass
        return 1

    except KeyboardInterrupt:
        print()
        info("interrupted")
        return 130

    finally:
        if vm:
            print()
            destroy(VM_NAME)


if __name__ == "__main__":
    sys.exit(main())
