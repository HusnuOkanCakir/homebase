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
                       json.dumps({"username": "alex", "password": PASSWORD}))
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
                    json.dumps({"username": "alex", "password": PASSWORD}))
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



def hostd_op(vm: VM, name: str, params: dict | None = None,
             confirmed: bool = False, timeout: int = 60) -> tuple[int, dict]:
    cmd = ["sudo", "curl", "--silent", "--max-time", str(timeout),
           "--unix-socket", "/run/homebase/hostd.sock",
           "-w", "\\n%{http_code}", "-X", "POST",
           "-H", "Content-Type: application/json"]
    if confirmed:
        # hostd checks the confirmation again itself. Sending this header is
        # core saying "the user was asked", which is the only thing core can
        # assert that hostd cannot check for itself.
        cmd += ["-H", "X-Homebase-Confirmed: true"]
    cmd += ["-d", json.dumps(params or {}), f"http://localhost/v1/op/{name}"]

    out = ssh(vm, cmd, check=False, timeout=timeout + 30).stdout.strip()
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

    # And through the browser-facing API, which is the only way a user reaches
    # it. The operations existing in hostd is not the same as the feature
    # existing, and that gap is what kept this off the dashboard for a milestone.
    status, body = api(vm, "/auth/login", "POST",
                       json.dumps({"username": "alex", "password": PASSWORD}))
    check(status == 200, f"signed in ({status})", body)

    status, body = api(vm, "/backups/schedule")
    check(status == 200, f"the schedule is readable from the dashboard ({status})", body)
    check(json.loads(body).get("every") == "off",
          "and reports the same thing hostd does", body)

    status, body = api(vm, "/backups/schedule", "POST", json.dumps({"every": "sometimes"}))
    check(status == 400, f"a schedule Homebase cannot keep to is refused there too "
                         f"({status})", body)

    # A route that can point somebody's data at a disk, reachable without an
    # account, would be worth having and this test exists to say it is not.
    for method in ("GET", "POST"):
        out = ssh(vm, ["curl", "--silent", "--insecure", "-o", "/dev/null",
                       "-w", "%{http_code}", "-X", method,
                       "-H", "Content-Type: application/json", "-d", "{}",
                       "https://127.0.0.1/api/v1/backups/schedule"],
                  check=False).stdout.strip()
        check(out == "401", f"{method} refuses a caller with no account ({out})")

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


def verify_the_cli_can_drive_the_server(vm: VM) -> None:
    """The whole product from a terminal, which is the point of Milestone 10.

    Every command here is an ordinary API client — the same surface the dashboard
    uses, with the same permission checks and the same job records. Nothing
    reaches hostd directly, because a second path to a privileged operation is a
    second place for the checks to be wrong.

    The assertions worth having are the two a CLI is judged by: `--json` produces
    something a script can parse, and the exit codes distinguish "failed" from
    "used wrongly" from "not answering".
    """
    step("Driving the server from a terminal")

    # Authentication by being root, with no token to create first. This is the
    # line that decides whether the CLI is pleasant or not.
    result = ssh(vm, ["sudo", "homebasectl", "system"], check=False)
    check(result.returncode == 0,
          f"`sudo homebasectl system` works with no setup ({result.returncode})",
          (result.stdout + result.stderr)[-400:])
    check("Memory:" in result.stdout, "and reports the machine",
          result.stdout[:300])

    # --json is the interface a script builds on, so it has to parse.
    result = ssh(vm, ["sudo", "homebasectl", "system", "--json"], check=False)
    check(result.returncode == 0, "and answers as JSON")
    try:
        parsed = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise TestFailure(f"--json did not produce JSON: {error}\n{result.stdout[:300]}")
    check(parsed.get("hostname") == "homebase-pkg",
          f"which parses, and names the machine ({parsed.get('hostname')})")

    for command in (["apps"], ["storage"], ["network"], ["update", "status"]):
        result = ssh(vm, ["sudo", "homebasectl", *command, "--json"], check=False)
        check(result.returncode == 0,
              f"`homebasectl {' '.join(command)} --json` works ({result.returncode})",
              (result.stdout + result.stderr)[-300:])
        json.loads(result.stdout)

    # An unprivileged caller has no way in, which is the other half of
    # authenticating by being root.
    result = ssh(vm, ["homebasectl", "system"], check=False)
    check(result.returncode != 0,
          f"an ordinary user is refused ({result.returncode})",
          "Running as root is what authenticates this. Without that, it must "
          "ask for a token rather than working anyway.")
    check("HOMEBASE_TOKEN" in (result.stdout + result.stderr),
          "and is told how to authenticate",
          (result.stdout + result.stderr)[-300:])

    # The exit codes, which are the difference between a usable script and a
    # script that treats every outcome the same way.
    result = ssh(vm, ["sudo", "homebasectl", "nonsense"], check=False)
    check(result.returncode == 2,
          f"a command that does not exist exits 2, not 1 ({result.returncode})")

    result = ssh(vm, ["sudo", "homebasectl", "apps", "install"], check=False)
    check(result.returncode == 2,
          f"and so does a command missing its argument ({result.returncode})")

    result = ssh(vm, ["sudo", "homebasectl", "--address", "https://127.0.0.1:9",
                      "system"], check=False)
    check(result.returncode == 3,
          f"a server that is not answering exits 3 ({result.returncode})",
          "A script has to be able to tell 'Homebase is down' from 'that "
          "operation failed'.")

    # And it can actually change something, through the job system.
    result = ssh(vm, ["sudo", "homebasectl", "backup", "schedule", "off"], check=False)
    check(result.returncode == 0, f"it can write, not only read ({result.returncode})",
          (result.stdout + result.stderr)[-300:])
    check("never" in result.stdout.lower() or "off" in result.stdout.lower(),
          "and says what it did", result.stdout[:200])


def verify_temperature_reporting(vm: VM) -> None:
    """A machine with no sensors must say so, not report zero.

    Homebase runs on old laptops in cupboards, and one cooking itself looks from
    the outside exactly like one that is broken — so the temperature is worth
    reporting. A VM has no thermal zones, which makes it the right place to check
    the half that is easy to get wrong: `null` rather than a confident 0 °C.
    """
    step("How hot the machine says it is")

    status, body = api(vm, "/system")
    check(status == 200, f"the system endpoint answers ({status})", body[:200])

    reported = json.loads(body)
    check("temperature" in reported,
          "the temperature is reported at all", body[:400])

    temperature = reported["temperature"]
    celsius = temperature.get("celsius")

    if celsius is None:
        check(not temperature.get("state"),
              f"a machine with no sensors says nothing about its state "
              f"({temperature.get('state')!r})")
        check(not temperature.get("message"),
              "and gives no advice about a temperature it does not have")
        ok("no thermal sensors here, and Homebase says so rather than reporting 0 °C")
        return

    # If the machine does have a sensor, the reading has to be plausible and the
    # advice has to match it.
    check(1 <= celsius <= 150, f"the reading is plausible ({celsius} °C)",
          json.dumps(temperature, indent=4))
    check(temperature.get("state") in ("ok", "warm", "hot"),
          f"with a state a person can read ({temperature.get('state')!r})")
    if temperature.get("state") == "ok":
        check(not temperature.get("message"),
              "and an ordinary temperature is not commented on",
              "An indicator that is always lit is one people stop seeing.")


def verify_destructive_commands_refuse_first(vm: VM) -> None:
    """The commands that destroy things, and what stops them by accident.

    In a browser, "type the backup id to confirm" works: the id is on the screen
    and the field is empty. At a shell it means much less — the id is already in
    the command that listed it, the up arrow re-runs whatever was done last, and
    a `--yes` flag becomes muscle memory within a week.

    So what is checked here is the three things that replace it: nothing
    irreversible runs without a terminal, `--confirm` has to name the thing
    itself, and a word like "yes" is refused.
    """
    step("What stops a destructive command by accident")

    hostname = ssh(vm, ["hostname"]).stdout.strip()

    # No terminal, no --confirm: it must refuse rather than prompt into a void.
    result = ssh(vm, ["sudo", "homebasectl", "factory-reset"], check=False, timeout=90)
    check(result.returncode == 2,
          f"a factory reset with no terminal is a usage error ({result.returncode})",
          (result.stdout + result.stderr)[-400:])
    check("--confirm" in (result.stdout + result.stderr),
          "and it says what a script has to pass instead",
          (result.stdout + result.stderr)[-300:])
    check("no --yes" in (result.stdout + result.stderr),
          "and why there is no --yes",
          "Somebody will look for one, and the answer should be in the message "
          "rather than in the source.")

    # A word instead of the name.
    for word in ("yes", "y", "true", "confirm"):
        result = ssh(vm, ["sudo", "homebasectl", "factory-reset", "--confirm", word],
                     check=False, timeout=90)
        check(result.returncode == 2,
              f"--confirm {word} is refused ({result.returncode})",
              (result.stdout + result.stderr)[-200:])

    # The server's own name is the only thing that works, and it is still there.
    status, body = api(vm, "/setup")
    check(json.loads(body)["needs_setup"] is False,
          "and after all of that the server is untouched", body)

    # Formatting names a disk that does not exist: it has to say so rather than
    # asking for confirmation of something imaginary.
    result = ssh(vm, ["sudo", "homebasectl", "storage", "format", "/dev/nonexistent"],
                 check=False, timeout=90)
    check(result.returncode != 0,
          f"formatting a disk that is not there is refused ({result.returncode})")
    check("cannot see" in (result.stdout + result.stderr),
          "and says the server cannot see it",
          (result.stdout + result.stderr)[-300:])

    info(f"(the confirmation for this machine would be {hostname!r})")


def verify_factory_reset(vm: VM) -> None:
    """Starting again without losing the photographs.

    The most destructive operation in Homebase, and the one whose failure is
    worst in both directions: it can leave an account behind on a machine
    somebody is giving away, or it can delete the files it promised to keep.
    Both are checked here, by content.

    It runs late, after everything else has used this machine, and before the
    reboot — so the reset is also proven to survive one.
    """
    step("Starting again")

    # Something in each of the three places, so "removed" and "kept" can be told
    # apart by looking rather than by trusting the report.
    ssh(vm, ["sudo", "-u", "homebase", "sh", "-c",
             "echo 'a photograph' > /srv/homebase/important.txt"])
    ssh(vm, ["sudo", "sh", "-c",
             "echo 'name: the-original-server' > /etc/homebase/homebase.yaml"])

    status, body = api(vm, "/setup")
    check(json.loads(body)["needs_setup"] is False,
          "there is an administrator to remove", body)

    hostname = ssh(vm, ["hostname"]).stdout.strip()

    # A word like "yes" must not work. This removes every account on the
    # machine, and a confirmation somebody can type out of habit is not one.
    for wrong in ("yes", "reset", "", "homebase"):
        status, body = hostd_op(vm, "system.factory_reset", {"confirm": wrong},
                                confirmed=True)
        check(status >= 400,
              f"{wrong!r} is not accepted as confirmation ({status})", json.dumps(body))

    status, body = api(vm, "/setup")
    check(json.loads(body)["needs_setup"] is False,
          "and after those refusals the account is still there", body)

    status, body = hostd_op(vm, "system.factory_reset", {"confirm": hostname},
                            confirmed=True)
    check(status == 200, f"the server's own name resets it ({status})", json.dumps(body))

    # The claim, checked against the disk.
    kept = ssh(vm, ["sudo", "cat", "/srv/homebase/important.txt"], check=False).stdout.strip()
    check(kept == "a photograph",
          f"your files are still there ({kept!r})",
          "keep_data defaults to true. If this is empty, a reset deleted somebody's "
          "photographs while promising not to.")

    gone = ssh(vm, ["sudo", "test", "-f", "/etc/homebase/homebase.yaml"], check=False)
    check(gone.returncode != 0, "and the settings are gone")

    # It has to say what it kept, in words somebody can check against the disk.
    # A reset that quietly keeps something is as bad as one that quietly removes
    # it — this machine may be about to be given away.
    kept_says = " ".join(body.get("kept", []))
    check("srv/homebase" in kept_says,
          "and it says your files were kept", json.dumps(body, indent=4))
    check("updates" in kept_says,
          "and that where updates come from was kept on purpose",
          json.dumps(body, indent=4))

    wait_for_api(vm)
    status, body = api(vm, "/setup")
    check(json.loads(body)["needs_setup"] is True,
          "the server asks to be set up again, like a new one", body)

    # And the old password must not work any more, which is the half that
    # matters when somebody is giving the machine away.
    status, _ = api(vm, "/auth/login", "POST",
                    json.dumps({"username": "alex", "password": PASSWORD}))
    check(status != 200,
          f"the account that was on it cannot sign in ({status})",
          "A reset that leaves an account behind is worse than one that fails.")

    # Set it up again — from the terminal, which is the one place `homebasectl
    # setup` can be tested honestly: it refuses once an account exists, so a
    # freshly reset server is the only server it works on.
    result = ssh(vm, ["sudo", "sh", "-c",
                      f"HOMEBASE_PASSWORD='{PASSWORD}' homebasectl setup alex"],
                 check=False)
    check(result.returncode == 0,
          f"and it can be set up again from a terminal ({result.returncode})",
          (result.stdout + result.stderr)[-400:])

    # The recovery code is shown once and must actually be shown, because there
    # is no second chance to read it.
    check("recovery" in result.stdout.lower() or "write" in result.stdout.lower(),
          "which prints a recovery code to write down", result.stdout[:400])

    # And the account it made really works.
    status, _ = api(vm, "/auth/login", "POST",
                    json.dumps({"username": "alex", "password": PASSWORD}))
    check(status == 200, f"and the account signs in ({status})")

    # Twice is refused: a second administrator created without credentials would
    # be a way past every permission check on the machine.
    again = ssh(vm, ["sudo", "sh", "-c",
                     f"HOMEBASE_PASSWORD='{PASSWORD}' homebasectl setup someone-else"],
                check=False)
    check(again.returncode != 0,
          f"and a second administrator is refused ({again.returncode})",
          (again.stdout + again.stderr)[-300:])


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
        verify_temperature_reporting(vm)
        verify_the_cli_can_drive_the_server(vm)
        verify_destructive_commands_refuse_first(vm)
        verify_factory_reset(vm)
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
