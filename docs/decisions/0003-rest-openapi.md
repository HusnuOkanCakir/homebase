# ADR-0003: Versioned REST with OpenAPI as the single contract

- **Status:** Accepted
- **Date:** 2026-08-06

## Context

The `core` API will have three consumers with quite different characteristics:

1. **The dashboard** — written against the API by the same people who write the API
2. **`homebasectl`** — same, plus scripting by users
3. **The Stage 2 AI operator** — a local language model constructing requests from a
   description of what is available

The third is the one that shapes this decision. A model calling an API needs the interface
to be *describable*: it must be possible to hand it a machine-readable specification of every
operation, its parameters, its constraints and its errors, and have that description be
complete and current. An API whose real behaviour lives in code and whose documentation is a
hand-maintained approximation is one a model will get wrong in ways nobody predicts.

## Decision

A versioned REST API over HTTP with JSON, specified in OpenAPI 3.1. The specification is the
contract: it is written first, validated in CI, and reference documentation is generated from
it.

## Alternatives considered

### gRPC

Better on the technical merits taken alone: a real schema language, generated clients,
streaming, efficient encoding. Streaming would suit job progress well.

Rejected on reach. gRPC from a browser needs a proxy layer, which puts a translating
component in the path of every dashboard request — and that component would sit exactly where
the privilege boundary needs to be legible. Debugging also matters more than it looks: a user
reporting a problem can be asked to visit a URL, and a maintainer can reproduce it with
`curl`. Neither is true of gRPC without tooling.

The AI argument cuts the same way. A protobuf service definition is a fine machine-readable
contract, but the surrounding ecosystem for describing tools to models is built around JSON
schemas and HTTP.

### GraphQL

Solves over-fetching, which is not a problem Homebase has — the dashboard shows a dozen
applications on a local network, not a social graph.

Rejected because it makes the security model harder in exactly the wrong place. A single
endpoint accepting arbitrary queries means authorisation must be enforced per field rather
than per operation, and a query's cost is not knowable before executing it. For an interface
that an automated operator will call, "every request names one operation with a fixed shape"
is worth considerably more than query flexibility.

### REST without a formal specification

The path of least resistance: write handlers, document them in Markdown.

Rejected outright. Hand-written API documentation drifts from the implementation, and drifted
documentation is worse than none because it is believed. For the AI operator it would be
actively dangerous: the model's understanding of what it can do would be a document nobody
verified.

### JSON-RPC

Simpler than REST and a reasonable fit for an operation-oriented API. Rejected for tooling:
OpenAPI has better validators, documentation generators and client generators, and this
project would rather adopt an ecosystem than assemble one.

## Consequences

### What this makes easier

- One contract that the dashboard, CLI, tests, documentation and Stage 2 all derive from
- CI can validate the specification, and later assert the implementation matches it
- Reference documentation is generated, so it cannot drift
- Debuggable with `curl` and a browser; reproducible in a bug report
- Adding a client language costs nothing

### What this makes harder

- OpenAPI 3.1 is verbose, and the specification file will be long before it is interesting
- Writing the contract before the implementation is slower up front, and occasionally means
  discovering during implementation that the contract was wrong
- REST has no streaming, so job progress is polled rather than pushed. Acceptable on a LAN;
  revisit with SSE if polling becomes noticeable
- Keeping the implementation faithful to the specification requires contract tests that do
  not exist yet

### Security impact

Positive, mostly by making the surface knowable. Every endpoint, parameter and permission is
enumerated in one reviewable file, so "what can an authenticated client do?" has an answer
that can be read rather than inferred.

Schema validation at the edge also removes a class of malformed-input bugs before any handler
runs.

The residual risk is the specification being wrong — describing an endpoint that does not
exist, or omitting one that does. Contract tests in Milestone 2 are the mitigation, and they
are not optional given that a model will be reading this file.

### What would make us revisit this

- Job progress polling proving too slow or too chatty on real hardware — SSE within REST is
  the first answer, not a protocol change
- A future need for high-frequency structured telemetry between components, where gRPC
  between `core` and `hostd` specifically might make sense while the public API stays REST
