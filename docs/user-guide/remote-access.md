# Reaching your server from anywhere

Your server is on your home network. This puts your phone and your laptop on that network
too, wherever they are, over an encrypted tunnel nobody else operates.

It uses [WireGuard](https://www.wireguard.com/). There is no account, no subscription, and
nothing between your devices and your server —
[ADR-0019](https://github.com/HusnuOkanCakir/homebase/blob/main/docs/decisions/0019-remote-access-is-self-hosted-wireguard.md)
explains why that mattered enough to accept the setting-up it costs.

## First, which of the two this server needs

Homebase's own remote access is WireGuard, described below. It needs one port forwarded from
your router, and on most connections that is a five-minute job.

**On some connections it cannot be made to work at all.** If your provider gives your router
an address starting `100.64.` to `100.127.`, you are behind carrier-grade NAT: your house has
no address of its own, so there is no port to forward and no amount of router configuration
will produce one. `homebasectl network` shows the address your router has.

The answer there is [Tailscale](#tailscale-when-no-port-can-be-forwarded), which does not need
a forwarded port. Homebase reports it and does not manage it — skip to that section, and skip
the WireGuard setup entirely.

## What you need first

**A name that follows your home address.** Most home connections get a new address every so
often. A dynamic DNS name — [DuckDNS](https://www.duckdns.org) is free and takes two minutes
— gives you a name that keeps pointing at your house. Homebase keeps it up to date for you:

```sh
sudo homebasectl vpn dns duckdns yourname
```

It asks for the token from DuckDNS, checks it straight away, and then updates every five
minutes. `homebasectl vpn dns` on its own says whether it is working — **a name that quietly
stopped updating three weeks ago is a server nobody can reach, and it looks exactly like one
that is fine.**

The token is kept in a root-only file and is not written to the audit log.

If your connection has a fixed address, use that instead and skip this.

**Access to your router.** One port has to be forwarded. This is the only step Homebase
cannot do for you, and it is worth knowing that up front.

## Setting it up

```sh
sudo homebasectl vpn setup yours.duckdns.org
```

Then, on your router, forward **UDP port 51820** to your server. Routers call this "port
forwarding", "virtual servers" or occasionally "NAT rules". You will need the server's
address on your home network, which `homebasectl network` will tell you.

Nothing answers on that port without a key. WireGuard does not reply to unsolicited packets
at all, so a scan of your address cannot even tell that the port is open.

## Adding a device

```sh
sudo homebasectl vpn add-device phone
```

This prints a QR code and a configuration.

**Install the WireGuard app** — it is free, from the same people who wrote WireGuard, on the
App Store, Google Play, and for Windows, macOS and Linux. Press **+**, choose to scan a QR
code, and point it at your screen.

On a laptop, save the text as `homebase.conf` and import it instead.

> **This is shown once.** The key is not stored on your server. If you lose the
> configuration, remove the device and add it again — you cannot ask for it a second time.
>
> That is deliberate. Most tools keep every device's configuration on the server, which is
> convenient right up to the day somebody gets into the server and walks away with every
> device's identity.

Add one per device. They are separate, so losing your phone does not mean re-doing your
laptop.

## Using it

Turn the VPN on in the WireGuard app. Your server is then at `https://its-name.local` —
exactly as it is at home, because as far as your phone is concerned it *is* at home.

Only your home network goes through the tunnel. The rest of your phone's internet does not,
so nothing else is slowed down by your home upload speed.

## Checking it works

```sh
sudo homebasectl vpn
```

It shows each device and when it last connected. **"Never" for every device is the thing to
look at**: it almost always means the port is not forwarded.

Homebase does not test the port by connecting to itself from outside — that would mean asking
some other company's server to try, and this whole feature exists to avoid depending on
anybody. What it reports is what it knows: whether a device has ever got through.

## Removing a device

```sh
sudo homebasectl vpn remove-device phone
```

Immediate. Do this if a device is lost or stolen; the configuration on it stops working, and
your other devices are unaffected. Adding it back later issues a *new* key — the old one
stays dead.

## When it does not work

**No device ever connects.** Almost always the port. Check that UDP 51820 is forwarded to the
right address, and that your router has not given the server a different address since — a
DHCP reservation for the server prevents that.

**It worked and then stopped.** Your home address changed and the dynamic DNS name did not
follow it. Check that whatever updates the name is still running.

**Nothing works, and your provider gives you an address starting `100.64.`** — that is
carrier-grade NAT. Your connection has no address of its own, so no port can be forwarded to
it and this cannot be made to work. Some providers will give you a real address if you ask;
otherwise use [Tailscale](#tailscale-when-no-port-can-be-forwarded).

**It works away from home but not on your home Wi-Fi.** Expected, and harmless: turn the VPN
off when you are at home. You are already on the network it connects you to.

## Waking a sleeping machine

Over the VPN you are on your home network, so you can wake other machines on it:

```sh
sudo homebasectl wake AA:BB:CC:DD:EE:FF
```

That is the desktop in the study, started from a train. Nothing acknowledges a wake-up
packet — it is fire-and-forget by design — so there is no confirmation, only the machine
appearing a minute later.

**Waking the server itself is different**, because nothing on a sleeping machine can run a
command. `homebasectl network` shows the server's own hardware address and whether its card
will accept a wake-up packet; if it will, send one from your phone with any wake-on-LAN app.
If it says it cannot be woken, the setting is in the machine's BIOS, and it usually has to be
enabled there before Linux can do anything about it.

## Tailscale, when no port can be forwarded

[Tailscale](https://tailscale.com) builds the same kind of encrypted tunnel WireGuard does —
it *is* WireGuard underneath — and arranges the connection through a coordination server of
its own, so neither end needs a forwarded port. That is what makes it work behind carrier-grade
NAT, and it is also the trade:
[ADR-0019](https://github.com/HusnuOkanCakir/homebase/blob/main/docs/decisions/0019-remote-access-is-self-hosted-wireguard.md)
rejected exactly this arrangement on principle, because the coordination server belongs to a
company. Your files never pass through it; who may connect to your server does.

**Homebase does not install, configure or manage Tailscale.** It reports what it finds. The
Remote access page shows whether it is running, the name it has, and the address to use.

On the server:

```sh
sudo tailscale up
```

Open the link it prints, sign in, and the server joins your tailnet. On your own phone or
laptop, install Tailscale, sign in with the **same account**, and the server is at the address
the Remote access page shows.

Use the address, not the name. The name works and has failed on iOS and on Windows here twice.

## Giving somebody else access, without giving them your account

Everybody in the house needs their own way in. Nobody should be signing in as you.

A person needs **two things**, and there is no way around that: a Tailscale account to reach
the server at all, and a Homebase account to sign in once they have. They are separate
systems and neither can stand in for the other.

**Do not add them to your tailnet as a user.** A user of your tailnet can see every machine
on it. What you want instead is *sharing a single machine*, which Tailscale supports and which
gives them the server and nothing else in your house.

1. **They make their own Tailscale account.** Any email; the free plan is enough. They install
   Tailscale on their Windows PC and sign in with it. This creates *their* tailnet, separate
   from yours.
2. **You share the server with them.** On [login.tailscale.com](https://login.tailscale.com),
   open **Machines**, find your server, and choose **Share…** from the menu at the end of its
   row. Copy the invite link and send it to them.
3. **They accept the link.** The server then appears in their machine list. Nothing else of
   yours does — sharing shares one machine, not a network. They cannot see your phone, your
   laptop, or anything else in the house, and you cannot see theirs.
4. **You give them a Homebase account.** In the dashboard, **Settings → People**, add them and
   choose what they may do. They get a joining code, shown once. They sign in with it, choose
   their own password, and that one password also opens folders from Windows — see
   [Sharing files](sharing.md).

To take it back, the same menu on [login.tailscale.com](https://login.tailscale.com) removes
the share, and **Remove** in **People** removes the Homebase account. Either one alone is
enough to lock somebody out; do both when somebody leaves.

!!! note "Their machine is on the same footing as yours"

    A shared machine is reachable on every port the server has open to the tunnel — the
    dashboard and file sharing both. What they may *do* is decided by their Homebase account
    and their role, not by Tailscale. Give somebody the Limited role if all they should have
    is files.

## One thing worth knowing

Your backups contain the server's key, because they contain `/etc/homebase` and
`/etc/wireguard`. A backup disk is therefore a way onto your home network as well as a copy
of your files. Keep it somewhere you would keep the spare keys to your house — which is what
[Backup and restore](backup.md) says anyway, for a different reason.
