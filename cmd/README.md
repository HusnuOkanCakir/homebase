# cmd/ — executables

One directory per binary. Nothing here yet; these land in Milestone 2.

| Binary | Runs as | Purpose |
|---|---|---|
| `core` | unprivileged `homebase` user | HTTP API, authentication, jobs, app metadata, events, audit history, SQLite state. The only thing the dashboard talks to. |
| `hostd` | `root` | Privileged host operations — container lifecycle, mounting, network configuration, system updates, power management. Accepts a **fixed set of typed operations** over `/run/homebase/hostd.sock`. |
| `homebasectl` | invoking user | Command-line client. Also builds installer USB media before the graphical controller exists. |

## The rule that governs this directory

`hostd` must never gain a generic command-execution operation. Every privileged capability
is a named, schema-validated, individually reviewed operation with a declared risk level and,
where possible, a rollback. This is not a temporary simplification — it is the property that
makes the Stage 2 AI operator safe to build.

See [ADR-0006](../docs/decisions/0006-privilege-split.md) and
[docs/security/privilege-boundaries.md](../docs/security/privilege-boundaries.md).
