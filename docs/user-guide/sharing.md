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

## Opening them in the dashboard

The **Files** page shows everything you can open on this server: each shared folder, and
your own. You can look through them, download something, drop a file in, make a folder,
rename and delete.

This is the way in that needs nothing at the other end. A mapped drive needs a computer that
can map one — a phone cannot, and a borrowed laptop should not — and this works anywhere the
dashboard works, including from another country over [Tailscale](remote-access.md).

A few things worth knowing about it:

- **A download is a normal browser download.** It resumes if the connection drops, which
  matters for a film over a home upload link.
- **There is no wastebasket.** Deleting a folder that has anything in it asks you to type its
  name first; deleting one file does not, because a confirmation on every ordinary action is
  one nobody reads by the third time.
- **Names are held to what Windows can open.** A file called `report?`, or one ending in a
  space, cannot be opened from a Windows machine at all, so Homebase will not create one.
- **Files never open in the browser, they download.** A file on this server arrived from a
  computer Homebase knows nothing about, and a page that ran here would run with your session.

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

## Joining a server somebody else set up

Whoever runs the server adds you under **Settings → People** and gives you a **joining
code** — twenty-five characters, and they can only show it to you once.

On the sign-in page, choose **I have a joining code**, type your name and the code, and pick
a password. That password is yours: the person who invited you does not know it and cannot
see it. You are then shown a **recovery code** of your own — write it down, because it is
the way back in if you forget the password, and it is shown once.

A joining code stops working a week after it was made. If yours no longer works, ask for
another; it costs them nothing and there is no limit.

## Your password is your Homebase password

The same name and the same password open the dashboard and open a folder from your laptop.
There is nothing separate to set up and nothing extra to remember.

It used to be two, and two was worse. A file-sharing password is typed into a Windows dialog
once and saved there for ever, which makes it exactly the kind nobody ever changes — so the
day it has to be typed again is the day it cannot be remembered.

Homebase sets it at the only moments it can: when you choose your password, and when you
sign in on a server where file sharing was switched on after you joined. It cannot read your
password back afterwards, which is why there is no button that syncs them.

Changing your Homebase password changes both. Any computer with the old one saved will ask
for it again.

!!! note "What that means for how safe it is"

    The file server keeps a weaker record of a password than Homebase does — an unsalted
    hash, the way the SMB protocol requires. Anybody who can get root on your server could
    already read both. It is not a reason to carry two passwords, and it *is* a reason not
    to use your Homebase password anywhere else.

## Your own folder

Everything under **Shared folders** is open to everybody in the house who has an account.
That is what most of a family server is for, and it is not all of it.

Each person also gets a folder that is theirs, called **people** when you open it from
another computer. Everybody connects to the same name and sees their own.

| | |
|---|---|
| **Windows** | `\\homebase\people` |
| **macOS, Linux** | `smb://homebase.local/people` |

It is created with the account, on the server's own disk. It cannot be put on a disk
formatted for Windows or a camera: those record no owner for a file, so a private folder on
one would not actually be private, and Homebase refuses rather than pretending.

!!! warning "What private means here, exactly"

    The applications on your server cannot read these folders — Jellyfin and the rest are
    kept out by the operating system itself.

    Between people in the house, it is Homebase that checks, on every request, rather than
    the operating system. Somebody who can already run commands as root on the server can
    read anyone's folder. That is the price of these folders being visible in the dashboard
    and openable from a phone; the alternative was a folder nothing but Windows could ever
    reach.

**Removing somebody does not delete their files.** Their folder is moved aside and kept, so
that the next person with the same name starts with an empty one instead of inheriting the
last one's files.

## Read-only folders

Tick **Read only** when sharing, and other computers can open and copy from the folder but
not change it. Use it for anything you want to watch or listen to from elsewhere but not
have deleted by accident.

## Stopping

**Stop sharing** takes the folder off the network. **Nothing in it is deleted** — the files
stay on the server exactly as they were, and sharing it again later brings it back with
everything still in it.

**Remove** next to a person's name stops them opening any shared folder. Any computer
currently signed in as them is disconnected. Removing their Homebase account does this too —
an account that can no longer open the dashboard must not still open a drive.

## Opening it when you are not at home

If you reach your server over [Tailscale](remote-access.md), a shared folder works from
anywhere, exactly as it does in the house. Use the address rather than the name:

| | |
|---|---|
| **Windows** | `\\100.107.85.93\documents` — your server's Tailscale address |
| **macOS, Linux** | `smb://100.107.85.93/documents` |

The **Remote access** page shows the address to use. The name works on the home network and
is unreliable over the tunnel on iOS and on Windows, which has cost this project two evenings
— so the address is what is written here.

Turn Tailscale off when you get home. You are already on the network it connects you to.

## A disk that is plugged into somebody's own computer

The situation this is for: a disk lives in a drawer, somebody at home plugs it into **their
own** computer, and somebody who is away needs a file off it. The server does not have that
disk, and copying it across first only works if you already know which files are wanted.

Nothing gets installed on the computer with the disk. Windows has shared folders built in;
Homebase just opens one.

**On the computer with the disk**

1. Plug the disk in.
2. Right-click the drive or the folder → **Properties** → **Sharing** → **Advanced Sharing**
   → tick **Share this folder**.
3. Note the **share name** it gives you. That is not the drive letter — a disk that appears
   as `G:` is usually shared as something like `sandisk`. Rename it here if it has a space
   in it.
4. Under **Permissions**, make sure the account you are going to use can read it.
5. Stop that computer going to sleep while somebody is reading from it. Sleep is the most
   common reason this appears to stop working.

**On Homebase**, under **Files** → **Folders on other computers** → *Open a folder from
another computer*: what to call it here, the computer's name on the network, the share name,
and an account on **that** computer with its password.

It then appears in **Files** like any other folder, which means somebody away from home
reaches it in a browser over [Tailscale](remote-access.md) — nothing installed at their end
either.

!!! note "Read-only, and not as a matter of policy"

    Homebase opens these folders read-only. Nothing on this server can change or delete
    anything on that computer — not the Files screen, not somebody signed in here, not a
    server that has been broken into. The disk belongs to whoever plugged it in.

**When that computer goes to sleep or is unplugged**, the folder stays listed and says it is
not answering, with a **Try again** button. It is not mounted at boot on purpose: a laptop in
the next room is switched off most of the time, and a server that waited for one at startup
would be a server that hung.

**The password is kept on the server**, so the folder can be opened again after a restart. It
is stored where only the server itself can read it and is never displayed again — but it is a
password for somebody else's computer. Use an account on that machine with no more access
than this needs, rather than the one it is administered with.

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

**A folder on another computer says that computer is not answering.** In order of how often
it is the cause: the computer went to sleep, the disk was unplugged, the folder stopped being
shared, or that computer's name changed on the network. Wake it and press **Try again**. If
it still refuses, check the share is still there under Properties → Sharing, and that the
account and password are the ones it expects — Windows will not tell Homebase which of those
is wrong, and neither can Homebase.

## From a terminal

```sh
homebasectl share                       # what is shared, and how to open it
homebasectl share add backup internal   # share a folder
homebasectl share password alex         # let somebody open it
homebasectl share remove backup         # stop sharing; the files stay
```

`homebasectl share` on its own prints the addresses for all three kinds of computer, which
is the fastest way to get them onto a machine you are already at a shell on.
