# internal/ — private packages

Go's `internal/` visibility rule keeps these importable only from within this module. That
is deliberate: Homebase's stable, supported surface is the **HTTP API**
([`api/openapi.yaml`](../api/openapi.yaml)), not its Go types.

| Package | Owns |
|---|---|
| `hostd` | **Built.** The privileged service: operation registry, socket server, audit log |
| `hostclient` | **Built.** The only thing in core permitted to open the privileged socket |
| `store` | **Built.** SQLite state and forward-only migrations |
| `api` | **Built.** HTTP handlers, routing, request/response types, error envelope |
| `auth` | **Built.** Sessions, password hashing, permission checks |
| `jobs` | **Built.** Long-running operations: progress, cancellation, idempotency |
| `containers` | Application lifecycle on top of the container runtime |
| `storage` | Disk discovery, mounts, managed storage locations |

`containers` and `storage` land in Milestones 3 and 4.

## Layering

`api` → `jobs` → (`containers` | `storage`) → `hostclient` → the privileged socket.

No package below `api` may import `api`, and **nothing except `hostclient` may open the
privileged socket**. That is what makes `git grep 'hostclient\.'` a complete list of the
privileged things core can do. Enforced by review; a lint rule would be better.
