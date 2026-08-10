#!/usr/bin/env python3
"""Milestone 6's exit condition.

    Starting from a Windows-occupied disk, the installer produces a working
    server that reaches the dashboard and installs an application — with no
    Linux commands.

Every clause of that is a step below, and the first one is the reason this test
is slow: the target disk is given a real GPT with Windows' partition types and
an NTFS signature before anything is installed onto it. A blank disk is the easy
case. It is not the case anybody has.

Nothing here is overlaid or simulated. The official Ubuntu ISO boots, Ubuntu's
own installer asks whether to continue, this test answers it by pressing keys,
and what comes up afterwards is a machine somebody could use.

Run with `make vm-test-installer`. It takes about fifteen minutes.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "vm"))

import vmctl  # noqa: E402
from vmctl import (  # noqa: E402
    VM,
    VMError,
    collect_logs,
    destroy,
    fail,
    info,
    ok,
    run,
    ssh,
    step,
    wait_for_ssh,
)

import installerctl as ic  # noqa: E402

REPO_ROOT = Path(__file__).resolve().parents[2]
VM_NAME = "homebase-installed"

# The account Homebase's own installer creates. Not "dev" — that is the
# development lab's cloud-init user, and a machine produced by the installer has
# never heard of it.
CONSOLE_USER = "console"

ADMIN = "okan"
PASSWORD = "a-sufficiently-long-password"


class TestFailure(Exception):
    pass


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        ok(description)
    else:
        raise TestFailure(f"{description}\n    {detail}" if detail else description)


def build_media(vm: VM) -> Path:
    """Build the installation media with the tool a user would use.

    The whole stick, not the ISO plus a seed handed to QEMU separately: what
    boots here is byte for byte what `homebasectl installer create` writes to a
    USB drive, so the thing being tested is the thing being shipped.
    """
    step("Building the installation media")

    result = subprocess.run(
        ["go", "build", "-trimpath", "-o", str(REPO_ROOT / "bin") + "/", "./cmd/..."],
        cwd=REPO_ROOT, capture_output=True, text=True,
        env={**os.environ, "CGO_ENABLED": "0"},
    )
    if result.returncode != 0:
        raise VMError("go build failed", result.stderr.strip()[:800])

    dashboard = REPO_ROOT / "dashboard"
    if not (dashboard / "node_modules").exists():
        run(["npm", "ci"], cwd=dashboard)
    build = subprocess.run(["npm", "run", "build"], cwd=dashboard, capture_output=True, text=True)
    if build.returncode != 0:
        raise VMError("dashboard build failed", build.stderr.strip()[:800])

    # A clean directory. `homebasectl installer seed` refuses to guess between
    # versions, which is the correct behaviour and a nuisance here.
    dist = REPO_ROOT / "dist"
    if dist.exists():
        run(["rm", "-rf", str(dist)])
    packaged = subprocess.run(
        [sys.executable, "scripts/build-packages.py", "--version", "0.0.0~installer"],
        cwd=REPO_ROOT, capture_output=True, text=True,
    )
    if packaged.returncode != 0:
        raise VMError("building packages failed", packaged.stdout[-800:])
    ok("four Debian packages")

    # A throwaway key, so the test can look inside the machine afterwards. A
    # real stick is usually made without one: the server is managed from a
    # browser and has no reason to run sshd at all.
    key = vm.key
    run(["ssh-keygen", "-t", "ed25519", "-N", "", "-f", str(key), "-q", "-C", "installer-test"])
    public_key = key.with_suffix(".pub").read_text().strip()

    iso = ic.fetch_iso()

    media = vm.dir / "installer.img"
    made = subprocess.run(
        [str(REPO_ROOT / "bin" / "homebasectl"), "installer", "create",
         "--iso", str(iso),
         "--packages", str(dist),
         "--output", str(media),
         "--hostname", "homebase",
         "--authorized-key", public_key],
        capture_output=True, text=True,
    )
    if made.returncode != 0:
        raise VMError("homebasectl installer create failed",
                      (made.stderr or made.stdout).strip()[:800])
    ok(f"media built by homebasectl ({media.stat().st_size // 1024 // 1024} MB)")

    return media


def install(vm: VM, media: Path) -> None:
    ic.boot_installer(vm, media)
    ic.wait_for_confirmation_prompt(vm)
    ic.wait_for_install(vm, timeout=ic.INSTALL_TIMEOUT_S)


def verify_the_machine_works(vm: VM) -> None:
    step("What came up")

    # The installer's account, not the lab's.
    vmctl.SSH_USER = CONSOLE_USER

    # This is the real finish line. The machine reboots itself at the end of the
    # install, and because the disk has boot priority it comes back up as the
    # system that was just installed — so a machine answering here has genuinely
    # completed, bootloader and all.
    wait_for_ssh(vm, timeout=900)

    stamp = ssh(vm, ["cat", "/etc/homebase-installed"], check=False).stdout
    check("installed_by=homebase-installer" in stamp,
          "the machine says it came from Homebase's installer",
          stamp)

    release = ssh(vm, ["lsb_release", "-ds"], check=False).stdout.strip()
    check("Ubuntu" in release, f"running {release}")

    # Windows is gone, and Ubuntu is on the disk that had it.
    filesystems = ssh(vm, ["sh", "-c", "lsblk -no FSTYPE"], check=False).stdout
    check("ntfs" not in filesystems.lower(),
          "nothing on the disk is NTFS any more",
          filesystems)
    check("ext4" in filesystems, "the disk carries an ext4 root filesystem", filesystems)

    for service in ("homebase-hostd.socket", "homebase-core.service"):
        state = ssh(vm, ["systemctl", "is-enabled", service], check=False).stdout.strip()
        check(state == "enabled", f"{service} starts at boot ({state})")
        active = ssh(vm, ["systemctl", "is-active", service], check=False).stdout.strip()
        check(active in ("active", "listening"), f"{service} is running ({active})")

    # The privilege boundary, as the installer leaves it. This is the same
    # assertion the package test makes, and it is worth making again here:
    # the packages are correct and the installer could still get this wrong.
    perms = ssh(vm, ["stat", "-c", "%a %U %G", "/run/homebase/hostd.sock"],
                check=False).stdout.strip()
    check(perms == "660 root homebase", f"the hostd socket is {perms}",
          "Expected '660 root homebase'. This is the boundary.")

    user = ssh(vm, ["ps", "-o", "user=", "-C", "core"], check=False).stdout.strip()
    check(user == "homebase", f"core runs as {user or '<not running>'}")


def verify_the_laptop_behaviour(vm: VM) -> None:
    """The things that make it a server rather than a laptop."""
    step("It behaves like a server, not a laptop")

    logind = ssh(vm, ["sh", "-c",
                      "cat /etc/systemd/logind.conf.d/homebase.conf 2>/dev/null"],
                 check=False).stdout
    check("HandleLidSwitch=ignore" in logind,
          "closing the lid does not switch it off",
          logind)

    masked = ssh(vm, ["systemctl", "is-enabled", "suspend.target"], check=False).stdout.strip()
    check(masked == "masked", f"it cannot suspend itself ({masked})")

    # Checked before the firewall, because sudo is how the firewall is read —
    # and because an account that cannot run sudo cannot run the console
    # recovery path either, which is the whole reason it exists (ADR-0015).
    elevated = ssh(vm, ["sudo", "-n", "true"], check=False)
    check(elevated.returncode == 0,
          "the console account can become root without a password",
          (elevated.stdout + elevated.stderr).strip() or
          "sudo produced nothing at all, which is what a locked password looks like")

    firewall = ssh(vm, ["sudo", "-n", "ufw", "status"], check=False)
    check("Status: active" in firewall.stdout, "the firewall is on",
          (firewall.stdout + firewall.stderr).strip() or "ufw said nothing")
    check("8080" in firewall.stdout, "and lets the dashboard through", firewall.stdout)

    autologin = ssh(vm, ["sh", "-c",
                         "cat /etc/systemd/system/getty@tty1.service.d/autologin.conf"],
                    check=False).stdout
    check("--autologin console" in autologin,
          "and is logged in automatically on the machine's own screen",
          autologin)

    # The console tool travels with core's package, and this is the machine
    # where somebody would actually reach for it.
    listed = ssh(vm, ["sudo", "-n", "homebasectl", "list-accounts"], check=False)
    check(listed.returncode == 0, "homebasectl is on PATH",
          listed.stdout + listed.stderr)


def verify_the_dashboard(vm: VM) -> None:
    """Reaching the dashboard, and claiming the server, from outside the machine."""
    step("Reaching it from a browser")

    url = f"http://127.0.0.1:{vm.api_port}"
    deadline = time.time() + 120
    while time.time() < deadline:
        result = subprocess.run(
            ["curl", "--silent", "--max-time", "5", url],
            capture_output=True, text=True,
        )
        if "<title>Homebase</title>" in result.stdout:
            break
        time.sleep(3)
    else:
        raise TestFailure(f"the dashboard never became reachable on {url}")
    ok("the dashboard is served, on the machine the installer produced")

    status = api(vm, "GET", "/api/v1/setup")
    check(status.get("needs_setup") is True,
          "it has no administrator yet, and says so")

    created = api(vm, "POST", "/api/v1/setup",
                  {"username": ADMIN, "password": PASSWORD})
    check(created.get("user", {}).get("username") == ADMIN,
          "an administrator can be created")
    check(bool(created.get("recovery_code")),
          "and is given a recovery code to write down",
          "ADR-0015: a server whose first screen produces no code is one that "
          "will be lost the first time its owner forgets a password.")


def verify_an_application_installs(vm: VM) -> None:
    """The last clause of the exit condition."""
    step("Installing an application")

    session = login(vm)

    catalogue = api(vm, "GET", "/api/v1/apps", session=session)
    names = [app["id"] for app in catalogue.get("items", [])]
    check(len(names) > 0, f"the catalogue is there ({', '.join(names)})")

    job = api(vm, "POST", "/api/v1/apps/hello-homebase/install", {}, session=session)
    job_id = job.get("job_id")
    check(bool(job_id), "installing started", json.dumps(job)[:300])

    deadline = time.time() + 600
    state = ""
    while time.time() < deadline:
        current = api(vm, "GET", f"/api/v1/jobs/{job_id}", session=session)
        state = current.get("state", "")
        if state in ("succeeded", "failed", "cancelled"):
            break
        time.sleep(5)

    check(state == "succeeded",
          f"the application installed ({state})",
          json.dumps(api(vm, 'GET', f'/api/v1/jobs/{job_id}', session=session))[:500])

    running = ssh(vm, ["sudo", "-n", "docker", "ps", "--format", "{{.Names}}"], check=False).stdout
    check("hello-homebase" in running or "homebase" in running,
          "and is running as a container", running)


# --- Talking to the machine --------------------------------------------------

def api(vm: VM, method: str, path: str, body: dict | None = None,
        session: str | None = None) -> dict:
    command = ["curl", "--silent", "--show-error", "--max-time", "60",
               "-X", method, f"http://127.0.0.1:{vm.api_port}{path}"]
    if body is not None:
        command += ["-H", "Content-Type: application/json", "-d", json.dumps(body)]
    if session:
        command += ["-H", f"Cookie: homebase_session={session}"]

    result = subprocess.run(command, capture_output=True, text=True)
    if not result.stdout.strip():
        return {}
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError:
        return {"raw": result.stdout[:400]}


def login(vm: VM) -> str:
    result = subprocess.run(
        ["curl", "--silent", "--max-time", "60", "-i",
         "-X", "POST", "-H", "Content-Type: application/json",
         "-d", json.dumps({"username": ADMIN, "password": PASSWORD}),
         f"http://127.0.0.1:{vm.api_port}/api/v1/auth/login"],
        capture_output=True, text=True,
    )
    for line in result.stdout.splitlines():
        if "homebase_session=" in line:
            return line.split("homebase_session=")[1].split(";")[0]
    raise TestFailure("could not sign in to the installed server")


# --- Main --------------------------------------------------------------------

def main() -> int:
    started = time.time()
    vm: VM | None = None

    print()
    step("Homebase installer")
    info("a Windows-occupied disk → a working server → an application installed")
    print()

    try:
        ic.check_host_memory()
        destroy(VM_NAME)
        vm = ic.create_target(VM_NAME, force=True)
        ic.put_windows_on(vm)

        media = build_media(vm)
        install(vm, media)

        verify_the_machine_works(vm)
        verify_the_laptop_behaviour(vm)
        verify_the_dashboard(vm)
        verify_an_application_installs(vm)

        elapsed = int(time.time() - started)
        print()
        ok(f"PASS — a Windows laptop became a Homebase server ({elapsed}s)")
        return 0

    except (TestFailure, VMError) as exc:
        print()
        fail("FAIL", str(exc))
        if isinstance(exc, VMError) and exc.hint:
            info(exc.hint)
        if vm is not None:
            shot = ic.screenshot(vm, "failure")
            if shot:
                info(f"  what was on the screen: {shot}")
            try:
                collect_logs(vm)
            except Exception:  # noqa: BLE001
                pass
        return 1

    finally:
        # Left running on request, because the interesting failures here are on
        # the machine rather than in the output: "the stamp file is missing" is
        # a sentence, and `ls /etc` is an answer.
        #
        #     HOMEBASE_KEEP_VM=1 make vm-test-installer
        if vm is not None and not os.environ.get("HOMEBASE_KEEP_VM"):
            destroy(VM_NAME)
        elif vm is not None:
            info(f"'{VM_NAME}' left running: "
                 f"ssh -i {vm.key} -p {vm.ssh_port} {CONSOLE_USER}@127.0.0.1")


if __name__ == "__main__":
    sys.exit(main())
