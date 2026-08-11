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

import json
import os
import re
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
    attach_removable_disk,
    create_removable_disk,
    apt,
    install_docker,
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
        ["go", "build", "-trimpath", "-o", str(out) + "/",
         "./cmd/core", "./cmd/hostd", "./cmd/homebasectl"],
        cwd=REPO_ROOT, capture_output=True, text=True, env=env,
    )
    if result.returncode != 0:
        raise VMError("go build failed", result.stderr.strip()[:800])
    ok("core, hostd and homebasectl")

    if not (DASHBOARD / "node_modules").exists():
        info("installing dashboard dependencies…")
        run(["npm", "ci"], DASHBOARD, "npm ci")

    run(["npm", "run", "build"], DASHBOARD, "dashboard build")
    dist = DASHBOARD / "dist"
    size = sum(f.stat().st_size for f in dist.rglob("*") if f.is_file())
    ok(f"dashboard ({size // 1024} KB)")

    return {"core": out / "core", "hostd": out / "hostd",
            "homebasectl": out / "homebasectl", "dist": dist}


def install(vm: VM, built: dict[str, Path]) -> None:
    step("Installing Homebase")

    ssh(vm, ["sudo", "groupadd", "--system", "--force", "homebase"])
    ssh(vm, ["sudo", "useradd", "--system", "--gid", "homebase",
             "--home-dir", "/var/lib/homebase", "--shell", "/usr/sbin/nologin",
             "homebase"], check=False)
    ssh(vm, ["sudo", "mkdir", "-p", "/usr/libexec/homebase", "/etc/homebase",
             "/var/lib/homebase", "/srv/homebase", "/srv/homebase/apps",
             "/var/log/homebase", "/usr/share/homebase/dashboard",
             "/usr/share/homebase/apps"])
    ssh(vm, ["sudo", "chown", "homebase:homebase",
             "/var/lib/homebase", "/srv/homebase", "/srv/homebase/apps",
             "/var/log/homebase"])

    # sqlite3, because hostd exports the database with VACUUM INTO rather than
    # copying it.
    #
    # Installed by hand *only* because this test copies binaries into place
    # rather than installing the packages — the homebase-hostd package declares
    # the dependency, and test_packages.py is what checks that it does.
    #
    # This line is also how the missing dependency went unnoticed: it was added
    # here to make a Milestone 5 test pass, which made the symptom go away on
    # the one machine anybody looked at while backup stayed broken on every
    # machine installed from packages. Making a test pass and fixing the product
    # are different things, and the difference is invisible from inside the
    # test.
    result = apt(vm, "install -y -qq sqlite3")
    if result.returncode != 0:
        raise TestFailure("installing sqlite3 failed\n" + result.stdout[-400:])

    for name in ("core", "hostd"):
        copy_to(vm, built[name], f"/usr/libexec/homebase/{name}", mode="0755")

    # On PATH rather than in libexec: it is the one part of Homebase somebody is
    # ever told to type, and they reach for it when they cannot sign in.
    copy_to(vm, built["homebasectl"], "/usr/bin/homebasectl", mode="0755")

    # The built dashboard: a handful of files, sent as a tarball so this is one
    # transfer rather than one per asset.
    tarball = REPO_ROOT / "bin" / "dashboard.tar.gz"
    run(["tar", "-czf", str(tarball), "-C", str(built["dist"]), "."], REPO_ROOT, "tar")
    upload(vm, tarball, "/tmp/dashboard.tar.gz")
    ssh(vm, ["sudo", "tar", "-xzf", "/tmp/dashboard.tar.gz",
             "-C", "/usr/share/homebase/dashboard"])
    ssh(vm, ["rm", "-f", "/tmp/dashboard.tar.gz"])

    # The application catalogue, left owned by root — that is the point of it.
    # These are the same manifests the homebase-apps package installs; sent
    # directly here because this test builds from the tree rather than from a
    # package, and test_packages.py covers the packaged path.
    for manifest in sorted((REPO_ROOT / "app-store").glob("*.json")):
        write_file(vm, f"/usr/share/homebase/apps/{manifest.name}",
                   manifest.read_text(), mode="0644")
    ok(f"{len(list((REPO_ROOT / 'app-store').glob('*.json')))} application manifests")

    for unit in ("homebase-hostd.service", "homebase-hostd.socket", "homebase-core.service"):
        write_file(vm, f"/etc/systemd/system/{unit}",
                   (REPO_ROOT / "packaging" / "systemd" / unit).read_text())

    # No listen override here any more. The shipped unit sets HOMEBASE_LISTEN,
    # so this test now reaches core the same way a phone on the same network
    # would — which is the point. The override that used to live here said "not
    # what ships", and was one of the two places quietly compensating for a
    # default that made a real installation unreachable.

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
    info("first-run setup → overview → restart → real reboot → sign out")
    info("then: install an application → use it → reboot → remove it, data kept")
    info("then: prepare a blank disk → set it up → give it to an application")
    info("then: be refused a backup onto that disk → set up a second one")
    info("then: back the whole thing up, check it, and preview restoring it")
    info("then: lose the password, and use the recovery code to get back in")
    info("then: be told what to do next, and give the server a name")
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


def verify_the_console_way_back_in(vm: VM) -> None:
    """The path for somebody who has lost the paper too.

    ADR-0015. Everything else about recovery happens in a browser, and is
    covered there. This is the part that cannot be: a person standing at the
    machine because the code they wrote down at setup is gone.

    It runs against the real database core has been using all journey, which is
    the only way to find out whether a second process can open it at all — core
    holds it open with a write-ahead log, and a tool that cannot read it while
    the server is running is a tool that does not work when it is needed.
    """
    step("The way back in from the console")

    listing = ssh(vm, ["sudo", "homebasectl", "list-accounts"], check=False)
    check("okan" in listing.stdout,
          f"the accounts on this server are listed ({listing.stdout.strip()})",
          listing.stdout + listing.stderr)

    result = ssh(vm, ["sudo", "homebasectl", "recovery-code"], check=False)
    check(result.returncode == 0, "a recovery code is issued from the console",
          result.stdout + result.stderr)

    code = ""
    for word in result.stdout.split():
        if re.fullmatch(r"[0-9A-HJ-KM-NP-TV-Z]{5}(-[0-9A-HJ-KM-NP-TV-Z]{5}){4}", word):
            code = word
    check(bool(code), "and printed in the shape a person can copy down",
          result.stdout)

    check("shown once" in result.stdout,
          "with the warning that it will not be shown again")

    # The claim worth testing: this code, typed into the browser, opens the
    # server. Anything less proves only that a string was printed.
    recovered = subprocess.run(
        ["curl", "--silent", "--show-error", "--max-time", "30",
         "-o", "/dev/null", "-w", "%{http_code}",
         "-X", "POST", "-H", "Content-Type: application/json",
         "-d", json.dumps({
             "username": "okan",
             "recovery_code": code,
             "new_password": "a-password-set-from-the-console-code",
         }),
         f"http://127.0.0.1:{vm.api_port}/api/v1/auth/recover"],
        capture_output=True, text=True,
    )
    check(recovered.stdout.strip() == "200",
          "and it really does open the server",
          f"the recovery endpoint answered {recovered.stdout.strip()}")

    # Spent. A code that keeps working is a permanent key printed on a screen.
    again = subprocess.run(
        ["curl", "--silent", "--max-time", "30",
         "-o", "/dev/null", "-w", "%{http_code}",
         "-X", "POST", "-H", "Content-Type: application/json",
         "-d", json.dumps({
             "username": "okan",
             "recovery_code": code,
             "new_password": "another-password-entirely-here",
         }),
         f"http://127.0.0.1:{vm.api_port}/api/v1/auth/recover"],
        capture_output=True, text=True,
    )
    check(again.stdout.strip() == "401", "and stops working once it is used",
          f"the second attempt answered {again.stdout.strip()}")


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
        # Two blank disks. One is for the storage journey to prepare and give to
        # an application; the other is for the backup journey, because Homebase
        # refuses to back up onto a disk an application keeps its files on. With
        # a single disk the browser journey cannot reach a backup at all — which
        # is the product being right, and is also what a real user needs.
        #
        # Plugged in after boot rather than present from the start, because that
        # is how a user meets one.
        create_removable_disk(vm, size_gb=2, slot=0)
        create_removable_disk(vm, size_gb=2, slot=1)
        start(vm)
        wait_for_ssh(vm)
        wait_for_boot_complete(vm)

        install_docker(vm, prepull=["traefik/whoami:v1.10.4",
                                    "filebrowser/filebrowser:v2.32.0"])
        install(vm, built)
        attach_removable_disk(vm, slot=0)
        attach_removable_disk(vm, slot=1)
        verify_reachable(vm)
        run_browser_tests(vm)
        verify_the_console_way_back_in(vm)

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
