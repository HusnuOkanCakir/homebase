# Sharing files with your other computers

A shared folder appears on the other computers in your house as a drive you can drag files
into. There is nothing to install at the other end: Windows, macOS, Linux, Android and iOS
all speak the protocol already.

This is also how you back a computer up **to** your server rather than the other way round.
Windows File History, Time Machine and most Linux backup tools will write to a shared
folder.

!!! warning "Pre-alpha"

    Shared folders are not encrypted in transit, and anyone on your network who has the
    password can read them. That is the same as every other home NAS, and worth knowing.

## Sharing a folder

Open **Files** in the dashboard.

1. Under **Shared folders**, type a name — `backup`, `films`, `documents` — and choose which
   disk it lives on.
2. Under **Who can open them**, add a name and a password.

That is both halves, and both are needed. A shared folder with no account is one that
refuses every attempt to open it, and Homebase will say so at the top of the page until you
have added one.

The first folder you share takes a few minutes, because it installs the file server.
Homebase does not ship it running: a service listening on your network that nobody asked for
is one that can be misconfigured without anybody noticing.

## Opening it from another computer

The **Files** page shows you exactly what to type. It differs by machine, so all three are
listed:

| | |
|---|---|
| **Windows** | `\\homebase\backup` — into the address bar of File Explorer |
| **macOS** | `smb://homebase.local/backup` — Finder, Go menu, "Connect to Server" |
| **Linux** | `smb://homebase.local/backup` — Files, "Other Locations" |

Substitute your server's name if you have renamed it. If Homebase is not publishing its name
on the network, the page shows its address instead — and says so, because an address changes
from time to time and a name does not.

To keep it on Windows, right-click **This PC** and choose **Map network drive**. On macOS,
drag it into the Finder sidebar.

Leave any "domain" or "workgroup" box empty.

## The password is not your Homebase password

It is deliberately separate, and this catches people out.

A file-sharing password is typed into a Windows dialog once and saved there for ever, which
makes it exactly the kind nobody ever changes. It must not also be a way to administer the
machine — so Homebase keeps these accounts apart from the ones that sign in to the
dashboard, and gives them no access to anything but the shared folders.

You can use the same *name* in both places. Homebase stores the sharing account under a
prefix and maps it back, so `alex` on the dashboard and `alex` on your laptop can be the
same person with two different passwords.

Homebase cannot show you a sharing password again after you set it. Setting it again is how
you change it.

## Read-only folders

Tick **Read only** when sharing, and other computers can open and copy from the folder but
not change it. Use it for anything you want to watch or listen to from elsewhere but not
have deleted by accident.

## Stopping

**Stop sharing** takes the folder off the network. **Nothing in it is deleted** — the files
stay on the server exactly as they were, and sharing it again later brings it back with
everything still in it.

**Remove** next to a person's name stops them opening any shared folder. Any computer
currently signed in as them is disconnected.

## When it does not work

**"Cannot find the server", or nothing at all.** Check the Files page. If it says the file
server is not running, that is the answer — nothing is reachable and the page will say so at
the top, because from the other end that state looks identical to a folder that was never
shared.

**Windows asks for a password over and over.** It is sending the wrong name. Type it as
`homebase\yourname` — with your server's name, a backslash, then the account — rather than
just the account. Windows otherwise offers your Microsoft account, which the server has
never heard of.

**The disk is not connected.** A share on a disk that has been unplugged is still configured
and still listed; it just has nothing behind it. The Files page says which. Plug the disk
back in and it works again with no further setting up.

## From a terminal

```sh
homebasectl share                       # what is shared, and how to open it
homebasectl share add backup internal   # share a folder
homebasectl share password alex         # let somebody open it
homebasectl share remove backup         # stop sharing; the files stay
```

`homebasectl share` on its own prints the addresses for all three kinds of computer, which
is the fastest way to get them onto a machine you are already at a shell on.
