# controller/ — installer USB creator

The application a user runs **on their existing computer** to write Homebase installation
media to a USB stick. Lands in Milestone 6.

Two stages:

1. **CLI first** — `homebasectl installer create --output /dev/sdb`, so the logic can be
   tested before any UI exists.
2. **Graphical wrapper** — a small Tauri application around the same code, because the
   target user is someone who has never opened a terminal.

Runs on Windows, macOS and Linux; this is the only Homebase component that is not Linux-only.

It writes directly to a block device, so it inherits the installer's caution: enumerate all
removable devices, show size and model, refuse system disks, and require explicit
confirmation naming the device being erased.
