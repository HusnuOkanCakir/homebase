# Storage

Your server has its own disk, and that is where applications keep their settings. When you
want somewhere bigger for films, photographs or backups, you plug in another disk and tell
Homebase to use it.

!!! warning "Pre-alpha"

    Homebase can [back up to another disk](backup.md), but not on a schedule — you have to
    make the backup. Until you have, do not put anything on this server that you do not have
    another copy of.

## Adding a disk

Plug it in, then open **Storage**. It appears under "Disks in this server".

What happens next depends on what is already on it.

**If it has files on it already** — from a Windows machine, a camera, an old backup —
Homebase can use it as it is. Press **Use this**, give it a name, and Homebase will keep it
available and reconnect it automatically every time the server starts. Nothing on it is
changed.

**If it is empty, or Homebase cannot use what is on it** — press **Erase and prepare**.
This deletes everything on the disk. Homebase will tell you what it found before you
confirm, and asks you to type the disk's identifier rather than clicking a second button,
because this is one of the few things Homebase cannot undo.

If you are not certain what is on a disk, check it on another computer first. Homebase will
tell you what kind of disk it is, but it cannot tell you whether the files on it matter to
you.

### Why Homebase asks you to type things

Twice on this page you are asked to type an identifier rather than press a button. It is not
bureaucracy. Both actions are ones where doing it to the wrong disk destroys something, and
typing the name is what makes you read which disk you picked.

Homebase also never picks a disk for you, even when there is only one it could be.

## The disk Homebase runs from

It is shown, and it has no buttons. Homebase will not erase the disk it is running from,
under any circumstances, whatever you type.

## Giving a disk to an application

Open the application, and look for **Where this keeps your files**. Choose one of your set-up
disks.

The change takes effect the next time the application starts. **Anything it has already
saved stays where it was** — Homebase does not move your files behind your back. If you want
them on the new disk, copy them across yourself.

Some applications will not run at all until you have done this. A media server with nowhere
to keep media has nothing to do, and Homebase would rather say so than start something that
appears broken.

## Unplugging a disk

Press **Prepare to unplug** first. Homebase finishes writing anything outstanding and tells
you when it is safe. Stop any application using the disk before you do this — Homebase will
refuse rather than pull the disk out from underneath something.

**If you unplug it without asking first**, Homebase copes: it notices the disk has gone,
says so, and refuses to write anything to where it used to be. Applications using it will
stop, and start again by themselves once you plug it back in.

It is still better to ask first, and the reason is worth knowing. When something saves a
file, the computer does not necessarily write it to the disk straight away — it can sit in
memory for a few seconds first, which is what makes computers feel fast. Pulling the plug in
those few seconds loses it, and nothing can prevent that. **Prepare to unplug** exists to
make sure everything has actually reached the disk before you pull it out.

Anything written more than a moment ago is safe either way.

## When a disk is not connected

The disk is listed as **Not connected**. That is a statement about the disk, not a fault —
plug it back in and Homebase picks it up on its own.

Applications that keep their files on it will not start until it is back. They will tell you
which disk they are waiting for.

## Plugging it into a different port

It does not matter. Homebase recognises a disk by something written inside the disk itself,
not by which socket it is in or what order you plugged things in. Move it to another port,
plug three other things in first, restart the server — it is still the same disk to
Homebase.

The one thing that changes a disk's identity is erasing it. That is deliberate: a disk that
has been erased is not the disk that had your films on it, and Homebase should not pretend
otherwise.

## Stopping using a disk

**Stop using it** removes it from Homebase. **Everything on the disk is left exactly as it
is** — you can plug it into another computer and all your files are there.

Applications keeping their files on that disk will stop working until you give them
somewhere else.

## What Homebase does not do yet

- Backups exist now — see [Backup and restore](backup.md) — but they are not automatic.
  You have to make them.
- **Warnings when a disk is nearly full** arrive as events in the history, not yet as
  anything on screen.
- **Nothing spanning several disks.** No pooling, no mirroring, no RAID. One disk is one
  place.
- **Encrypted disks cannot be opened.** Homebase will tell you a disk is encrypted rather
  than describing it as empty, but it cannot unlock it.
