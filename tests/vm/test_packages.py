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
    # HTTPS, on the ordinary port, exactly as a browser reaches it. --insecure
    # stands in for the "proceed once" a person clicks over the server's own
    # certificate. Plain HTTP would answer a 307 rather than the request.
    cmd = ["curl", "--silent", "--show-error", "--insecure",
           "-c", "/tmp/cookies", "-b", "/tmp/cookies",
           "-o", "/dev/stdout", "-w", "\\n%{http_code}", "-X", method]
    if body is not None:
        cmd += ["-H", "Content-Type: application/json", "-d", body]
    cmd.append(f"https://127.0.0.1/api/v1{path}")

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

    # Everything hostd shells out to must be pulled in by the packages.
    #
    # This is the check that was missing. hostd exports the database with
    # `VACUUM INTO` via the sqlite3 binary, and nothing declared a dependency on
    # it — so backup failed on every machine except one installed from the ISO,
    # whose autoinstall happened to list sqlite3 for its own reasons. The VM
    # tests installed it by hand, which hid the gap rather than finding it.
    #
    # Checked by asking the machine, not by reading the control file: a
    # dependency that is declared and not installed is the same failure.
    for tool, why in [
        ("sqlite3", "hostd exports core's database with it when backing up"),
    ]:
        found = ssh(vm, ["sh", "-c", f"command -v {tool}"], check=False)
        check(found.returncode == 0,
              f"{tool} is on the machine — {why}",
              f"The packages must depend on it. Installing Homebase and then "
              f"discovering a feature does not work is not a thing a user can debug.")

    # Without sudo it must explain itself rather than appearing not to exist:
    # /usr/bin rather than /usr/sbin is what buys that.
    unprivileged = ssh(vm, ["homebasectl", "list-accounts"], check=False)
    check("sudo" in (unprivileged.stdout + unprivileged.stderr).lower(),
          "and without sudo it says to use sudo",
          (unprivileged.stdout + unprivileged.stderr).strip()[:300])

    verify_reachable_from_another_device(vm)
    verify_socket_survives_a_restart(vm)


def verify_reachable_from_another_device(vm: VM) -> None:
    """The dashboard must answer from off the machine, not only on it.

    Everything else in this file talks to the API by running curl *inside* the
    VM, which reaches 127.0.0.1 and therefore cannot tell the difference between
    a server the household can use and one that answers only its own keyboard.
    It was the second kind once, and nothing noticed.
    """
    # HTTPS, because that is where the dashboard now lives. --insecure is the
    # "proceed once" a person clicks: the certificate is the server's own, and
    # this machine has not been told to trust it.
    secure = f"https://127.0.0.1:{vm.dashboard_port}/api/v1/health"
    code, last = poll_http(secure, "200", extra=["--insecure"])
    if code != "200":
        raise TestFailure(
            "the dashboard is not reachable from outside the machine\n"
            f"    {secure} said {last or 'nothing'}.\n"
            "    core binds 127.0.0.1 unless the unit sets HOMEBASE_LISTEN, and a\n"
            "    server nobody can open is the one thing this product cannot be.")
    ok("the dashboard answers over HTTPS from another machine, not just its own")

    # And plain HTTP has to send somebody to it rather than answering nothing.
    # Anyone who types the name without https:// arrives here first.
    plain = f"http://127.0.0.1:{vm.api_port}/api/v1/health"
    code, last = poll_http(plain, "307")
    if code != "307":
        raise TestFailure(
            "plain HTTP did not redirect to HTTPS\n"
            f"    {plain} said {last or 'nothing'}.\n"
            "    Somebody who omits https:// must not reach nothing.")
    ok("and plain HTTP on port 80 redirects there")


def poll_http(url: str, want: str, extra: list[str] | None = None) -> tuple[str, str]:
    """Wait for a URL to answer with a particular status.

    Returns the last status seen and whatever curl said, so the caller can
    explain the failure in its own terms.
    """
    deadline = time.time() + 60
    last = ""
    while time.time() < deadline:
        result = subprocess.run(
            ["curl", "--silent", "--show-error", "--max-time", "5",
             *(extra or []), "-o", "/dev/null", "-w", "%{http_code}", url],
            capture_output=True, text=True,
        )
        code = result.stdout.strip()
        if code == want:
            return code, ""
        last = (result.stdout + result.stderr).strip()[:160]
        time.sleep(3)
    return "", last


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
    ssh(vm, ["curl", "--silent", "--insecure", "--max-time", "10", "-o", "/dev/null",
             "https://127.0.0.1/api/v1/health"], check=False)
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


def update_status(vm: VM) -> dict:
    """Ask hostd what this machine thinks it is running."""
    out = ssh(vm, ["sudo", "curl", "--silent", "--unix-socket",
                   "/run/homebase/hostd.sock", "-X", "POST",
                   "-H", "Content-Type: application/json", "-d", "{}",
                   "http://localhost/v1/op/update.status"], check=False).stdout
    try:
        body = json.loads(out)
    except json.JSONDecodeError as exc:
        raise TestFailure(f"could not read update.status: {exc}\n{out[:400]}") from exc
    return body.get("result", body)


def verify_version_reporting(vm: VM) -> None:
    """What the machine says it is running, against real dpkg.

    The unit tests behind this parse fixtures. Fixtures agree with whatever
    wrote them, so they cannot catch a format string dpkg does not support or a
    state word it spells differently — which is the whole class of bug that has
    cost this project three milestones of silence.
    """
    step("What version this machine says it is running")

    status = update_status(vm)
    check(status.get("version") == "0.0.1~dev",
          f"it reports version {status.get('version')!r}",
          f"got: {status}")
    check(status.get("consistent") is True,
          "and all four packages agree",
          f"components: {status.get('components')}")
    check(status.get("interrupted") is False,
          "and dpkg has nothing outstanding")
    check(len(status.get("components") or []) == 4,
          f"all four components are reported ({len(status.get('components') or [])})")

    # No update source is configured on a machine installed from packages, and
    # saying otherwise would promise updates that cannot arrive.
    check(status.get("channel") == "",
          f"and it does not claim a channel it has not got ({status.get('channel')!r})")


def verify_a_half_applied_upgrade_is_visible(vm: VM, old: list[Path], new: list[Path]) -> None:
    """The state an interrupted update leaves, and whether Homebase notices.

    Every package still reads as fully installed. Nothing is half-configured,
    no service has failed, and the dashboard keeps working — which is precisely
    why a status check reporting a single version string would call this
    healthy. The only evidence is that the four packages disagree.

    Produced here by putting two of them back, because killing dpkg mid-unpack
    is a different failure and gets its own test with the interruption matrix.
    """
    step("A half-applied update, and whether the machine admits it")

    going_back = [p for p in old if "apps" in p.name or "dashboard" in p.name]
    for package in going_back:
        upload(vm, package, f"/tmp/{package.name}")
    names = " ".join(f"/tmp/{p.name}" for p in going_back)

    # dpkg rather than apt, and deliberately: apt would refuse to leave the
    # machine with unsatisfied dependencies, which is the state being staged.
    ssh(vm, ["sudo", "sh", "-c", f"dpkg -i --force-depends {names}"], check=False)

    status = update_status(vm)
    check(status.get("consistent") is False,
          "two packages at 0.0.0~dev and two at 0.0.1~dev is reported as inconsistent",
          f"components: {status.get('components')}")

    versions = {c["package"]: c["version"] for c in status.get("components") or []}
    check(versions.get("homebase-core") == "0.0.1~dev"
          and versions.get("homebase-dashboard") == "0.0.0~dev",
          f"and the report names which ones disagree ({versions})")

    # Put it back, and confirm the machine stops complaining. A check that can
    # only ever say "broken" is not a check.
    #
    # dpkg again rather than apt: apt refuses to act on a system whose
    # dependencies are already unsatisfied, which is the state this test just
    # created. Every dependency these two need is on the machine, so dpkg has
    # nothing to resolve.
    step("Finishing the interrupted upgrade")
    catching_up = [p for p in new if "apps" in p.name or "dashboard" in p.name]
    for package in catching_up:
        upload(vm, package, f"/tmp/{package.name}")
    names = " ".join(f"/tmp/{p.name}" for p in catching_up)
    result = ssh(vm, ["sudo", "sh", "-c", f"dpkg -i {names}"], check=False)
    check(result.returncode == 0, "the remaining packages install",
          (result.stdout + result.stderr)[-400:])

    status = update_status(vm)
    check(status.get("consistent") is True,
          "completing the upgrade clears it",
          f"components: {status.get('components')}")


def verify_reinstall_is_idempotent(vm: VM, packages: list[Path]) -> None:
    install(vm, packages, "Installing the same version again")
    ok("dpkg accepted a repeat install without error")
    verify_data_survived(vm, "the reinstall")



def hostd_op(vm: VM, name: str, params: dict | None = None) -> tuple[int, dict]:
    out = ssh(vm, ["sudo", "curl", "--silent", "--max-time", "60",
                   "--unix-socket", "/run/homebase/hostd.sock",
                   "-w", "\\n%{http_code}", "-X", "POST",
                   "-H", "Content-Type: application/json",
                   "-d", json.dumps(params or {}),
                   f"http://localhost/v1/op/{name}"], check=False).stdout.strip()
    if not out:
        raise TestFailure(f"{name} returned nothing")
    parts = out.rsplit("\n", 1)
    status = int(parts[1]) if len(parts) == 2 else int(out)
    body = parts[0] if len(parts) == 2 else ""
    return status, (json.loads(body) if body else {})


def verify_scheduled_backups(vm: VM) -> None:
    """Backups that happen without anybody pressing anything.

    Deferred here from Milestone 5, and the gap it closes is the one the
    documentation has been apologising for: a backup you have to remember is a
    backup that exists until the week you are busy, which is reliably the week
    the disk fails.
    """
    step("Backups on a schedule")

    for unit in ("homebase-backup.timer", "homebase-backup.service"):
        present = ssh(vm, ["test", "-f", f"/lib/systemd/system/{unit}"], check=False)
        check(present.returncode == 0, f"{unit} is installed")

    # Persistent is the setting that makes this work on the machine Homebase
    # actually runs on. A laptop in a cupboard is asleep at three in the
    # morning more often than not, and without this the run is skipped
    # silently, every night, until somebody needs it.
    timer = ssh(vm, ["cat", "/lib/systemd/system/homebase-backup.timer"]).stdout
    check("Persistent=true" in timer,
          "and catches up a run the machine was switched off for")

    status, body = hostd_op(vm, "backup.get_schedule")
    check(status == 200, f"backup.get_schedule answers ({status})", json.dumps(body))
    check(body.get("every") == "off",
          f"nothing is scheduled on a new server ({body.get('every')})")
    check(body.get("enabled") is False, "and the timer is not running")

    # A schedule pointing at a disk that is not there must be refused now,
    # rather than failing at three in the morning weeks later, to somebody who
    # was told backups were working.
    status, body = hostd_op(vm, "backup.set_schedule",
                            {"every": "daily", "location": "loc_nothing_here"})
    check(status >= 400,
          f"a schedule pointing at a disk that does not exist is refused ({status})",
          json.dumps(body))

    status, body = hostd_op(vm, "backup.set_schedule", {"every": "sometimes"})
    check(status == 400, f"and so is a schedule Homebase cannot keep to ({status})",
          json.dumps(body))

    status, body = hostd_op(vm, "backup.get_schedule")
    check(body.get("every") == "off",
          "and after both refusals nothing has been scheduled",
          json.dumps(body))

    # Updates are looked for on their own. A server nobody touches again should
    # still find out that a security fix exists, and the unit this starts does
    # nothing until a channel is configured — so enabling it on every machine
    # costs nothing and being switched off costs somebody a patch.
    state = ssh(vm, ["systemctl", "is-enabled", "homebase-update-check.timer"],
                check=False).stdout.strip()
    check(state == "enabled", f"the update check runs on its own ({state})")

    timer = ssh(vm, ["cat", "/lib/systemd/system/homebase-update-check.timer"]).stdout
    check("Persistent=true" in timer,
          "including on a server that is only switched on at weekends")


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
        verify_version_reporting(vm)
        verify_a_half_applied_upgrade_is_visible(vm, first, second)
        verify_reinstall_is_idempotent(vm, second)
        verify_scheduled_backups(vm)
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
