#!/usr/bin/env python3
"""Run Homebase on a throwaway virtual machine, and leave it running.

The VM tests each create a machine, assert something about it, and destroy it.
This does the first part and stops — so there is a real Homebase, installed from
its real packages, with a real privilege boundary, that you can open in a browser
and click around in.

It is the closest thing to an installation that exists before Milestone 6 brings
an installer. Unlike `make run`, which runs both services as you on your own
machine:

  * hostd runs as root and core as the `homebase` account, so the privilege
    boundary is the real one
  * restarting the server restarts the VM rather than your laptop
  * a spare disk is plugged in, so storage can actually be tried

Run with `make vm-run`, and `make vm-run-destroy` when finished.
"""

from __future__ import annotations

import argparse
import subprocess
import sys
import time
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(REPO_ROOT / "tests/vm"))

from vmctl import (  # noqa: E402
    VM,
    VMError,
    apt,
    attach_removable_disk,
    create,
    create_removable_disk,
    destroy,
    fail,
    info,
    install_docker,
    ok,
    ssh,
    start,
    step,
    upload,
    wait_for_boot_complete,
    wait_for_ssh,
)

VM_NAME = "homebase-demo"


def build_packages(version: str) -> list[Path]:
    step("Building the packages")
    result = subprocess.run(
        ["make", "packages", f"VERSION={version}"],
        cwd=REPO_ROOT, capture_output=True, text=True,
    )
    if result.returncode != 0:
        raise VMError("make packages failed", (result.stderr or result.stdout)[-800:])

    packages = sorted((REPO_ROOT / "dist").glob(f"*_{version}_*.deb"))
    if not packages:
        raise VMError("no packages were built")
    for package in packages:
        ok(package.name)
    return packages


def install(vm: VM, packages: list[Path]) -> None:
    step("Installing Homebase")

    for package in packages:
        upload(vm, package, f"/tmp/{package.name}")

    names = " ".join(f"/tmp/{p.name}" for p in packages)
    result = apt(vm, f"install -y -qq --allow-downgrades {names}", timeout=1200)
    if result.returncode != 0:
        raise VMError("installing the packages failed",
                      (result.stdout + result.stderr).strip()[-800:])

    ssh(vm, ["sudo", "sh", "-c", "rm -f /tmp/*.deb"], check=False)

    # core listens on localhost in production, behind a reverse proxy that
    # Milestone 7 brings. Until then the forwarded port has to reach it, so this
    # widens the listen address — for this demo machine only, and it is worth
    # knowing that is not what ships.
    ssh(vm, ["sudo", "mkdir", "-p", "/etc/systemd/system/homebase-core.service.d"])
    ssh(vm, ["sudo", "sh", "-c",
             "printf '[Service]\\nExecStart=\\n"
             "ExecStart=/usr/libexec/homebase/core --listen 0.0.0.0:8080\\n' > "
             "/etc/systemd/system/homebase-core.service.d/demo-listen.conf"])
    ssh(vm, ["sudo", "systemctl", "daemon-reload"])
    ssh(vm, ["sudo", "systemctl", "restart", "homebase-core.service"])

    ok("installed")


def wait_for_dashboard(vm: VM, timeout: int = 120) -> str:
    step("Waiting for the dashboard")

    url = f"http://127.0.0.1:{vm.api_port}"
    deadline = time.time() + timeout

    while time.time() < deadline:
        result = subprocess.run(
            ["curl", "--silent", "--show-error", "--max-time", "5", url],
            capture_output=True, text=True,
        )
        if result.returncode == 0 and "Homebase" in result.stdout:
            ok(f"serving on {url}")
            return url
        time.sleep(2)

    journal = ssh(vm, ["sudo", "journalctl", "-u", "homebase-core",
                       "--no-pager", "-n", "30"], check=False).stdout
    raise VMError(f"the dashboard did not come up within {timeout}s", journal[-800:])


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--destroy", action="store_true",
                        help="destroy the demo machine and stop")
    parser.add_argument("--no-disk", action="store_true",
                        help="do not plug in the spare disk")
    parser.add_argument("--version", default="0.0.0~demo")
    args = parser.parse_args()

    if args.destroy:
        destroy(VM_NAME)
        return 0

    started = time.time()
    print()
    step("Homebase on a throwaway machine")
    info("about five minutes the first time, then a minute or two")
    print()

    vm: VM | None = None
    try:
        packages = build_packages(args.version)

        vm = create(VM_NAME, force=True)
        if not args.no_disk:
            create_removable_disk(vm, size_gb=2)
        start(vm)
        wait_for_ssh(vm)
        wait_for_boot_complete(vm)

        install_docker(vm)
        install(vm, packages)

        if not args.no_disk:
            attach_removable_disk(vm)
            info("a blank 2 GB disk is plugged in, for trying Storage")

        url = wait_for_dashboard(vm)

        elapsed = int(time.time() - started)
        print()
        ok(f"Homebase is running ({elapsed}s)")
        print()
        print(f"  Open {url}")
        print()
        print("  It will ask you to create an administrator — any name, and a")
        print("  password of twelve characters or more.")
        print()
        print("  Things worth trying:")
        print("    • Applications → install Hello Homebase, then use it")
        print("    • Storage      → erase and prepare the blank disk, then give")
        print("                     it to File Browser under Applications")
        print("    • This server  → restart it, and watch the page notice")
        print()
        print("  This is a real installation on a real machine: hostd runs as")
        print("  root, core runs as the homebase account, and restarting the")
        print("  server restarts the VM rather than your laptop.")
        print()
        print(f"  A shell on it:  python3 tests/vm/vmctl.py ssh --name {VM_NAME}")
        print("  When finished:  make vm-run-destroy")
        print()
        return 0

    except VMError as error:
        print()
        fail(str(error))
        if error.hint:
            info(error.hint)
        return 1

    except KeyboardInterrupt:
        print()
        info("interrupted; the machine is left running")
        return 130


if __name__ == "__main__":
    sys.exit(main())
