# ADR-0002: Go for services, React and TypeScript for the dashboard

- **Status:** Accepted
- **Date:** 2026-08-06

## Context

`core` and `hostd` run on hardware chosen for being spare rather than capable: an old laptop
with perhaps 8 GB of memory, much of which belongs to the applications the user actually
wanted. They start at boot, run for months, and must be debuggable by someone who did not
write them.

Deployment constrains the choice more than performance does. Homebase installs itself onto a
machine; a runtime that must be installed and kept in step with the application is a second
thing that can be wrong at 11pm.

`hostd` runs as root, so its language choice is a security decision. Memory-safety matters,
and so does the size of the dependency tree — every transitive dependency in a root process
is code that runs as root.

## Decision

- **`core`, `hostd`, `homebasectl`:** Go
- **Dashboard:** React with TypeScript, built to static assets

## Alternatives considered

### Rust for the services

The stronger choice on the merits that matter most here: no garbage collector, stricter
guarantees than Go's, and a better story for a root process.

Rejected on maintainability rather than technical grounds. Homebase needs to be contributed
to and audited by people who are competent but not specialists, and `hostd` in particular
needs to be readable by someone deciding whether to trust it with root on their own machine.
Go's smaller conceptual surface makes that a lower bar. Compile times matter too, on a
project where the test loop involves booting a VM.

This is the closest call in this document, and Rust would be a defensible choice for `hostd`
alone if it grows beyond what one person can review.

### Python for the services

Fast to write, and the ecosystem is excellent. Rejected on deployment: a Python service means
an interpreter and a dependency tree on the appliance, versioned against a distribution that
also wants opinions about Python. A single static binary removes an entire category of
"worked in development" failure.

Milestone 0's tooling *is* Python — but that runs on a developer's machine, not on the
appliance. See [ADR-0009](0009-python-docs-toolchain.md).

### Server-rendered HTML instead of a single-page application

Genuinely tempting: less JavaScript, smaller attack surface, no build step, works better on
an old browser.

Rejected because of [jobs](../architecture/jobs.md). The interface is mostly long-running
operations with live progress — installing an application, running a backup, watching a
restore. That is a state-synchronisation problem, and server-rendered pages solve it by
accumulating exactly the machinery an SPA already has.

### Svelte or Vue instead of React

Both would work, and both produce smaller bundles. React was chosen for the size of the pool
of people who can contribute to it without learning something first. For a project whose
success depends on attracting contributors, that outweighs a bundle-size difference nobody
on a home network will notice.

## Consequences

### What this makes easier

- Static binaries: no runtime to install, no dependency to drift, trivial `.deb` packaging
- Predictable memory use, which matters when competing with the user's actual applications
- Cross-compilation, including for the desktop controller
- Fast tests, so the VM-based loop is the only slow part
- A standard library good enough that `hostd` can have very few dependencies

### What this makes harder

- Go's error handling is verbose, and the parts of `hostd` that must handle every failure
  path will be the most verbose of all
- Node 20+ becomes a build dependency for the dashboard — notably, the current development
  machine has Node 12 and will need upgrading at Milestone 2
- Two languages means two toolchains, two linters and two CI paths
- Go's GC pauses are irrelevant here but its memory *baseline* is not, on a 4 GB machine

### Security impact

Positive. Memory safety in a root process removes the vulnerability class that would
otherwise dominate `hostd`'s threat model. Static linking means no shared-library
substitution at load time.

The main residual risk is dependency count. `hostd` should stay close to the standard
library, and adding a dependency to it is a security review, not a build change.

The dashboard's npm dependency tree is the larger supply-chain exposure — but it runs in the
user's browser, not as root, and Dependabot plus `dependency-review` cover it.

### What would make us revisit this

- `hostd` growing past what one reviewer can hold in their head — at which point rewriting
  it in Rust becomes worth the cost
- A dependency in `hostd` that cannot be avoided and cannot be audited
- The dashboard bundle becoming slow enough on target hardware to matter
