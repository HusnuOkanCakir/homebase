# From a terminal

`homebasectl` does everything the dashboard does. If you are the sort of person who would
rather type than click, this is the interface.

```sh
ssh you@your-server.local
sudo homebasectl system
```

## Signing in

There isn't one. Running as root, `homebasectl` reads Homebase's database to authenticate
itself — which is what root can do anyway, with `sqlite3`, less carefully. There is no
escalation in it: somebody who is root on the machine already has everything Homebase could
give them.

What it buys is that `sudo homebasectl apps list` simply works, with no token to create,
store, rotate or leak.

As an ordinary user it needs a token instead, from `HOMEBASE_TOKEN` or
`~/.config/homebase/token`. That is the path for a script running as somebody other than
root, and it is deliberately the less convenient one.

## From nothing to a working server

The whole sequence, which is also in the [README](https://github.com/HusnuOkanCakir/homebase#building-the-server-start-to-finish):

```sh
# On your own machine
homebasectl installer devices
sudo homebasectl installer create --device /dev/sdX

# Boot the laptop from it, wait, then
ssh you@homebase.local
sudo homebasectl setup okan            # prints a recovery code — write it down
sudo homebasectl apps install jellyfin
sudo homebasectl backup schedule daily backups
sudo homebasectl update check
```

## What it can do

```sh
homebasectl setup NAME                      # the first administrator, once
homebasectl system                          # version, uptime, memory, load, temperature
homebasectl apps                            # what is installed
homebasectl apps install jellyfin
homebasectl apps logs jellyfin
homebasectl storage                         # the disks Homebase manages
homebasectl backup schedule daily backups   # every night, onto the disk called "backups"
homebasectl backup now backups
homebasectl backup list backups
homebasectl update check
homebasectl update apply
homebasectl network                         # how this server is connected
homebasectl network wifi scan
homebasectl network wifi join "Your Network"
homebasectl vpn                             # who can reach this server from outside
homebasectl vpn setup yours.duckdns.org
homebasectl vpn add-device phone            # prints a QR code, once
homebasectl vpn remove-device phone
homebasectl vpn dns duckdns yourname        # keep a name pointing at the house
homebasectl vpn off                         # close the port; the keys are kept
homebasectl share                           # folders on the network, and what to type
homebasectl share add backup internal
homebasectl share password okan
homebasectl apps storage jellyfin           # where an application keeps its files
homebasectl apps open jellyfin              # the address, and open it if there is a desktop
homebasectl system history 7                # how hot it has been, as a chart
homebasectl wake AA:BB:CC:DD:EE:FF          # start a sleeping machine
homebasectl repair                          # fix what a power cut left broken
homebasectl diagnostics                     # a file safe to send to somebody
```

Anything that takes a while — installing an application, making a backup — waits and reports
how it ended, rather than handing back a job number for you to poll. The polling loop would
otherwise be written once per caller, slightly differently each time.

## Reaching it from outside the house

Wireguard, and Homebase runs the server itself — there is no third party in the path and
no account anywhere ([ADR-0019](../decisions/0019-remote-access-is-self-hosted-wireguard.md)).

```sh
sudo homebasectl vpn dns duckdns yourname       # asks for the token, never an argument
sudo homebasectl vpn setup yourname.duckdns.org
sudo homebasectl vpn add-device phone
```

The last one prints a QR code **once**. It contains the device's private key, which is
stored nowhere — losing it means removing the device and adding it again.

**Scan it from inside the Wireguard app** — Add tunnel, then Scan from QR code. A phone's
own camera decodes a QR code to text, so pointing it at this one displays the private key
on screen and does nothing useful with it.

Three things are worth knowing before you rely on it.

**It opens a port.** UDP 51820, to the whole internet rather than to private addresses
only, because being reachable from outside is what this is for. Every other listening
thing Homebase sets up is offered to the house alone. What makes the difference acceptable
is Wireguard's answer to a packet it does not recognise, which is silence — there is no
banner, no error, and no way to distinguish the port from a closed one without a key.

**Your router has to forward it, and Homebase cannot.** Nor can it check from here: a
firewalled port and a wrong key are the same experience on a phone, which is to say
nothing happening. `homebasectl vpn` therefore keeps telling you the forwarding is
outstanding until a device has actually connected once, and `ever_connected` is the field
that makes that possible.

**Give the server a fixed address in the router** while you are in there. A forward points
at an address, and the server's changes when its lease does — which is how remote access
stops working three weeks later for no visible reason.

To switch it off:

```sh
sudo homebasectl vpn off
```

The port closes first and the tunnel stops second, deliberately: a failure to stop leaves
something nothing can reach, where the other order would leave the door open after you
were told it was shut. The devices keep their keys and work again when it is switched back
on — "switch this off" and "forget every device I set up" are different intentions.

## Scripting it

`--json` prints the server's answer unmodified. That is the interface to build on; the
readable output is not, and may be reworded at any time.

```sh
homebasectl storage --json | jq -r '.items[] | select(.mounted) | .id'
```

Exit codes:

| | |
|---|---|
| `0` | it worked |
| `1` | it failed, and the server said why |
| `2` | the command was used wrongly |
| `3` | Homebase is not answering on this machine |

`2` and `3` are separated from `1` on purpose. A script that cannot tell "that operation
failed" from "the server is down" will eventually treat both the same way, and the two want
completely different handling.

```sh
if ! homebasectl backup now backups; then
    case $? in
        3) echo "Homebase is not running" ;;
        *) echo "the backup failed" ;;
    esac
fi
```

## The commands that destroy things

```sh
homebasectl backup restore ID --from DISK
homebasectl storage format /dev/sdX
homebasectl storage detach NAME
homebasectl factory-reset
```

Each of these shows you what it is about to do — from the server, not from
memory — and then asks. Restoring prints how many files would be replaced;
formatting prints what is on the disk now.

**The confirmation is the thing's own name**, never "yes". A backup's id, a
disk's device, the server's hostname. A confirmation that reads the same on
every machine is one you can type without looking, and one a script can carry
from a safe context into a dangerous one.

From a script, pass it explicitly:

```sh
homebasectl storage format /dev/sdb --confirm /dev/sdb
```

**There is no `--yes`.** A flag meaning "do it anyway" ends up in every
invocation within a week, and then it is not a confirmation at all. Without a
terminal and without `--confirm`, these refuse and tell you what to pass.

## Passwords

The Wi-Fi password is read from `HOMEBASE_WIFI_PASSWORD`, or asked for. There is no flag for
it and there should not be: an argument is in your shell history, and it is in `ps` output
for every user on the machine for as long as the command runs — which for joining a network
is up to a minute.

```sh
HOMEBASE_WIFI_PASSWORD="$(pass show wifi/home)" \
    homebasectl network wifi join "Your Network"
```

## What it is not

`homebasectl` is an ordinary client of Homebase's HTTP API — the same one the dashboard
uses, with the same permission checks, the same job records and the same events. It does not
talk to the privileged service directly, even though as root it could. A second path to a
privileged operation is a second place for the checks to be wrong.

Two commands are exceptions, and they are exceptions on purpose:
`homebasectl recovery-code` and `homebasectl list-accounts` read the database directly,
because they exist for a server nobody can sign in to — see
[If you forget your password](passwords.md).
