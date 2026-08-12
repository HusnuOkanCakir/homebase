#!/usr/bin/env python3
"""Join a wireless network, and fail to join one, without losing the machine.

Wi-Fi was held out of Milestone 7 for one reason: **the failure mode is a server
that can no longer be reached to fix it**, and there was no wireless hardware to
get it wrong on. This is that hardware, simulated well enough to be worth
trusting.

`mac80211_hwsim` is the kernel's simulated wireless driver. It creates real
`wlanN` interfaces on the real `mac80211` stack, so `wpa_supplicant`, `netplan`
and `iw` all behave exactly as they would on a card — association, four-way
handshake, DHCP, and failure. Two radios are created: one runs `hostapd` as the
house router, the other is the server's.

What that does not cover is the half Milestone 9 still owes: driver quirks,
firmware that needs loading, cards that vanish on resume, and roaming between
access points. Those need a laptop. What it does cover is every line of
Homebase's own code, and the three claims that matter:

  - a wrong password does not change anything, and says so
  - the Ethernet connection is never touched, so the dashboard survives both
  - the passphrase never comes back out

Run with `make vm-test-wifi`.
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
VM_NAME = "homebase-wifi"

SSID = "Homebase Test Network"
PASSPHRASE = "correct-horse-battery"
WRONG_PASSPHRASE = "this-is-not-the-password"

# The network the simulated router hands out. Deliberately not 10.0.2.x, which
# is what QEMU's user-mode networking uses for the wired interface — the whole
# point is being able to tell the two apart.
AP_ADDRESS = "192.168.77.1"
AP_RANGE = "192.168.77.10,192.168.77.50"


class TestFailure(Exception):
    pass


def check(condition: bool, description: str, detail: str = "") -> None:
    if condition:
        ok(description)
    else:
        raise TestFailure(f"{description}\n    {detail}" if detail else description)


def op(vm: VM, name: str, params: dict | None = None, confirmed: bool = False,
       timeout: int = 120) -> tuple[int, dict]:
    cmd = ["sudo", "curl", "--silent", "--max-time", str(timeout),
           "--unix-socket", "/run/homebase/hostd.sock",
           "-w", "\\n%{http_code}", "-X", "POST",
           "-H", "Content-Type: application/json"]
    if confirmed:
        cmd += ["-H", "X-Homebase-Confirmed: true"]
    cmd += ["-d", json.dumps(params or {}), f"http://localhost/v1/op/{name}"]

    out = ssh(vm, cmd, check=False, timeout=timeout + 60).stdout.strip()
    if not out:
        raise TestFailure(f"{name} returned nothing")
    parts = out.rsplit("\n", 1)
    status = int(parts[1]) if len(parts) == 2 else int(out)
    body = parts[0] if len(parts) == 2 else ""
    return status, (json.loads(body) if body else {})


def must(vm: VM, name: str, params: dict | None = None, confirmed: bool = False,
         timeout: int = 120) -> dict:
    status, body = op(vm, name, params, confirmed, timeout)
    if status != 200:
        raise TestFailure(f"{name} returned {status}\n    {json.dumps(body, indent=4)}")
    return body


# --- The machine ------------------------------------------------------------------


def build_hostd() -> Path:
    step("Building hostd")
    out = REPO_ROOT / "bin" / "hostd"
    out.parent.mkdir(exist_ok=True)
    result = subprocess.run(
        ["go", "build", "-o", str(out), "./cmd/hostd"],
        cwd=REPO_ROOT, capture_output=True, text=True,
        env={**__import__("os").environ, "CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64"})
    if result.returncode != 0:
        raise VMError("building hostd failed:\n" + result.stderr[-600:])
    ok(f"hostd ({out.stat().st_size // 1024} KB)")
    return out


def install_homebase(vm: VM, binary: Path) -> None:
    step("Installing Homebase")

    ssh(vm, ["sudo", "groupadd", "--system", "--force", "homebase"])
    ssh(vm, ["sudo", "useradd", "--system", "--gid", "homebase",
             "--home-dir", "/var/lib/homebase", "--shell", "/usr/sbin/nologin",
             "homebase"], check=False)
    ssh(vm, ["sudo", "mkdir", "-p", "/usr/libexec/homebase", "/etc/homebase",
             "/var/lib/homebase", "/srv/homebase", "/var/log/homebase",
             "/usr/share/homebase/apps"])
    ssh(vm, ["sudo", "chown", "root:homebase", "/etc/homebase"])
    ssh(vm, ["sudo", "chown", "homebase:homebase", "/var/lib/homebase"])

    copy_to(vm, binary, "/usr/libexec/homebase/hostd", mode="0755")
    for unit in ("homebase-hostd.service", "homebase-hostd.socket"):
        write_file(vm, f"/etc/systemd/system/{unit}",
                   (REPO_ROOT / "packaging" / "systemd" / unit).read_text())

    ssh(vm, ["sudo", "systemctl", "daemon-reload"])
    ssh(vm, ["sudo", "systemctl", "enable", "--now", "homebase-hostd.socket"])

    status, _ = op(vm, "system.get_info", timeout=30)
    if status != 200:
        journal = ssh(vm, ["sudo", "journalctl", "-u", "homebase-hostd",
                           "--no-pager", "-n", "20"], check=False).stdout
        raise TestFailure(f"hostd did not start\n{journal}")
    ok("hostd is answering")


def create_wireless_hardware(vm: VM) -> None:
    """Two simulated radios, and a router on one of them."""
    step("Giving the machine a wireless card")

    result = apt(vm, "install -y -qq iw wpasupplicant hostapd dnsmasq "
                     f"linux-modules-extra-$(uname -r)", timeout=1200)
    if result.returncode != 0:
        raise TestFailure("installing the wireless tools failed\n"
                          + (result.stdout + result.stderr)[-500:])

    # dnsmasq and hostapd start themselves on install and would fight for the
    # interface before it is configured.
    for unit in ("hostapd", "dnsmasq"):
        ssh(vm, ["sudo", "systemctl", "stop", unit], check=False)
        ssh(vm, ["sudo", "systemctl", "disable", unit], check=False)

    loaded = ssh(vm, ["sudo", "modprobe", "mac80211_hwsim", "radios=2"], check=False)
    check(loaded.returncode == 0, "two simulated radios exist",
          (loaded.stdout + loaded.stderr)[-300:])

    wireless = ssh(vm, ["sh", "-c",
                        "ls -d /sys/class/net/*/wireless 2>/dev/null | wc -l"]).stdout.strip()
    check(wireless == "2", f"the kernel reports two wireless interfaces ({wireless})")

    # netplan must not try to manage the access point's radio; it is the router
    # in this story, not part of the server.
    write_file(vm, "/etc/systemd/network/10-ignore-ap.network",
               "[Match]\nName=wlan1\n\n[Link]\nUnmanaged=yes\n")

    step("Starting a wireless router")

    # WPA2-PSK, which is what a home router runs. The whole handshake happens
    # for real; only the radio is simulated.
    write_file(vm, "/etc/hostapd/homebase-test.conf",
               f"interface=wlan1\n"
               f"driver=nl80211\n"
               f"ssid={SSID}\n"
               f"hw_mode=g\n"
               f"channel=6\n"
               f"wpa=2\n"
               f"wpa_passphrase={PASSPHRASE}\n"
               f"wpa_key_mgmt=WPA-PSK\n"
               f"rsn_pairwise=CCMP\n")

    ssh(vm, ["sudo", "ip", "addr", "flush", "dev", "wlan1"], check=False)
    ssh(vm, ["sudo", "ip", "addr", "add", f"{AP_ADDRESS}/24", "dev", "wlan1"])
    ssh(vm, ["sudo", "ip", "link", "set", "wlan1", "up"])

    ssh(vm, ["sudo", "sh", "-c",
             "nohup hostapd -B /etc/hostapd/homebase-test.conf "
             ">/var/log/hostapd-test.log 2>&1 || true"], check=False)
    time.sleep(3)

    running = ssh(vm, ["pgrep", "-x", "hostapd"], check=False)
    if running.returncode != 0:
        log = ssh(vm, ["sudo", "cat", "/var/log/hostapd-test.log"], check=False).stdout
        raise TestFailure(f"the simulated router did not start\n{log[-800:]}")
    ok(f"broadcasting {SSID!r} with WPA2")

    # Something has to hand out addresses, or the server associates and then has
    # no address — which is a real failure mode, and not the one being tested
    # here.
    ssh(vm, ["sudo", "sh", "-c",
             f"nohup dnsmasq --interface=wlan1 --bind-interfaces "
             f"--dhcp-range={AP_RANGE},12h --except-interface=lo "
             f"--no-resolv --port=0 >/var/log/dnsmasq-test.log 2>&1 || true"],
        check=False)
    time.sleep(2)
    check(ssh(vm, ["pgrep", "-x", "dnsmasq"], check=False).returncode == 0,
          "and handing out addresses")


# --- What is being tested ----------------------------------------------------------


def verify_the_card_is_seen(vm: VM) -> None:
    step("What Homebase says about the wireless")

    status = must(vm, "network.wifi_status")
    check(status.get("available") is True,
          f"it knows there is a wireless card ({status.get('interface')})",
          json.dumps(status, indent=4))
    check(status.get("connected") is False, "and that it has not joined anything")
    check(status.get("configured") is False, "and that nothing is configured")

    # The field the screen uses to decide how frightening to be.
    check(status.get("has_wired_connection") is True,
          "and that a cable is carrying the connection",
          "Without this the dashboard would warn about being stranded on a "
          "machine that is plugged in.")


def verify_scanning(vm: VM) -> None:
    step("Looking for networks")

    result = must(vm, "network.wifi_scan", timeout=120)
    networks = result.get("networks", [])
    names = [n.get("ssid") for n in networks]
    check(SSID in names, f"the router is found ({names})", json.dumps(result, indent=4))

    found = next(n for n in networks if n["ssid"] == SSID)
    check(found.get("security") == "wpa",
          f"and is reported as needing a password ({found.get('security')})")
    check(0 <= found.get("bars", -1) <= 4, f"with a signal strength ({found.get('bars')} bars)")
    check(found.get("current") is False, "and is not the one we are on")


def verify_a_wrong_password_changes_nothing(vm: VM) -> None:
    """The claim the whole design rests on.

    A wrong password is the ordinary mistake, and on this screen the ordinary
    mistake must not cost somebody their server. hostd puts the previous
    configuration back and re-applies it *before* answering, so by the time the
    error arrives there is nothing left to undo.
    """
    step("Getting the password wrong")

    before = ssh(vm, ["ip", "-4", "-brief", "addr", "show", "enp0s1"]).stdout.strip()

    status, body = op(vm, "network.wifi_connect",
                      {"ssid": SSID, "passphrase": WRONG_PASSPHRASE},
                      confirmed=True, timeout=240)
    check(status >= 400, f"joining is refused ({status})", json.dumps(body, indent=4))

    error = body.get("error", body)

    # The code, and then the *reason* — because the first run of this test passed
    # while never touching a password. /etc/netplan was read-only for hostd, the
    # write failed, and every assertion here still held: it was refused, nothing
    # changed, no file was left behind. All true, all irrelevant. So the detail
    # has to show the failure came from the network rather than from the machine.
    check(error.get("code") == "wifi.did_not_join",
          f"with a code that says what happened ({error.get('code')})",
          json.dumps(body, indent=4))
    check("did not join" in (error.get("detail") or "").lower(),
          "and it failed at joining rather than at writing the settings",
          f"detail was: {error.get('detail')!r}\n    "
          "A configuration that cannot be written is a different fault and has "
          "its own code — passing here on that one would prove nothing about "
          "passwords.")
    check("nothing has changed" in (error.get("recovery") or "").lower(),
          "and a message that says nothing changed",
          f"recovery was: {error.get('recovery')!r}")

    # The claim, checked rather than trusted.
    after = ssh(vm, ["ip", "-4", "-brief", "addr", "show", "enp0s1"]).stdout.strip()
    check(before == after,
          "and the wired connection is untouched",
          f"before: {before!r}\n    after: {after!r}")

    left = ssh(vm, ["sudo", "test", "-f", "/etc/netplan/90-homebase-wifi.yaml"],
               check=False)
    check(left.returncode != 0,
          "and no wireless configuration was left behind",
          "A failed attempt that leaves its configuration in place would join "
          "the wrong network on the next reboot.")

    status = must(vm, "network.wifi_status")
    check(status.get("configured") is False, "and Homebase agrees nothing is configured")


def verify_joining(vm: VM) -> None:
    step("Joining the network")

    joined = must(vm, "network.wifi_connect",
                  {"ssid": SSID, "passphrase": PASSPHRASE},
                  confirmed=True, timeout=240)

    check(joined.get("connected") is True, "the server joined", json.dumps(joined, indent=4))
    check(joined.get("ssid") == SSID, f"the network it asked for ({joined.get('ssid')})")

    addresses = joined.get("addresses") or []
    check(any(a.startswith("192.168.77.") for a in addresses),
          f"and was given an address on it ({addresses})",
          "Associating without an address is a real failure mode and must not "
          "be reported as success.")

    # The cable still wins. A machine with both should send everything down the
    # wire — it is faster, and it is the one that was already working.
    routes = ssh(vm, ["ip", "-4", "route", "show", "default"]).stdout
    check("enp0s1" in routes, f"and the cable is still the default route\n    {routes.strip()}")

    wired_metric, wireless_metric = route_metrics(routes)
    check(wireless_metric is None or wired_metric is None or wireless_metric > wired_metric,
          f"with wireless a worse route than the cable "
          f"({wired_metric} vs {wireless_metric})",
          routes.strip())


def route_metrics(routes: str) -> tuple[int | None, int | None]:
    wired = wireless = None
    for line in routes.splitlines():
        fields = line.split()
        if "metric" not in fields:
            continue
        metric = int(fields[fields.index("metric") + 1])
        if "enp0s1" in fields:
            wired = metric
        elif "wlan0" in fields:
            wireless = metric
    return wired, wireless


def verify_the_password_is_not_readable(vm: VM) -> None:
    """A password that can be read back is one that ends up somewhere."""
    step("Where the password went")

    mode = ssh(vm, ["sudo", "stat", "-c", "%U:%G %a",
                    "/etc/netplan/90-homebase-wifi.yaml"]).stdout.strip()
    check(mode == "root:root 600",
          f"the file holding it is readable only by root ({mode})",
          "It contains somebody's Wi-Fi password in plain text.")

    # Not through any operation, which is the part that matters: hostd is the
    # only thing that has it, and it does not hand it back.
    for name in ("network.wifi_status", "network.wifi_scan"):
        body = json.dumps(must(vm, name, timeout=120))
        check(PASSPHRASE not in body, f"{name} does not return it")

    # And not in the audit log, which records every privileged call.
    audit = ssh(vm, ["sudo", "cat", "/var/log/homebase/audit.log"], check=False).stdout
    check(PASSPHRASE not in audit,
          "and it is not in the audit log",
          "Every privileged call is recorded there, including this one.")

    # A diagnostic file is meant to be sent to a stranger.
    diagnostics = must(vm, "system.diagnostics", timeout=200)
    contents = ssh(vm, ["sudo", "cat", diagnostics["path"]], check=False).stdout
    check(PASSPHRASE not in contents,
          "and not in a diagnostic file either")


def verify_the_file_is_what_netplan_reads(vm: VM) -> None:
    """The JSON-as-YAML trick, checked against netplan rather than assumed."""
    step("What netplan makes of it")

    result = ssh(vm, ["sudo", "netplan", "get", "network.wifis"], check=False)
    check(result.returncode == 0, "netplan reads the file Homebase wrote",
          (result.stdout + result.stderr)[-400:])
    check(SSID in result.stdout,
          f"and finds the network in it\n    {result.stdout.strip()[:300]}")


def verify_an_awkward_name_cannot_break_the_file(vm: VM) -> None:
    """The reason the file is produced by an encoder rather than by formatting.

    A network name is somebody else's string. If it were pasted into YAML it
    could end the value and start a new key — and this file decides what the
    machine connects to.
    """
    step("A network name with punctuation in it")

    awkward = 'a: "b"\nc: d'
    status, body = op(vm, "network.wifi_connect",
                      {"ssid": awkward, "passphrase": PASSPHRASE},
                      confirmed=True, timeout=240)
    check(status >= 400, f"it is refused ({status})", json.dumps(body, indent=4))

    error = body.get("error", body)
    check(error.get("code") == "wifi.invalid_request",
          f"as an invalid request rather than a failed join ({error.get('code')})",
          "A newline in an SSID is rejected before anything is written, not "
          "discovered afterwards.")

    # And the real network is still joined: a refused request must not have
    # disturbed the working one.
    status = must(vm, "network.wifi_status")
    check(status.get("ssid") == SSID,
          f"and the server is still on {SSID} ({status.get('ssid')})")


def verify_it_survives_a_reboot(vm: VM) -> None:
    step("After a restart")

    ssh(vm, ["sudo", "systemctl", "reboot"], check=False)
    time.sleep(5)
    wait_for_ssh(vm)
    wait_for_boot_complete(vm)

    # The simulated radios and the router do not survive a reboot — they are the
    # house, not the server. What has to survive is Homebase's configuration.
    left = ssh(vm, ["sudo", "cat", "/etc/netplan/90-homebase-wifi.yaml"],
               check=False).stdout
    check(SSID in left, "the network is still configured after a restart")

    # And the machine still came up on the network, which is the property
    # `optional: true` exists for: a server carried out of range must still boot.
    ssh(vm, ["sudo", "systemctl", "start", "homebase-hostd.socket"], check=False)
    status = must(vm, "network.wifi_status", timeout=60)
    check(status.get("configured") is True, "Homebase still reports it as configured")
    check(status.get("has_wired_connection") is True,
          "and the machine came back on the network without waiting for wireless",
          "netplan's `optional: true` is what stops a boot hanging for two "
          "minutes on a network that is not in range.")


def verify_forgetting(vm: VM) -> None:
    step("Turning wireless off")

    status = must(vm, "network.wifi_forget", confirmed=True, timeout=150)
    check(status.get("configured") is False, "wireless is no longer configured",
          json.dumps(status, indent=4))

    gone = ssh(vm, ["sudo", "test", "-f", "/etc/netplan/90-homebase-wifi.yaml"],
               check=False)
    check(gone.returncode != 0, "and the file is gone")

    reachable = ssh(vm, ["ip", "-4", "-brief", "addr", "show", "enp0s1"]).stdout
    check("192.168." in reachable or "10.0." in reachable,
          f"and the machine is still reachable over the cable\n    {reachable.strip()}")


def verify_a_machine_with_no_wireless_says_so(vm: VM) -> None:
    """Most of the hardware Homebase runs on has no wireless at all.

    "No networks in range" and "this machine cannot see networks" send somebody
    to entirely different places — one to move the router, one to buy a cable.
    """
    step("A machine with no wireless card")

    ssh(vm, ["sudo", "pkill", "-x", "hostapd"], check=False)
    ssh(vm, ["sudo", "pkill", "-x", "dnsmasq"], check=False)
    unloaded = ssh(vm, ["sudo", "modprobe", "-r", "mac80211_hwsim"], check=False)
    if unloaded.returncode != 0:
        info("could not unload the simulated radios; skipping")
        return
    time.sleep(2)

    status = must(vm, "network.wifi_status")
    check(status.get("available") is False,
          "Homebase reports no wireless", json.dumps(status, indent=4))

    code, body = op(vm, "network.wifi_scan", timeout=60)
    error = body.get("error", body)
    check(code == 404, f"and scanning says so rather than returning nothing ({code})",
          json.dumps(body, indent=4))
    check(error.get("code") == "wifi.no_adapter",
          f"with its own code ({error.get('code')})")
    check("cable" in (error.get("recovery") or "").lower(),
          "and tells somebody what to do instead",
          f"recovery was: {error.get('recovery')!r}")


# --- main -------------------------------------------------------------------------


def main() -> int:
    started = time.time()
    vm: VM | None = None

    print()
    step("Homebase on a wireless network")
    info("simulated radios, a real WPA2 handshake, and a wrong password that")
    info("must not cost anybody their server")
    print()

    try:
        binary = build_hostd()

        vm = create(VM_NAME, force=True)
        start(vm)
        wait_for_ssh(vm)
        wait_for_boot_complete(vm)

        apt(vm, "update -qq", timeout=600)
        create_wireless_hardware(vm)
        install_homebase(vm, binary)

        verify_the_card_is_seen(vm)
        verify_scanning(vm)
        verify_a_wrong_password_changes_nothing(vm)
        verify_joining(vm)
        verify_the_password_is_not_readable(vm)
        verify_the_file_is_what_netplan_reads(vm)
        verify_an_awkward_name_cannot_break_the_file(vm)
        verify_it_survives_a_reboot(vm)
        verify_forgetting(vm)
        verify_a_machine_with_no_wireless_says_so(vm)

        elapsed = int(time.time() - started)
        print()
        ok(f"PASS — a server joins a wireless network, refuses a wrong password "
           f"without changing anything, and keeps its cable throughout ({elapsed}s)")
        return 0

    except (TestFailure, VMError) as error:
        print()
        fail(str(error))
        if vm:
            try:
                for unit in ("homebase-hostd",):
                    journal = ssh(vm, ["sudo", "journalctl", "-u", unit,
                                       "--no-pager", "-n", "25"], check=False).stdout
                    if journal.strip():
                        print(f"\n  --- {unit} ---")
                        for line in journal.strip().splitlines()[-15:]:
                            print(f"  {line}")
                supplicant = ssh(vm, ["sudo", "journalctl", "-u",
                                      "wpa_supplicant*", "--no-pager", "-n", "20"],
                                 check=False).stdout
                if supplicant.strip():
                    print("\n  --- wpa_supplicant ---")
                    for line in supplicant.strip().splitlines()[-15:]:
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
        if vm:
            print()
            destroy(VM_NAME)


if __name__ == "__main__":
    sys.exit(main())
