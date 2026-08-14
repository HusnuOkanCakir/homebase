# When something is wrong

Under **Something's wrong** in the dashboard. Three things, in the order to try them.

If you cannot reach the dashboard at all, start with
[Finding your server](network.md) — the problem is more often the address than the server.
If you cannot sign in, see [If you forget your password](passwords.md).

## 1. Make a file to send to somebody

Press **Make a diagnostic file**, then **Download it**.

It collects what somebody helping you would ask for first: which version this server is
running, whether its services are running, anything that failed to start, the last day of
messages, how full the disks are, and whether an update was left unfinished.

**It does not contain your password, your recovery code, or anything from your own files.**
The screen lists exactly what is left out, and so does the top of the file itself. Read it
before you send it if you like — it is ordinary text.

It *does* contain the name of your server, the names of your disks and applications, and
error messages, which sometimes include the names of files. Send it to somebody you would
tell those things to.

## 2. Try to fix it

Press **Check and repair**.

Homebase goes through a short list of things that are commonly wrong after a power cut or
an interrupted update, and puts right the ones it can:

- an installation that was interrupted part way through
- folders that are missing, or belong to the wrong account
- services that are not running

**Nothing is deleted.** Every check only ever puts something back, which is why it is safe
to press without knowing what is wrong.

If it says **nothing needed fixing**, that is a real answer rather than a failure: whatever
is wrong is not one of the things it knows about. Make a diagnostic file and ask somebody.

You can press it twice. The second time will find nothing, which is how it should be.

### The one it fixes most often

A power cut during an update leaves the machine part way through installing packages. It
still boots and usually still works, which is exactly why nobody notices — Homebase says so
under **Updates**, and **Check and repair** finishes the job.

## 3. Start again

**Reset this server** removes every account and every setting, and afterwards the server
asks to be set up again, like a new one.

**Your files are kept.** Everything on your storage disks stays where it is, and you will
see it again once you have made an account. Where the server gets its updates from is kept
too, so it keeps receiving security fixes while nobody is signed in.

You have to type the server's own name to confirm. Not "yes" — the name, because this is the
one thing here that cannot be undone.

Two things worth knowing before you do it:

- **Make a backup first if you can.** The accounts and settings this removes cannot be
  brought back any other way, and a backup made beforehand restores them.
- **Your browser will warn you about the server once more.** The server gets a new identity
  as part of the reset, so the warning from the very first time comes back. That is on
  purpose: if you are passing the machine on, whoever had it before should not keep anything
  that still works as it.

### If you are giving the machine away

Reset it, and then also erase your files — the reset keeps them by default, which is the
right default for somebody fixing their own server and the wrong one for somebody handing it
to a stranger. There is no button for that yet; ask somebody to help, or wipe the disks.

## What Homebase cannot fix from here

- **A machine that will not boot.** Nothing in the dashboard can help, because the dashboard
  is on the machine. What helps is the backup on the disk in the drawer, restored onto
  another computer — see [Backup and restore](backup.md).
- **A disk that is failing.** Homebase will tell you a backup is damaged, and that usually
  means the disk. Replace it; nothing repairs a dying disk.
- **A forgotten password.** That is [its own page](passwords.md), and the recovery code you
  wrote down at setup is the answer.
