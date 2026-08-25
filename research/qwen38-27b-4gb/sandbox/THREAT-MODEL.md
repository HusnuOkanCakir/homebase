# The contained environment, and what it is actually for

This directory runs a model whose refusal behaviour has been removed by a third
party, on a machine that also serves a household's files, media and backups.
Those two facts are the whole design.

Written down because a sandbox nobody can describe is a sandbox nobody can
check, and because the honest list of what this does *not* stop is the more
useful half.

## What is being defended against

In the order they are actually likely:

1. **A malicious or malformed GGUF.** Weights are a file format parsed by a C++
   program, and llama.cpp has had parser bugs. This is the realistic route to
   code execution, and it does not require anybody to have done anything wrong
   except download a file. It is the reason for every control below.
2. **Exfiltration after a compromise.** If the process is taken over, what can
   it reach and where can it send it.
3. **Blast radius.** The household's files, the dashboard's database and
   sessions, the backups, and the API key of the *other* model — the one wired
   into Homebase and reachable by anyone signed in from the house.
4. **Accidental exposure.** Somebody in the house finding this model through the
   dashboard because it was quietly added to the same place as the safe one.
5. **Disclosure at rest.** The weights, and anything typed into them, ending up
   in a backup, on a disk that leaves the house, or readable by another account.

## What each control does

**No network at all.** `PrivateNetwork=yes` puts the process in a network
namespace containing only loopback — no route to the house, no route to the
internet, no DNS. `RestrictAddressFamilies=AF_UNIX` means it cannot create an IP
socket even in principle, and `IPAddressDeny=any` is a third lock on the same
door. This is why the API is a Unix socket rather than a port: sockets are
filesystem objects and cross namespaces freely, so the model stays reachable
from the host while having no way to reach anything itself.

A fully compromised process here has nowhere to send what it finds. That is the
single control worth most, and everything else is defence in depth behind it.

**Blind to what matters.** `ProtectSystem=strict` makes the filesystem
read-only; `InaccessiblePaths` then names the things whose disclosure would
actually hurt — `/srv/homebase`, `/var/lib/homebase`, `/etc/qwen-lab`, `/home`,
`/root` — so they are not merely unwritable but absent.

**No privilege, and no route to any.** Empty capability bounding set, no new
privileges, its own user namespace, its own PID namespace view. It runs as an
account with no home, no shell and no other group.

**Bounded.** 8 GB memory ceiling, 350% CPU quota, 64 tasks, idle I/O priority.
A research experiment must not be the reason the house loses its file server.

**Encrypted at rest.** A LUKS2 volume, unlocked from a root-only key when the
service starts and locked again when it stops, mounted `ro,noexec,nosuid,nodev`.

**Not logged.** Request bodies never reach the journal. Whatever somebody types
into a model with its refusals removed is exactly the material that should not
be sitting in a log to be read over a shoulder, shipped in a backup, or pasted
into a bug report.

**Not connected to anything.** A different socket, a different unit, a different
user. `HOMEBASE_ASSISTANT_URL` continues to point at the safe 4B on
`127.0.0.1:8088`. The dashboard cannot see this model and neither can the house.

**Not enabled at boot.** Started by hand, stopped when the research stops. A
model with its refusals removed should not be a thing that quietly returns every
time the machine reboots.

## What this does NOT protect against

Stated plainly, because the gaps are where somebody gets hurt.

- **Root on the running machine.** The volume key is on the same disk, because
  the service has to mount it unattended. Anybody who is already root can read
  the key, open the volume, and read the weights. Encryption at rest protects a
  disk that has left the house and a backup that got copied somewhere; it does
  not protect a machine that is already lost.
- **The volume while it is open.** Between `start` and `stop` the plaintext is
  mounted. The window is exactly as long as somebody is using it, which is the
  best that can be done for a service that must load weights.
- **Anything the operator chooses to do with the output.** Nothing here
  constrains what a person does with an answer once they have it. That is not a
  technical control and pretending otherwise would be theatre.
- **The model being wrong, or confidently wrong.** Abliteration removes refusal
  directions; it does not add knowledge. The publisher's own card says behaviour
  near the old refusal boundary is *less stable than the base model*. Expect
  worse calibration exactly where it used to decline.
- **Prompt injection.** Removing refusals plausibly removes injection resistance
  too — the 2B distill already failed that gate by emitting a commanded string
  verbatim. **This is measured before the model is used for anything, not
  assumed.** See `results/`.
- **A kernel bug.** Every control here is enforced by the kernel. A container
  escape is an escape.

## The rules that follow from all this

1. It is never reachable from the house network, only over the Unix socket, and
   therefore only from an SSH session on the server.
2. It is never wired into the dashboard, on any port, under any name.
3. It is never enabled at boot.
4. Its weights are pinned by revision and SHA-256 before installation, and the
   copy inside the volume is verified again after it is made.
5. It is stopped when it is not being used, which re-locks the volume.
