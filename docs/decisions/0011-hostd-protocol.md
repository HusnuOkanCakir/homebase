# ADR-0011: HTTP over a Unix socket, with a Go registry as the operation schema

- **Status:** Accepted
- **Date:** 2026-08-07
- **Implements:** [ADR-0006](0006-privilege-split.md)

## Context

[ADR-0006](0006-privilege-split.md) fixes *what* the boundary between `core` and `hostd`
is: a fixed set of named, typed, audited operations with no generic execution path. It
deliberately says nothing about *how* they travel, because that is an implementation
question.

It is not a small one. The protocol chosen here is the precedent every future operation
follows, and the schema mechanism decides whether the declared risk level of an operation is
a real property or a comment.

Two constraints narrow the field. `hostd` runs as root, so every dependency in it is code
running as root — [ADR-0002](0002-implementation-languages.md) commits to keeping it close
to the standard library. And when something goes wrong at 11pm, whoever is debugging needs
to be able to see what is happening without first building a tool.

## Decision

**HTTP/1.1 with JSON bodies, over the Unix socket, using Go's `net/http`.**

Operations are `POST /v1/op/<name>`, dispatched from a compiled-in registry. Two read-only
endpoints exist alongside them: `/v1/health` and `/v1/operations`.

**The Go registry is the schema.** One table declares each operation with its typed request
struct, risk, permissions, confirmation requirement, timeout and rollback. Request bodies are
decoded strictly — unknown fields are an error. The registry exports itself as JSON via
`hostd --describe`.

**The kernel identifies the caller**, via `SO_PEERCRED`, not anything in the request.

## Why HTTP is not a generic execution path

The obvious objection: ADR-0006 requires that the set of things that can happen be
enumerable in advance, and HTTP paths are arbitrary strings.

Paths are looked up in a compiled-in map. There is no prefix matching, no aliasing, no
fallback handler and no dynamic dispatch. An unrecognised name is a 404 and is audited as an
attempted invocation of something that does not exist. The set of reachable operations is
exactly the set in the registry, which is exactly the set that was reviewed.

HTTP is the transport. It is not a way to reach code.

## Alternatives considered

### Newline-delimited JSON

Minimal and legible on the wire. Rejected because framing, request correlation, timeouts,
concurrency and back-pressure all become our code — in the root process. Go's `net/http` has
had those problems solved and tested for a decade, and reimplementing them in the most
security-sensitive component of the system to save a dependency we would not have added
anyway is a poor trade.

### Length-prefixed JSON

The most robust framing and the least code. Rejected on debuggability: nothing off-the-shelf
can inspect it, so every debugging session starts by writing a client. That cost lands
exactly when it is least affordable.

### gRPC

A real schema language, generated clients, strong typing. Rejected because it puts `grpc-go`
and protobuf into a process running as root, which is precisely what ADR-0002 exists to
prevent. [ADR-0003](0003-rest-openapi.md) rejected gRPC for the public API on related
grounds; the argument is stronger here, where the process is privileged.

### JSON Schema files as the source of truth

Language-neutral, and directly readable by the future Stage 2 policy engine — which is a
genuine advantage, since that engine must reason about what each operation can do.

Rejected because it creates two sources of truth. The Go struct and the schema file would
have to be kept in step by hand, and the failure mode when they drift is the worst kind: a
request that validates against the schema and decodes into a struct that means something
else. Exporting JSON *from* the registry gets the same machine-readable description with one
source of truth.

## Consequences

### What this makes easier

- Zero non-stdlib dependencies in the privileged process, enforced by a CI check rather than
  by intention
- Debuggable by hand:
  `sudo curl --unix-socket /run/homebase/hostd.sock -X POST http://hostd/v1/op/system.get_info -d '{}'`
- Framing, timeouts and concurrency are stdlib-tested
- One table to read to know the entire privileged surface, and `--describe` to export it
- Adding an operation is a line in a reviewed table; forgetting to declare a risk level, a
  timeout or a permission is a startup failure rather than a silent gap

### What this makes harder

- An HTTP parser runs in a root process. It is Go stdlib and memory-safe, and the socket is
  `0660 root:homebase` so only `core` can reach it — but the surface is larger than a
  bespoke protocol's would be, and that is a real cost rather than one to argue away
- HTTP has no native streaming for progress, so long-running operations will need the job
  model rather than a streaming response
- The registry is Go, so a non-Go consumer reads the exported JSON rather than the source.
  Acceptable, because the export is generated and cannot drift
- Strict decoding will occasionally reject a request that "obviously" meant something. That
  is the intent: a caller who misspells a field is a caller whose intent we do not know

### Security impact

Positive on balance, with one honest caveat.

The peer check is defence in depth behind the socket mode. Socket permissions are the
primary control; `SO_PEERCRED` is what still refuses the connection if a packaging change
widens the mode to `0666`, which is a change that would otherwise remove the boundary
without touching any Go.

Confirmation is enforced before the handler runs, and `ConfirmExplicit` requires the caller
to name the target — so a confirmation obtained for one machine cannot be spent on another.
That matters most in Stage 2, where the thing proposing the action is a model.

The audit record is written **before** the operation runs and again after. An action that
reboots the machine still leaves evidence it was attempted; without the first record the log
would only be trustworthy for actions that finished.

The caveat: HTTP in a root process is more parser than a minimal protocol needs. Mitigated
by using the standard library rather than writing one, by the socket permissions, and by
`RestrictAddressFamilies=AF_UNIX AF_NETLINK` in the unit — `hostd` cannot open a network
socket at all.

### What would make us revisit this

- A vulnerability in Go's HTTP server that reaches a Unix-socket listener
- Long-running operations needing progress streaming, which HTTP/1.1 does not do well —
  though the job model in `core` is the intended answer rather than a protocol change
- `hostd` growing past what one reviewer can hold in their head, which
  [ADR-0002](0002-implementation-languages.md) already names as the trigger for
  reconsidering its language
