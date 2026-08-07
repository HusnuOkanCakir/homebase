#!/usr/bin/env python3
"""The Milestone 2 exit condition, driven through a real browser.

Boots a VM running core, hostd and the built dashboard, forwards its port, and
runs the Playwright journey against it.

Everything below the browser is real: a real reboot of a real machine, so the
dashboard's behaviour while the server is away is exercised rather than
imagined. A mocked API would let every one of these assertions pass while the
thing a user touches was broken.

Run with `make vm-test-dashboard`.
"""

from __future__ import annotations

import os
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
    upload,
    wait_for_boot_complete,
    wait_for_ssh,
    write_file,
)

REPO_ROOT = Path(__file__).resolve().parents[2]
DASHBOARD = REPO_ROOT / "dashboard"
VM_NAME = "homebase-dash"


class TestFailure(Exception):
    pass


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        ok(description)
    else:
        raise TestFailure(f"{description}\n    {detail}" if detail else description)


def run(cmd: list[str], cwd: Path, what: str) -> None:
    result = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True)
    if result.returncode != 0:
        raise VMError(f"{what} failed", (result.stderr or result.stdout).strip()[:800])


def build() -> dict[str, Path]:
    step("Building")

    out = REPO_ROOT / "bin"
    out.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, "CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64"}
    result = subprocess.run(
        ["go", "build", "-trimpath", "-o", str(out) + "/", "./cmd/core", "./cmd/hostd"],
        cwd=REPO_ROOT, capture_output=True, text=True, env=env,
    )
    if result.returncode != 0:
        raise VMError("go build failed", result.stderr.strip()[:800])
    ok("core and hostd")

    if not (DASHBOARD / "node_modules").exists():
        info("installing dashboard dependencies…")
        run(["npm", "ci"], DASHBOARD, "npm ci")

    run(["npm", "run", "build"], DASHBOARD, "dashboard build")
    dist = DASHBOARD / "dist"
    size = sum(f.stat().st_size for f in dist.rglob("*") if f.is_file())
    ok(f"dashboard ({size // 1024} KB)")

    return {"core": out / "core", "hostd": out / "hostd", "dist": dist}


def install(vm: VM, built: dict[str, Path]) -> None:
    step("Installing Homebase")

    ssh(vm, ["sudo", "groupadd", "--system", "--force", "homebase"])
    ssh(vm, ["sudo", "useradd", "--system", "--gid", "homebase",
             "--home-dir", "/var/lib/homebase", "--shell", "/usr/sbin/nologin",
             "homebase"], check=False)
    ssh(vm, ["sudo", "mkdir", "-p", "/usr/libexec/homebase", "/etc/homebase",
             "/var/lib/homebase", "/srv/homebase", "/var/log/homebase",
             "/usr/share/homebase/dashboard"])
    ssh(vm, ["sudo", "chown", "homebase:homebase",
             "/var/lib/homebase", "/srv/homebase", "/var/log/homebase"])

    for name in ("core", "hostd"):
        copy_to(vm, built[name], f"/usr/libexec/homebase/{name}", mode="0755")

    # The built dashboard: a handful of files, sent as a tarball so this is one
    # transfer rather than one per asset.
    tarball = REPO_ROOT / "bin" / "dashboard.tar.gz"
    run(["tar", "-czf", str(tarball), "-C", str(built["dist"]), "."], REPO_ROOT, "tar")
    upload(vm, tarball, "/tmp/dashboard.tar.gz")
    ssh(vm, ["sudo", "tar", "-xzf", "/tmp/dashboard.tar.gz",
             "-C", "/usr/share/homebase/dashboard"])
    ssh(vm, ["rm", "-f", "/tmp/dashboard.tar.gz"])

    for unit in ("homebase-hostd.service", "homebase-hostd.socket", "homebase-core.service"):
        write_file(vm, f"/etc/systemd/system/{unit}",
                   (REPO_ROOT / "packaging" / "systemd" / unit).read_text())

    # Listen on all interfaces so the forwarded port reaches it. In production
    # core listens on localhost behind Caddy; this override exists for the test
    # and is not what ships.
    ssh(vm, ["sudo", "mkdir", "-p", "/etc/systemd/system/homebase-core.service.d"])
    write_file(vm, "/etc/systemd/system/homebase-core.service.d/test-listen.conf",
               "[Service]\nExecStart=\n"
               "ExecStart=/usr/libexec/homebase/core --listen 0.0.0.0:8080\n")

    ssh(vm, ["sudo", "systemctl", "daemon-reload"])
    ssh(vm, ["sudo", "systemctl", "enable", "--now", "homebase-hostd.socket"])
    ssh(vm, ["sudo", "systemctl", "enable", "--now", "homebase-core.service"])

    for _ in range(30):
        state = ssh(vm, ["systemctl", "is-active", "homebase-core.service"],
                    check=False).stdout.strip()
        if state == "active":
            break
        if state == "failed":
            logs = ssh(vm, ["sudo", "journalctl", "-u", "homebase-core",
                            "--no-pager", "-n", "40"], check=False).stdout
            raise TestFailure(f"core failed to start\n{logs}")
        time.sleep(1)
    else:
        raise TestFailure("core never became active")
    ok("core and hostd are running")


def verify_reachable(vm: VM) -> None:
    step("The dashboard is reachable from outside the VM")

    url = f"http://127.0.0.1:{vm.api_port}"
    deadline = time.time() + 60
    last = ""
    while time.time() < deadline:
        result = subprocess.run(
            ["curl", "--silent", "--show-error", "--max-time", "5", url],
            capture_output=True, text=True,
        )
        if result.returncode == 0 and "<title>Homebase</title>" in result.stdout:
            ok(f"serving the dashboard on {url}")
            return
        last = (result.stderr or result.stdout).strip()[:200]
        time.sleep(2)

    raise TestFailure(f"the dashboard did not become reachable on {url}", last)


def run_browser_tests(vm: VM) -> None:
    step("Running the browser journey")
    info("first-run setup → overview → restart confirmation → real reboot → sign out")
    print()

    env = {
        **os.environ,
        "HOMEBASE_URL": f"http://127.0.0.1:{vm.api_port}",
        "HOMEBASE_HOSTNAME": VM_NAME,
    }

    result = subprocess.run(
        ["npx", "playwright", "test", "--reporter=list"],
        cwd=DASHBOARD, env=env, text=True,
    )
    if result.returncode != 0:
        raise TestFailure("the browser journey failed — see the Playwright output above")

    print()
    ok("every step of the journey passed")


def ensure_browser() -> None:
    """Install the browser Playwright needs, once."""
    result = subprocess.run(
        ["npx", "playwright", "install", "--with-deps", "chromium"],
        cwd=DASHBOARD, capture_output=True, text=True,
    )
    if result.returncode != 0:
        # --with-deps needs root for system packages. Fall back to the browser
        # alone, which is usually enough on a desktop that already has them.
        result = subprocess.run(
            ["npx", "playwright", "install", "chromium"],
            cwd=DASHBOARD, capture_output=True, text=True,
        )
        if result.returncode != 0:
            raise VMError(
                "could not install the Playwright browser",
                (result.stderr or result.stdout).strip()[:500],
            )


def main() -> int:
    started = time.time()
    print()
    step("Homebase dashboard journey")
    print()

    vm: VM | None = None
    try:
        built = build()

        step("Preparing the browser")
        ensure_browser()
        ok("chromium ready")

        vm = create(VM_NAME, force=True)
        start(vm)
        wait_for_ssh(vm)
        wait_for_boot_complete(vm)

        install(vm, built)
        verify_reachable(vm)
        run_browser_tests(vm)

        elapsed = int(time.time() - started)
        print()
        ok(f"PASS — the milestone's user journey works ({elapsed}s)")
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
                                       "--no-pager", "-n", "25"], check=False).stdout
                    if journal.strip():
                        print(f"\n  --- {unit} ---")
                        for line in journal.strip().splitlines()[-15:]:
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
