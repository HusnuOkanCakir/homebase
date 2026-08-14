#!/usr/bin/env python3
"""Reach the server from outside the house, over WireGuard.

Two machines on a shared segment. One is the Homebase server; the other stands in
for a phone somewhere else — it has no Homebase on it and knows nothing about the
server except what a WireGuard configuration tells it.

What that proves is the whole path: a key issued by the server, imported by a
device that was never told anything else, a handshake completed, and the
dashboard answering over the tunnel. It does not prove the router bit, which
cannot be simulated and which nothing in Homebase automates — the test stands in
for a correctly forwarded port by putting both machines on one segment.

The assertions that matter are about the key. It is shown once and stored
nowhere, so:

  - the configuration handed out actually works
  - asking again does not produce it
  - removing the device really stops it connecting

Run with `make vm-test-vpn`.
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
    copy_to,
    create,
    destroy,
    fail,
    info,
    ok,
    ssh,
    start,
    step,
    wait_for_boot_complete,
    wait_for_ssh,
    write_file,
)

REPO_ROOT = Path(__file__).resolve().parents[2]
SERVER = "homebase-vpn-server"
CLIENT = "homebase-vpn-client"
LAN = 71

PASSWORD = "a-sufficiently-long-password"


class TestFailure(Exception):
    pass


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        ok(description)
    else:
        raise TestFailure(f"{description}\n    {detail}" if detail else description)


def ctl(vm: VM, *args: str, check_it: bool = False, timeout: int = 180):
    result = ssh(vm, ["sudo", "homebasectl", *args], check=False, timeout=timeout)
    if check_it and result.returncode != 0:
        raise TestFailure(f"homebasectl {' '.join(args)} failed "
                          f"({result.returncode})\n{(result.stdout + result.stderr)[-500:]}")
    return result


def ctl_json(vm: VM, *args: str, timeout: int = 180) -> dict:
    result = ctl(vm, *args, "--json", check_it=True, timeout=timeout)
    return json.loads(result.stdout)


def build_packages() -> list[Path]:
    step("Building packages")
    result = subprocess.run(["make", "packages", "VERSION=0.0.0~dev"],
                            cwd=REPO_ROOT, capture_output=True, text=True)
    if result.returncode != 0:
        raise VMError("building failed:\n" + (result.stdout + result.stderr)[-800:])
    packages = sorted((REPO_ROOT / "dist").glob("*_0.0.0~dev_*.deb"))
    check(len(packages) == 4, f"four packages ({len(packages)})")
    return packages


def install_server(vm: VM, packages: list[Path]) -> None:
    step("Installing Homebase on the server")

    apt(vm, "update -qq", timeout=600)
    for package in packages:
        copy_to(vm, package, f"/tmp/{package.name}")
    names = " ".join(f"/tmp/{p.name}" for p in packages)
    result = apt(vm, f"install -y -qq {names}", timeout=1200)
    if result.returncode != 0:
        raise TestFailure("installing failed\n" + (result.stdout + result.stderr)[-600:])

    for _ in range(60):
        if ssh(vm, ["systemctl", "is-active", "homebase-core.service"],
               check=False).stdout.strip() == "active":
            break
        time.sleep(1)

    result = ssh(vm, ["sudo", "sh", "-c",
                      f"HOMEBASE_PASSWORD='{PASSWORD}' homebasectl setup okan"],
                 check=False)
    check(result.returncode == 0, "and an administrator exists",
          (result.stdout + result.stderr)[-300:])

    # The tools have to come from the package rather than from this test, or the
    # test proves the test rather than the product.
    for tool in ("wg", "wg-quick"):
        found = ssh(vm, ["sh", "-c", f"command -v {tool}"], check=False)
        check(found.returncode == 0,
              f"{tool} came with homebase-hostd",
              "hostd shells out to it, so the package has to depend on it.")


# The shared segment, which has no DHCP on it — the same arrangement
# test_network.py uses, and for the same reason: a static address takes one
# variable out of a test that is about something else.
HOUSE_NETWORK = "10.7.0"
SERVER_ADDRESS = HOUSE_NETWORK + ".10"
CLIENT_ADDRESS = HOUSE_NETWORK + ".11"


def give_it_an_address(vm: VM, address: str) -> None:
    """Put the machine on the shared segment.

    Without this the server has only QEMU's user-mode NAT address and whatever
    Docker's bridge happens to be — and the first run of this test picked
    Docker's, 172.17.0.1, which the other machine cannot reach. The handshake
    then failed for a reason that had nothing to do with WireGuard.
    """
    interfaces = ssh(vm, ["sh", "-c",
                          "ls /sys/class/net | grep -E '^(en|eth)' | sort"]).stdout.split()
    if len(interfaces) < 2:
        raise TestFailure(f"{vm.name} has no second interface: {interfaces}")

    lan = interfaces[1]
    ssh(vm, ["sudo", "sh", "-c",
             f"ip addr add {address}/24 dev {lan} 2>/dev/null; ip link set {lan} up"])
    ok(f"{vm.name} is {address} on {lan}")


# --- The tests ---------------------------------------------------------------------


def verify_nothing_is_set_up(vm: VM) -> None:
    step("Before anything is set up")

    status = ctl_json(vm, "vpn", "status")
    check(status.get("configured") is False, "remote access is off",
          json.dumps(status, indent=4))
    check("vpn setup" in (status.get("message") or ""),
          "and it says how to switch it on", status.get("message", ""))


def verify_setup(vm: VM, address: str) -> None:
    step(f"Switching on remote access at {address}")

    result = ctl(vm, "vpn", "setup", address, check_it=True)
    check("port forwarding" in result.stdout.lower() or "forward" in result.stdout.lower(),
          "it says the router still needs configuring",
          "That is the one step Homebase cannot do, and leaving it to be "
          "discovered when the first device fails is not good enough.\n" +
          result.stdout[:400])

    status = ctl_json(vm, "vpn", "status")
    check(status.get("configured") is True, "remote access is on")
    check(status.get("running") is True, "and the interface is up",
          json.dumps(status, indent=4))
    check(status.get("ever_connected") is False,
          "and nothing has connected yet, which it says rather than guessing")

    # The key must be root-only. It is the credential for the whole network.
    mode = ssh(vm, ["sudo", "stat", "-c", "%U:%G %a",
                    "/etc/wireguard/wg0.conf"]).stdout.strip()
    check(mode == "root:root 600", f"the configuration is root-only ({mode})")

    # Setting up again must not invalidate what has been handed out.
    before = ssh(vm, ["sudo", "grep", "PrivateKey", "/etc/wireguard/wg0.conf"]).stdout
    ctl(vm, "vpn", "setup", address, check_it=True)
    after = ssh(vm, ["sudo", "grep", "PrivateKey", "/etc/wireguard/wg0.conf"]).stdout
    check(before == after,
          "and setting it up again keeps the server's key",
          "Regenerating it would silently kill every device already handed out.")


def add_device(vm: VM) -> str:
    step("Issuing a key for one device")

    result = ctl(vm, "vpn", "add-device", "laptop", check_it=True)
    check("[Interface]" in result.stdout, "a configuration is printed",
          result.stdout[:400])
    check("PrivateKey" in result.stdout, "with the device's own key in it")
    check("only time" in result.stdout.lower() or "once" in result.stdout.lower(),
          "and it says this is the only chance to read it", result.stdout[-400:])

    config = result.stdout[result.stdout.index("[Interface]"):]
    config = config[:config.index("PersistentKeepalive")] + "PersistentKeepalive = 25\n"

    # The lab puts both machines on one segment; a phone in the real world is
    # somewhere else entirely. Routing the house's range into the tunnel from a
    # machine that is *on* that range would send the handshake packets into a
    # tunnel that is not up yet — a loop that exists here and not in the
    # situation the feature is for. So the house range is dropped from the
    # client's routes, leaving the VPN's own, which is what is being tested.
    narrowed = []
    for line in config.splitlines():
        if line.startswith("AllowedIPs"):
            line = "AllowedIPs = 10.71.0.0/24"
        narrowed.append(line)
    config = "\n".join(narrowed) + "\n"

    # The private half must not be on the server anywhere.
    private = ""
    for line in config.splitlines():
        if line.startswith("PrivateKey"):
            private = line.split("=", 1)[1].strip()
    check(bool(private), "the configuration carries a private key")

    found = ssh(vm, ["sudo", "grep", "-r", "-l", private, "/etc", "/var/lib"],
                check=False)
    check(found.returncode != 0,
          "and that key is nowhere on the server",
          f"It was found in:\n    {found.stdout.strip()}\n    "
          "The server keeps only the public half — see ADR-0019.")

    # Nor in the audit log, which records every privileged call for ever.
    audit = ssh(vm, ["sudo", "cat", "/var/log/homebase/audit.log"], check=False).stdout
    check(private not in audit, "and not in the audit log")

    status = ctl_json(vm, "vpn", "status")
    names = [d["name"] for d in status.get("devices", [])]
    check(names == ["laptop"], f"the server lists it ({names})")

    return config


def verify_the_device_connects(client: VM, config: str, server_vpn_ip: str) -> None:
    step("Connecting from the other machine")

    result = apt(client, "install -y -qq wireguard-tools", timeout=900)
    if result.returncode != 0:
        raise TestFailure("installing wireguard on the client failed\n"
                          + (result.stdout + result.stderr)[-400:])

    write_file(client, "/etc/wireguard/wg0.conf", config, mode="0600")
    up = ssh(client, ["sudo", "wg-quick", "up", "wg0"], check=False, timeout=120)
    check(up.returncode == 0, "the configuration is accepted by WireGuard",
          (up.stdout + up.stderr)[-500:])

    # A handshake takes a moment. Poll rather than sleep a fixed time, because a
    # fixed time is either flaky or slow.
    for _ in range(30):
        handshake = ssh(client, ["sudo", "wg", "show", "wg0", "latest-handshakes"],
                        check=False).stdout.strip()
        if handshake and not handshake.endswith("0"):
            break
        time.sleep(2)
    check(bool(handshake) and not handshake.endswith("0"),
          f"and a handshake completes ({handshake})",
          "Without this the tunnel exists and carries nothing.")

    # The point of all of it: the dashboard, over the tunnel, from a machine that
    # knows nothing about the server except the configuration it was given.
    code = ssh(client, ["curl", "--silent", "--insecure", "--max-time", "20",
                        "-o", "/dev/null", "-w", "%{http_code}",
                        f"https://{server_vpn_ip}/api/v1/health"],
               check=False).stdout.strip()
    check(code == "200",
          f"and the dashboard answers over the VPN ({code})",
          "This is the milestone's whole point.")


def verify_the_server_knows(vm: VM) -> None:
    step("What the server says now")

    status = ctl_json(vm, "vpn", "status")
    check(status.get("ever_connected") is True,
          "it knows a device has connected",
          json.dumps(status, indent=4))

    device = status["devices"][0]
    check(bool(device.get("last_handshake")),
          f"and when ({device.get('last_handshake')})")

    # The reachability answer is evidence, not a probe: nothing asked anybody
    # outside whether the port was open.
    check(not status.get("message"),
          "and has nothing left to warn about",
          f"message was: {status.get('message')!r}")


def verify_removing_a_device_stops_it(server: VM, client: VM, server_vpn_ip: str) -> None:
    """The remedy for a lost phone, which has to actually work."""
    step("Taking the key away")

    ctl(server, "vpn", "remove-device", "laptop", check_it=True)

    status = ctl_json(server, "vpn", "status")
    check(status.get("devices") == [] or status.get("devices") is None,
          f"the server has no devices left ({status.get('devices')})")

    # The client still has its configuration and still thinks it is connected.
    # What must be true is that the server no longer answers it.
    ssh(client, ["sudo", "wg-quick", "down", "wg0"], check=False, timeout=60)
    ssh(client, ["sudo", "wg-quick", "up", "wg0"], check=False, timeout=60)

    code = ssh(client, ["curl", "--silent", "--insecure", "--max-time", "15",
                        "-o", "/dev/null", "-w", "%{http_code}",
                        f"https://{server_vpn_ip}/api/v1/health"],
               check=False).stdout.strip()
    check(code != "200",
          f"and a removed device cannot reach it any more ({code or 'no answer'})",
          "A device that keeps working after being removed makes the whole "
          "feature useless on the day somebody loses a phone.")


def main() -> int:
    started = time.time()
    server: VM | None = None
    client: VM | None = None

    print()
    step("Reaching the server from outside the house")
    info("two machines, one key, and a device that knows nothing about the")
    info("server except the configuration it was handed")
    print()

    try:
        packages = build_packages()

        server = create(SERVER, force=True)
        start(server, lan=LAN)
        client = create(CLIENT, force=True)
        start(client, lan=LAN)

        for vm in (server, client):
            wait_for_ssh(vm)
            wait_for_boot_complete(vm)

        give_it_an_address(server, SERVER_ADDRESS)
        give_it_an_address(client, CLIENT_ADDRESS)

        install_server(server, packages)

        verify_nothing_is_set_up(server)
        verify_setup(server, SERVER_ADDRESS)
        config = add_device(server)
        verify_the_device_connects(client, config, "10.71.0.1")
        verify_the_server_knows(server)
        verify_removing_a_device_stops_it(server, client, "10.71.0.1")

        elapsed = int(time.time() - started)
        print()
        ok(f"PASS — a device outside the house reaches the server over WireGuard, "
           f"and stops reaching it when its key is taken away ({elapsed}s)")
        return 0

    except (TestFailure, VMError) as error:
        print()
        fail(str(error))
        for vm in (server, client):
            if vm is None:
                continue
            try:
                journal = ssh(vm, ["sudo", "journalctl", "-u", "wg-quick@wg0",
                                   "-u", "homebase-hostd", "--no-pager", "-n", "20"],
                              check=False).stdout
                if journal.strip():
                    print(f"\n  --- {vm.name} ---")
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
        for name in (SERVER, CLIENT):
            try:
                destroy(name)
            except Exception:
                pass


if __name__ == "__main__":
    sys.exit(main())
