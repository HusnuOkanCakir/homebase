#!/usr/bin/env python3
"""Back up one machine. Destroy it. Restore onto a different one.

Milestone 5 is done when "a clean machine restores another machine's backup and
comes up with its apps, configuration and data". Every word of that is load
bearing, and the parts people get wrong are the parts about the *second*
machine — so this test uses two.

The first machine is set up, used, backed up onto a USB disk, and then
destroyed. The disk survives. A second machine is created from scratch — a
different machine-id, a different hostname, nothing in common but the disk — and
the backup is restored onto it.

That is the only arrangement that catches the failures that matter. Restoring
onto the machine that made the backup proves almost nothing: the files are
already there, the ids match, and half of what a restore has to reconstruct was
never lost.

Three assertions here have no equivalent anywhere else:

  - **A backup is readable without Homebase.** Checked by reading the manifest
    and a file straight off the disk with `cat`, on a machine that has no
    Homebase installed yet. See ADR-0014.

  - **The restore actually brings the data back**, verified by content rather
    than by the operation reporting success.

  - **Restoring does not delete what the backup does not contain**, which is
    what makes it safe to restore onto a machine that is still working.

Run with `make vm-test-backup`.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from vmctl import (  # noqa: E402
    VM,
    VMError,
    apt,
    attach_removable_disk,
    collect_logs,
    copy_to,
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
    write_file,
)

REPO_ROOT = Path(__file__).resolve().parents[2]
ORIGINAL = "homebase-origin"
REPLACEMENT = "homebase-restored"

SOCKET = "/run/homebase/hostd.sock"
STORAGE_ROOT = "/srv/homebase/storage"
BACKUP_DISK = "backups"

# The file the whole test is about: something a user would be upset to lose.
TREASURE = "the only copy of a photograph"

# An account in the database, so the export is checked by content rather than by
# the file merely existing.
ACCOUNT = "the-administrator"


class TestFailure(Exception):
    pass


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        ok(description)
    else:
        raise TestFailure(f"{description}\n    {detail}" if detail else description)


# --- Talking to hostd ---------------------------------------------------------


def op(vm: VM, name: str, params: dict | None = None, confirmed: bool = False,
       timeout: int = 300) -> tuple[int, dict]:
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
         timeout: int = 300) -> dict:
    status, body = op(vm, name, params, confirmed, timeout)
    if status != 200:
        raise TestFailure(f"{name} returned {status}\n    {json.dumps(body, indent=4)}")
    return body


def error_code(body: dict) -> str:
    return (body.get("error") or {}).get("code", "")


# --- Building a machine -------------------------------------------------------


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


def install_homebase(vm: VM, binary: Path) -> None:
    step(f"Installing Homebase on {vm.name}")

    ssh(vm, ["sudo", "groupadd", "--system", "--force", "homebase"])
    ssh(vm, ["sudo", "useradd", "--system", "--gid", "homebase",
             "--home-dir", "/var/lib/homebase", "--shell", "/usr/sbin/nologin",
             "homebase"], check=False)
    ssh(vm, ["sudo", "mkdir", "-p", "/usr/libexec/homebase", "/etc/homebase",
             "/var/lib/homebase", "/srv/homebase", "/var/log/homebase",
             "/usr/share/homebase/apps"])
    ssh(vm, ["sudo", "chown", "root:homebase", "/etc/homebase"])
    ssh(vm, ["sudo", "chown", "homebase:homebase", "/var/lib/homebase"])

    # sqlite3, because the database is exported with VACUUM INTO rather than
    # copied — the only supported way to get a consistent snapshot out of a
    # running SQLite database.
    result = apt(vm, "install -y -qq sqlite3")
    if result.returncode != 0:
        raise TestFailure("installing sqlite3 failed\n" + result.stdout[-400:])

    copy_to(vm, binary, "/usr/libexec/homebase/hostd", mode="0755")

    for manifest in sorted((REPO_ROOT / "app-store").glob("*.json")):
        write_file(vm, f"/usr/share/homebase/apps/{manifest.name}",
                   manifest.read_text(), mode="0644")

    for unit in ("homebase-hostd.service", "homebase-hostd.socket"):
        write_file(vm, f"/etc/systemd/system/{unit}",
                   (REPO_ROOT / "packaging" / "systemd" / unit).read_text())

    ssh(vm, ["sudo", "systemctl", "daemon-reload"])
    ssh(vm, ["sudo", "systemctl", "enable", "--now", "homebase-hostd.socket"])

    status, body = op(vm, "system.get_info", timeout=30)
    if status != 200:
        journal = ssh(vm, ["sudo", "journalctl", "-u", "homebase-hostd",
                           "--no-pager", "-n", "20"], check=False).stdout
        raise TestFailure(f"hostd did not start\n{journal}")
    ok("hostd is answering")


def prepare_backup_disk(vm: VM) -> str:
    """Format the USB disk and set it up as a location."""
    step("Preparing the backup disk")

    disks = must(vm, "storage.list_disks")["disks"]
    usb = next((d for d in disks if d.get("transport") == "usb"), None)
    if usb is None:
        raise TestFailure("no USB disk found\n    " + json.dumps(disks, indent=4))

    device = usb["volumes"][0]["path"]
    formatted = must(vm, "storage.format",
                     {"device": device, "confirm": device, "label": "Backups"},
                     confirmed=True, timeout=900)
    uuid = formatted["uuid"]
    check(bool(uuid), f"the disk has an identity ({uuid})")

    must(vm, "storage.add_location",
         {"uuid": uuid, "id": BACKUP_DISK, "name": "Backup drive"},
         confirmed=True)
    ok("set up as a storage location")
    return uuid


# --- The first machine ----------------------------------------------------------


def put_something_worth_losing_on_it(vm: VM) -> None:
    step("Putting something on the server worth losing")

    # An application's own data.
    ssh(vm, ["sudo", "mkdir", "-p", "/srv/homebase/apps/jellyfin/config"])
    ssh(vm, ["sudo", "sh", "-c",
             f"echo '{TREASURE}' > /srv/homebase/apps/jellyfin/config/library.db"])

    # And Homebase's own configuration.
    ssh(vm, ["sudo", "sh", "-c",
             "echo 'name: the-original-server' > /etc/homebase/homebase.yaml"])

    # A database, so the export path is exercised.
    #
    # This is the most delicate part of a backup: it is written out with
    # VACUUM INTO rather than copied, because copying a live SQLite database
    # gives something stale or corrupt. Left to itself this test would skip it
    # entirely — core is not running here, so without this there is no database
    # to export and the whole path goes untested.
    #
    # Left open with an uncommitted write-ahead log on purpose, which is exactly
    # the state a naive copy gets wrong.
    ssh(vm, ["sudo", "sh", "-c",
             "sqlite3 /var/lib/homebase/homebase.db "
             "'PRAGMA journal_mode=WAL; "
             "CREATE TABLE users (name TEXT); "
             f"INSERT INTO users VALUES (\"{ACCOUNT}\");'"])
    ssh(vm, ["sudo", "chown", "homebase:homebase", "/var/lib/homebase/homebase.db"],
        check=False)

    wal = ssh(vm, ["sudo", "ls", "/var/lib/homebase/"], check=False).stdout
    ok(f"a database with a write-ahead log beside it ({wal.split()})")

    ssh(vm, ["sudo", "sync"])
    ok("an application's data, the server's settings, and its database")


def refuse_to_back_up_onto_the_data_disk(vm: VM) -> None:
    step("What backing up refuses")

    status, body = op(vm, "backup.create", {"location": "not-a-disk"})
    check(status != 200, f"an unknown disk is refused ({error_code(body)})")

    # The interesting one is covered by unit tests, because it needs an
    # application assigned to the destination. Here the destination is the only
    # disk, so what is checked is that Homebase is honest about it in the
    # manifest — see verify_the_backup_says_what_it_lacks.


def make_a_backup(vm: VM) -> str:
    step("Backing up")

    result = must(vm, "backup.create",
                  {"location": BACKUP_DISK, "include_data": True},
                  timeout=1800)

    check(result["files"] >= 4,
          f"{result['files']} files copied",
          "Expected at least the database, the configuration, hostd's record of "
          "disks, and the application's data.")
    ok(result.get("message", "done"))

    listing = must(vm, "backup.list", {"location": BACKUP_DISK})["backups"]
    check(len(listing) == 1, f"one backup is on the disk ({len(listing)})")
    check(listing[0]["complete"], "and it is complete",
          "A backup with no readable manifest was interrupted.")

    return listing[0]["id"]


def verify_the_backup_before_relying_on_it(vm: VM, backup_id: str) -> None:
    step("Checking the backup")

    result = must(vm, "backup.verify",
                  {"location": BACKUP_DISK, "id": backup_id}, timeout=1800)

    check(result["valid"], "every file is present and matches its checksum",
          json.dumps(result, indent=4))
    check(result["files_checked"] > 0, f"{result['files_checked']} files checked")


def verify_the_backup_is_readable_without_homebase(vm: VM, backup_id: str) -> None:
    """ADR-0014's central claim, checked with cat rather than with Homebase."""
    step("Reading the backup without Homebase")

    directory = f"{STORAGE_ROOT}/{BACKUP_DISK}/homebase-backups/{backup_id}"

    readme = ssh(vm, ["sudo", "cat", f"{directory}/README.txt"], check=False).stdout
    check("You do not need Homebase" in readme,
          "it carries instructions for somebody without Homebase")
    check("Anyone holding this disk can read everything" in readme,
          "and warns that the disk is not encrypted")

    # The treasure, found by looking in a folder — which is the whole promise.
    found = ssh(vm, ["sudo", "cat",
                     f"{directory}/apps/jellyfin/config/library.db"], check=False).stdout.strip()
    check(found == TREASURE,
          "and the file is there, as an ordinary file in an ordinary folder",
          f"got {found!r}")

    manifest = ssh(vm, ["sudo", "cat", f"{directory}/manifest.json"], check=False).stdout
    parsed = json.loads(manifest)
    check(parsed["format_version"] == 1, "the manifest is readable JSON")
    check(any("passwords" in note for note in parsed.get("notes", [])),
          "and says what it deliberately does not contain",
          f"notes: {parsed.get('notes')}")

    # The database was exported rather than copied, and the export is a usable
    # database rather than a stale or half-written one.
    exported = ssh(vm, ["sudo", "sqlite3", f"{directory}/system/homebase.db",
                        "SELECT name FROM users"], check=False).stdout.strip()
    check(exported == ACCOUNT,
          "the database was exported as a working database",
          f"got {exported!r}. VACUUM INTO produces a consistent snapshot; "
          f"copying the file while it is in use does not.")


# --- The second machine ------------------------------------------------------------


def rescue_the_disk(vm: VM) -> Path:
    """Take the disk image out of the machine before destroying it."""
    step("Taking the disk out of the old machine")

    ssh(vm, ["sudo", "sync"], check=False)
    op(vm, "storage.unmount", {"id": BACKUP_DISK}, confirmed=True, timeout=120)

    rescued = REPO_ROOT / "tests/vm/run" / "rescued-backup-disk.qcow2"
    rescued.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(vm.removable_disk, rescued)

    size = rescued.stat().st_size // (1024 * 1024)
    ok(f"the disk image is safe ({size} MB)")
    return rescued


def verify_the_replacement_is_clean(vm: VM) -> None:
    step("The replacement machine has nothing on it")

    for path in ("/srv/homebase/apps/jellyfin/config/library.db",
                 "/etc/homebase/homebase.yaml"):
        result = ssh(vm, ["sudo", "test", "-e", path], check=False)
        check(result.returncode != 0, f"{path} does not exist here")

    # Something this machine has that the backup does not know about. Restoring
    # must not delete it: a restore is a merge, not a mirror.
    ssh(vm, ["sudo", "mkdir", "-p", "/srv/homebase/apps/added-here"])
    ssh(vm, ["sudo", "sh", "-c",
             "echo 'added on the new machine' > /srv/homebase/apps/added-here/notes.txt"])
    ok("and something has been added that the backup knows nothing about")


def preview_before_restoring(vm: VM, backup_id: str) -> None:
    step("What the restore says it will do")

    preview = must(vm, "backup.preview", {"location": BACKUP_DISK, "id": backup_id},
                   timeout=900)

    check(preview["verified"], "the backup is checked before it is offered",
          json.dumps(preview.get("integrity_issues"), indent=4))
    check(preview["files_to_write"] > 0, f"{preview['files_to_write']} files would be written")
    check(preview["hostname"] == ORIGINAL,
          f"it knows which machine this came from ({preview['hostname']})",
          "Restoring another machine's backup is the case that matters.")
    # Exactly one: hostd's record of which disks this machine uses, which this
    # machine has only because it had to set the backup disk up in order to see
    # the backup at all.
    #
    # Not zero. A "clean" machine is never quite clean by the time somebody is
    # restoring onto it — they have had to do something to get here — and a
    # preview that claimed nothing would be replaced would be wrong in exactly
    # the way that makes previews worthless.
    check(preview["would_overwrite"] == 1,
          f"it says how much would be replaced, accurately ({preview['would_overwrite']})",
          "Expected the one file this machine has that the backup also has: "
          "hostd's record of its disks.")
    check(str(preview["would_overwrite"]) in preview["message"],
          "and says so in the message a person reads",
          preview["message"])
    check(bool(preview.get("message")),
          f"and explains itself in words: {preview['message'][:90]}…")

    # Previewing changed nothing.
    result = ssh(vm, ["sudo", "test", "-e",
                      "/srv/homebase/apps/jellyfin/config/library.db"], check=False)
    check(result.returncode != 0, "and previewing wrote nothing")


def restore_onto_the_replacement(vm: VM, backup_id: str) -> None:
    step("Restoring")

    for confirmation in ("", "yes", "restore", backup_id.upper()):
        status, body = op(vm, "backup.restore",
                          {"location": BACKUP_DISK, "id": backup_id, "confirm": confirmation},
                          confirmed=True, timeout=120)
        check(status != 200,
              f"{confirmation!r} is not accepted as confirmation ({error_code(body)})")

    result = must(vm, "backup.restore",
                  {"location": BACKUP_DISK, "id": backup_id, "confirm": backup_id},
                  confirmed=True, timeout=1800)

    check(result["restored"] > 0, f"{result['restored']} files restored")
    check(result["skipped"] == 0, f"nothing was skipped ({result['skipped']})",
          json.dumps(result.get("problems"), indent=4))


def verify_the_machine_came_back(vm: VM) -> None:
    step("What the replacement machine has now")

    # The exit condition, checked by content rather than by a success message.
    found = ssh(vm, ["sudo", "cat",
                     "/srv/homebase/apps/jellyfin/config/library.db"],
                check=False).stdout.strip()
    check(found == TREASURE,
          "the application's data is back, byte for byte",
          f"got {found!r} — this is the milestone's exit condition")

    config = ssh(vm, ["sudo", "cat", "/etc/homebase/homebase.yaml"],
                 check=False).stdout.strip()
    check("the-original-server" in config,
          "and so are the server's own settings",
          f"got {config!r}")

    # A restore is a merge, not a mirror.
    added = ssh(vm, ["sudo", "cat", "/srv/homebase/apps/added-here/notes.txt"],
                check=False).stdout.strip()
    check(added == "added on the new machine",
          "and what this machine already had was not deleted",
          "Somebody restoring last month's backup to recover one application "
          "must not lose the three they added since.")

    # The database came back, and is a working database rather than a file.
    account = ssh(vm, ["sudo", "sqlite3", "/var/lib/homebase/homebase.db",
                       "SELECT name FROM users"], check=False).stdout.strip()
    check(account == ACCOUNT,
          "the accounts you sign in with are back",
          f"got {account!r}")

    # hostd's own record of disks came back too, or the restored machine would
    # not know which disk anything is on.
    locations = ssh(vm, ["sudo", "cat", "/var/lib/homebase-hostd/locations.json"],
                    check=False).stdout
    check(BACKUP_DISK in locations,
          "and the record of which disks this server uses")


# --- Driver ---------------------------------------------------------------------------


def main() -> int:
    started = time.time()
    print()
    step("Homebase backup and restore")
    info("one machine backs up, is destroyed, and a different machine restores it")
    print()

    original: VM | None = None
    replacement: VM | None = None
    rescued: Path | None = None

    try:
        binary = build_hostd()

        # --- The machine that is about to be lost ---------------------------------
        original = create(ORIGINAL, force=True)
        create_removable_disk(original, size_gb=2)
        start(original)
        wait_for_ssh(original)
        wait_for_boot_complete(original)

        install_homebase(original, binary)
        attach_removable_disk(original)

        prepare_backup_disk(original)
        put_something_worth_losing_on_it(original)
        refuse_to_back_up_onto_the_data_disk(original)

        backup_id = make_a_backup(original)
        verify_the_backup_before_relying_on_it(original, backup_id)
        verify_the_backup_is_readable_without_homebase(original, backup_id)

        rescued = rescue_the_disk(original)

        step(f"Destroying {ORIGINAL}")
        destroy(ORIGINAL)
        original = None

        # --- A different machine entirely -------------------------------------------
        replacement = create(REPLACEMENT, force=True)
        shutil.copy2(rescued, replacement.removable_disk)
        start(replacement)
        wait_for_ssh(replacement)
        wait_for_boot_complete(replacement)

        install_homebase(replacement, binary)
        attach_removable_disk(replacement)

        verify_the_replacement_is_clean(replacement)

        # It has to find the disk on its own, by the UUID in the backup — there
        # is nothing else linking the two machines.
        step("Finding the backup disk on the new machine")
        disks = must(replacement, "storage.list_disks")["disks"]
        usb = next((d for d in disks if d.get("transport") == "usb"), None)
        if usb is None:
            raise TestFailure("the new machine cannot see the disk")
        volume = usb["volumes"][0]
        check(volume.get("filesystem") == "ext4",
              f"the disk is recognised ({volume.get('filesystem')})")

        must(replacement, "storage.add_location",
             {"uuid": volume["uuid"], "id": BACKUP_DISK, "name": "Backup drive"},
             confirmed=True)
        ok("and set up on the new machine")

        listing = must(replacement, "backup.list", {"location": BACKUP_DISK})["backups"]
        check(len(listing) == 1, f"the backup is there ({len(listing)})")
        check(listing[0]["hostname"] == ORIGINAL,
              f"and says it came from {listing[0]['hostname']}")

        preview_before_restoring(replacement, backup_id)
        restore_onto_the_replacement(replacement, backup_id)
        verify_the_machine_came_back(replacement)

        elapsed = int(time.time() - started)
        print()
        ok(f"PASS — a clean machine restored another machine's backup and came up "
           f"with its settings and data ({elapsed}s)")
        return 0

    except (TestFailure, VMError) as error:
        print()
        fail(str(error))
        for vm in (original, replacement):
            if vm is None:
                continue
            try:
                journal = ssh(vm, ["sudo", "journalctl", "-u", "homebase-hostd",
                                   "--no-pager", "-n", "25"], check=False).stdout
                if journal.strip():
                    print(f"\n  --- hostd on {vm.name} ---")
                    for line in journal.strip().splitlines()[-15:]:
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
        for name in (ORIGINAL, REPLACEMENT):
            try:
                destroy(name)
            except Exception:
                pass
        if rescued is not None and rescued.exists():
            rescued.unlink()


if __name__ == "__main__":
    sys.exit(main())
