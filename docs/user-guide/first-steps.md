# First steps

You have installed Homebase, opened it in a browser, created your account and written down
your recovery code. The server works. It just does not do anything yet.

The dashboard shows a short list of what is worth doing next. Nothing on it is compulsory,
nothing has to be done in order, and it stops mentioning each thing once you have done it.

## Give your server a name

Every Homebase starts out called **homebase**. That is fine until you have two of them, or
until you are trying to remember which machine in the house you are looking at.

Under **This server**, either in the getting-started list or with **Rename this server**
lower down, type a name and choose **Use this name**. The list stops offering it once the
server has a name of its own; the button under **This server** stays for ever, so you can
change your mind years later.

Letters, digits and hyphens only — no spaces, no dots, no accents. Something like
`living-room`, `bookshelf` or `attic`. It is what the machine calls itself, not a full
address.

Two things change when you rename it:

- **The address may change.** If you reach the server by name rather than by number, use the
  new one. The address made of numbers keeps working either way, and the machine's own
  screen always shows the current one.
- **Restarting asks for the new name.** Homebase makes you type the server's name before it
  restarts, so that a click by accident cannot take it down. That question now expects the
  name you just chose.

Nothing is lost by renaming, and you can do it again whenever you like.

## Set up a disk for your files

The disk Homebase runs from is the one inside the machine, and it holds the system. It is
usually small, and it is not where photographs and films belong.

Plug in a USB disk and open **Storage**. Homebase will offer to prepare it, tell you what is
on it first, and make you type the disk's name before erasing anything.

See [Storage](storage.md).

## Install something

A server with no applications is a computer making a noise in a cupboard. Open
**Applications** and install one — File Browser is a good first choice, because it does one
obvious thing and you can see it working.

See [Applications](applications.md).

## Make a backup

**Nothing on this server is backed up until you do this.**

You need a second disk, separate from the one holding your files — Homebase will not let you
put a backup on the same disk as the thing it is backing up, because a disk that fails takes
everything on it.

See [Backup and restore](backup.md).

## Switching it off, and restarting it

At the bottom of **This server** there are two buttons, and both make you type the server's
name first — a click by accident should not be able to take a machine down.

**Restart** takes a minute or two and the server comes back by itself. The page waits and
tells you when it has.

**Switch off** does not come back. Before you confirm, Homebase says how you would switch it
on again: pressing the power button on the machine itself, always, and — if waking over the
network is enabled on this server — a command you can run from any other computer in the
house.

It is worth reading that line before you press the button rather than after. Once the machine
is off, every page in Homebase is off with it, including the one that would have told you.

Waking over the network needs three things: it enabled in the machine's BIOS, the network
card set to listen, and the machine left plugged in. `homebasectl network` reports the middle
one. A laptop running on its battery has nothing listening once it is off.

## Updates

There is no update mechanism yet. Homebase does not check for new versions, and will not
install them by itself. That arrives in a later version, and this page will say so when it
does.

## Hiding the list

Choose **Hide this** and it will not come back. Everything on it is reachable from the
sections themselves, so nothing is lost — and if you never want a backup, you should not be
asked about it for ever.
