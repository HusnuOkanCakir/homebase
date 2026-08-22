#!/usr/bin/env python3
"""Milestone 7's exit condition: reachable by name from another device.

Two machines on one network segment. The first runs Homebase; the second is an
ordinary Linux box standing in for somebody's laptop, and it is the one that has
to find the server — by name, without being told an address.

That second machine is the whole point. Every other test in this repository
reaches Homebase through a port forwarded to the host's loopback, which is the
one origin browsers treat as trustworthy and the one address that always works.
Neither is what a user has. mDNS is multicast, so it cannot cross QEMU's
user-mode NAT at all: a shared segment is not a convenience here, it is the only
arrangement in which the claim can be tested.

Run with `make vm-test-network`.
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
    ok,
    ssh,
    start,
    step,
    upload,
    wait_for_boot_complete,
    wait_for_ssh,
)

REPO_ROOT = Path(__file__).resolve().parents[2]
SERVER = "homebase-net"
CLIENT = "homebase-client"

# The shared segment both machines sit on.
LAN = 7

NET_PASSWORD = "a-sufficiently-long-password"


class TestFailure(Exception):
    pass


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        ok(description)
    else:
        raise TestFailure(f"{description}\n    {detail}" if detail else description)


def build_packages() -> list[Path]:
    step("Building the packages")
    result = subprocess.run(
        ["make", "packages"], cwd=REPO_ROOT, capture_output=True, text=True,
    )
    if result.returncode != 0:
        raise VMError("building the packages failed",
                      (result.stdout + result.stderr).strip()[-800:])
    packages = sorted((REPO_ROOT / "dist").glob("*.deb"))
    ok(f"{len(packages)} packages")
    return packages


def give_it_an_address(vm: VM, last: int) -> None:
    """Configure the shared segment.

    No DHCP server on it, so the addresses are static. A real network has a
    router handing them out; what is being tested here is name resolution, and
    static addresses take one variable out of it.
    """
    interfaces = ssh(vm, ["sh", "-c",
                          "ls /sys/class/net | grep -v lo"], check=False).stdout.split()
    check(len(interfaces) >= 2,
          f"the machine has a second network interface ({' '.join(interfaces)})",
          "The shared segment did not attach.")

    # The second one is the shared segment: the first is QEMU's NAT, which is
    # how this test reaches the machine to configure it in the first place.
    lan = sorted(interfaces)[1]
    ssh(vm, ["sudo", "sh", "-c",
             f"ip addr add 10.7.0.{last}/24 dev {lan} 2>/dev/null; ip link set {lan} up"])
    ok(f"{vm.name} is 10.7.0.{last} on {lan}")


def install_server(vm: VM, packages: list[Path]) -> None:
    step("Installing Homebase on the server")

    # The package index first. homebase-core depends on avahi-daemon, which is
    # in Ubuntu's archive rather than on the machine — without this, apt cannot
    # resolve it and reports "held broken packages", which says nothing about
    # what is actually missing.
    result = apt(vm, "update -qq", timeout=600)
    if result.returncode != 0:
        raise TestFailure("apt-get update failed\n" + (result.stdout + result.stderr)[-500:])

    for package in packages:
        upload(vm, package, f"/tmp/{package.name}")
    names = " ".join(f"/tmp/{p.name}" for p in packages)

    result = apt(vm, f"install -y -qq --allow-downgrades {names}", timeout=1200)
    if result.returncode != 0:
        raise TestFailure("installing the packages failed\n" +
                          (result.stdout + result.stderr)[-700:])

    for _ in range(30):
        if ssh(vm, ["systemctl", "is-active", "homebase-core.service"],
               check=False).stdout.strip() == "active":
            break
        time.sleep(1)
    else:
        logs = ssh(vm, ["sudo", "journalctl", "-u", "homebase-core", "-n", "30",
                        "--no-pager"], check=False).stdout
        raise TestFailure(f"core did not start\n{logs[-700:]}")
    ok("core is running")

    # avahi comes in as a dependency of homebase-core. If it did not, the server
    # would have no name on the network and this whole milestone would be a
    # documentation change.
    check(ssh(vm, ["systemctl", "is-active", "avahi-daemon"],
              check=False).stdout.strip() == "active",
          "avahi-daemon is running, so the server has a name on the network",
          "It is a dependency of homebase-core; if it is missing, the package "
          "no longer declares it.")

    # An administrator, because the network status is now read through core —
    # which is where the internet check had to move to, since hostd is forbidden
    # from opening a socket. Reading it needs a session.
    ssh(vm, ["curl", "--silent", "--insecure",
             "-c", "/tmp/net-cookies", "-b", "/tmp/net-cookies",
             "-H", "Content-Type: application/json",
             "-d", json.dumps({"username": "alex", "password": NET_PASSWORD}),
             "https://127.0.0.1/api/v1/setup"], check=False)
    signed_in = ssh(vm, ["curl", "--silent", "--insecure",
                         "-c", "/tmp/net-cookies", "-b", "/tmp/net-cookies",
                         "-o", "/dev/null", "-w", "%{http_code}",
                         "https://127.0.0.1/api/v1/network"], check=False).stdout.strip()
    check(signed_in == "200", f"and the network status is readable ({signed_in})")


def verify_ports(vm: VM) -> None:
    step("What the server listens on")

    listening = ssh(vm, ["sudo", "ss", "-tlnp"], check=False).stdout
    check(":443" in listening, "HTTPS on 443",
          "The address people are given has no port number in it, which means "
          "core has to bind the ordinary one.\n" + listening[-400:])
    check(":80" in listening, "and plain HTTP on 80, to redirect",
          listening[-400:])

    # Bound to a privileged port while running as nobody in particular. That
    # pairing is the point: one narrow capability rather than a root process.
    user = ssh(vm, ["ps", "-o", "user=", "-C", "core"], check=False).stdout.strip()
    check(user == "homebase", f"while running as {user or '<not running>'}",
          "Binding 443 must not have cost the privilege boundary.")


def verify_reachable_by_name(client: VM) -> None:
    """The exit condition, from the machine that is not the server."""
    step("Finding the server by name, from another machine")

    result = apt(client, "install -y -qq avahi-utils curl", timeout=600)
    if result.returncode != 0:
        raise TestFailure("installing avahi-utils failed\n" + result.stdout[-400:])

    # Resolution first, separately from fetching anything: if this fails, the
    # name is the problem, and a failed download would not say which half broke.
    resolved = ""
    for _ in range(20):
        time.sleep(3)
        out = ssh(client, ["avahi-resolve", "-4", "-n", f"{SERVER}.local"],
                  check=False).stdout.strip()
        if out:
            resolved = out
            break

    check(bool(resolved),
          f"{SERVER}.local resolves from the other machine ({resolved})",
          "Nothing answered for the name. Without this, the address printed on "
          "the server's own screen is the only way anybody can reach it.")
    check("10.7.0." in resolved,
          "and resolves to its address on the shared network",
          f"got: {resolved}")

    # Then the dashboard itself, by name, over HTTPS.
    #
    # --insecure is the "proceed once" a person clicks: the certificate is the
    # server's own and this machine has not been told to trust it. What the
    # fingerprint on the server's screen is for.
    page = ssh(client, ["curl", "--silent", "--show-error", "--insecure",
                        "--max-time", "20", f"https://{SERVER}.local/"], check=False)
    check("Homebase" in page.stdout,
          f"and the dashboard loads at https://{SERVER}.local",
          (page.stdout + page.stderr)[-400:])

    # The certificate has to be valid for the name people actually type, or the
    # browser warning becomes a name-mismatch every time rather than once.
    names = ssh(client, ["sh", "-c",
                         f"echo | openssl s_client -connect {SERVER}.local:443 2>/dev/null "
                         f"| openssl x509 -noout -text | grep -A1 'Subject Alternative'"],
                check=False).stdout
    check(f"DNS:{SERVER}.local" in names,
          "and the certificate is valid for that name",
          f"subject alternative names: {names.strip()[:200]}")

    redirect = ssh(client, ["curl", "--silent", "--output", "/dev/null", "--max-time", "15",
                            "--write-out", "%{http_code} %{redirect_url}",
                            f"http://{SERVER}.local/"], check=False).stdout.strip()
    check(redirect.startswith("307") and "https://" in redirect,
          f"and plain HTTP redirects to it ({redirect})",
          "Somebody who types the name without https:// must not reach nothing.")


def verify_honest_when_the_internet_is_gone(vm: VM) -> None:
    """A server with no internet is not a broken server, and must not say so."""
    step("What it says when the internet is gone")

    # First, that it says so when the internet *is* there.
    #
    # This assertion did not exist, and its absence is why the internet check
    # went four milestones without working: the only thing checked was that
    # `online` is false once the interface is down, which was true for a reason
    # that had nothing to do with the interface. A check that can only ever
    # answer "no" passes every test that only asks when the answer should be no.
    before = network_status(vm)
    check(before["online"] is True,
          "before anything is unplugged, it knows the internet is reachable",
          f"online={before.get('online')} — this VM has NAT'd internet access "
          f"and can reach 1.1.1.1. If this fails, the check cannot answer "
          f"'yes' at all, which is exactly the bug this assertion exists for.")

    # The NAT interface is how this machine reaches the world. Taking it down
    # leaves the shared segment up, which is exactly the shape of a household
    # whose broadband has failed: the network is fine, the internet is not.
    nat = sorted(ssh(vm, ["sh", "-c", "ls /sys/class/net | grep -v lo"],
                     check=False).stdout.split())[0]
    ssh(vm, ["sudo", "ip", "link", "set", nat, "down"])
    try:
        time.sleep(3)
        status = network_status(vm)

        check(status["online"] is False,
              "it knows the internet is not reachable",
              f"online={status['online']}")
        check(status["reachable"] is True,
              "and knows it is still on a network itself",
              "This is the distinction the whole screen exists for: a server "
              "somebody can still reach, on a network with no way out.\n"
              f"interfaces: {status['interfaces']}")
    finally:
        ssh(vm, ["sudo", "ip", "link", "set", nat, "up"], check=False)
        time.sleep(5)


def network_status(vm: VM) -> dict:
    """Ask core, the way anything real does.

    This asked hostd directly over its socket, and that is how the internet check
    went four milestones without working. hostd's unit forbids it AF_INET, so it
    could never dial anything and always answered `online: false` — and the only
    assertion here was that `online` is false, after the interface was taken
    down. It passed for the wrong reason every time.

    The check lives in core now, because reaching 1.1.1.1 needs no privilege.
    Asking core is also simply more honest: it is the surface everything uses.
    """
    out = ssh(vm, ["curl", "--silent", "--insecure",
                   "-c", "/tmp/net-cookies", "-b", "/tmp/net-cookies",
                   "https://127.0.0.1/api/v1/network"], check=False).stdout
    try:
        return json.loads(out)
    except json.JSONDecodeError as exc:
        raise TestFailure(f"could not read /network: {exc}\n{out[:400]}") from exc


def main() -> int:
    started = time.time()
    server: VM | None = None
    client: VM | None = None

    print()
    step("Homebase on a network with another machine on it")
    info("one server, one laptop, one shared segment — and the laptop has to")
    info("find the server by name, the way a person's phone would")
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

        step("Putting both machines on one network")
        give_it_an_address(server, 10)
        give_it_an_address(client, 11)

        install_server(server, packages)
        verify_ports(server)
        verify_reachable_by_name(client)
        verify_honest_when_the_internet_is_gone(server)

        elapsed = int(time.time() - started)
        print()
        ok(f"PASS — a server is reachable by name from another machine, and is "
           f"honest when the internet is not ({elapsed}s)")
        return 0

    except (TestFailure, VMError) as exc:
        print()
        fail("FAIL", str(exc))
        if isinstance(exc, VMError) and exc.hint:
            info(exc.hint)
        for vm in (server, client):
            if vm is not None:
                collect_logs(vm)
        return 1

    finally:
        for vm in (server, client):
            if vm is not None:
                destroy(vm.name)


if __name__ == "__main__":
    sys.exit(main())
