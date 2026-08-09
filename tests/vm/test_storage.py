#!/usr/bin/env python3
"""Storage, against a real disk that gets plugged and unplugged for real.

Milestone 4 is done when "a USB disk can be added as Jellyfin's media storage,
removed and reconnected without corrupting anything". Every hard part of that
sentence is about a disk that is not there, so this test spends most of its time
pulling one out.

The disk is hot-plugged over QEMU's monitor rather than simulated by unmounting.
That distinction is the test: unmounting is the tidy case, and the one that
destroys data is the device disappearing underneath a filesystem that is still
mounted and still being written to.

Two things this checks that nothing else can:

  - **A disk is found again after being unplugged**, even though the kernel gave
    it a different device name. This is ADR-0013's central claim, and the first
    run of the probe that inspired it came back as sdb having left as sda.

  - **The mount point cannot be written to when the disk is absent.** Otherwise
    an application carries on writing to the system disk, filling it with files
    that vanish behind the disk the moment it is reconnected.

Run with `make vm-test-storage`.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from vmctl import (  # noqa: E402
    VM,
    VMError,
    attach_removable_disk,
    collect_logs,
    copy_to,
    create,
    create_removable_disk,
    destroy,
    detach_removable_disk,
    fail,
    info,
    ok,
    reboot,
    ssh,
    start,
    step,
    wait_for_boot_complete,
    wait_for_ssh,
    write_file,
)

REPO_ROOT = Path(__file__).resolve().parents[2]
VM_NAME = "homebase-storage"
SOCKET = "/run/homebase/hostd.sock"
STORAGE_ROOT = "/srv/homebase/storage"
LOCATION = "media"


class TestFailure(Exception):
    pass


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        ok(description)
    else:
        raise TestFailure(f"{description}\n    {detail}" if detail else description)


# --- Talking to hostd ---------------------------------------------------------


def op(vm: VM, name: str, params: dict | None = None, confirmed: bool = False,
       timeout: int = 120) -> tuple[int, dict]:
    """Invoke a privileged operation, as core would."""
    # --max-time, because the socket belongs to systemd rather than to hostd:
    # if hostd cannot start, curl connects successfully and then waits for a
    # reply that never comes. Without a timeout that is an indefinite hang, and
    # a hang tells you nothing about what went wrong.
    cmd = ["sudo", "-u", "homebase", "curl", "--silent", "--show-error",
           "--max-time", str(timeout), "--unix-socket", SOCKET,
           "-w", "\\n%{http_code}", "-X", "POST",
           "-H", "Content-Type: application/json",
           "-d", json.dumps(params or {})]
    if confirmed:
        cmd += ["-H", "X-Homebase-Confirmed: true"]
    cmd.append(f"http://hostd/v1/op/{name}")

    result = ssh(vm, cmd, check=False, timeout=timeout + 60)
    output = result.stdout.strip()
    if not output:
        raise TestFailure(f"{name} returned nothing: {result.stderr.strip()[:300]}")

    parts = output.rsplit("\n", 1)
    status = int(parts[1]) if len(parts) == 2 else int(output)
    body = parts[0] if len(parts) == 2 else ""

    try:
        return status, json.loads(body) if body else {}
    except json.JSONDecodeError as exc:
        raise TestFailure(f"{name} did not return JSON: {exc}\n    {body[:300]!r}") from exc


def must(vm: VM, name: str, params: dict | None = None, confirmed: bool = False,
         timeout: int = 120) -> dict:
    status, body = op(vm, name, params, confirmed, timeout)
    if status != 200:
        raise TestFailure(f"{name} returned {status}\n    {json.dumps(body, indent=4)}")
    return body


def error_code(body: dict) -> str:
    return (body.get("error") or {}).get("code", "")


# --- Setting the machine up ---------------------------------------------------


def build_hostd() -> Path:
    step("Building hostd")
    out = REPO_ROOT / "bin"
    out.mkdir(parents=True, exist_ok=True)

    env = {**os.environ, "CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64"}
    result = subprocess.run(
        ["go", "build", "-trimpath", "-o", str(out / "hostd"), "./cmd/hostd"],
        cwd=REPO_ROOT, capture_output=True, text=True, env=env,
    )
    if result.returncode != 0:
        raise VMError("go build failed", result.stderr.strip()[:800])
    ok("hostd")
    return out / "hostd"


def install_hostd(vm: VM, binary: Path) -> None:
    step("Installing hostd")

    ssh(vm, ["sudo", "groupadd", "--system", "--force", "homebase"])
    ssh(vm, ["sudo", "useradd", "--system", "--gid", "homebase",
             "--home-dir", "/var/lib/homebase", "--shell", "/usr/sbin/nologin",
             "homebase"], check=False)
    # Everything the unit's ReadWritePaths names. A missing one used to stop
    # hostd starting at all, with an error naming a directory rather than the
    # reason — the unit now tolerates it, and this creates them anyway because
    # the packages do.
    ssh(vm, ["sudo", "mkdir", "-p", "/usr/libexec/homebase", "/etc/homebase",
             "/var/lib/homebase", "/srv/homebase", "/var/log/homebase"])
    ssh(vm, ["sudo", "chown", "root:homebase", "/etc/homebase"])
    ssh(vm, ["sudo", "chmod", "0750", "/etc/homebase"])

    copy_to(vm, binary, "/usr/libexec/homebase/hostd", mode="0755")

    for unit in ("homebase-hostd.service", "homebase-hostd.socket"):
        write_file(vm, f"/etc/systemd/system/{unit}",
                   (REPO_ROOT / "packaging" / "systemd" / unit).read_text())

    ssh(vm, ["sudo", "systemctl", "daemon-reload"])
    ssh(vm, ["sudo", "systemctl", "enable", "--now", "homebase-hostd.socket"])

    # Provoke activation, so the service is running before anything is asked of
    # it and a failure to start shows up here rather than inside a test.
    status, body = op(vm, "system.get_info", timeout=30)
    if status != 200:
        journal = ssh(vm, ["sudo", "journalctl", "-u", "homebase-hostd",
                           "--no-pager", "-n", "15"], check=False).stdout
        raise TestFailure(f"hostd did not start\n{journal}")
    ok("hostd is answering")


# --- The disk -----------------------------------------------------------------


def find_removable_volume(vm: VM) -> dict:
    """The USB disk, as hostd sees it."""
    disks = must(vm, "storage.list_disks")["disks"]

    for disk in disks:
        if disk.get("transport") != "usb":
            continue
        if disk.get("system"):
            raise TestFailure("the USB disk was reported as holding the system")
        return disk

    raise TestFailure(
        "hostd did not find the USB disk\n    " +
        json.dumps([{k: d.get(k) for k in ("device", "transport", "removable")}
                    for d in disks]))


def verify_the_disk_is_seen(vm: VM) -> None:
    step("What hostd makes of a freshly plugged-in disk")

    disk = find_removable_volume(vm)
    check(disk["removable"], f"{disk['device']} is reported as removable")
    check(disk["size_bytes"] > 0, f"its size is known ({disk['size_bytes']} bytes)")

    volume = disk["volumes"][0]
    # A blank disk. Crucially this must be a positive answer rather than a
    # failure to read: it is what decides whether Homebase offers to erase it.
    check(not volume.get("unreadable"),
          "hostd could read it",
          "An unreadable disk must never be described as blank.")
    check(not volume.get("filesystem"),
          f"and found no filesystem on it ({volume.get('filesystem') or 'none'})")


def verify_formatting_refuses_the_system_disk(vm: VM) -> None:
    step("What formatting refuses")

    disks = must(vm, "storage.list_disks")["disks"]
    system = [d for d in disks if d.get("system")]
    check(len(system) >= 1, f"the system disk is identified ({[d['device'] for d in system]})")

    for disk in system:
        for volume in disk["volumes"]:
            if not volume.get("uuid"):
                continue
            status, body = op(vm, "storage.format", {
                "uuid": volume["uuid"], "confirm": volume["uuid"],
            }, confirmed=True)
            check(status != 200,
                  f"erasing {volume['device']} is refused ({error_code(body)})",
                  "It holds the running system.")
            check(error_code(body) == "storage.refused_system_disk",
                  "and refused for that reason specifically",
                  json.dumps(body))
            return

    info("no system volume with a UUID to try; skipped")


def verify_format_requires_naming_the_disk(vm: VM) -> None:
    step("Erasing requires naming the disk")

    disk = find_removable_volume(vm)
    device = disk["volumes"][0]["path"]

    for confirmation in ("", "yes", "/dev/sdz", device.upper()):
        status, body = op(vm, "storage.format",
                          {"device": device, "confirm": confirmation}, confirmed=True)
        check(status != 200,
              f"{confirmation!r} is not accepted as confirmation ({error_code(body)})")

    # And it is still blank, so nothing was erased by the refusals.
    volume = find_removable_volume(vm)["volumes"][0]
    check(not volume.get("filesystem"), "and the disk is untouched")


def format_the_disk(vm: VM) -> str:
    step("Preparing the disk")

    disk = find_removable_volume(vm)
    device = disk["volumes"][0]["path"]

    result = must(vm, "storage.format",
                  {"device": device, "confirm": device, "label": "HomebaseMedia"},
                  confirmed=True, timeout=900)

    check(bool(result.get("uuid")),
          f"it has a filesystem and an identity ({result.get('uuid')})",
          "Without a UUID it cannot be assigned to an application at all — and "
          "the caller would be recording an identity that does not exist.")
    check(result.get("filesystem") == "ext4",
          f"the filesystem is {result.get('filesystem')}")

    return result["uuid"]


def add_the_location(vm: VM, uuid: str) -> None:
    step("Setting it up as a storage location")

    result = must(vm, "storage.add_location",
                  {"uuid": uuid, "id": LOCATION, "name": "Films drive"},
                  confirmed=True)
    ok(result.get("message", "added"))

    state = one_location(vm)
    check(state["connected"], "it is connected")
    check(state["mounted"], f"and mounted at {state['mount_point']}")
    check(state["total_bytes"] > 0,
          f"its size is known ({state['total_bytes'] // (1024*1024)} MB)")

    # nosuid and nodev are not decoration: a removable disk can be prepared on
    # another machine, and a setuid root binary on one is a local root exploit
    # that arrives by hand.
    options = ssh(vm, ["findmnt", "-no", "OPTIONS", state["mount_point"]],
                  check=False).stdout.strip()
    for option in ("nosuid", "nodev"):
        check(option in options, f"mounted {option} ({options})")

    unit = ssh(vm, ["sudo", "cat",
                    "/etc/systemd/system/srv-homebase-storage-media.mount"],
               check=False).stdout
    check("nofail" in unit,
          "its mount unit carries nofail",
          "Without it, a disk that is not connected stops the machine booting.")
    check(f"/dev/disk/by-uuid/{uuid}" in unit,
          "and names the disk by UUID rather than by device path")


def one_location(vm: VM) -> dict:
    locations = must(vm, "storage.list_locations")["locations"]
    for location in locations:
        if location["id"] == LOCATION:
            return location
    raise TestFailure(f"no location {LOCATION}\n    {json.dumps(locations, indent=4)}")


# --- The parts that matter ----------------------------------------------------


def write_something_to_it(vm: VM) -> str:
    step("Putting a file on the disk")

    marker = "a film the user would be upset to lose"
    path = f"{STORAGE_ROOT}/{LOCATION}/film.txt"
    ssh(vm, ["sudo", "sh", "-c", f"echo '{marker}' > {path}"])
    ok("written")
    return marker


def verify_it_survives_a_reboot(vm: VM, marker: str) -> None:
    step("Restarting the machine with the disk still plugged in")

    reboot(vm)

    # hostd is socket-activated, so it is not running until something asks. The
    # mount, though, is systemd's and should already be back.
    deadline = time.time() + 60
    while time.time() < deadline:
        mounted = ssh(vm, ["findmnt", "-no", "TARGET", f"{STORAGE_ROOT}/{LOCATION}"],
                      check=False).returncode == 0
        if mounted:
            break
        time.sleep(2)

    state = one_location(vm)
    check(state["mounted"],
          "the disk mounted itself at boot",
          "The mount unit is enabled, so systemd should have done this without "
          "anything from Homebase.")

    content = ssh(vm, ["sudo", "cat", f"{STORAGE_ROOT}/{LOCATION}/film.txt"],
                  check=False).stdout.strip()
    check(content == marker, "and the file is still there")


def verify_unplugging_is_noticed(vm: VM) -> None:
    step("Pulling the disk out, without warning")

    detach_removable_disk(vm)

    # The kernel leaves a stale mount behind: the filesystem is still listed as
    # mounted over a device that no longer exists. Homebase has to report the
    # location as gone regardless of what the mount table still claims.
    state = one_location(vm)
    check(not state["connected"],
          "Homebase reports the disk as disconnected",
          f"got connected={state['connected']} mounted={state['mounted']}")
    check(not state["mounted"], "and not mounted")


def verify_nothing_can_write_while_it_is_absent(vm: VM) -> None:
    step("What happens to a write while the disk is away")

    # Clear the stale mount first, which is what leaves a bare mountpoint on the
    # root filesystem — the state where a stray write would silently land on the
    # system disk.
    ssh(vm, ["sudo", "umount", "-l", f"{STORAGE_ROOT}/{LOCATION}"], check=False)

    result = ssh(vm, ["sudo", "sh", "-c",
                      f"echo x > {STORAGE_ROOT}/{LOCATION}/written-while-absent.txt"],
                 check=False)
    check(result.returncode != 0,
          "a write into the empty mount point fails",
          "Otherwise an application carries on writing to the system disk, "
          "filling it with files that vanish behind the disk the moment it is "
          "reconnected. Nothing reports an error, and the user sees an "
          "application that lost their data and a server out of space.")

    mode = ssh(vm, ["stat", "-c", "%a %U", f"{STORAGE_ROOT}/{LOCATION}"],
               check=False).stdout.strip()
    check(mode.startswith("555"), f"the empty mount point is {mode}")


def verify_reconnecting_finds_it_again(vm: VM, uuid: str, marker: str) -> None:
    step("Plugging it back in")

    attach_removable_disk(vm)

    # It will not be the device it was. That is the whole reason a location
    # records a filesystem UUID rather than a device path.
    disk = find_removable_volume(vm)
    ok(f"the kernel calls it {disk['device']} now")
    check(disk["volumes"][0]["uuid"] == uuid,
          "and it is the same filesystem, by UUID",
          f"expected {uuid}, got {disk['volumes'][0].get('uuid')}")

    must(vm, "storage.mount", {"id": LOCATION})

    state = one_location(vm)
    check(state["mounted"], f"mounted again at {state['mount_point']}")

    content = ssh(vm, ["sudo", "cat", f"{STORAGE_ROOT}/{LOCATION}/film.txt"],
                  check=False).stdout.strip()
    check(content == marker,
          "and the file is intact",
          "This is the milestone's exit condition: removed and reconnected "
          "without corrupting anything.")

    # And nothing was written to the system disk while it was away.
    stray = ssh(vm, ["sudo", "test", "-e",
                     f"{STORAGE_ROOT}/{LOCATION}/written-while-absent.txt"],
                check=False)
    check(stray.returncode != 0,
          "nothing that was written while it was absent is shadowing the disk")


def verify_removing_the_location_keeps_the_data(vm: VM, marker: str) -> None:
    step("Removing the location")

    for confirmation in ({}, {"id": "not-a-location"}):
        status, body = op(vm, "storage.remove_location", confirmation, confirmed=True)
        check(status != 200, f"{confirmation} is refused ({error_code(body)})")

    result = must(vm, "storage.remove_location", {"id": LOCATION}, confirmed=True)
    ok(result.get("message", "removed"))

    unit = ssh(vm, ["test", "-e",
                    "/etc/systemd/system/srv-homebase-storage-media.mount"],
               check=False)
    check(unit.returncode != 0, "its mount unit is gone")

    # The disk itself is untouched. Removing a location stops Homebase managing
    # a disk; it does not erase one.
    disk = find_removable_volume(vm)
    check(disk["volumes"][0]["uuid"] == "" or disk["volumes"][0].get("filesystem") == "ext4",
          "the disk still has its filesystem")

    ssh(vm, ["sudo", "mkdir", "-p", "/mnt/check"])
    ssh(vm, ["sudo", "mount", disk["volumes"][0]["path"], "/mnt/check"], check=False)
    content = ssh(vm, ["sudo", "cat", "/mnt/check/film.txt"], check=False).stdout.strip()
    ssh(vm, ["sudo", "umount", "/mnt/check"], check=False)

    check(content == marker,
          "and everything on it is still there",
          "Removing a location must never delete anything.")


# --- Driver -------------------------------------------------------------------


def main() -> int:
    started = time.time()
    print()
    step("Homebase storage")
    info("a real USB disk, plugged and unplugged for real")
    print()

    vm: VM | None = None
    try:
        binary = build_hostd()

        vm = create(VM_NAME, force=True)
        create_removable_disk(vm, size_gb=2)
        start(vm)
        wait_for_ssh(vm)
        wait_for_boot_complete(vm)

        install_hostd(vm, binary)
        attach_removable_disk(vm)

        verify_the_disk_is_seen(vm)
        verify_formatting_refuses_the_system_disk(vm)
        verify_format_requires_naming_the_disk(vm)

        uuid = format_the_disk(vm)
        add_the_location(vm, uuid)
        marker = write_something_to_it(vm)

        verify_it_survives_a_reboot(vm, marker)
        verify_unplugging_is_noticed(vm)
        verify_nothing_can_write_while_it_is_absent(vm)
        verify_reconnecting_finds_it_again(vm, uuid, marker)
        verify_removing_the_location_keeps_the_data(vm, marker)

        elapsed = int(time.time() - started)
        print()
        ok(f"PASS — a disk can be added, unplugged and reconnected without "
           f"corrupting anything ({elapsed}s)")
        return 0

    except (TestFailure, VMError) as error:
        print()
        fail(str(error))
        if vm is not None:
            try:
                journal = ssh(vm, ["sudo", "journalctl", "-u", "homebase-hostd",
                                   "--no-pager", "-n", "30"], check=False).stdout
                if journal.strip():
                    print("\n  --- hostd ---")
                    for line in journal.strip().splitlines()[-20:]:
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
        if vm is not None:
            destroy(VM_NAME)


if __name__ == "__main__":
    sys.exit(main())
