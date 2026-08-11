#!/usr/bin/env python3
"""Disposable VM harness for Homebase.

Creates throwaway Ubuntu VMs to test things that cannot be tested honestly anywhere
else: systemd units, real disks, real reboots.

Raw QEMU, cloud images, UEFI, no libvirt and no root — see
docs/decisions/0010-vm-lab-qemu-cloud-image.md.

Usage is through the Makefile (`make vm-create`, `make vm-test`, ...), but this can
be driven directly:

    ./tests/vm/vmctl.py create [--name NAME]
    ./tests/vm/vmctl.py ssh    [--name NAME] [-- command...]
    ./tests/vm/vmctl.py reboot [--name NAME]
    ./tests/vm/vmctl.py logs   [--name NAME]
    ./tests/vm/vmctl.py status
    ./tests/vm/vmctl.py destroy [--name NAME | --all]

Written in Python rather than shell because this is process supervision with
timeouts, and a harness whose failures are inscrutable is a harness people stop
trusting. See docs/development/testing.md.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shlex
import shutil
import signal
import socket
import subprocess
import sys
import textwrap
import time
import urllib.request
from dataclasses import dataclass, asdict
from pathlib import Path

# --- Configuration -----------------------------------------------------------

# Ubuntu Server LTS. Pinned deliberately: an image that silently changes underneath
# the tests makes a failure impossible to attribute.
UBUNTU_RELEASE = "24.04"
UBUNTU_CODENAME = "noble"
CLOUD_IMAGE = f"ubuntu-{UBUNTU_RELEASE}-server-cloudimg-amd64.img"
CLOUD_IMAGE_URL = (
    f"https://cloud-images.ubuntu.com/releases/{UBUNTU_RELEASE}/release/{CLOUD_IMAGE}"
)
CHECKSUM_URL = (
    f"https://cloud-images.ubuntu.com/releases/{UBUNTU_RELEASE}/release/SHA256SUMS"
)

VM_MEMORY_MB = 2048
VM_CPUS = 2
VM_DISK_SIZE = "20G"

BOOT_TIMEOUT_S = 180
SSH_TIMEOUT_S = 10
SHUTDOWN_TIMEOUT_S = 60

REPO_ROOT = Path(__file__).resolve().parents[2]
VM_DIR = Path(os.environ.get("HOMEBASE_VM_DIR", REPO_ROOT / "tests" / "vm"))
CACHE_DIR = VM_DIR / "cache"
RUN_DIR = VM_DIR / "run"

DEFAULT_NAME = "homebase-dev"


# --- Output ------------------------------------------------------------------

def _colour(code: str, text: str) -> str:
    return text if os.environ.get("NO_COLOR") else f"\033[{code}m{text}\033[0m"


def info(msg: str) -> None:
    print(f"  {msg}", flush=True)


def step(msg: str) -> None:
    print(_colour("1;34", f"==> {msg}"), flush=True)


def ok(msg: str) -> None:
    print(_colour("32", f"  ✓ {msg}"), flush=True)


def fail(msg: str, hint: str = "") -> None:
    print(_colour("31", f"  ✗ {msg}"), file=sys.stderr, flush=True)
    if hint:
        for line in textwrap.wrap(hint, 76):
            print(f"    {line}", file=sys.stderr, flush=True)


class VMError(Exception):
    """A harness failure with, wherever possible, a way out."""

    def __init__(self, message: str, hint: str = "") -> None:
        super().__init__(message)
        self.hint = hint


# --- Process liveness --------------------------------------------------------

def process_alive(pid: int) -> bool:
    """Whether a pid is a live process.

    `os.kill(pid, 0)` is the usual test and is wrong here. QEMU is started as a
    child of this process, so when it exits it becomes a **zombie** until reaped —
    and a zombie still answers signal 0. Using that test alone made `destroy` wait
    out its full shutdown timeout and then SIGKILL an already-dead process, every
    single time.

    So: reap it if it is ours, and treat state Z as dead.
    """
    try:
        # Reap if this is our child. Harmless otherwise.
        reaped, _ = os.waitpid(pid, os.WNOHANG)
        if reaped == pid:
            return False
    except (ChildProcessError, PermissionError, OSError):
        pass  # Not our child, or already reaped.

    try:
        state = Path(f"/proc/{pid}/stat").read_text()
        # Field 3 is the state character, after "pid (comm) ". comm may contain
        # spaces and parentheses, so split on the last ')'.
        return state.rsplit(")", 1)[1].split()[0] != "Z"
    except FileNotFoundError:
        return False
    except (IndexError, OSError):
        pass

    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


# --- VM state ----------------------------------------------------------------

@dataclass
class VM:
    name: str
    ssh_port: int
    api_port: int
    dashboard_port: int
    pid: int | None = None

    @property
    def dir(self) -> Path:
        return RUN_DIR / self.name

    @property
    def disk(self) -> Path:
        return self.dir / "disk.qcow2"

    @property
    def seed(self) -> Path:
        return self.dir / "seed.iso"

    @property
    def removable_disk(self) -> Path:
        """A disk that can be plugged and unplugged while the machine runs.

        Storage cannot be tested honestly without one. Every interesting failure
        in that milestone is about a disk that is not there — mid-write, at boot,
        or between a user assigning it and an application starting.
        """
        return self.removable_disk_at(0)

    def removable_disk_at(self, slot: int) -> Path:
        """One of several removable disks, by slot.

        Backups need two: Homebase refuses to back up onto the disk an
        application keeps its files on, so a machine with a single spare disk
        can hold data or hold backups, but not both. That is not a limitation
        of the test — it is the rule the product enforces, and a test with one
        disk cannot reach the backup at all.
        """
        return self.dir / (f"removable{slot}.qcow2" if slot else "removable.qcow2")

    @property
    def qmp_socket(self) -> Path:
        return self.dir / "qmp.sock"

    @property
    def key(self) -> Path:
        return self.dir / "id_ed25519"

    @property
    def console_log(self) -> Path:
        return self.dir / "console.log"

    @property
    def state_file(self) -> Path:
        return self.dir / "vm.json"

    @property
    def efi_vars(self) -> Path:
        return self.dir / "efivars.fd"

    def save(self) -> None:
        self.dir.mkdir(parents=True, exist_ok=True)
        self.state_file.write_text(json.dumps(asdict(self), indent=2) + "\n")

    @classmethod
    def load(cls, name: str) -> "VM":
        path = RUN_DIR / name / "vm.json"
        if not path.exists():
            raise VMError(
                f"No VM named '{name}'.",
                "Run `make vm-create` first, or `make vm-status` to see what exists.",
            )
        return cls(**json.loads(path.read_text()))

    def is_running(self) -> bool:
        return self.pid is not None and process_alive(self.pid)


# --- Prerequisites -----------------------------------------------------------

def find_ovmf() -> tuple[Path, Path]:
    """Locate OVMF firmware. Homebase is UEFI-only; BIOS boot would test the wrong path."""
    code_candidates = [
        "/usr/share/OVMF/OVMF_CODE_4M.fd",
        "/usr/share/OVMF/OVMF_CODE.fd",
        "/usr/share/edk2/ovmf/OVMF_CODE.fd",
        "/usr/share/qemu/OVMF_CODE.fd",
    ]
    vars_candidates = [
        "/usr/share/OVMF/OVMF_VARS_4M.fd",
        "/usr/share/OVMF/OVMF_VARS.fd",
        "/usr/share/edk2/ovmf/OVMF_VARS.fd",
        "/usr/share/qemu/OVMF_VARS.fd",
    ]
    code = next((Path(p) for p in code_candidates if Path(p).exists()), None)
    varsf = next((Path(p) for p in vars_candidates if Path(p).exists()), None)
    if not code or not varsf:
        raise VMError(
            "UEFI firmware (OVMF) not found.",
            "Install it with: sudo apt install ovmf. Homebase ships UEFI-only, so the "
            "lab boots UEFI rather than legacy BIOS — testing a firmware path we do "
            "not ship would prove very little.",
        )
    return code, varsf


def check_prerequisites() -> None:
    missing = [t for t in ("qemu-system-x86_64", "qemu-img") if not shutil.which(t)]
    if missing:
        raise VMError(
            f"Missing: {', '.join(missing)}",
            "Install with: sudo apt install qemu-system-x86 qemu-utils "
            "cloud-image-utils ovmf",
        )

    if not shutil.which("ssh") or not shutil.which("ssh-keygen"):
        raise VMError("openssh-client is not installed.", "sudo apt install openssh-client")

    find_ovmf()

    kvm = Path("/dev/kvm")
    if not kvm.exists():
        raise VMError(
            "/dev/kvm is missing.",
            "Hardware virtualisation is probably disabled in firmware. VMs would fall "
            "back to emulation, which is roughly ten times slower.",
        )
    if not os.access(kvm, os.R_OK | os.W_OK):
        raise VMError(
            "/dev/kvm is not accessible.",
            "Run: sudo usermod -aG kvm \"$USER\" — then log out and back in.",
        )


def free_port(start: int) -> int:
    for port in range(start, start + 200):
        with socket.socket() as s:
            try:
                s.bind(("127.0.0.1", port))
                return port
            except OSError:
                continue
    raise VMError(f"No free port found near {start}.")


# --- Base image --------------------------------------------------------------

def fetch_base_image() -> Path:
    """Download and checksum-verify the cloud image. Cached across runs."""
    CACHE_DIR.mkdir(parents=True, exist_ok=True)
    image = CACHE_DIR / CLOUD_IMAGE
    stamp = CACHE_DIR / f"{CLOUD_IMAGE}.verified"

    if image.exists() and stamp.exists():
        return image

    if not image.exists():
        step(f"Downloading {CLOUD_IMAGE} (~600 MB, cached for future runs)")
        tmp = image.with_suffix(".part")
        try:
            with urllib.request.urlopen(CLOUD_IMAGE_URL, timeout=60) as response:
                total = int(response.headers.get("Content-Length", 0))
                done = 0
                with tmp.open("wb") as handle:
                    while chunk := response.read(1024 * 512):
                        handle.write(chunk)
                        done += len(chunk)
                        if total:
                            pct = done * 100 // total
                            print(f"\r    {pct:3d}%  {done // 1048576} MB", end="", flush=True)
                print()
            tmp.rename(image)
        except Exception as exc:
            tmp.unlink(missing_ok=True)
            raise VMError(f"Download failed: {exc}", f"Source: {CLOUD_IMAGE_URL}") from exc

    step("Verifying checksum")
    try:
        with urllib.request.urlopen(CHECKSUM_URL, timeout=30) as response:
            sums = response.read().decode()
    except Exception as exc:
        raise VMError(f"Could not fetch SHA256SUMS: {exc}") from exc

    expected = next(
        (line.split()[0] for line in sums.splitlines() if line.strip().endswith(CLOUD_IMAGE)),
        None,
    )
    if not expected:
        raise VMError(f"{CLOUD_IMAGE} is not listed in SHA256SUMS.")

    digest = hashlib.sha256()
    with image.open("rb") as handle:
        while chunk := handle.read(1024 * 1024):
            digest.update(chunk)
    actual = digest.hexdigest()

    if actual != expected:
        image.unlink()
        raise VMError(
            "Checksum mismatch — the downloaded image has been deleted.",
            f"expected {expected}, got {actual}. This image is the base of every test "
            "VM, so an unverified one would compromise every result derived from it.",
        )

    stamp.write_text(f"{actual}  {CLOUD_IMAGE}\n")
    ok(f"sha256 {actual[:16]}…")
    return image


# --- cloud-init --------------------------------------------------------------

def build_seed_iso(vm: VM, public_key: str) -> None:
    """Build the cloud-init seed ISO that configures the VM on first boot."""
    user_data = f"""#cloud-config
hostname: {vm.name}
fqdn: {vm.name}.local
manage_etc_hosts: true

users:
  # Deliberately not "homebase": that is the name of the service account the
  # package creates, and a login user holding it makes `adduser --system` adopt
  # an interactive account instead of creating the system one.
  - name: dev
    groups: [sudo]
    shell: /bin/bash
    sudo: ['ALL=(ALL) NOPASSWD:ALL']
    ssh_authorized_keys:
      - {public_key}

ssh_pwauth: false
disable_root: true

# Serial console output is the only diagnostic available when a VM fails to boot,
# and a VM that fails to boot is exactly when it is needed.
output:
  all: '| tee -a /var/log/cloud-init-output.log'

package_update: false
package_upgrade: false

write_files:
  - path: /etc/homebase-vm
    content: |
      name={vm.name}
      created={time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}
      release={UBUNTU_RELEASE}

runcmd:
  - [systemctl, enable, --now, systemd-networkd]

final_message: "HOMEBASE_VM_READY after $UPTIME seconds"
"""

    meta_data = f"instance-id: {vm.name}\nlocal-hostname: {vm.name}\n"

    ud = vm.dir / "user-data"
    md = vm.dir / "meta-data"
    ud.write_text(user_data)
    md.write_text(meta_data)

    if shutil.which("cloud-localds"):
        run(["cloud-localds", str(vm.seed), str(ud), str(md)])
    elif shutil.which("genisoimage"):
        run([
            "genisoimage", "-output", str(vm.seed), "-volid", "cidata",
            "-joliet", "-rock", str(ud), str(md),
        ])
    elif shutil.which("xorriso"):
        run([
            "xorriso", "-as", "genisoimage", "-output", str(vm.seed),
            "-volid", "cidata", "-joliet", "-rock", str(ud), str(md),
        ])
    else:
        raise VMError(
            "No tool available to build the cloud-init seed image.",
            "Install one with: sudo apt install cloud-image-utils",
        )


# --- Process helpers ---------------------------------------------------------

def run(cmd: list[str], **kwargs) -> subprocess.CompletedProcess:
    result = subprocess.run(cmd, capture_output=True, text=True, **kwargs)
    if result.returncode != 0:
        raise VMError(
            f"{cmd[0]} failed (exit {result.returncode})",
            (result.stderr or result.stdout).strip()[:500],
        )
    return result


# --- Lifecycle ---------------------------------------------------------------

def create(name: str, force: bool = False) -> VM:
    check_prerequisites()

    existing = RUN_DIR / name
    if existing.exists():
        if not force:
            raise VMError(
                f"VM '{name}' already exists.",
                "Use `make vm-destroy` first, or `make vm-reset` to recreate it.",
            )
        destroy(name)

    base = fetch_base_image()

    vm = VM(
        name=name,
        ssh_port=free_port(2222),
        api_port=free_port(8080),
        dashboard_port=free_port(8443),
    )
    vm.dir.mkdir(parents=True, exist_ok=True)

    step(f"Creating '{name}'")

    # A throwaway key per VM. Never the developer's own — a harness that installs
    # your personal key into every disposable machine eventually installs it
    # somewhere you did not intend.
    run([
        "ssh-keygen", "-t", "ed25519", "-N", "", "-q",
        "-C", f"homebase-vm-{name}", "-f", str(vm.key),
    ])
    public_key = (vm.key.with_suffix(".pub")).read_text().strip()
    ok("throwaway SSH key generated")

    # Copy-on-write overlay: seconds and a few MB, rather than copying the base.
    run([
        "qemu-img", "create", "-f", "qcow2",
        "-F", "qcow2", "-b", str(base.resolve()),
        str(vm.disk), VM_DISK_SIZE,
    ])
    ok(f"overlay created ({VM_DISK_SIZE} virtual, backed by the cached base image)")

    build_seed_iso(vm, public_key)
    ok("cloud-init seed built")

    _, ovmf_vars = find_ovmf()
    shutil.copy(ovmf_vars, vm.efi_vars)

    vm.save()
    return vm


def start(vm: VM, lan: int | None = None) -> None:
    """Boot the machine.

    `lan` joins it to a shared network segment with every other machine
    given the same number. QEMU's user-mode networking isolates each VM
    behind its own NAT, which is right for everything else and makes it
    impossible to test the one claim this milestone rests on: that a server
    is reachable *by name* from another device. mDNS is multicast, and
    multicast needs a shared segment.

    A multicast socket rather than a bridge or a tap: those need root, and
    ADR-0010 says the VM lab does not.
    """
    if vm.is_running():
        info(f"'{vm.name}' is already running (pid {vm.pid})")
        return

    ovmf_code, _ = find_ovmf()

    cmd = [
        "qemu-system-x86_64",
        "-name", vm.name,
        "-machine", "q35,accel=kvm",
        "-cpu", "host",
        "-smp", str(VM_CPUS),
        "-m", str(VM_MEMORY_MB),
        "-nographic",
        "-nodefaults",
        # UEFI: Homebase ships UEFI-only.
        "-drive", f"if=pflash,format=raw,unit=0,readonly=on,file={ovmf_code}",
        "-drive", f"if=pflash,format=raw,unit=1,file={vm.efi_vars}",
        "-drive", f"if=virtio,format=qcow2,file={vm.disk}",
        "-drive", f"if=virtio,format=raw,file={vm.seed}",
        # User-mode networking: no root, no bridge, no daemon. See ADR-0010.
        "-netdev",
        (
            f"user,id=net0"
            f",hostfwd=tcp::{vm.ssh_port}-:22"
            f",hostfwd=tcp::{vm.api_port}-:80"
            f",hostfwd=tcp::{vm.dashboard_port}-:443"
        ),
        "-device", "virtio-net-pci,netdev=net0",
    ]

    if lan is not None:
        # Same multicast group and port for every machine on this segment, so
        # they see each other's broadcasts the way they would on one switch.
        cmd += [
            "-netdev", f"socket,id=lan0,mcast=230.0.0.{lan}:{11000 + lan}",
            "-device", "virtio-net-pci,netdev=lan0",
        ]

    cmd += [
        "-serial", f"file:{vm.console_log}",
        "-monitor", "none",
        # A USB controller with nothing on it. Disks are attached at runtime
        # rather than at boot, because the point is to plug and unplug them.
        "-device", "qemu-xhci,id=xhci",
        # QMP, so a test can do the plugging. `nowait` keeps QEMU from blocking
        # on a client that may never connect — most tests never touch this.
        "-qmp", f"unix:{vm.qmp_socket},server=on,wait=off",
    ]

    (vm.dir / "qemu.cmd").write_text(" ".join(cmd) + "\n")

    process = subprocess.Popen(
        cmd,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )

    time.sleep(1.5)
    if process.poll() is not None:
        stderr = (process.stderr.read().decode() if process.stderr else "").strip()
        raise VMError(
            "QEMU exited immediately.",
            f"{stderr[:400]}\nThe full command line is in {vm.dir / 'qemu.cmd'}.",
        )

    vm.pid = process.pid
    vm.save()


# --- Removable disks ---------------------------------------------------------
#
# Spoken to QEMU over QMP rather than simulated. A test that "unplugs" a disk by
# unmounting it proves nothing about the case that matters: the device going away
# underneath a filesystem that is still mounted and still being written to.


def qmp(vm: VM, command: str, **arguments) -> dict:
    """Send one QMP command and return its reply."""
    if not vm.qmp_socket.exists():
        raise VMError(
            f"'{vm.name}' has no QMP socket.",
            "The VM was started by an older vmctl, or is not running. Recreate it.",
        )

    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(15)
        client.connect(str(vm.qmp_socket))
        reader = client.makefile("rwb")

        # The greeting, then capabilities negotiation, before anything else is
        # accepted.
        reader.readline()
        reader.write(json.dumps({"execute": "qmp_capabilities"}).encode() + b"\n")
        reader.flush()
        reader.readline()

        request = {"execute": command}
        if arguments:
            request["arguments"] = arguments
        reader.write(json.dumps(request).encode() + b"\n")
        reader.flush()

        # Events are interleaved with replies and are not what was asked for.
        for _ in range(50):
            line = reader.readline()
            if not line:
                break
            reply = json.loads(line)
            if "event" in reply:
                continue
            if "error" in reply:
                raise VMError(
                    f"QMP {command} failed: {reply['error'].get('desc', reply['error'])}")
            return reply.get("return", {})

    raise VMError(f"QMP {command} returned nothing")


def create_removable_disk(vm: VM, size_gb: int = 2, slot: int = 0) -> Path:
    """Create the disk image, without attaching it."""
    path = vm.removable_disk_at(slot)
    if path.exists():
        path.unlink()

    run(["qemu-img", "create", "-f", "qcow2", str(path), f"{size_gb}G"])
    ok(f"removable disk {slot} created ({size_gb} GB, not yet plugged in)")
    return path


def attach_removable_disk(vm: VM, slot: int = 0) -> None:
    """Plug the disk in, as a USB stick."""
    path = vm.removable_disk_at(slot)
    if not path.exists():
        create_removable_disk(vm, slot=slot)

    qmp(vm, "blockdev-add",
        **{"node-name": f"removable{slot}", "driver": "qcow2",
           "file": {"driver": "file", "filename": str(path)}})
    qmp(vm, "device_add",
        driver="usb-storage", id=f"removable-stick{slot}",
        drive=f"removable{slot}", bus="xhci.0")

    # The guest needs a moment to notice and enumerate it.
    time.sleep(3)
    ok(f"disk {slot} plugged in")


def detach_removable_disk(vm: VM, slot: int = 0) -> None:
    """Pull the disk out, without warning, exactly as a person would."""
    qmp(vm, "device_del", id=f"removable-stick{slot}")

    # device_del is a request: the guest is asked to release the device, and
    # blockdev-del fails while it is still in use. Retried rather than slept on,
    # because how long it takes depends on what the guest was doing with it.
    for _ in range(20):
        time.sleep(1)
        try:
            qmp(vm, "blockdev-del", **{"node-name": f"removable{slot}"})
            ok(f"disk {slot} pulled out")
            return
        except VMError:
            continue

    raise VMError(
        "The guest would not release the disk.",
        "Something still has it open. That is worth understanding rather than "
        "forcing: on real hardware the user pulls the plug regardless, and what "
        "happens next is the thing being tested.")


# The account tests reach a machine through.
#
# The development lab's cloud-init seed makes a user called `dev`. A machine
# produced by Homebase's own installer has no such user — it has whatever the
# autoinstall identity section named, which is `console`. Tests that install
# rather than overlay set this.
SSH_USER = "dev"


def ssh_args(vm: VM, user: str | None = None) -> list[str]:
    return [
        "ssh",
        # A C locale in the guest, always.
        #
        # ssh forwards the client's LC_* variables by default, and a guest with
        # no matching locale answers every command with a dozen lines of perl
        # warnings. Those lines are what a failing command's captured output
        # ends with, so the real error scrolls off — an apt failure was reported
        # as "LC_TELEPHONE = de_CH.UTF-8" and nothing else.
        "-o", "SetEnv=LC_ALL=C LANG=C",
        "-i", str(vm.key),
        "-p", str(vm.ssh_port),
        "-o", "StrictHostKeyChecking=no",
        "-o", "UserKnownHostsFile=/dev/null",
        "-o", "LogLevel=ERROR",
        "-o", f"ConnectTimeout={SSH_TIMEOUT_S}",
        f"{user or SSH_USER}@127.0.0.1",
    ]


def wait_for_ssh(vm: VM, timeout: int = BOOT_TIMEOUT_S) -> None:
    step(f"Waiting for '{vm.name}' to boot")
    deadline = time.time() + timeout
    last_error = ""

    while time.time() < deadline:
        if not vm.is_running():
            raise VMError(
                "QEMU exited while booting.",
                f"Serial console: {vm.console_log}",
            )

        result = subprocess.run(
            ssh_args(vm) + ["true"],
            capture_output=True, text=True,
        )
        if result.returncode == 0:
            elapsed = int(timeout - (deadline - time.time()))
            ok(f"reachable over SSH after {elapsed}s")
            return

        last_error = result.stderr.strip()
        print(".", end="", flush=True)
        time.sleep(3)

    print()
    tail = ""
    if vm.console_log.exists():
        tail = "\n".join(vm.console_log.read_text(errors="replace").splitlines()[-15:])
    raise VMError(
        f"'{vm.name}' did not become reachable within {timeout}s.",
        f"Last SSH error: {last_error}\n\nSerial console tail:\n{tail}",
    )


def ssh(
    vm: VM,
    command: list[str] | None = None,
    check: bool = True,
    stdin: str | None = None,
    timeout: int = 300,
) -> subprocess.CompletedProcess:
    """Run a command in the guest.

    `command` is an argv list and is treated as one: each element is quoted so it
    arrives as a single argument, whatever it contains.

    This is not incidental. ssh does not take an argv — it joins whatever it is
    given into one string and hands that to the login shell, which then re-splits
    it. So `["stat", "-c", "%a %U %G", path]` arrives as five arguments and stat
    tries to open "%U" as a file. Anything containing a space silently becomes
    two arguments, which is a confusing failure a long way from its cause.

    `timeout` bounds the wait. There is a default because without one a wedged
    guest hangs the whole test run with no output — and a test suite that hangs
    is worse than one that fails, because nobody can tell it apart from one that
    is still working. Raise it for anything that legitimately takes minutes,
    such as apt or a container pull.
    """
    args = ssh_args(vm)
    if command:
        args.append(" ".join(shlex.quote(part) for part in command))
        try:
            result = subprocess.run(
                args, capture_output=True, text=True, input=stdin, timeout=timeout,
            )
        except subprocess.TimeoutExpired as expired:
            raise VMError(
                f"Command timed out after {timeout}s in '{vm.name}': {' '.join(command)}",
                (expired.stderr or expired.stdout or b"").decode(errors="replace").strip()[:500]
                if isinstance(expired.stderr or expired.stdout, bytes)
                else str(expired.stdout or "")[:500],
            ) from expired
        if check and result.returncode != 0:
            raise VMError(
                f"Command failed in '{vm.name}': {' '.join(command)}",
                (result.stderr or result.stdout).strip()[:500],
            )
        return result
    return subprocess.run(args)


def upload(vm: VM, local: Path, remote: str) -> None:
    """Copy a file into the guest as the login user.

    Use this for anything that does not need to be root-owned — a tarball being
    unpacked, a fixture, a script the test runs. Installing such a file as root
    makes it undeletable by the user afterwards, because /tmp is sticky.
    """
    result = subprocess.run(
        [
            "scp",
            "-i", str(vm.key),
            "-P", str(vm.ssh_port),
            "-o", "StrictHostKeyChecking=no",
            "-o", "UserKnownHostsFile=/dev/null",
            "-o", "LogLevel=ERROR",
            str(local),
            f"dev@127.0.0.1:{remote}",
        ],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise VMError(
            f"Could not copy {local.name} into '{vm.name}'.",
            (result.stderr or result.stdout).strip()[:400],
        )


def copy_to(vm: VM, local: Path, remote: str, mode: str = "0755") -> None:
    """Install a file into the guest as root.

    Uploads to a staging path then `sudo install`s it, because scp connects as
    the unprivileged user and cannot write to /usr or /etc. `install` sets the
    mode in the same step, so the file is never briefly present with the wrong
    permissions — which matters for anything about to run as root.
    """
    # A distinct staging name: staging to /tmp/<basename> collides when the
    # destination is itself in /tmp, and `install` then refuses because source
    # and target are the same file.
    staged = f"/tmp/.homebase-upload-{Path(remote).name}"

    upload(vm, local, staged)
    ssh(vm, ["sudo", "install", "-m", mode, "-o", "root", "-g", "root", staged, remote])
    ssh(vm, ["rm", "-f", staged])


def write_file(vm: VM, path: str, content: str, mode: str = "0644") -> None:
    """Write a file in the guest as root.

    Content travels over **stdin**, never as part of the command line.

    The obvious approach — `sudo sh -c "printf ... > /path"` — does not work, and
    fails in a way that looks like a permissions problem rather than a quoting
    one: ssh joins argv into a string for the remote shell, so the `>` is
    interpreted by the *outer* unprivileged shell rather than by the sudo'd one.
    The redirect then happens as the login user and the write is denied.

    Piping into `sudo tee` puts the privileged part where the write actually
    happens, and keeps file content out of shell quoting entirely — which also
    means a unit file containing quotes, newlines or `%` needs no escaping.
    """
    ssh(vm, ["sudo", "tee", path], stdin=content, check=True)
    ssh(vm, ["sudo", "chmod", mode, path])


def wait_for_boot_complete(vm: VM, timeout: int = 120) -> str:
    """Wait until systemd has finished starting up.

    SSH answers well before the system is actually ready — a first run observed
    `systemctl is-system-running` still reporting "starting" on a VM the harness
    had already declared reachable. A test that begins there races cloud-init and
    the remaining units, and races produce flaky tests that get blamed on the code
    under test rather than on the harness.

    "degraded" is accepted: it means every job finished but at least one unit
    failed, which is normal in a cloud image and is caught explicitly by the
    lifecycle test's "no failed units" assertion rather than being hidden here.
    """
    deadline = time.time() + timeout
    state = "unknown"

    while time.time() < deadline:
        result = ssh(vm, ["systemctl", "is-system-running"], check=False)
        state = result.stdout.strip() or result.stderr.strip()
        if state in ("running", "degraded"):
            ok(f"systemd finished starting ({state})")
            return state
        if state == "maintenance":
            raise VMError(
                "The guest booted into maintenance mode.",
                f"Serial console: {vm.console_log}",
            )
        time.sleep(2)

    raise VMError(
        f"systemd was still '{state}' after {timeout}s.",
        f"Serial console: {vm.console_log}",
    )


def reboot(vm: VM) -> None:
    step(f"Rebooting '{vm.name}'")
    boot_id_before = ssh(vm, ["cat", "/proc/sys/kernel/random/boot_id"]).stdout.strip()

    # The connection dies with the machine; a non-zero exit here is expected.
    ssh(vm, ["sudo", "systemctl", "reboot"], check=False)
    time.sleep(5)
    wait_for_ssh(vm)
    wait_for_boot_complete(vm)

    boot_id_after = ssh(vm, ["cat", "/proc/sys/kernel/random/boot_id"]).stdout.strip()
    if boot_id_before == boot_id_after:
        raise VMError(
            "The machine did not actually reboot.",
            "boot_id is unchanged, so SSH reconnected to the same boot. A reboot test "
            "that silently does not reboot is worse than no reboot test.",
        )
    ok(f"rebooted (boot_id {boot_id_before[:8]}… → {boot_id_after[:8]}…)")


def apt(vm: VM, arguments: str, timeout: int = 900) -> subprocess.CompletedProcess:
    """Run apt in the guest, without it pulling the floor out from under us.

    Two environment variables, both load-bearing:

    DEBIAN_FRONTEND=noninteractive stops a package stopping to ask a question
    nobody is there to answer.

    NEEDRESTART_SUSPEND stops Ubuntu's needrestart deciding, part-way through an
    install, that ssh.service should be restarted — which drops the connection
    the command is running over. That failure looks like "Connection closed by
    remote host" with no other explanation, arrives intermittently, and is
    entirely a property of installing something that touches a library sshd
    links against. Installing docker.io does.
    """
    return ssh(vm, ["sudo", "sh", "-c",
                    "DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1 "
                    "NEEDRESTART_MODE=l apt-get " + arguments],
               check=False, timeout=timeout)


def install_docker(vm: VM, prepull: list[str] | None = None) -> str:
    """Install a container runtime in the guest, and return the API version it speaks.

    From Ubuntu's own archive rather than Docker's repository. That is what a user
    gets from `apt install docker.io`, and it is deliberately not the newest
    Docker — the point of hostd negotiating the Engine API version is that
    Homebase works with whatever Docker the user happens to have, and a test that
    always installed the latest would never exercise that.

    `prepull` names images to fetch up front, so a test of the application
    lifecycle is not also a test of this connection's throughput or of whether
    Docker Hub is up. Homebase still performs its own pull; it finds the bytes
    already there.
    """
    step("Installing Docker")

    result = apt(vm, "update -qq")
    if result.returncode != 0:
        raise VMError("apt-get update failed",
                      (result.stderr or result.stdout).strip()[-600:])

    result = apt(vm, "install -y -qq docker.io")
    if result.returncode != 0:
        raise VMError("installing docker.io failed",
                      (result.stderr or result.stdout).strip()[-600:])

    # sudo: the login user is not in the docker group, and without it this
    # reports "<unknown>" — which reads like Docker failing to install rather
    # than the query failing to authenticate.
    version = ssh(vm, ["sudo", "docker", "version", "--format",
                       "{{.Server.APIVersion}} (minimum {{.Server.MinAPIVersion}})"],
                  check=False).stdout.strip()
    if not version:
        raise VMError("Docker installed but would not report its version",
                      "hostd negotiates the API version against this, so a daemon "
                      "that will not answer is a daemon Homebase cannot use.")
    ok(f"Docker speaks API {version}")

    for image in prepull or []:
        result = ssh(vm, ["sudo", "docker", "pull", image], check=False, timeout=900)
        if result.returncode != 0:
            raise VMError(f"could not pull {image}",
                          (result.stdout + result.stderr).strip()[-400:])
        ok(f"{image} is on the machine")

    return version


def collect_logs(vm: VM, destination: Path | None = None) -> Path:
    destination = destination or (vm.dir / "logs")
    destination.mkdir(parents=True, exist_ok=True)

    step(f"Collecting logs from '{vm.name}'")

    if vm.console_log.exists():
        shutil.copy(vm.console_log, destination / "console.log")
        ok("serial console")

    if vm.is_running():
        collectors = {
            "journal.log": ["sudo", "journalctl", "--no-pager", "-b"],
            "systemd-failed.log": ["systemctl", "--failed", "--no-pager"],
            "cloud-init.log": ["sudo", "cat", "/var/log/cloud-init-output.log"],
            "os-release.txt": ["cat", "/etc/os-release"],
        }
        for filename, command in collectors.items():
            result = ssh(vm, command, check=False)
            if result.returncode == 0:
                (destination / filename).write_text(result.stdout)
                ok(filename)
    else:
        info("VM is not running — serial console only")

    return destination


def destroy(name: str) -> None:
    path = RUN_DIR / name
    if not path.exists():
        info(f"No VM named '{name}'")
        return

    try:
        vm = VM.load(name)
        if vm.is_running() and vm.pid:
            step(f"Stopping '{name}' (pid {vm.pid})")
            try:
                os.kill(vm.pid, signal.SIGTERM)
                for _ in range(SHUTDOWN_TIMEOUT_S):
                    if not vm.is_running():
                        break
                    time.sleep(1)
                else:
                    os.kill(vm.pid, signal.SIGKILL)
                    info("did not stop on SIGTERM; killed")
            except ProcessLookupError:
                pass
    except (VMError, json.JSONDecodeError):
        # State unreadable. Remove the directory anyway — leaving an
        # undestroyable VM behind is the worse failure.
        pass

    shutil.rmtree(path, ignore_errors=True)
    ok(f"'{name}' destroyed")


def status() -> int:
    if not RUN_DIR.exists() or not any(RUN_DIR.iterdir()):
        print("No VMs.")
        return 0

    print(f"{'NAME':<20} {'STATE':<10} {'PID':<8} {'SSH':<7} DISK")
    for entry in sorted(RUN_DIR.iterdir()):
        if not (entry / "vm.json").exists():
            continue
        try:
            vm = VM.load(entry.name)
        except VMError:
            continue
        state = "running" if vm.is_running() else "stopped"
        size = ""
        if vm.disk.exists():
            size = f"{vm.disk.stat().st_size / 1048576:.0f} MB"
        print(f"{vm.name:<20} {state:<10} {str(vm.pid or '-'):<8} {vm.ssh_port:<7} {size}")
    return 0


# --- CLI ---------------------------------------------------------------------

def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    sub = parser.add_subparsers(dest="command", required=True)

    def with_name(p):
        p.add_argument("--name", default=DEFAULT_NAME)
        return p

    with_name(sub.add_parser("create")).add_argument("--force", action="store_true")
    with_name(sub.add_parser("start"))
    with_name(sub.add_parser("reboot"))
    with_name(sub.add_parser("logs")).add_argument("--out", type=Path)
    sub.add_parser("status")

    p_ssh = with_name(sub.add_parser("ssh"))
    p_ssh.add_argument("rest", nargs=argparse.REMAINDER)

    p_destroy = sub.add_parser("destroy")
    p_destroy.add_argument("--name", default=DEFAULT_NAME)
    p_destroy.add_argument("--all", action="store_true")

    args = parser.parse_args()

    try:
        if args.command == "status":
            return status()

        if args.command == "destroy":
            if args.all:
                if RUN_DIR.exists():
                    for entry in sorted(RUN_DIR.iterdir()):
                        if entry.is_dir():
                            destroy(entry.name)
                else:
                    info("No VMs.")
            else:
                destroy(args.name)
            return 0

        if args.command == "create":
            vm = create(args.name, force=args.force)
            start(vm)
            wait_for_ssh(vm)
            wait_for_boot_complete(vm)
            print()
            ok(f"'{vm.name}' is ready")
            info(f"ssh:       make vm-ssh")
            info(f"api:       http://127.0.0.1:{vm.api_port}")
            info(f"dashboard: https://127.0.0.1:{vm.dashboard_port}")
            return 0

        vm = VM.load(args.name)

        if args.command == "start":
            start(vm)
            wait_for_ssh(vm)
            wait_for_boot_complete(vm)
            return 0

        if args.command == "reboot":
            reboot(vm)
            return 0

        if args.command == "logs":
            destination = collect_logs(vm, args.out)
            print(f"\nLogs in {destination}")
            return 0

        if args.command == "ssh":
            rest = [a for a in args.rest if a != "--"]
            if rest:
                result = ssh(vm, rest, check=False)
                sys.stdout.write(result.stdout)
                sys.stderr.write(result.stderr)
                return result.returncode
            return ssh(vm).returncode

    except VMError as exc:
        print()
        fail(str(exc), exc.hint)
        return 1
    except KeyboardInterrupt:
        print("\nInterrupted. The VM is still running — `make vm-destroy` to remove it.")
        return 130

    return 0


if __name__ == "__main__":
    sys.exit(main())
