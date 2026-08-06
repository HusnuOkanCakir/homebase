# ADR-0005: Docker Engine as the initial container runtime

- **Status:** Accepted
- **Date:** 2026-08-06

## Context

Applications run in containers: it gives isolation, a declarative image, and an ecosystem
where the software users want — Jellyfin, File Browser, and the rest — is already packaged
and maintained by people who are not us.

The runtime choice affects what images work, what isolation is available, how much runs as
root, and how much of the user's disk goes on the runtime rather than their data.

One constraint dominates: **`core` must never talk to the container runtime.** Access to the
Docker socket is equivalent to root, so a runtime reachable only through `hostd` is not a
detail of this decision, it is a precondition of it. See
[ADR-0006](0006-privilege-split.md).

## Decision

Docker Engine, controlled exclusively by `hostd`. `core` has no access to the Docker socket.

This is explicitly the *initial* runtime. The application manifest schema describes
containers in runtime-neutral terms so that changing it later is a `hostd` change, not a
catalogue rewrite.

## Alternatives considered

### Podman

The better choice on security, and it was close. Rootless containers, no long-running
privileged daemon, and systemd integration that fits how the rest of Homebase is supervised.
A compromised application in a rootless container is meaningfully less dangerous.

Rejected for Milestone 3 on compatibility friction. Rootless Podman has real rough edges that
land precisely where this project cannot afford them: port binding below 1024, user namespace
mapping for bind-mounted user data, and hardware access for Jellyfin's GPU transcoding. Each
is solvable, and each would be solved by us rather than by upstream documentation the user
might find.

Docker also remains what upstream projects test against and write instructions for. When a
user compares their Homebase Jellyfin against Jellyfin's own documentation, the fewer
differences the better.

**This is the alternative most likely to win later.** The abstraction in the manifest schema
exists specifically to keep that door open.

### containerd directly

Lower level, fewer moving parts, what Docker uses underneath. Rejected because we would then
implement networking, volume management and image handling ourselves — recreating Docker,
worse, in a component that runs as root.

### systemd-nspawn, or no containers

Would avoid a runtime entirely and fit the systemd-centric design. Rejected because it gives
up the ecosystem, which is most of the value. Users want Jellyfin, and Jellyfin ships a
container image.

## Consequences

### What this makes easier

- Every application users ask for is already packaged and tested against Docker
- Upstream documentation broadly applies, which reduces the support surface
- GPU passthrough for transcoding is well-trodden
- Networking, volumes and image management are somebody else's problem
- Familiar to contributors

### What this makes harder

- **A privileged daemon runs permanently.** Docker's socket is root-equivalent, so `hostd`
  becomes the only thing that may touch it — and that rule is now load-bearing rather than
  stylistic
- Disk footprint: images plus the daemon on a machine whose value is its remaining space
- Docker manages its own networking and iptables rules, which will complicate the firewall
  work in Milestone 7
- Containers are not a strong security boundary. A container escape is a host compromise,
  and Homebase's application isolation is therefore weaker than the word "isolation" suggests
- Docker's storage location must be managed deliberately, or images silently fill the disk

### Security impact

The largest single compromise in the architecture, and worth stating without softening.

Docker requires a root daemon, and access to its socket is equivalent to root. That is
precisely the sort of capability the rest of this design works to avoid. The mitigations are
real but partial:

- Only `hostd` may reach the socket. `core`, the dashboard, applications and the future AI
  operator have no path to it
- `hostd` exposes named container operations — `app.start`, `app.stop` — never arbitrary
  Docker API calls
- Applications get declared, minimal permissions: no privileged mode, no host networking
  without justification, capabilities dropped by default, read-only root filesystem where the
  image tolerates it
- Only the curated catalogue is installable, so the images are ones we have tested

What remains, honestly: an attacker who compromises `hostd` owns the machine, and a container
escape is a host compromise. The threat model states this rather than implying otherwise. See
[threat model](../security/threat-model.md).

### What would make us revisit this

- Rootless Podman resolving port binding, user-namespace mapping and GPU access well enough
  that the migration cost is the only remaining obstacle
- A Docker escape affecting Homebase users in practice
- Disk footprint becoming a common complaint on target hardware
- Docker's iptables management proving genuinely incompatible with the firewall design
