# internal/ — private packages

Go's `internal/` visibility rule keeps these importable only from within this module. That
is deliberate: Homebase's stable, supported surface is the **HTTP API**
([`api/openapi.yaml`](../api/openapi.yaml)), not its Go types.

| Package | Owns |
|---|---|
| `hostd` | **Built.** The privileged service: operation registry, socket server, audit log |
| `api` | HTTP handlers, routing, request/response types, error envelope |
| `auth` | Sessions, password hashing, permission checks, credential references |
| `jobs` | Long-running operations: queue, progress, cancellation, idempotency, rollback |
| `system` | System inventory and resource readings (via `hostd`) |
| `containers` | Application lifecycle on top of the container runtime |
| `storage` | Disk discovery, mounts, managed storage locations |

`hostd` is built. The rest land in Milestone 2 and after.

## Layering

`api` → `jobs` → (`system` | `containers` | `storage`) → `hostd` client.

No package below `api` may import `api`, and nothing except the `hostd` client may open the
privileged socket. Enforced by review now, by a lint rule once the code exists.
