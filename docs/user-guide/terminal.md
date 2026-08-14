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

## What it can do

```sh
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
homebasectl repair                          # fix what a power cut left broken
homebasectl diagnostics                     # a file safe to send to somebody
```

Anything that takes a while — installing an application, making a backup — waits and reports
how it ended, rather than handing back a job number for you to poll. The polling loop would
otherwise be written once per caller, slightly differently each time.

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
