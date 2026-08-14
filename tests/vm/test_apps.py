#!/usr/bin/env python3
"""The application journey, on a real machine, across a real reboot.

Milestone 3 is done when "a user installs an app, uses it, reboots, finds it and
its data intact, and uninstalls it without collateral damage". That sentence is
about a machine, not about a function, and the parts of it most likely to be
wrong are the parts no unit test can reach: whether a container comes back after
the power cycles, and whether uninstalling takes somebody's files with it.

So this installs Homebase from the packages a user receives, installs an
application through the API, uses it over HTTP, restarts the machine for real,
and then checks that the application is running again and the file is still
there. The reboot is verified by the kernel's boot id changing — the one piece
of evidence that cannot be produced without the machine actually going down.

Two assertions here have no equivalent anywhere else in the suite:

  - **Uninstalling does not delete data.** Verified by writing a file, removing
    the application, and reading the file back. A test that only checked the
    container was gone would pass on an implementation that wiped the disk.

  - **core never sends a container specification.** Read out of hostd's audit
    log, which records the parameters of every privileged call. ADR-0012 is a
    claim about what crosses the socket, and this is the only place it is
    checked against what actually crossed it.

Run with `make vm-test-apps`.
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
    apt,
    collect_logs,
    create,
    destroy,
    fail,
    info,
    install_docker,
    ok,
    reboot,
    ssh,
    start,
    step,
    upload,
    wait_for_boot_complete,
    wait_for_ssh,
)

REPO_ROOT = Path(__file__).resolve().parents[2]
VM_NAME = "homebase-apps"
PASSWORD = "a-sufficiently-long-password"

# The test application. Two megabytes rather than Jellyfin's 1.8 GB: this test
# runs in CI on a metered connection, and what it proves about the lifecycle is
# identical. Jellyfin's own manifest is exercised by the schema and catalogue
# checks.
APP = "hello-homebase"
APP_NAME = "Hello Homebase"
CONTAINER = "homebase-hello-homebase"
DATA_DIR = f"/srv/homebase/apps/{APP}"
APP_ROOT = "/srv/homebase/apps"


class TestFailure(Exception):
    pass


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        ok(description)
    else:
        raise TestFailure(f"{description}\n    {detail}" if detail else description)


# --- Talking to the API -------------------------------------------------------


# The session cookie, kept in the login user's home directory rather than /tmp.
# /tmp is cleared on boot, so a jar there survives everything except the reboot
# this test is built around — and the resulting 401 looks exactly like a session
# that did not survive, which is a product bug rather than a test one.
COOKIE_JAR = ".homebase-test-cookies"


def api(vm: VM, path: str, method: str = "GET", body: str | None = None) -> tuple[int, str]:
    """Call core's API from inside the VM, keeping the session cookie."""
    # HTTPS on the ordinary port, the way a browser reaches it. --insecure is
    # the "proceed once" over the server's own certificate; plain HTTP answers
    # a redirect rather than the request.
    cmd = ["curl", "--silent", "--show-error", "--insecure",
           "-c", COOKIE_JAR, "-b", COOKIE_JAR,
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


def wait_for_api(vm: VM, timeout: int = 90) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        status, _ = api(vm, "/setup")
        if status == 200:
            return
        time.sleep(2)
    logs = ssh(vm, ["sudo", "journalctl", "-u", "homebase-core", "--no-pager", "-n", "40"],
               check=False).stdout
    raise TestFailure(f"the API did not come up within {timeout}s\n{logs}")


def run_job(vm: VM, path: str, body: str | None = None, timeout: int = 600) -> dict:
    """Start an operation and wait for its job to finish.

    Returns the finished job. Does not assert success: several callers here are
    checking that something failed, and a helper that raised on failure would
    make those tests express the opposite of what they mean.
    """
    status, response = api(vm, path, "POST", body)
    if status != 202:
        raise TestFailure(f"POST {path} returned {status}, expected 202\n    {response}")

    job_id = json.loads(response)["job_id"]

    deadline = time.time() + timeout
    last = {}
    while time.time() < deadline:
        status, body_text = api(vm, f"/jobs/{job_id}")
        if status != 200:
            raise TestFailure(f"reading job {job_id} returned {status}\n    {body_text}")
        last = json.loads(body_text)
        if last["state"] in ("succeeded", "failed", "cancelled"):
            return last
        time.sleep(2)

    raise TestFailure(f"job {job_id} did not finish within {timeout}s\n    {json.dumps(last)}")


def succeeded(vm: VM, path: str, body: str | None = None, timeout: int = 600) -> dict:
    job = run_job(vm, path, body, timeout)
    if job["state"] != "succeeded":
        raise TestFailure(
            f"{path} failed\n    {json.dumps(job.get('error') or job, indent=4)}")
    return job


def app_status(vm: VM) -> dict:
    status, body = api(vm, f"/apps/{APP}")
    if status != 200:
        raise TestFailure(f"GET /apps/{APP} returned {status}\n    {body}")
    return json.loads(body)


# --- Setting the machine up ---------------------------------------------------


def build_packages(version: str) -> list[Path]:
    step(f"Building packages ({version})")
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


def install_homebase(vm: VM, packages: list[Path]) -> None:
    step("Installing Homebase from its packages")

    for package in packages:
        upload(vm, package, f"/tmp/{package.name}")

    names = " ".join(f"/tmp/{p.name}" for p in packages)
    # apt rather than dpkg: homebase-apps depends on a container runtime, and
    # dpkg does not resolve dependencies.
    result = apt(vm, f"install -y -qq --allow-downgrades {names}", timeout=1200)
    if result.returncode != 0:
        raise TestFailure(f"installing the packages failed\n{result.stdout}\n{result.stderr}")

    ssh(vm, ["sudo", "sh", "-c", "rm -f /tmp/*.deb"], check=False)

    # The catalogue is root-owned and outside core's reach. That is the whole of
    # ADR-0012 as it exists on disk: if core could write here, it could describe
    # a container after all.
    listing = ssh(vm, ["stat", "-c", "%a %U %G", "/usr/share/homebase/apps"]).stdout.strip()
    check(listing.endswith("root root"), f"the catalogue is owned by root ({listing})")

    result = ssh(vm, ["sudo", "-u", "homebase", "sh", "-c",
                      "echo x > /usr/share/homebase/apps/evil.json"], check=False)
    check(result.returncode != 0,
          "core cannot add an application to the catalogue",
          "If it can, ADR-0012 is decoration: core could write a manifest and "
          "then ask hostd to install it.")

    wait_for_api(vm)
    status, body = api(vm, "/setup", "POST",
                       json.dumps({"username": "okan", "password": PASSWORD}))
    check(status == 201, f"administrator created ({status})", body)


# --- The journey --------------------------------------------------------------


def verify_catalogue(vm: VM) -> None:
    step("What this server can install")

    status, body = api(vm, "/apps")
    check(status == 200, f"GET /apps ({status})", body)

    listing = json.loads(body)
    check(listing["docker_available"] is True,
          "Homebase can see the container runtime",
          "Without this every application reports 'unknown' and nothing can be installed.")
    check(not listing.get("unavailable"),
          "every shipped manifest loaded",
          f"rejected: {listing.get('unavailable')}")

    ids = {app["id"] for app in listing["items"]}
    check(APP in ids, f"{APP} is in the catalogue", f"found: {sorted(ids)}")

    app = next(a for a in listing["items"] if a["id"] == APP)
    check(app["state"] == "not_installed",
          f"{APP} starts out not installed (state={app['state']})")
    check(app["installed"] is False,
          "and says so positively rather than reporting unknown",
          "The runtime was asked and answered; installed must be false, not null.")


def refuse_before_downloading(vm: VM) -> None:
    """An application that needs a disk is refused before anything is fetched.

    Jellyfin is about a gigabyte. Asking to install it without choosing a disk
    used to download the whole image and *then* refuse with "Jellyfin needs
    somewhere to keep its files" — a fact that was true before the first byte.
    On a home connection that is ten minutes and a chunk of somebody's monthly
    allowance, spent to be told no.

    The assertion is that nothing was downloaded, rather than that it was quick:
    a timing test on somebody else's broadband is a test that fails for reasons
    unrelated to the change.
    """
    step("An application that needs a disk says so before downloading it")

    before = ssh(vm, ["sudo", "docker", "images", "--format", "{{.Repository}}"],
                 check=False).stdout

    job = run_job(vm, "/apps/jellyfin/install", timeout=300)
    check(job["state"] == "failed",
          f"installing without a disk is refused (state={job['state']})")

    failure = job.get("error") or {}
    check(failure.get("code") == "app.storage_not_assigned",
          f"and says why ({failure.get('code')})",
          json.dumps(failure))
    check(bool(failure.get("recovery")),
          "and what to do about it",
          "A refusal a user cannot act on is a dead end.")

    after = ssh(vm, ["sudo", "docker", "images", "--format", "{{.Repository}}"],
                check=False).stdout
    check("jellyfin" not in after or "jellyfin" in before,
          "and nothing was downloaded first",
          f"images before: {before.split()}\n    images after:  {after.split()}")


def install_app(vm: VM) -> None:
    step(f"Installing {APP_NAME}")

    job = succeeded(vm, f"/apps/{APP}/install")
    ok(job["message"] or "installed")

    app = app_status(vm)
    check(app["state"] == "running", f"it is running (state={app['state']})")
    check(app["installed"] is True, "and reports itself installed")


def verify_container_hardening(vm: VM) -> None:
    step("How the container was built")

    fmt = ("{{.HostConfig.Privileged}}|{{.HostConfig.CapDrop}}|"
           "{{.HostConfig.SecurityOpt}}|{{.HostConfig.RestartPolicy.Name}}|"
           "{{json .HostConfig.PortBindings}}|{{json .HostConfig.Binds}}")
    raw = ssh(vm, ["sudo", "docker", "inspect", CONTAINER, "--format", fmt]).stdout.strip()
    privileged, cap_drop, security_opt, restart, ports, binds = raw.split("|", 5)

    check(privileged == "false", "it is not privileged",
          "A privileged container is a root shell on the host.")
    check("ALL" in cap_drop, f"every capability is dropped ({cap_drop})")
    check("no-new-privileges" in security_opt,
          f"no-new-privileges is set ({security_opt})")
    check(restart == "unless-stopped",
          f"it will come back after a reboot (restart={restart})",
          "This is what makes the reboot half of this test meaningful.")

    # Bound to loopback only. A home server is on a network with a printer, a
    # television and whatever the neighbours can reach; an application is
    # published deliberately or not at all.
    bindings = json.loads(ports)
    hosts = {binding["HostIp"] for spec in bindings.values() for binding in spec}
    check(hosts == {"127.0.0.1"},
          f"its port is bound to loopback only ({sorted(hosts)})",
          "0.0.0.0 would expose it to the whole network without anybody asking.")

    # Every mount must be under the application's own directory.
    for bind in json.loads(binds) or []:
        host_path = bind.split(":")[0]
        check(host_path.startswith(DATA_DIR + "/"),
              f"mount {host_path} is inside its own data directory")

    # The application's own data belongs to the application's own identifier.
    #
    # It used to belong to the shared service account, which is what stopped
    # every application that writes to disk from running: the container drops
    # every capability, so root inside it has no CAP_DAC_OVERRIDE and cannot
    # write a directory somebody else owns.
    # Every directory the container actually writes to belongs to the
    # application's own identifier.
    for bind in json.loads(binds) or []:
        host_path = bind.split(":")[0]
        mode, uid, gid = ssh(
            vm, ["sudo", "stat", "-c", "%a %u %g", host_path]).stdout.strip().split()
        check(mode == "750", f"{host_path} is {mode}",
              "Unreadable by anybody else on the machine.")
        check(int(uid) >= 61000 and uid == gid,
              f"and belongs to the application's own identifier ({uid}:{gid})",
              "Applications must not share an identifier: one that is "
              "compromised would be able to read the others' files.")

    # The directories above stay with the service account. They are shared —
    # /srv/homebase/apps holds every application — and core has to traverse them
    # to make a backup.
    for shared in (APP_ROOT, DATA_DIR):
        held = ssh(vm, ["sudo", "stat", "-c", "%U %G", shared]).stdout.strip()
        check(held == "homebase homebase",
              f"{shared} is still {held}, so core can traverse it",
              "core is unprivileged and reads through here when backing up.")


def use_app(vm: VM) -> str:
    """Use the application over HTTP, and leave a file behind."""
    step(f"Using {APP_NAME}")

    port = ssh(vm, ["sudo", "docker", "port", CONTAINER, "80"]).stdout.strip()
    check(bool(port), f"it published a port ({port or 'none'})")
    address = port.splitlines()[0].strip()

    result = ssh(vm, ["curl", "--silent", "--max-time", "10", f"http://{address}/"],
                 check=False)
    check("Hostname:" in result.stdout,
          "it answers an HTTP request",
          f"got: {result.stdout[:200]!r} {result.stderr[:200]!r}")

    # A file where the application's own data lives, standing in for somebody's
    # media library.
    #
    # Written as the application's own identifier, because that is what owns the
    # directory now — the service account deliberately cannot write here any
    # more, which is the isolation this exists for.
    marker = "a file the user would be upset to lose"
    # Written as root and then handed to the application, because nothing on the
    # host can reach this directory as the application itself: the directory
    # above it is 0750 and owned by the service account. That does not affect the
    # container, whose bind mount is resolved as root when it is set up — which
    # is exactly why the container can write here and a host process cannot.
    uid = ssh(vm, ["sudo", "stat", "-c", "%u", f"{DATA_DIR}/config"]).stdout.strip()
    ssh(vm, ["sudo", "sh", "-c",
             f"echo '{marker}' > {DATA_DIR}/config/mine.txt && "
             f"chown {uid}:{uid} {DATA_DIR}/config/mine.txt"])
    ok("a file in its data directory")

    return marker


def verify_survived_reboot(vm: VM, marker: str) -> None:
    step("Restarting the machine")

    # vmctl.reboot compares the kernel's boot id before and after and raises if
    # it is unchanged — a reboot test that silently does not reboot is worse than
    # no reboot test, so the check lives there rather than being repeated here.
    reboot(vm)

    wait_for_api(vm)

    # The session outlived the machine. core keeps sessions in SQLite rather than
    # in memory, so somebody signed in on their phone is still signed in after a
    # power cut — being asked to sign in again every time the server restarts
    # would be a small thing that happens often.
    status, body = api(vm, "/auth/me")
    check(status == 200, f"the session survived the restart ({status})", body)

    # Docker's restart policy brings the container back; nothing in Homebase
    # needs to intervene, and this is the assertion that it does not have to.
    deadline = time.time() + 120
    app = {}
    while time.time() < deadline:
        app = app_status(vm)
        if app["state"] == "running":
            break
        time.sleep(3)

    check(app.get("state") == "running",
          f"{APP_NAME} came back on its own (state={app.get('state')})",
          "An application that needs somebody to press start after every power "
          "cut is not looking after itself.")

    survived = ssh(vm, ["sudo", "cat", f"{DATA_DIR}/config/mine.txt"], check=False).stdout.strip()
    check(survived == marker, "its data survived the restart",
          f"expected {marker!r}, got {survived!r}")

    port = ssh(vm, ["sudo", "docker", "port", CONTAINER, "80"]).stdout.strip().splitlines()[0]
    result = ssh(vm, ["curl", "--silent", "--max-time", "10", f"http://{port.strip()}/"],
                 check=False)
    check("Hostname:" in result.stdout, "and it is serving again")


def verify_uninstall_keeps_data(vm: VM, marker: str) -> None:
    step("Removing the application")

    # A confirmation naming the application is required. Checked first, because a
    # destructive endpoint that accepts an empty body is the bug worth finding.
    status, body = api(vm, f"/apps/{APP}/uninstall", "POST", "{}")
    check(status == 428, f"an unconfirmed removal is refused ({status})", body)

    status, body = api(vm, f"/apps/{APP}/uninstall", "POST",
                       json.dumps({"confirm": "something-else"}))
    check(status == 428, f"a confirmation naming the wrong application is refused ({status})",
          body)

    check(ssh(vm, ["sudo", "docker", "inspect", CONTAINER], check=False).returncode == 0,
          "and the application is still installed after those refusals")

    job = succeeded(vm, f"/apps/{APP}/uninstall", json.dumps({"confirm": APP}))
    ok(job["message"] or "removed")

    check(ssh(vm, ["sudo", "docker", "inspect", CONTAINER], check=False).returncode != 0,
          "the container is gone")

    app = app_status(vm)
    check(app["state"] == "not_installed", f"it reports not installed (state={app['state']})")

    # The assertion this whole test exists for.
    survived = ssh(vm, ["sudo", "cat", f"{DATA_DIR}/config/mine.txt"], check=False).stdout.strip()
    check(survived == marker,
          "the user's file is still there after uninstalling",
          f"expected {marker!r}, got {survived!r}. Uninstalling must never "
          f"delete data: somebody removing an application to free space has not "
          f"asked for their files to be destroyed.")

    # Reinstalling must find it, which is the promise the kept data is making.
    succeeded(vm, f"/apps/{APP}/install")
    survived = ssh(vm, ["sudo", "cat", f"{DATA_DIR}/config/mine.txt"], check=False).stdout.strip()
    check(survived == marker, "and reinstalling finds it where it was")

    succeeded(vm, f"/apps/{APP}/uninstall", json.dumps({"confirm": APP}))


def verify_data_removal(vm: VM) -> None:
    step("Deleting the data, deliberately")

    for confirmation in ("{}", json.dumps({"confirm": "yes"}),
                         json.dumps({"confirm": APP.upper()})):
        status, body = api(vm, f"/apps/{APP}/data/remove", "POST", confirmation)
        check(status == 428, f"{confirmation} is not accepted as confirmation ({status})", body)

    check(ssh(vm, ["sudo", "test", "-d", DATA_DIR], check=False).returncode == 0,
          "and the data is still there after those refusals")

    job = succeeded(vm, f"/apps/{APP}/data/remove", json.dumps({"confirm": APP}))
    ok(job["message"] or "deleted")

    check(ssh(vm, ["sudo", "test", "-e", DATA_DIR], check=False).returncode != 0,
          f"{DATA_DIR} is gone")

    # And only that. A deletion that took the parent with it would be a very
    # quiet way to lose every other application's data.
    check(ssh(vm, ["sudo", "test", "-d", "/srv/homebase/apps"], check=False).returncode == 0,
          "the other applications' data directory is untouched")


def verify_events(vm: VM) -> None:
    step("What the machine recorded")

    status, body = api(vm, "/events?limit=100")
    check(status == 200, f"GET /events ({status})", body)

    events = json.loads(body)["items"]
    by_type = {event["type"]: event for event in events}

    for expected in ("application_installed", "application_uninstalled",
                     "application_data_removed"):
        check(expected in by_type, f"{expected} was recorded",
              f"recorded: {sorted(by_type)}")

    installed = by_type.get("application_installed", {})
    check(installed.get("subject") == APP,
          f"the event names the application ({installed.get('subject')})")

    # An event is read by a person in a history list weeks later.
    message = installed.get("message") or ""
    check(APP_NAME in message and "app.install" not in message,
          f"and reads as a sentence ({message!r})",
          "The operation name belongs in `type`; `message` is an account of "
          "what happened.")

    removal = by_type.get("application_data_removed", {})
    check(removal.get("severity") == "warning",
          f"deleting data is recorded above 'info' ({removal.get('severity')})",
          "An irreversible deletion that reads like a routine event is one "
          "nobody notices in a list.")


def verify_core_never_described_a_container(vm: VM) -> None:
    """ADR-0012, checked against what actually crossed the socket.

    hostd's audit log records the parameters of every privileged call. If core
    could describe a container, the evidence would be here.
    """
    step("What core sent across the privilege boundary")

    raw = ssh(vm, ["sudo", "cat", "/var/log/homebase/audit.log"]).stdout

    forbidden = {"image", "binds", "mounts", "privileged", "environment", "env",
                 "command", "entrypoint", "ports", "capabilities", "volumes",
                 "network", "devices", "user", "cgroup"}

    app_calls = 0
    for line in raw.splitlines():
        if not line.strip():
            continue
        entry = json.loads(line)
        operation = entry.get("operation", "")
        if not operation.startswith("app."):
            continue
        params = entry.get("params") or {}
        if entry.get("phase") == "attempt":
            app_calls += 1
        offending = forbidden & set(params)
        if offending:
            raise TestFailure(
                f"{operation} carried {sorted(offending)} across the boundary\n"
                f"    {json.dumps(params)}\n"
                "    core must send an application id and nothing else — ADR-0012.")

        allowed = {"id", "confirm", "lines"}
        unexpected = set(params) - allowed
        if unexpected:
            raise TestFailure(
                f"{operation} carried unexpected parameters {sorted(unexpected)}\n"
                f"    {json.dumps(params)}")

    check(app_calls > 0, f"{app_calls} application operations were audited",
          "An empty audit log would make this check vacuous.")
    ok("none of them described a container")

    # The critical operation must be recorded as critical, and as confirmed.
    for line in raw.splitlines():
        entry = json.loads(line) if line.strip() else {}
        if entry.get("operation") == "app.remove_data" and entry.get("phase") == "attempt":
            check(entry.get("risk") == "critical",
                  f"app.remove_data is audited as critical ({entry.get('risk')})")
            return
    raise TestFailure("app.remove_data was never audited")


# --- Driver -------------------------------------------------------------------


def main() -> int:
    version = "0.0.0~apps"

    try:
        packages = build_packages(version)
    except VMError as error:
        fail(str(error))
        return 1

    vm = None
    try:
        vm = create(VM_NAME, force=True)
        start(vm)
        wait_for_ssh(vm)
        wait_for_boot_complete(vm)

        install_docker(vm, prepull=["traefik/whoami:v1.10.4"])
        install_homebase(vm, packages)

        verify_catalogue(vm)
        refuse_before_downloading(vm)
        install_app(vm)
        verify_container_hardening(vm)
        marker = use_app(vm)

        verify_survived_reboot(vm, marker)

        verify_uninstall_keeps_data(vm, marker)
        verify_data_removal(vm)
        verify_events(vm)
        verify_core_never_described_a_container(vm)

        print()
        ok("A user can install an application, use it, restart the server, find "
           "it and its data intact, and remove it without losing anything.")
        return 0

    except (TestFailure, VMError) as error:
        print()
        fail(str(error))
        if vm is not None:
            try:
                info(f"logs: {collect_logs(vm)}")
            except Exception:
                pass
        return 1

    except KeyboardInterrupt:
        print()
        info("interrupted")
        return 130

    finally:
        if vm is not None:
            destroy(VM_NAME)


if __name__ == "__main__":
    sys.exit(main())
