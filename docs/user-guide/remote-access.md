# Reaching your server from anywhere

Your server is on your home network. This puts your phone and your laptop on that network
too, wherever they are, over an encrypted tunnel nobody else operates.

It uses [WireGuard](https://www.wireguard.com/). There is no account, no subscription, and
nothing between your devices and your server —
[ADR-0019](https://github.com/HusnuOkanCakir/homebase/blob/main/docs/decisions/0019-remote-access-is-self-hosted-wireguard.md)
explains why that mattered enough to accept the setting-up it costs.

## What you need first

**A name that follows your home address.** Most home connections get a new address every so
often. A dynamic DNS name — [DuckDNS](https://www.duckdns.org) is free and takes two minutes
— gives you a name that keeps pointing at your house. If your connection has a fixed address,
use that instead.

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
otherwise remote access needs a different design from the one Homebase has.

**It works away from home but not on your home Wi-Fi.** Expected, and harmless: turn the VPN
off when you are at home. You are already on the network it connects you to.

## One thing worth knowing

Your backups contain the server's key, because they contain `/etc/homebase` and
`/etc/wireguard`. A backup disk is therefore a way onto your home network as well as a copy
of your files. Keep it somewhere you would keep the spare keys to your house — which is what
[Backup and restore](backup.md) says anyway, for a different reason.
