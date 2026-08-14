# ADR-0017 — A certificate the server signs itself, and a name the network resolves

- **Status:** Accepted
- **Date:** 2026-08-11
- **Milestone:** 7 — Networking and private access
- **Related:** [ADR-0006](0006-privilege-split.md), [ADR-0016](0016-installation-media.md),
  [threat model](../security/threat-model.md)

## Context

Until this milestone the dashboard was plain HTTP on port 8080, reached by typing an address
somebody had to have been told. Both halves of that are a problem, and they are different
problems.

**The password crosses the network in clear.** The
[threat model](../security/threat-model.md) does not describe a home network as safe. It
describes it as containing a smart television with firmware nobody has updated in four years,
a guest's laptop, and whatever else has been given the Wi-Fi password since. An
administrator password and a session cookie travelling in clear across that network is the
kind of exposure that is invisible until it is not.

There was also a concrete bug, and it is worth recording because it is what turned this from
a checkbox into a decision. The session cookie was already marked `Secure`. Browsers refuse a
`Secure` cookie from a non-secure origin — with one exception, `localhost`, which is treated
as trustworthy. Every browser test in this repository reaches the server through a port
forwarded to the host's loopback. So the suite was green while a real installation, reached
at `http://192.168.1.50:8080`, silently discarded the session and answered `401` immediately
after a correct password. The product did not work at all, in the one respect it exists for,
and nothing failed.

**Nobody can find the machine.** An address is not a name. It is handed out by DHCP, it
changes when the router restarts, and it is written down on a piece of paper that gets lost.
The screen shown after installation ([ADR-0016](0016-installation-media.md)) can print the
current one, but a server that has to be re-discovered every few weeks is a server people
stop using.

And a third thing, smaller but load-bearing: **an address with a port number in it is a
different class of instruction.** `https://attic.local` is something a person can be told
over the phone. `http://192.168.1.50:8080` is something they have to be sent in a message and
copy carefully.

## Decision

**HTTPS on the ordinary ports, with a certificate the server signs itself and the user trusts
once, at a name published over mDNS.**

Four parts, each of which could have been decided differently.

### The certificate is the server's own

`core` generates an ECDSA P-256 self-signed certificate at first start, valid for the
hostname, its `.local` form, `localhost` and the machine's current addresses. It is stored in
`core`'s own state directory — the key `0600` inside a `0700` directory — and written to a
temporary file and renamed, so a machine that loses power mid-write comes back with either
the old pair or the new one and never half of a certificate it then refuses to start with.

**The lifetime is ten years.** This is trust on first use: the point of a long lifetime is
that the user is asked exactly once. A one-year certificate would ask them again every year,
which is precisely how "check this fingerprint" degrades into "click through the warning".

**The machine prints its own fingerprint on its own screen**, in the format browsers display,
and logs it to the journal so it can be read out over the phone. This is the entire
mitigation for the browser warning, and it is what makes the warning a thing to check rather
than a thing to dismiss.

**Addresses deliberately do not invalidate a certificate.** DHCP hands out a different address
on a different network; regenerating for that would change the fingerprint the user checked
and ask them to trust the machine again, routinely, for a reason they have no way to
distinguish from an attack. Names are the stable identity. Addresses are a convenience for
reaching it before mDNS works.

### Ports 80 and 443, via one capability

`core` binds the ordinary ports so the address people are given has no port number in it. It
does this with `AmbientCapabilities=CAP_NET_BIND_SERVICE` and a `CapabilityBoundingSet`
holding that and nothing else — it does not run as root, and there is no privileged proxy in
front of it.

Port 80 serves exactly one thing: a `307` redirect to the same name on 443. Not a proxy and
not a second copy of the dashboard, so there is exactly one origin the dashboard is ever
served from. Two origins would mean two sets of cookies and a session that appears to vanish
when somebody follows a link on the wrong one. The redirect keeps the host from the request,
so whatever name the user reached the machine by is the name they keep. `307` rather than
`301`, because permanent redirects are cached indefinitely and a machine that later has to
serve plain HTTP during recovery would be unreachable from every browser that had ever seen
the old answer.

### The name is published over mDNS, by avahi

`homebase-core` depends on `avahi-daemon`, so a machine that has Homebase on it publishes its
name. `hostd` reports `mdns_works` only when the responder is actually running, checked by
asking `systemctl` rather than by assuming the package is installed — a responder that is
installed and stopped publishes nothing, and the difference is invisible from here otherwise.
Claiming a name the network cannot resolve is worse than claiming none, because it sends
somebody to type an address that will never load.

### The network is reported, not configured

`network.status` is read-only, and it is the only network operation. It reads
`/sys/class/net`, `/proc/net/route` and `/etc/resolv.conf` live on every request rather than
remembering anything, and it tests reachability by opening a TCP connection to two well-known
resolvers **by address** — ICMP is blocked on plenty of networks, and resolving a name first
would make a broken resolver look like a broken connection.

It reports `reachable` and `online` separately, and that separation is the point of the whole
screen. A server with a good address on a network whose broadband is down is a working
server. Telling its owner "not connected" sends them to restart a router for an hour over a
problem with their phone's Wi-Fi.

Changing the network from the dashboard is a much larger surface — it can strand the machine
it is running on — and it is not in this decision.

## Alternatives considered

### Plain HTTP, on the grounds that a home network is private

The status quo, and it has a real argument behind it: everything here is local, and TLS on a
LAN adds a warning screen that the alternative does not have. Rejected because "private
network" is an assumption about other people's devices, not a property anybody controls, and
because an administrator password is the one credential in the system that opens everything
else.

### A publicly trusted certificate from Let's Encrypt

No warning at all, which is worth a great deal. Rejected for three reasons that compound.
HTTP-01 validation needs the machine reachable from the internet, which Homebase deliberately
is not. DNS-01 needs a domain the user owns and an API token for their DNS provider stored on
the server — a credential that can repoint their real domain, held on an appliance, to solve
a local problem. And every issued certificate is published in Certificate Transparency logs,
so every household running Homebase would publish the existence and name of their server to a
public, permanently archived index.

### A private CA whose root the user installs on their devices

Genuinely better on the security merits: check a fingerprint once, install one root, and
every renewal afterwards is silent with no warnings ever again. Rejected because the cost
lands on the wrong person. Installing a root CA is several screens deep in the settings of
every phone, laptop and tablet in the house, differs on each, and — done wrong, or left
installed on a device that is later sold — weakens that device's browsing everywhere, not
just against Homebase. Asking a non-technical user to permanently expand what their phone
trusts, in order to reach a server in their own house, is a worse trade than one warning.

### A hosted rendezvous service, the way commercial NAS boxes do it

A vendor-run name and relay removes both the warning and the discovery problem in one move.
Rejected outright: it requires an account, makes the product depend on a service that can be
switched off or start charging, and routes household traffic through somebody else's
machines. This contradicts the first principle in the README, and no amount of convenience
buys it back.

### Running core as root, or putting a root-owned proxy in front

Both bind 443 without a capability. Running `core` as root is prohibited by
[ADR-0006](0006-privilege-split.md) and is not open for discussion. A root-owned nginx or
Caddy in front is respectable and common, and was rejected as a poor trade for what it buys
here: a second configuration surface, a second thing to keep patched, a second place TLS can
be misconfigured, and another root process on the machine — to avoid one narrowly-scoped
kernel capability that permits binding a low port and nothing else.

## Consequences

### What this costs

**The browser warns, once per device.** This is the real price and it is not hidden. Teaching
people to click through security warnings is its own harm, and this decision does some of
that harm in exchange for encrypting the password. The fingerprint on the machine's screen is
what makes it checkable; the ten-year lifetime is what keeps it to once.

**A ten-year certificate cannot be revoked in any way a browser checks.** Accepted: the key
sits `0600` on the server's own disk, and an attacker who can read it already has the machine
and does not need the certificate. Deleting the two files and restarting `core` regenerates
the pair — which invalidates the trust every device granted, and correctly so.

**mDNS does not work everywhere.** Multicast is filtered on some guest and enterprise
networks and does not cross a router. This is why the dashboard shows addresses as well and
why `mdns_works` is reported honestly instead of assumed.

**Ports 80 and 443 belong to Homebase.** An application in the catalogue cannot have them.
Acceptable, because the installer takes the whole machine ([ADR-0016](0016-installation-media.md))
and the dashboard is what the machine is for — but it is a constraint on every future app
manifest.

**The name is announced to the local network.** mDNS is broadcast by design. Anybody already
on the network could enumerate the machine anyway; nothing secret is published, and it does
not leave the network segment.

### Security impact

Passwords and session cookies no longer cross the local network in clear, and the `Secure`
attribute on the session cookie now means what it says — it is set from whether the request
actually arrived over TLS, rather than being asserted unconditionally on a connection that
was not.

The new surface is a TLS listener and a private key on disk. `hostd` is unchanged: no
privileged operation was added, nothing moved across the boundary, and the certificate is
generated by the unprivileged service because nothing about it needs privilege.
`CAP_NET_BIND_SERVICE` is an increase over "no capabilities at all", bounded to binding
privileged ports, with `NoNewPrivileges=yes` alongside it.

### What would make us revisit this

- **Browsers removing the ability to accept a self-signed certificate**, or burying it deeply
  enough that a non-technical user cannot get through. The trend has been in this direction
  for a decade and this decision does not survive the end of it.
- **A standard for local-network certificates with actual browser support.** This problem is
  not Homebase's alone, and a real answer would replace all of the above.
- **Evidence that households commonly block multicast**, which would make mDNS discovery a
  minority path and the address the primary one.
- **Homebase gaining a legitimate reason to be reachable from the internet**, at which point
  the name, the certificate and the threat model all change together and this record should
  be superseded rather than amended.
