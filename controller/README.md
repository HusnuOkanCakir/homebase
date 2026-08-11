# controller/ — the graphical stick-maker

The application a user runs **on their existing computer** to write Homebase installation
media to a USB stick. **Milestone 10 — not built yet.**

## Why this is still empty

The media logic exists and is proven. `homebasectl installer create` writes a stick that
turns a Windows-occupied laptop into a working Homebase server, and
[`tests/installer/test_install.py`](../tests/installer/test_install.py) boots that stick and
carries the whole thing through to installing an application. What is missing is the wrapper
for people who have never opened a terminal — which is, of course, everybody this product is
for.

It was split out rather than rushed, for one reason.

**This is the only part of Homebase that writes to a disk on somebody else's computer.** Not
the server's disk, which is expendable by definition — the disk of the laptop they are
sitting at, with their photographs and their tax returns on it. `--output /dev/sda` is one
keystroke from `--output /dev/sdb`.

Everything that stops that is in `refusal()`: it declines anything that is not a whole disk,
anything read-only, anything with a mounted filesystem anywhere on it, anything that is not
removable, and anything too small to be a real stick. On Linux those are `lsblk` semantics
and there is a test behind them.

Windows and macOS share none of that. `\\.\PhysicalDrive0` and `/dev/rdisk0` are different
worlds with different failure modes, and their versions of "is this the disk the computer is
running from" would have to be written from documentation rather than from a machine. A
refusal that has never been observed refusing is not a safety feature; it is a comment.

So Milestone 10 begins with somewhere to run it — a Windows and macOS lab, the way
[Milestone 1](../ROADMAP.md) built one for Linux — and the wrapper comes after.

## What it will be

A small [Tauri](https://tauri.app) application around **the same media logic**, not a second
implementation of it. One description of what goes on a stick; two would drift, and the way
that drift shows up is a machine installed from media built differently from the media the
tests use.

Three screens, and the middle one is the product:

1. Choose the drive — every removable device, with its size and model, and the refused ones
   shown *and* explained rather than hidden. Somebody who cannot see their drive needs to
   know why, or they will go looking for a tool with fewer scruples.
2. Confirm — naming the drive being erased, in the pattern the rest of Homebase uses for
   anything irreversible.
3. Write, verify, and say what to do next.

It will need signing and notarisation. An unsigned application that asks for administrator
rights in order to erase a disk is indistinguishable from malware, and a user who has been
taught to click through that warning has been taught something dangerous.

## Until then

Making a stick takes one command on a Linux machine:

```console
$ homebasectl installer devices          # what may be written to, and what may not
$ sudo homebasectl installer create --iso ubuntu.iso --packages dist/ --device /dev/sdX
```

See [Installing Homebase](../docs/user-guide/installing.md).
