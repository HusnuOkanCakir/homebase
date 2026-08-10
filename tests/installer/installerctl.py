#!/usr/bin/env python3
"""Booting Homebase's installation media in a virtual machine.

The development lab (tests/vm/) deliberately does not install anything: it
overlays a cloud image, which takes twenty seconds instead of fifteen minutes.
[ADR-0010](../../docs/decisions/0010-vm-lab-qemu-cloud-image.md) recorded that
choice and its cost — the machine every other test runs on is *close to*, not
identical to, a real installation — and named this directory as the answer.

So this is the slow, faithful one. It boots the official Ubuntu Server ISO,
unmodified, with a Homebase seed beside it, and installs onto a blank disk or
one that already has Windows on it. Nothing here is simulated: the partitioning
is real, the reboot is real, and what comes up afterwards is what a user would
have.

See [ADR-0016](../../docs/decisions/0016-installation-media.md) for what the
media is and why it is not repacked.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "vm"))

from vmctl import (  # noqa: E402
    VM,
    VMError,
    CACHE_DIR,
    RUN_DIR,
    fail,
    find_ovmf,
    free_port,
    info,
    ok,
    run,
    step,
)

# Pinned, and verified before use. A point release can change the installer's
# behaviour underneath us, so moving to a new one is a deliberate change with a
# test run behind it rather than something that happens on a Tuesday.
ISO_RELEASE = "24.04.4"
ISO_NAME = f"ubuntu-{ISO_RELEASE}-live-server-amd64.iso"
ISO_URL = f"https://releases.ubuntu.com/24.04/{ISO_NAME}"
ISO_SHA256 = "e907d92eeec9df64163a7e454cbc8d7755e8ddc7ed42f99dbc80c40f1a138433"

# The installer needs considerably more than a running server does: it unpacks a
# squashfs into memory and runs curtin alongside it. Three gigabytes is enough
# for Ubuntu Server's autoinstall and leaves room on a developer laptop — four
# was fine until the machine had a browser open, at which point the kernel
# killed QEMU four minutes into a fourteen-minute test.
#
# Overridable, because "enough" depends on what else the machine is doing:
#     HOMEBASE_INSTALL_MEMORY_MB=4096 make vm-test-installer
INSTALL_MEMORY_MB = int(os.environ.get("HOMEBASE_INSTALL_MEMORY_MB", "2560"))
INSTALL_CPUS = 2

# What the host needs free on top of that: QEMU's own overhead, and the page
# cache for a 3.2 GB medium it is reading from.
HOST_HEADROOM_MB = 512

# Big enough for Ubuntu plus room to prove an application's data landed
# somewhere sensible. Sparse, so it costs what it uses.
TARGET_DISK_SIZE = "20G"

# Installing takes minutes, not seconds, and a machine that is genuinely stuck
# looks identical to one that is merely slow until the clock runs out.
INSTALL_TIMEOUT_S = 2400


def fetch_iso() -> Path:
    """Download the official ISO once, and verify it every time it is used.

    Verified on every run rather than only on download: the interesting failure
    is not a bad download, it is a cache that was fine last month and has since
    been touched by something else. This is the image that erases disks.
    """
    CACHE_DIR.mkdir(parents=True, exist_ok=True)
    iso = CACHE_DIR / ISO_NAME

    if not iso.exists():
        step(f"Downloading {ISO_NAME} (about 2.6 GB, once)")
        partial = iso.with_suffix(iso.suffix + ".part")
        result = subprocess.run(
            ["curl", "--fail", "--location", "--progress-bar",
             "--output", str(partial), ISO_URL],
        )
        if result.returncode != 0:
            partial.unlink(missing_ok=True)
            raise VMError(
                f"Could not download {ISO_NAME}.",
                f"Tried {ISO_URL}. Check the network, or download it by hand into "
                f"{CACHE_DIR}.",
            )
        partial.rename(iso)

    digest = subprocess.run(
        ["sha256sum", str(iso)], capture_output=True, text=True, check=True,
    ).stdout.split()[0]

    if digest != ISO_SHA256:
        raise VMError(
            f"{ISO_NAME} does not match its published checksum.",
            f"Expected {ISO_SHA256}\n     got {digest}\n\n"
            "Delete it and let it download again. Do not install from it.",
        )

    ok(f"{ISO_NAME} verified against its published checksum")
    return iso


def create_target(name: str, size: str = TARGET_DISK_SIZE, force: bool = False) -> VM:
    """A machine with an empty disk, waiting to have something installed on it."""
    vm = VM(
        name=name,
        ssh_port=free_port(2300),
        api_port=free_port(8180),
        dashboard_port=free_port(8543),
    )

    if vm.dir.exists():
        if not force:
            raise VMError(f"'{name}' already exists.", "Pass force=True to replace it.")
        shutil.rmtree(vm.dir)
    vm.dir.mkdir(parents=True)

    run(["qemu-img", "create", "-f", "qcow2", str(vm.disk), size])
    ok(f"an empty {size} disk, as a new machine would have")

    _, ovmf_vars = find_ovmf()
    shutil.copy(ovmf_vars, vm.efi_vars)

    vm.save()
    return vm


def put_windows_on(vm: VM) -> None:
    """Make the target disk look like the laptop in the cupboard.

    The exit condition is deliberately about a Windows-occupied disk, because a
    blank one is the easy case and not the one people have. What matters is not
    that Windows would boot — it is that the disk is *not empty*, carries a
    recognisable signature, and that Homebase says so before erasing it.

    So this writes a real GPT with the partition types Windows uses, and an NTFS
    boot sector at the start of the data partition. Enough for `blkid` and
    `lsblk` to report exactly what they would report on the real thing.
    """
    step("Making the disk look like it already has Windows on it")

    raw = vm.dir / "windows-target.raw"
    run(["qemu-img", "create", "-f", "raw", str(raw), TARGET_DISK_SIZE])

    # The layout a Windows 10/11 UEFI installation leaves behind.
    run([
        "sgdisk",
        "--new", "1:2048:+100M", "--typecode", "1:ef00", "--change-name", "1:EFI system partition",
        "--new", "2:0:+16M", "--typecode", "2:0c01", "--change-name", "2:Microsoft reserved partition",
        "--new", "3:0:+18G", "--typecode", "3:0700", "--change-name", "3:Basic data partition",
        str(raw),
    ])

    # An NTFS boot sector at the start of partition three, so blkid reports
    # ntfs rather than "unknown". Written by hand rather than by mkfs.ntfs,
    # which is not installed everywhere and is not needed for this.
    _write_ntfs_signature(raw, partition_start=(2048 * 512) + (100 * 1024 * 1024) + (16 * 1024 * 1024))

    run(["qemu-img", "convert", "-f", "raw", "-O", "qcow2", str(raw), str(vm.disk)])
    raw.unlink()

    ok("a GPT disk with an EFI partition, a reserved partition and NTFS data")


def _write_ntfs_signature(image: Path, partition_start: int) -> None:
    """Write a minimal NTFS boot sector, so the partition identifies itself."""
    sector = bytearray(512)
    sector[0:3] = b"\xeb\x52\x90"          # jump instruction
    sector[3:11] = b"NTFS    "              # OEM identifier — what blkid reads
    sector[11:13] = (512).to_bytes(2, "little")   # bytes per sector
    sector[13] = 8                          # sectors per cluster
    sector[21] = 0xF8                       # media descriptor: fixed disk
    sector[24:26] = (63).to_bytes(2, "little")
    sector[26:28] = (255).to_bytes(2, "little")
    sector[510:512] = b"\x55\xaa"           # boot signature

    with image.open("r+b") as handle:
        handle.seek(partition_start)
        handle.write(sector)


def check_host_memory() -> None:
    """Refuse to start if this machine cannot hold the installation.

    Checked before anything is built rather than discovered by the kernel
    killing QEMU partway through: an out-of-memory kill looks exactly like a
    machine that crashed on its own, and the test spent four minutes getting
    there before saying so.
    """
    try:
        fields = {}
        for line in Path("/proc/meminfo").read_text().splitlines():
            name, _, rest = line.partition(":")
            fields[name] = int(rest.strip().split()[0]) // 1024
    except (OSError, ValueError, IndexError):
        return  # Not Linux, or an unreadable meminfo. Not worth failing over.

    available = fields.get("MemAvailable")
    if available is None:
        return

    needed = INSTALL_MEMORY_MB + HOST_HEADROOM_MB
    if available < needed:
        raise VMError(
            f"This machine has {available} MB free, and installing needs about {needed} MB.",
            "Close what you can and try again. Started anyway, the kernel picks a "
            "process to kill partway through — usually QEMU, which looks "
            "indistinguishable from the installer crashing.",
        )


def boot_installer(vm: VM, media: Path) -> None:
    """Boot the machine from the installation media.

    One drive, because that is what a user has: the stick `homebasectl installer
    create` wrote, carrying Ubuntu's image untouched and Homebase's seed in a
    partition appended after it.

    Attached over USB rather than as a disk, so the firmware sees what it would
    see on the real thing — and so that booting from it exercises the same
    hybrid boot path Canonical publishes rather than one this project invented.
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
        "-smp", str(INSTALL_CPUS),
        "-m", str(INSTALL_MEMORY_MB),
        "-nodefaults",
        # A display, which the development lab deliberately does without.
        #
        # Ubuntu's live-server ISO does not put `console=ttyS0` on its kernel
        # command line the way the cloud images do, so with no graphics adapter
        # the installer has nowhere to write and the machine looks hung when it
        # is merely silent. The screen is also the only way to see what the
        # installer is asking when it stops to ask something.
        "-vga", "std",
        "-display", "none",
        # UEFI, because Homebase ships UEFI-only and an installer tested under a
        # different firmware path is an installer tested on a machine we do not
        # ship. See ADR-0010.
        "-drive", f"if=pflash,format=raw,unit=0,readonly=on,file={ovmf_code}",
        "-drive", f"if=pflash,format=raw,unit=1,file={vm.efi_vars}",
        # The disk being installed onto — and the *first* thing the firmware
        # tries. It has no bootloader on it yet, so the firmware falls through
        # to the medium below and the installer runs. Once Ubuntu is installed
        # the disk boots, which is what makes the reboot at the end of the
        # install land on the new system without anybody touching anything.
        #
        # The alternative was to boot the medium first, then pull it out and
        # reset the machine. That looked reasonable and was a race: "the disk
        # has stopped being written to" is not "the installer has finished", and
        # resetting a few seconds early left a machine with partitions, no
        # bootloader, and a UEFI shell prompt.
        "-drive", f"id=target,if=none,format=qcow2,file={vm.disk}",
        "-device", "virtio-blk-pci,drive=target,bootindex=0",
        # A USB controller, declared before anything is plugged into it.
        "-device", "qemu-xhci,id=xhci",
        # The installation media, as a USB drive.
        "-drive", f"id=media,if=none,format=raw,readonly=on,file={media}",
        "-device", "usb-storage,id=media-stick,drive=media,bus=xhci.0,bootindex=1",
        "-netdev",
        (
            f"user,id=net0"
            f",hostfwd=tcp::{vm.ssh_port}-:22"
            f",hostfwd=tcp::{vm.api_port}-:8080"
            f",hostfwd=tcp::{vm.dashboard_port}-:8443"
        ),
        "-device", "virtio-net-pci,netdev=net0",
        "-serial", f"file:{vm.console_log}",
        "-monitor", "none",
        "-qmp", f"unix:{vm.qmp_socket},server=on,wait=off",
    ]

    (vm.dir / "qemu.cmd").write_text(" ".join(cmd) + "\n")

    process = subprocess.Popen(
        cmd, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, start_new_session=True,
    )

    time.sleep(2)
    if process.poll() is not None:
        stderr = (process.stderr.read().decode() if process.stderr else "").strip()
        raise VMError(
            "QEMU exited immediately.",
            f"{stderr[:400]}\nThe full command line is in {vm.dir / 'qemu.cmd'}.",
        )

    vm.pid = process.pid
    vm.save()
    ok(f"booted from {media.name}")


def wait_for_install(vm: VM, timeout: int = INSTALL_TIMEOUT_S) -> None:
    """Wait for the installation to actually happen.

    Measured by watching the target disk grow, which is the one signal that
    cannot be faked: Ubuntu's live-server ISO writes its progress to a screen
    rather than to the serial port, so there is no log to follow, and a test
    that merely slept could not tell a slow install from a stuck one.

    Growth also distinguishes the two failures worth telling apart — an
    installer that crashed, and an installer quietly waiting for an answer.
    """
    step("Installing")
    info("this takes several minutes — a real installation, not an overlay")

    deadline = time.time() + timeout
    started_writing = False
    last_reported = 0
    idle_since = time.time()
    previous_size = _written_bytes(vm)

    while time.time() < deadline:
        if not vm.is_running():
            # The machine going away is what success looks like here: the
            # install ends with `shutdown: reboot`, and QEMU keeps running, so
            # a *stopped* machine means something went wrong.
            raise VMError(
                "The machine stopped during installation.",
                f"QEMU is gone. The usual cause is the host running out of "
                f"memory and the kernel killing it — check `dmesg | grep -i oom`, "
                f"which reports it as an ordinary process being killed rather "
                f"than as anything to do with this test.\n"
                f"Console log: {vm.console_log}",
            )

        written = _written_bytes(vm)

        if written > previous_size:
            idle_since = time.time()
            if not started_writing:
                started_writing = True
                ok("the disk is being written to")
            megabytes = written // (1024 * 1024)
            if megabytes >= last_reported + 500:
                last_reported = megabytes - (megabytes % 500)
                info(f"  {last_reported} MB written")
        previous_size = written

        # Writing having stopped is a hint, not a finish. curtin pauses while it
        # runs commands in the installed system — writing the bootloader among
        # them — so this waits long enough that a pause is unlikely to be
        # mistaken for the end, and the machine coming back up on its own is
        # what actually settles it.
        if started_writing and written > 1_000_000_000 and time.time() - idle_since > 120:
            ok(f"the disk has stopped changing, {written // (1024 * 1024)} MB written")
            info("waiting for the machine to restart into what it installed")
            return

        if not started_writing and time.time() - idle_since > 600:
            shot = screenshot(vm, "stalled")
            raise VMError(
                "The installer never began writing to the disk.",
                f"Ten minutes with nothing written. Look at {shot}.",
            )

        time.sleep(5)

    shot = screenshot(vm, "timed-out")
    raise VMError(
        f"The installation did not finish within {timeout}s.",
        f"{previous_size // (1024 * 1024)} MB written. Look at {shot}.",
    )


def _written_bytes(vm: VM) -> int:
    """How much has actually landed on the target disk."""
    try:
        return vm.disk.stat().st_blocks * 512
    except OSError:
        return 0


def screenshot(vm: VM, name: str = "screen") -> Path | None:
    """Capture what is actually on the machine's screen.

    The single most useful diagnostic here, and the one the development lab has
    never needed. Everything else in this project reports failure through a log;
    an installer that stops to ask a question reports nothing at all, and looks
    exactly like one that has crashed. This is how the difference gets seen.
    """
    from vmctl import qmp  # local import: only this file needs the screen

    if not vm.is_running():
        return None

    target = vm.dir / f"{name}.ppm"
    try:
        qmp(vm, "screendump", filename=str(target))
    except (VMError, OSError):
        # A diagnostic that raises while explaining a failure hides the failure.
        return None

    if not target.exists():
        return None

    png = target.with_suffix(".png")
    try:
        _ppm_to_png(target, png)
    except Exception:  # noqa: BLE001 — a diagnostic must not break the test
        return target

    target.unlink(missing_ok=True)
    return png


def _ppm_to_png(source: Path, destination: Path) -> None:
    """Convert QEMU's framebuffer dump to something that can be looked at.

    Written out by hand rather than with a library, because the machine running
    the installer tests should not need an image toolchain installed to produce
    the one diagnostic that explains a stuck installer.
    """
    import struct
    import zlib

    data = source.read_bytes()

    # P6 <width> <height> <maxval>, whitespace-separated, then raw RGB.
    fields: list[bytes] = []
    offset = 0
    while len(fields) < 4:
        while offset < len(data) and data[offset : offset + 1].isspace():
            offset += 1
        if data[offset : offset + 1] == b"#":
            while offset < len(data) and data[offset] != 0x0A:
                offset += 1
            continue
        start = offset
        while offset < len(data) and not data[offset : offset + 1].isspace():
            offset += 1
        fields.append(data[start:offset])
    offset += 1

    if fields[0] != b"P6":
        raise ValueError(f"not a binary PPM: {fields[0]!r}")
    width, height = int(fields[1]), int(fields[2])
    pixels = data[offset:]

    stride = width * 3
    raw = bytearray()
    for row in range(height):
        raw.append(0)  # filter type: none
        raw += pixels[row * stride : (row + 1) * stride]

    def chunk(kind: bytes, payload: bytes) -> bytes:
        return (
            struct.pack(">I", len(payload))
            + kind
            + payload
            + struct.pack(">I", zlib.crc32(kind + payload) & 0xFFFFFFFF)
        )

    destination.write_bytes(
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
        + chunk(b"IDAT", zlib.compress(bytes(raw), 6))
        + chunk(b"IEND", b"")
    )


# QMP names for the keys this needs to press. Only what is used, because a
# lookup table nobody reads is a lookup table that rots.
_KEY_NAMES = {
    "\n": "ret", " ": "spc", "-": "minus", ".": "dot", "/": "slash",
}


def type_text(vm: VM, text: str, delay_ms: int = 40) -> None:
    """Type at the machine's console, as somebody standing in front of it would.

    Used for exactly one thing: answering Ubuntu's "Continue with autoinstall?"
    prompt. That prompt is the last chance to stop before the disk is erased,
    and it exists because Homebase deliberately does not repack the ISO to put
    `autoinstall` on the kernel command line — see ADR-0016.

    So the test answers it by pressing keys, rather than by arranging for the
    question not to be asked. A test that skipped it would be testing an
    installer nobody has.
    """
    from vmctl import qmp  # local import: only this file drives a keyboard

    for character in text:
        name = _KEY_NAMES.get(character)
        if name is None:
            if not character.isalnum():
                raise VMError(f"No key mapping for {character!r}.")
            name = character.lower()
        qmp(vm, "send-key", keys=[{"type": "qcode", "data": name}])
        time.sleep(delay_ms / 1000)


def wait_for_confirmation_prompt(vm: VM, timeout: int = 900) -> None:
    """Wait until Ubuntu stops to ask whether to go ahead, then say yes.

    The prompt is written to the graphical console and never to the serial port,
    which is why the first attempt at this test looked like a machine that had
    hung ten minutes in. Rather than read the words off a framebuffer, this
    waits for the thing that is actually true when a program is waiting for
    input: the screen stops changing, and nothing is being written to the disk.

    That is a weaker signal than reading the question, and it is deliberately
    checked afterwards — if answering did not start an installation, this raises
    with a screenshot rather than waiting out the clock.
    """
    step("Waiting for the last chance to stop")

    deadline = time.time() + timeout

    # A settled screen is not a *identical* screen: the cursor blinks, so two
    # dumps of the same still console differ every time. What settles is the
    # number of distinct images — one for cursor-on, one for cursor-off. More
    # than that and something is still being drawn.
    #
    # Learned by watching it alternate between exactly two hashes for ten
    # minutes while the test waited for them to be equal.
    window: list[bytes] = []
    samples = 8          # spanning about 24 seconds
    cursor_states = 2

    while time.time() < deadline:
        if not vm.is_running():
            raise VMError("The machine stopped before asking anything.",
                          f"Console log: {vm.console_log}")

        current = _screen_fingerprint(vm)
        if current:
            window.append(current)
            if len(window) > samples:
                window.pop(0)

        if len(window) == samples and len(set(window)) <= cursor_states:
            ok("the installer is waiting for an answer")
            type_text(vm, "yes\n")
            ok("answered yes, as somebody at the machine would")
            return

        time.sleep(3)

    shot = screenshot(vm, "never-asked")
    raise VMError(
        "The installer never settled on a question.",
        f"The screen kept changing for {timeout}s. Look at {shot}.",
    )


def _screen_fingerprint(vm: VM) -> bytes:
    """A cheap hash of what is on the screen."""
    import hashlib

    shot = screenshot(vm, "_scan")
    if shot is None:
        return b""
    try:
        return hashlib.sha256(shot.read_bytes()).digest()
    finally:
        shot.unlink(missing_ok=True)
        shot.with_suffix(".ppm").unlink(missing_ok=True)


def console_text(vm: VM) -> str:
    """What the installer has written to its screen, as readable text.

    Read out of the framebuffer dump rather than the serial port, because
    Ubuntu's live-server ISO does not redirect its console to serial the way the
    cloud images do.
    """
    shot = screenshot(vm, "console-read")
    if shot is None:
        return ""
    if shutil.which("tesseract") is None:
        return ""
    result = subprocess.run(
        ["tesseract", str(shot), "stdout"], capture_output=True, text=True,
    )
    return result.stdout


__all__ = [
    "ISO_NAME",
    "ISO_RELEASE",
    "INSTALL_TIMEOUT_S",
    "boot_installer",
    "create_target",
    "fetch_iso",
    "put_windows_on",
    "wait_for_install",
]
