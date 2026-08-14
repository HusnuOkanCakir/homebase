# ADR-0019 — Remote access is self-hosted Wireguard

- **Status:** Accepted
- **Date:** 2026-08-14
- **Milestone:** 11 — Reachable from anywhere
- **Related:** [ADR-0006](0006-privilege-split.md), [ADR-0017](0017-local-https-and-discovery.md),
  [threat model](../security/threat-model.md)

## Context

Everything Homebase does so far assumes the same house. The server is reachable at
`https://its-name.local` from the sofa and from nowhere else. That is half of what a home
server is for: the other half is the photographs you want on your phone while you are not at
home, and the file you left on it while you are at work.

This was deferred out of Milestone 7, and the reasoning recorded there was:

> it cannot be tested without an account on somebody else's service, and depending on a
> third-party coordination network needs a decision record of its own

Every word of that is about designs like Tailscale. None of it is true of plain Wireguard,
which is self-hosted, needs no account, and can be tested with two machines in the VM lab.
The deferral was sound reasoning about a design that was not chosen — which is a useful thing
to notice about one's own deferrals.

The first principle in the README is that **nobody can switch Homebase off or start charging
for it**. That principle decides this record almost on its own.

## Decision

**Wireguard, running on the server, with the keys generated on the server and no
coordination service of any kind.**

A `wg0` interface, a forwarded UDP port on the household's router, and a dynamic DNS name so
a changing home address stays reachable. Devices are added one at a time, each with its own
keypair, and each can be removed without disturbing the others.

### hostd writes the configuration; systemd brings it up

The now-familiar shape. `hostd` writes `/etc/wireguard/wg0.conf` and then asks systemd to
start `wg-quick@wg0`. It does not create the interface itself.

That is not squeamishness about netlink — `hostd` has `AF_NETLINK` and could. It is that
`wg-quick` also adds routes and firewall rules, and `homebase-hostd.service` sets
`RestrictAddressFamilies=AF_UNIX AF_NETLINK` precisely so that the root service which manages
this machine cannot reach the network. Widening that for a convenience would trade a real
structural property for a smaller diff. The unit is `wg-quick@wg0`, installed by the
`wireguard-tools` package, and the only thing `hostd` chooses is whether to start it.

### Keys are generated on the server and shown once

Each device gets its own keypair. The server generates both halves, prints the client
configuration once — as text and as a QR code for a phone — and then **stores only the public
key**.

The alternative is generating the private key on the client, which is better in principle:
the secret would never exist on the server at all. It is rejected because it cannot be done
for a phone. A phone joins a Wireguard network by scanning a QR code, and that code has to
contain the private key, so the key has to have been generated somewhere the QR code is
drawn. Every practical tool in this space does the same thing.

What is *not* the same as every practical tool is that the private key is discarded
immediately. PiVPN and its relatives keep every client configuration on the server, which is
convenient — you can re-display one — and means a single compromise of the server yields
every device's identity.

So the cost is stated plainly: **a configuration that is lost cannot be re-shown.** Remove
the device and add it again. This is the same model as the recovery code in
[ADR-0015](0015-password-recovery.md), for the same reason, and it is the second time this
project has chosen "shown once" over "stored for convenience".

### The reachability check is a completed handshake

"Is my router actually forwarding the port?" cannot be answered from inside the house.
Answering it properly means asking something on the outside to try, which means a third-party
service, which is the thing this record exists to avoid.

So Homebase does not ask. It reports what `wg show` already knows: **whether any device has
ever completed a handshake.** A handshake proves the entire path — the DNS name resolved, the
router forwarded, the key was accepted — and it proves it with evidence rather than with a
probe. Until the first device connects, the honest answer is "nothing has connected yet", and
that is what it says.

### Dynamic DNS is a fixed table of providers

A home connection's address changes. The name has to follow it, and something has to be told
when it moves — which is inherently a service somebody else runs.

It is the one outside dependency here and it is kept small: a name, a token, and a URL to
poll. Providers are a fixed table in the code, the same shape as the backup schedule's
calendar table, so nothing a caller sends becomes part of a URL that is fetched as root. The
token is declared `Secret` on the operation, so it is redacted from the audit log — the
machinery for that exists because the Wi-Fi passphrase needed it first.

A household with a static address, or its own DNS, uses neither.

### Wake-on-LAN is part of this, not a separate feature

A server that is asleep is not reachable from anywhere, so remote access and waking the
machine are the same problem seen twice. `homebasectl wake` sends the magic packet; the
server is configured to accept one.

## Alternatives considered

### Tailscale, or headscale

**The strongest argument against this decision**, and it is worth being honest about how
strong. Tailscale requires no port forwarding and no dynamic DNS, and — the part that
actually matters — **it works behind carrier-grade NAT**, where Wireguard simply cannot. A
household whose ISP does not give them a real address cannot use what this record chooses,
and there is no configuration that fixes that.

It is rejected because the coordination server is either Tailscale's — an account, which
somebody can close or start charging for, against the first principle in the README — or
headscale, which the user must host somewhere with a public address, at which point they
needed the thing this was meant to avoid.

The rejection is about the principle, not about the technology. Tailscale is Wireguard with
a control plane, and the control plane is exactly what is being declined.

### OpenVPN

Older, slower, in userspace, and with a configuration surface many times larger. Its one
advantage is running over TCP on port 443, which gets through hotel networks that block UDP.
That is a real advantage and not enough of one: it costs a daemon, a certificate authority,
and a great deal more that can be configured wrongly.

### Exposing the dashboard to the internet directly

Port-forward 443 and be done. Rejected without much hesitation: it puts an authentication
form written by this project on the public internet, permanently, on a machine in somebody's
house that they do not administer. The threat model assumes the local network is hostile; the
internet is not a step up from that.

A VPN means the only thing on the public internet is a UDP port that does not answer
unauthenticated packets at all. Wireguard is silent to anybody without a key — an unsolicited
packet gets no response, so the port cannot even be confirmed to exist.

### A hosted rendezvous of our own

Homebase running a coordination service for its users. Rejected for the same reason as
Tailscale plus one more: it would make this project an operator of infrastructure that
everybody's server depends on, which is the opposite of what it is for.

## Consequences

**The user has to configure their router.** One UDP port forwarded to the server. It cannot
be automated — UPnP exists and is a hole this project will not open on somebody's network —
so it is documented plainly, with what to do when it does not work. Pretending otherwise
would help nobody.

**Carrier-grade NAT is not supported.** A household behind it cannot use this, and the
product should say so rather than fail mysteriously. Detecting it is possible — the address
on the interface is private while the address the world sees is different — and worth doing,
so the failure has a name.

**A lost client configuration means a new one.** Stated above, and it will annoy somebody.

**The signing key of this feature is the server's private key.** It never leaves
`/etc/wireguard`, which is root-only, and it is in the backup — which means a backup of a
Homebase server is a credential for reaching that server's network. The backup already holds
the password database, so this does not change what a backup is worth; it does raise the
consequence of losing one, and the backup documentation should say so.

**One more package.** `wireguard-tools`, from Ubuntu, for `wg` and `wg-quick`.

## What would make us revisit this

- **Carrier-grade NAT becoming common enough to matter.** If a significant share of
  households cannot use a forwarded port, a coordination server stops being an avoidable
  dependency and becomes the only way the feature works at all. The answer would then be to
  support both, with the self-hosted path remaining the default
- **IPv6 becoming universal**, which would remove the port-forwarding problem for most people
  rather than adding a dependency — the outcome to hope for
