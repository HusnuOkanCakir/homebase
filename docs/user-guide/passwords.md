# If you forget your password

Homebase cannot email you a reset link. Nothing about your server leaves it — there is no
account with us, because there is no us. That is the point of the thing, and it means there
is nobody who can let you back in.

So instead you are given a **recovery code**: twenty-five characters, shown once, when you
first set the server up. It is the spare key.

## Where your code is

You were shown it on the screen straight after choosing your password, and asked to tick a
box saying you had written it down. It looks like this:

```
7K2M9-QRVWX-3TZ4P-HNBCD-8FGJ5
```

Keep it where you keep important papers. Not on the server, and not in a note on the
computer you use to reach the server — if that computer is the problem, the note goes with
it.

**Anyone who has this code can take over your server.** Treat it like a spare key to the
house. That is the trade: a key you can lose is what makes a password you can forget
survivable.

## Getting back in

1. Open Homebase in a browser as usual.
2. Under the password box, choose **I have forgotten my password**.
3. Type your name and your recovery code.
4. Choose a new password.

Capitals do not matter, and neither do the dashes — type it however it is easiest to read
off the paper.

Two things happen that are worth knowing before you start:

- **Everything signed in gets signed out**, on every device. That is deliberate. If you are
  doing this because you think somebody else got into your server, signing them out is most
  of the point.
- **The code you just used stops working**, and you are given a new one. Write that one down
  too, and throw away the old paper.

## Checking you still have one

Sign in, open **Security**, and it will tell you whether a recovery code exists and when it
was created. It will not show you the code — Homebase keeps it the same way it keeps your
password, scrambled beyond reading, so that somebody who steals the disk out of your server
does not get a key with it.

If the date does not match the paper you are holding, the paper is out of date.

## If you have lost the code as well

Somebody with access to the server itself can create a new one. On the machine, or over SSH
if you have set that up:

```console
$ sudo homebasectl recovery-code
```

It prints a new code. Take it to the browser and follow the steps above.

This needs a keyboard on the server, which is the one thing Homebase otherwise never asks
of you. It is the last resort, and it exists because the alternative is losing everything on
the machine over a forgotten word.

If several people have accounts, say which one:

```console
$ sudo homebasectl list-accounts
$ sudo homebasectl recovery-code --user rosa
```

## If you never wrote one down

Sign in — while you still can — open **Security**, and choose **Create a new recovery
code**. Do it now rather than later. The moment you need it is the moment you cannot get it.

## What about backups?

A backup contains your accounts, so restoring one brings back the password you had when the
backup was made. If you have forgotten that password, restoring does not help: you get the
same locked door on a new machine.

Your **recovery code travels with the backup** as well. The code you wrote down still opens
the server rebuilt from the disk — which is what makes a backup worth having in this
situation rather than merely disappointing.

The other side of that: **a backup disk and a written code together are complete access to
your server.** If you keep the disk at a relative's house, do not tape the code to it.

## Why it is done this way

Every other product recovers your account by proving you control something else — an email
address, a phone, another device you are already signed in to. Homebase has none of those,
and adding them would mean sending your details somewhere.

The reasoning, and what was rejected, is in
[ADR-0015](../decisions/0015-password-recovery.md).
