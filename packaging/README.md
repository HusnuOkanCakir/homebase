# packaging/ — distribution artifacts

Three Debian packages and the systemd units they install.

```sh
make packages                # build into dist/
make vm-test-packages        # install, upgrade, reboot and purge on a clean machine
```

| Package | Contents |
|---|---|
| `homebase-hostd` | The root service, its systemd service and socket units |
| `homebase-core` | The unprivileged service and its unit. Depends on `homebase-hostd` |
| `homebase-dashboard` | Built static assets. Architecture-independent |

## Packaging is a security boundary

The socket mode, the service account and the directory ownership are what turn the privilege
split from an architectural intention into something the kernel enforces. A change here can
undo [ADR-0006](../docs/decisions/0006-privilege-split.md) without touching a line of Go —
`0666` instead of `0660` on the socket removes the boundary entirely.

What the packages guarantee, and what the VM suite asserts on a real machine:

| | |
|---|---|
| `/run/homebase/hostd.sock` | `0660 root:homebase` — only `core` can ask for a privileged operation |
| `/etc/homebase` | `0750 root:homebase` — `core` reads its configuration and **cannot rewrite it** |
| `/var/lib/homebase` | `0750 homebase:homebase` — `core`'s own state |
| `/srv/homebase` | `0750 homebase:homebase` — user data |
| The `homebase` account | System user, no login shell, no writable home |

`make packages` refuses to produce a package that ships a setuid file, a group- or
world-writable directory, or anything outside `/usr`, `/lib` and `/etc`.

## Why not debhelper

`dpkg-deb --build` from a staged tree, driven by
[`scripts/build-packages.py`](../scripts/build-packages.py).

Not because debhelper is wrong — it is the right tool for a package with anything
complicated in it. These three have no build system to invoke at package time, no libraries
to `shlibdeps`, and nothing to compile. What they *do* have is a privilege boundary
expressed in file ownership and modes, and that is worth reading in one file rather than
inferring from a dozen debhelper defaults.

It also means packages build with nothing installed beyond `dpkg` itself, so `make packages`
does the same thing on a laptop and on a CI runner.

This is worth revisiting at Milestone 8, when signing, SBOMs and provenance arrive and the
standard toolchain starts earning its complexity.

## Upgrades must never destroy data

Every maintainer script runs on a machine that already holds somebody's photographs and
their only administrator account, and runs again on every upgrade. Two rules follow:

**Idempotent.** `set -e` plus a command that fails the second time leaves the package
half-configured, which is a state nothing has code for. The VM suite installs the same
version twice for exactly this reason.

**Never destructive.** Nothing removes user data on any path — including `purge`.

That last point is a deliberate deviation from Debian policy, which says purge should remove
everything the package created. On a machine holding the only copy of a family's
photographs, that policy loses. `purge` removes `/etc/homebase` and `/var/log/homebase`,
keeps `/var/lib/homebase` and `/srv/homebase`, and tells the user where they are and how to
remove them:

```text
Homebase has been removed, but your data has been kept:
  /var/lib/homebase   settings, accounts, application state
  /srv/homebase       your files

Delete them yourself if you are sure:
  sudo rm -rf /var/lib/homebase /srv/homebase
```

Somebody who meant to delete their data can do it in one command. Somebody who did not gets
their photographs back.

## One thing the tests caught

`homebase-core`'s postinst uses `adduser --system`, which silently adopts an account that
already exists. During testing the VM's own login user was also called `homebase`, so the
package ran the service as an interactive account with a shell and a home directory — and
said nothing.

The login user was renamed, and the postinst now warns when it adopts an account with a
login shell. It warns rather than fails: refusing would break every upgrade to guard against
a rare case.

## Milestone 8

Signing, SBOMs, build provenance, downgrade protection and the channel promotion that moves
the *same* artifact from alpha to stable rather than rebuilding it. See
[update security](../docs/security/update-security.md).
