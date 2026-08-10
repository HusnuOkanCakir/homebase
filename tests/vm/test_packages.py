#!/usr/bin/env python3
"""Installing, upgrading and removing the Debian packages, on a clean machine.

The earlier VM tests install Homebase by copying files and writing units by
hand. That proves the software works; it does not prove the *package* does, and
the package is what a user actually receives.

The assertions that matter here are the ones about data. An upgrade runs on a
machine that already holds somebody's photographs and their only administrator
account, and a maintainer script is the last place a mistake gets noticed —
`dpkg` has already started, and the alternative to getting it right is a
half-configured system with no obvious way back.

Run with `make vm-test-packages`.
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
    apt,
    VMError,
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
VM_NAME = "homebase-pkg"
PASSWORD = "a-sufficiently-long-password"


class TestFailure(Exception):
    pass


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        ok(description)
    else:
        raise TestFailure(f"{description}\n    {detail}" if detail else description)


def build(version: str) -> list[Path]:
    step(f"Building packages ({version})")
    result = subprocess.run(
        ["make", "packages", f"VERSION={version}"],
        cwd=REPO_ROOT, capture_output=True, text=True,
    )
    if result.returncode != 0:
        raise VMError("make packages failed", (result.stderr or result.stdout)[-800:])

    packages = sorted((REPO_ROOT / "dist").glob(f"*_{version}_*.deb"))
    # hostd, core, apps, dashboard. Named rather than counted, so a package that
    # silently stops being built is a failure here rather than a surprise on
    # somebody's machine.
    expected = {"homebase-hostd", "homebase-core", "homebase-apps", "homebase-dashboard"}
    built = {p.name.split("_")[0] for p in packages}
    if built != expected:
        raise VMError(f"expected {sorted(expected)}, built {sorted(built)}")
    for p in packages:
        ok(p.name)
    return packages


def api(vm: VM, path: str, method: str = "GET", body: str | None = None) -> tuple[int, str]:
    cmd = ["curl", "--silent", "--show-error",
           "-c", "/tmp/cookies", "-b", "/tmp/cookies",
           "-o", "/dev/stdout", "-w", "\\n%{http_code}", "-X", method]
    if body is not None:
        cmd += ["-H", "Content-Type: application/json", "-d", body]
    cmd.append(f"http://127.0.0.1:8080/api/v1{path}")

    result = ssh(vm, cmd, check=False)
    output = result.stdout.strip()
    if not output:
        return 0, result.stderr.strip()
    parts = output.rsplit("\n", 1)
    return (int(parts[1]), parts[0]) if len(parts) == 2 else (int(output), "")


def install(vm: VM, packages: list[Path], what: str) -> None:
    step(what)

    for package in packages:
        upload(vm, package, f"/tmp/{package.name}")

    # apt rather than dpkg, because homebase-apps depends on a container runtime
    # and dpkg does not resolve dependencies — it would report them unmet and
    # stop. This is also closer to what a user does, and it is the path where an
    # unsatisfiable dependency actually shows up.
    names = " ".join(f"/tmp/{p.name}" for p in packages)
    result = apt(vm, f"install -y -qq --allow-downgrades {names}", timeout=1200)
    if result.returncode != 0:
        raise TestFailure(f"dpkg failed\n{result.stdout}\n{result.stderr}")

    ssh(vm, ["sudo", "sh", "-c", "rm -f /tmp/*.deb"], check=False)


def wait_for_api(vm: VM, timeout: int = 60) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        status, _ = api(vm, "/setup")
        if status == 200:
            return
        time.sleep(2)
    logs = ssh(vm, ["sudo", "journalctl", "-u", "homebase-core", "--no-pager", "-n", "30"],
               check=False).stdout
    raise TestFailure(f"the API did not come up within {timeout}s\n{logs}")


def verify_installed(vm: VM) -> None:
    step("What the packages put on the machine")

    # The privilege boundary, as the package leaves it.
    perms = ssh(vm, ["stat", "-c", "%a %U %G", "/run/homebase/hostd.sock"]).stdout.strip()
    check(perms == "660 root homebase", f"the hostd socket is {perms}",
          "Expected '660 root homebase'. This is the boundary; a package that "
          "gets it wrong removes it without any code changing.")

    for path, expected in [
        ("/etc/homebase", "750 root homebase"),
        ("/var/lib/homebase", "750 homebase homebase"),
        ("/srv/homebase", "750 homebase homebase"),
    ]:
        actual = ssh(vm, ["sudo", "stat", "-c", "%a %U %G", path]).stdout.strip()
        check(actual == expected, f"{path} is {actual}")

    # core must not be able to rewrite its own configuration.
    result = ssh(vm, ["sudo", "-u", "homebase", "touch", "/etc/homebase/probe"], check=False)
    check(result.returncode != 0,
          "core cannot write to /etc/homebase",
          "A configuration file core can rewrite is one an attacker who owns core can rewrite.")

    user = ssh(vm, ["ps", "-o", "user=", "-C", "core"], check=False).stdout.strip()
    check(user == "homebase", f"core runs as {user or '<not running>'}")

    shell = ssh(vm, ["getent", "passwd", "homebase"]).stdout.strip()
    check(shell.endswith("nologin") or shell.endswith("false"),
          f"the homebase account has no login shell ({shell.split(':')[-1]})")

    for service in ("homebase-hostd.socket", "homebase-core.service"):
        state = ssh(vm, ["systemctl", "is-enabled", service], check=False).stdout.strip()
        check(state == "enabled", f"{service} is enabled at boot ({state})")

    # The console tool, on PATH. It ships in the core package because it is what
    # somebody reaches for when they cannot sign in — a recovery path that is
    # not installed by the package is a recovery path nobody has. ADR-0015.
    listing = ssh(vm, ["sudo", "homebasectl", "list-accounts"], check=False)
    check(listing.returncode == 0,
          "homebasectl is on PATH and can read the database",
          (listing.stdout + listing.stderr).strip()[:300])

    # Without sudo it must explain itself rather than appearing not to exist:
    # /usr/bin rather than /usr/sbin is what buys that.
    unprivileged = ssh(vm, ["homebasectl", "list-accounts"], check=False)
    check("sudo" in (unprivileged.stdout + unprivileged.stderr).lower(),
          "and without sudo it says to use sudo",
          (unprivileged.stdout + unprivileged.stderr).strip()[:300])

    verify_socket_survives_a_restart(vm)


def verify_socket_survives_a_restart(vm: VM) -> None:
    """Restarting hostd must not destroy the socket.

    systemd removes a RuntimeDirectory when a service stops, and the socket the
    *socket unit* owns lives inside hostd's. So stopping hostd for a moment
    deleted /run/homebase/hostd.sock while homebase-hostd.socket carried on
    reporting "active (running)" against a path that no longer existed — nothing
    could connect again, and nothing said so.

    Every upgrade restarts hostd, so this is not a corner. It is checked here
    rather than only in the upgrade step because the failure is silent: the units
    all look healthy afterwards, and the only symptom is core reporting that it
    cannot reach the part of itself that manages the server, for ever.
    """
    # Activate it first: the directory is only at risk once the service has run.
    ssh(vm, ["curl", "--silent", "--max-time", "10", "-o", "/dev/null",
             "http://127.0.0.1:8080/api/v1/health"], check=False)
    ssh(vm, ["sudo", "systemctl", "restart", "homebase-hostd.service"], check=False)

    perms = ssh(vm, ["stat", "-c", "%a %U %G", "/run/homebase/hostd.sock"],
                check=False)
    check(perms.returncode == 0 and perms.stdout.strip() == "660 root homebase",
          f"the socket survives restarting hostd ({perms.stdout.strip() or 'gone'})",
          "systemd removed the RuntimeDirectory and the socket inside it. "
          "The socket unit still reports itself as listening, so nothing looks "
          "wrong — but the privilege boundary is unreachable until it is "
          "restarted. See RuntimeDirectoryPreserve in homebase-hostd.service.")

    # And it still works, which is the property the mode is a proxy for.
    status, _ = api(vm, "/setup")
    check(status == 200, f"and core can still reach hostd through it ({status})")


def create_data(vm: VM) -> None:
    step("Putting real data on the machine")

    wait_for_api(vm)

    status, body = api(vm, "/setup", "POST",
                       json.dumps({"username": "okan", "password": PASSWORD}))
    check(status == 201, f"administrator created ({status})", body)

    # A file where a user's own files would live.
    ssh(vm, ["sudo", "-u", "homebase", "sh", "-c",
             "echo 'a photograph' > /srv/homebase/important.txt"])
    ok("a file in /srv/homebase")

    status, body = api(vm, "/system")
    check(status == 200, "the API works through the packaged install", body)


def verify_data_survived(vm: VM, what: str) -> None:
    step(f"Data survived {what}")

    wait_for_api(vm)

    status, body = api(vm, "/setup")
    check(json.loads(body)["needs_setup"] is False,
          "the administrator account is still there",
          "If setup is offered again, the database was lost and anybody on the "
          "network can claim this server.")

    status, _ = api(vm, "/auth/login", "POST",
                    json.dumps({"username": "okan", "password": PASSWORD}))
    check(status == 200, "the same password still signs in")

    contents = ssh(vm, ["sudo", "cat", "/srv/homebase/important.txt"]).stdout.strip()
    check(contents == "a photograph", f"the file in /srv/homebase is intact ({contents!r})")

    perms = ssh(vm, ["sudo", "stat", "-c", "%U %G", "/srv/homebase/important.txt"]).stdout.strip()
    check(perms == "homebase homebase", f"and still owned by {perms}")


def verify_upgrade(vm: VM, packages: list[Path]) -> None:
    install(vm, packages, "Upgrading in place")

    version = ssh(vm, ["dpkg-query", "-W", "-f=${Version}", "homebase-core"]).stdout.strip()
    check(version == "0.0.1~dev", f"homebase-core is now {version}")

    verify_data_survived(vm, "the upgrade")


def verify_reinstall_is_idempotent(vm: VM, packages: list[Path]) -> None:
    install(vm, packages, "Installing the same version again")
    ok("dpkg accepted a repeat install without error")
    verify_data_survived(vm, "the reinstall")


def verify_reboot(vm: VM) -> None:
    step("Everything comes back after a reboot")

    ssh(vm, ["sudo", "systemctl", "reboot"], check=False)
    time.sleep(5)
    wait_for_ssh(vm)
    wait_for_boot_complete(vm)

    for _ in range(30):
        state = ssh(vm, ["systemctl", "is-active", "homebase-core.service"],
                    check=False).stdout.strip()
        if state == "active":
            break
        time.sleep(1)
    check(state == "active", f"core started by itself ({state})")

    verify_data_survived(vm, "the reboot")


def verify_removal_keeps_data(vm: VM) -> None:
    step("Removing the packages does not take the data with them")

    result = apt(vm, "remove -y --purge homebase-dashboard homebase-core homebase-apps homebase-hostd")
    if result.returncode != 0:
        raise TestFailure(f"purge failed\n{result.stdout}\n{result.stderr}")

    for service in ("homebase-core.service", "homebase-hostd.socket"):
        state = ssh(vm, ["systemctl", "is-active", service], check=False).stdout.strip()
        check(state != "active", f"{service} is stopped ({state})")

    check(
        ssh(vm, ["test", "-f", "/usr/libexec/homebase/core"], check=False).returncode != 0
        and ssh(vm, ["test", "-f", "/usr/bin/homebasectl"], check=False).returncode != 0,
        "the binaries are gone",
    )
    check(
        ssh(vm, ["test", "-d", "/etc/homebase"], check=False).returncode != 0,
        "configuration is gone, as purge should",
    )

    # The assertion this whole test exists for.
    check(
        ssh(vm, ["sudo", "test", "-f", "/srv/homebase/important.txt"], check=False).returncode == 0,
        "THE USER'S FILES ARE STILL THERE",
        "Debian policy says purge removes what the package created. On a machine "
        "holding the only copy of a family's photographs, that policy loses. The "
        "package says where the data is and lets the user remove it themselves.",
    )
    check(
        ssh(vm, ["sudo", "test", "-f", "/var/lib/homebase/homebase.db"], check=False).returncode == 0,
        "and the database, with the administrator account, is still there",
    )


def main() -> int:
    started = time.time()
    print()
    step("Homebase package lifecycle")
    info("install → data → upgrade → reinstall → reboot → purge, with data intact throughout")
    print()

    vm: VM | None = None
    try:
        first = build("0.0.0~dev")

        vm = create(VM_NAME, force=True)
        start(vm)
        wait_for_ssh(vm)
        wait_for_boot_complete(vm)

        # homebase-apps depends on a container runtime, so apt needs an index to
        # resolve it from. Refreshed once, here, rather than on every install.
        step("Refreshing the package index")
        apt(vm, "update -qq")
        ok("apt is ready")

        install(vm, first, "Installing on a clean machine")
        verify_installed(vm)
        create_data(vm)

        second = build("0.0.1~dev")
        verify_upgrade(vm, second)
        verify_reinstall_is_idempotent(vm, second)
        verify_reboot(vm)
        verify_removal_keeps_data(vm)

        elapsed = int(time.time() - started)
        print()
        ok(f"PASS — the packages install, upgrade and remove safely ({elapsed}s)")
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
