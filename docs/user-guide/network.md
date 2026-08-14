# Finding your server, and what to do when you cannot

Your server has a name. From any phone, tablet or computer on the same network, open:

```
https://homebase.local
```

If you gave the server a different name in [First steps](first-steps.md), use that one
instead, with `.local` on the end. A server called `attic` is at `https://attic.local`.

That address does not change. The server's number on the network does change from time to
time, which is why the name is the one worth remembering.

!!! warning "Pre-alpha"

    Your server has to be plugged into your router with a network cable. Setting it up on
    Wi-Fi is not possible yet — see [what is missing](#what-is-not-here-yet) at the end of
    this page.

## The warning your browser shows the first time

The first time you open your server, your browser will interrupt you with a warning. It will
say something like "your connection is not private", and it will not be obvious that
continuing is safe.

**This one time, it is.** Here is what is happening.

Everything you do with your server travels over your home network encrypted, including your
password. To encrypt it, your server needs a certificate. Certificates are normally issued by
outside companies who first check that you own a name like `example.com` — and your server
does not have one of those, because it lives in your house and is deliberately not on the
public internet.

So your server made its own certificate. It is real encryption, and it is exactly as strong.
What your browser cannot do is confirm who made it — and being unable to confirm that is
worth a warning, because in other circumstances it would mean something was wrong.

### How to be sure

Your server shows its own fingerprint — a long line of letters and numbers — on the screen
of the machine itself, on the same screen that tells you what address to open.

When your browser warns you, it will let you view the certificate's details, which include
the same fingerprint. If the two match, you are talking to your server and nothing is
between you.

Then choose **Advanced** and continue. Your browser remembers, and will not ask that device
again.

You will see the warning once per device: once on your phone, once on your laptop. That is
normal, and it is the last you will see of it.

## When it will not load

Three quite different things look identical from a browser that will not open. Homebase's
**Network** screen exists to tell you which one it is — but if you cannot reach the dashboard
at all, work through these in order.

### The name does not work, but the server is fine

Some networks do not carry the messages that make `.local` names work. Guest networks and
some office networks block them, and a few VPN apps on your phone will too.

Use the number instead. It is shown on the server's own screen, and looks like
`https://192.168.1.50`. If that works, the server is running and only the name is the
problem.

### Nothing loads at all

Check, in this order:

1. **The network cable.** Both ends — the server and the router. Most lights are on a
   working socket and off on a dead one.
2. **Whether you are on the same network.** A phone on mobile data instead of Wi-Fi cannot
   reach your server, and this is by far the most common cause. Homebase is not on the
   internet on purpose.
3. **Whether the server is switched on.** If it is a laptop, check that closing the lid did
   not send it to sleep — Homebase configures it not to, but a laptop that was set up
   differently before might.

### The dashboard loads, but says the internet is not reachable

Your server is fine. Your broadband is not.

Everything already on the server keeps working: your files, your applications, your backups.
The only things that need the internet are installing a new application and updating, because
those are downloaded.

If other things in the house are also offline, that is the answer, and there is nothing to
fix on the server.

## The Network screen

Open **Network** in the dashboard. It leads with one sentence saying which of three
situations you are in:

| What it says | What it means |
|---|---|
| **This server is connected** | Nothing is wrong here. |
| **Your server is fine. The internet is not reachable.** | The server works and you can reach it. Your broadband is down. |
| **This server is not on a network** | Nothing is plugged in, or the router did not give it an address. |

Underneath is the address to use, the fingerprint explanation, and a section called **The
details** — the server's name, its number, its router, and the hardware address your router
lists it under.

You can ignore the details entirely. They are there for the one time you are asking somebody
for help, and it is much easier to read them off a screen than to look them up.

## Why the address starts with `https://` and has no number after it

Two small things that were worth doing properly.

**`https://` means encrypted.** Your password does not cross your home network in the clear,
where any other device on it could read it. If you type `http://` by mistake, your server
sends you to the right place automatically.

**No `:8080` on the end.** An address you can say out loud is one somebody can actually use.
It costs the server one narrow permission to listen on the ordinary web address instead of an
unusual one, and it is worth it.

## What is not here yet

**Wi-Fi.** Your server needs a network cable. Setting up Wi-Fi from the dashboard is coming
with the hardware testing in Milestone 9 — a Wi-Fi setup screen that has never been tried on
real wireless hardware would be a guess, and the thing it fails at is reaching the server
afterwards to fix it.

**Remote access from outside your house.** Not yet, and never by putting your server on the
public internet. When it arrives it will be private and something you switch on deliberately.

Both are tracked in the [roadmap](https://github.com/HusnuOkanCakir/homebase/blob/main/ROADMAP.md).
