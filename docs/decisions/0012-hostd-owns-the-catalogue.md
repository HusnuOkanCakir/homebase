# ADR-0012: hostd owns the application catalogue

- **Status:** Accepted
- **Date:** 2026-08-08
- **Implements:** [ADR-0006](0006-privilege-split.md)

## Context

Milestone 3 gives Homebase applications. Something has to turn "the user wants Jellyfin" into
a running container with the right image, ports, volumes, capabilities and resource limits.

That is a problem for [ADR-0006](0006-privilege-split.md), and the difficulty is worth
stating precisely rather than working around. Container creation is *parameterised by
definition*: an image reference, a set of mounts, a port. If `core` sends those parameters to
`hostd`, then `hostd` accepts a container specification — and a service that accepts a
container specification and hands it to Docker is a Docker proxy with a validation layer,
whatever the operations are named.

That fails ADR-0006's test. "Create a container from this specification" does not have an
enumerable set of outcomes. A specification is a program.

It also fails the specific thing the boundary exists to prevent. A compromised `core` that
can describe a container can describe one with `privileged: true`, or with `/` bind-mounted,
or with `CAP_SYS_ADMIN`. Validation would have to be perfect, and validation is a filter —
only as good as the imagination of whoever wrote it. This is precisely the "generic
operation with strong validation" middle ground ADR-0006 rejected, arriving in a different
costume.

## Decision

**`hostd` owns the catalogue. `core` never sends a container specification.**

Application manifests are files on disk under `/usr/share/homebase/apps/`, installed by
Debian packages, owned by `root` and not writable by `core`. `hostd` reads them, validates
them against
[`schemas/app-manifest.schema.json`](https://github.com/HusnuOkanCakir/homebase/blob/main/schemas/app-manifest.schema.json),
and constructs the container itself.

The operations `core` can invoke take an **application id and nothing else** of consequence:

```yaml
app.list          # what is installable on this machine
app.install       { id }
app.start         { id }
app.stop          { id }
app.restart       { id }
app.status        { id }
app.uninstall     { id }                    # keeps data
app.remove_data   { id, confirm: <id> }     # separate, critical, explicit
app.logs          { id, lines }
```

`id` must name a manifest `hostd` already has. An unknown id is rejected the same way an
unknown operation is.

**The set of containers that can exist is therefore the set of manifests on disk.** That set
is enumerable in advance, reviewable in a package diff, and outside `core`'s reach. A
compromised `core` can start and stop the applications the machine already has. It cannot
invent one, cannot alter one's permissions, and cannot describe a container at all.

### Where user choice still enters

Some manifest storage is `user-selected` — Jellyfin's media directory, for instance. That is
a genuine parameter, and it is handled by *narrowing* rather than by accepting a path:

```yaml
storage.assign { app: "jellyfin", storage: "media", location: "photos" }
```

`location` names a managed storage location `hostd` already knows about, from the same
enumerable set `storage.list_locations` returns. It is not a path. `core` cannot cause
`/etc/shadow` to be mounted into a container by sending a string.

## Alternatives considered

### core sends a validated container specification

The obvious design, and what most container managers do. `core` reads the manifest, builds a
specification, `hostd` validates it against a schema and creates the container.

Rejected for the reasons above. It puts a specification — an arbitrarily expressive object —
across the boundary, and makes the boundary's strength equal to the strength of a validator.
ADR-0006 rejected exactly this shape and predicted it would be proposed again in a different
form. It was; this is the form.

### hostd reads manifests, but core chooses the image tag

A narrower version: `core` passes `{id, version}` so a user can pin or upgrade.

Rejected because the image tag is the one field that decides *what code runs*. A caller who
can choose the tag can choose an image that is not the one reviewed. Version pinning is
real, and belongs in the manifest — an upgrade is a catalogue change, delivered as a package
update, which is the same path every other privileged change takes.

### A catalogue in the database, managed through the API

Would let a user add an application without a package update, which is the flexibility a
"third-party app store" needs.

Rejected, and this is the trade worth being explicit about. It would mean the set of
installable applications is data `core` can write, so a compromised `core` could add a
manifest and install it. It would also mean the catalogue is not reviewable in a diff. The
roadmap already lists arbitrary third-party app stores as out of scope; this decision is
what makes that scope boundary structural rather than a policy nobody enforces.

## Consequences

### What this makes easier

- The complete set of containers a machine can run is a directory listing
- Adding an application is a package change, reviewed like any other privileged change
- A compromised `core` cannot create, alter or escalate a container
- `hostd` validates manifests against the schema Milestone 0 wrote, giving that contract its
  first real consumer — and the invalid fixtures their first real purpose
- The Stage 2 operator inherits this for free: it can propose installing something from the
  catalogue and cannot describe an image

### What this makes harder

- **A user cannot install an application Homebase does not ship.** This is the cost, and it
  is a real one. It is also the same statement as "the catalogue is a set of applications we
  have tested", which the project already makes
- `hostd` grows: manifest parsing, container construction and the Docker client all live in
  the privileged process. That runs against keeping it small
  ([ADR-0002](0002-implementation-languages.md)), and is the strongest argument against this
  decision
- Trying an application means building a package, which is slower than editing a database row
- `hostd` gains its first non-standard-library dependency unless the Docker API is spoken
  directly over its socket. It is spoken directly — see below

### The dependency question

`hostd` talks to Docker over `/var/run/docker.sock` using `net/http` against the Engine API,
not the `docker/docker` client library. That library is enormous, and every line of it would
run as root in the process whose whole value is being small enough to read.

The Engine API is versioned, documented and stable, and Homebase uses a handful of
endpoints. This keeps the CI check that `hostd` has no third-party dependencies passing, and
that check is not a formality — it is what makes ADR-0002's commitment observable.

### Security impact

The central point of the decision.

What it prevents: a compromised `core` describing a container. No image reference, no mount,
no capability and no privileged flag crosses the boundary. The blast radius of owning `core`
is starting and stopping applications that are already installed.

What it does not prevent, stated plainly:

- **A malicious manifest in the catalogue.** The catalogue is reviewed by people, and people
  are fallible. `hostd` refuses `privileged: true` and rejects a manifest that fails the
  schema, but a manifest requesting a plausible-looking capability could get through review
- **A container escape.** Docker's daemon is root
  ([ADR-0005](0005-container-runtime.md)); this is unchanged and remains the largest accepted
  risk in the architecture
- **`hostd` bugs.** More code in a root process is more root-process code. Mitigated by the
  Engine API rather than a client library, and by review

### What would make us revisit this

- `hostd` growing past what one reviewer can hold in their head, which
  [ADR-0002](0002-implementation-languages.md) already names as the trigger for
  reconsidering its language — this decision brings that closer
- A genuine need for user-supplied applications, which would require rethinking the boundary
  rather than relaxing this
- Rootless Podman becoming viable ([ADR-0005](0005-container-runtime.md)), which would change
  the calculus for how much trust the container runtime needs
