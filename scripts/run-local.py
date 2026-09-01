#!/usr/bin/env python3
"""Run Homebase on this machine, for looking at.

Starts hostd and core against a scratch directory under ./run/, serves the built
dashboard, and prints the address. Ctrl-C stops both.

This is not how Homebase is deployed. It runs both services as you rather than
as root and the homebase account, so the privilege boundary is not the one a
real installation has — see `make vm-test-packages` for that, which installs the
Debian packages on a clean machine and checks the boundary properly.

What it is for is seeing the thing work, and having somewhere to click while
changing it.

Run with `make run`.
"""

from __future__ import annotations

import argparse
import getpass
import os
import shutil
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
RUN_DIR = REPO_ROOT / "run"


def colour(code: str, text: str) -> str:
    return text if os.environ.get("NO_COLOR") else f"\033[{code}m{text}\033[0m"


def step(msg: str) -> None:
    print(colour("1;34", f"==> {msg}"), flush=True)


def ok(msg: str) -> None:
    print(colour("32", f"  ✓ {msg}"), flush=True)


def warn(msg: str) -> None:
    print(colour("33", f"  ! {msg}"), flush=True)


def die(msg: str, hint: str = "") -> None:
    print(colour("31", f"  ✗ {msg}"), file=sys.stderr)
    if hint:
        print(f"    {hint}", file=sys.stderr)
    sys.exit(1)


def wait_for(url: str, timeout: int = 20) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=2):
                return True
        except urllib.error.HTTPError:
            # Answering at all is what we are waiting for; 503 while hostd
            # starts is still an answer.
            return True
        except OSError:
            time.sleep(0.3)
    return False


def needs_setup(url: str) -> bool:
    """Ask the server whether it has an administrator yet.

    Inspecting the database file instead looks obvious and is wrong: core
    creates and migrates it at startup, so by the time anything can be asked
    the file exists and is non-empty whether or not anybody has claimed the
    server. The server is the thing that knows.
    """
    try:
        with urllib.request.urlopen(url + "/api/v1/setup", timeout=3) as response:
            import json
            return bool(json.load(response).get("needs_setup"))
    except Exception:
        return True


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--port", type=int, default=8080)
    parser.add_argument("--fresh", action="store_true",
                        help="discard the existing state and start over")
    args = parser.parse_args()

    binaries = REPO_ROOT / "bin"
    dashboard = REPO_ROOT / "dashboard/dist"

    for path, how in [(binaries / "hostd", "make go-build"),
                      (binaries / "core", "make go-build"),
                      (dashboard / "index.html", "make dash-build")]:
        if not path.exists():
            die(f"missing {path.relative_to(REPO_ROOT)}", f"Run: {how}")

    if args.fresh and RUN_DIR.exists():
        shutil.rmtree(RUN_DIR)
        ok("previous state discarded")

    RUN_DIR.mkdir(parents=True, exist_ok=True)
    socket = RUN_DIR / "hostd.sock"
    processes: list[subprocess.Popen] = []

    def stop(*_) -> None:
        print()
        step("Stopping")
        for process in reversed(processes):
            if process.poll() is None:
                process.terminate()
        for process in reversed(processes):
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
        ok("stopped")
        sys.exit(0)

    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)

    print()
    step("Starting Homebase")

    processes.append(subprocess.Popen(
        [str(binaries / "hostd"),
         "--socket", str(socket),
         "--audit-log", str(RUN_DIR / "audit.log"),
         # As you, not as root. The account that would normally be `homebase`
         # is whoever is running this.
         "--peer-user", getpass.getuser(),
         # The repository's own catalogue, rather than the installed
         # /usr/share/homebase/apps. This is the one place where local
         # development reads a manifest a user could edit — which is exactly
         # what makes it useful for working on one, and exactly why it is not
         # how the packaged service behaves.
         "--catalogue", str(REPO_ROOT / "app-store"),
         # Application data under ./run rather than /srv/homebase, so
         # installing something here needs no root and touches nothing outside
         # the repository. The packaged service uses the real path.
         "--app-data", str(RUN_DIR / "apps"),
         # And the storage root, for the same reason and a sharper one: the
         # default is /srv/homebase, which this cannot create without root. It
         # failed, quietly, in hostd's log — so a development instance has never
         # had a storage location, which now means it has no files to browse
         # either. Anything that reads a disk was untestable here without
         # noticing why.
         "--storage-root", str(RUN_DIR / "storage"),
         "--state-dir", str(RUN_DIR / "hostd-state")],
        stdout=(RUN_DIR / "hostd.log").open("w"),
        stderr=subprocess.STDOUT,
    ))

    for _ in range(40):
        if socket.exists():
            break
        if processes[0].poll() is not None:
            die("hostd exited immediately",
                f"See {(RUN_DIR / 'hostd.log').relative_to(REPO_ROOT)}")
        time.sleep(0.1)
    ok(f"hostd on {socket.relative_to(REPO_ROOT)}")

    processes.append(subprocess.Popen(
        [str(binaries / "core"),
         "--listen", f"127.0.0.1:{args.port}",
         "--db", str(RUN_DIR / "homebase.db"),
         "--hostd-socket", str(socket),
         "--dashboard", str(dashboard)],
        stdout=(RUN_DIR / "core.log").open("w"),
        stderr=subprocess.STDOUT,
    ))

    url = f"http://127.0.0.1:{args.port}"
    if not wait_for(url + "/api/v1/setup"):
        die("core did not start", f"See {(RUN_DIR / 'core.log').relative_to(REPO_ROOT)}")
    ok(f"core on {url}")

    print()
    print(colour("1", f"  Open {url}"))
    print()
    if needs_setup(url):
        print("  It will ask you to create an administrator. Any name; the password")
        print("  needs twelve characters or more.")
    else:
        print("  Sign in with the account you created earlier.")
        print("  `make run-fresh` starts over with an empty database.")
    print()

    warn("This is a development instance, not an installation.")
    print("    Both services run as you rather than as root and the homebase")
    print("    account, so the privilege boundary is not the real one. Restarting")
    print("    is refused here for that reason — it would restart your own machine.")
    print()
    print(f"    Logs: {(RUN_DIR / 'core.log').relative_to(REPO_ROOT)}, "
          f"{(RUN_DIR / 'hostd.log').relative_to(REPO_ROOT)}")
    print("    Stop with Ctrl-C.")
    print()

    while True:
        for process in processes:
            if process.poll() is not None:
                name = "hostd" if process is processes[0] else "core"
                die(f"{name} exited unexpectedly",
                    f"See run/{name}.log")
        time.sleep(0.5)


if __name__ == "__main__":
    sys.exit(main())
