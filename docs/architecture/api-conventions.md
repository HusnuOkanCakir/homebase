# API conventions

The `core` HTTP API is Homebase's only supported interface. The dashboard uses it, the CLI
uses it, and the Stage 2 AI operator will use it. Nothing bypasses it.

Because it has more than one consumer — and one of them generates its requests from a
language model — the conventions below lean towards being explicit rather than terse.

## Versioning

Everything lives under a version prefix:

```text
/api/v1/system
```

Within `v1`, these are compatible changes and may ship at any time:

- Adding an endpoint
- Adding an optional request field
- Adding a response field
- Adding a new value to an open-ended enumeration

These require `v2`:

- Removing or renaming anything
- Making an optional field required
- Changing a type or the meaning of a field
- Removing an enumeration value

When `v2` arrives, `v1` keeps working for at least two minor releases with a deprecation
notice in the response headers.

## Requests

JSON in, JSON out. `snake_case` field names, matching the app manifest schema, so the same
vocabulary appears everywhere a user or a model might encounter it.

Timestamps are RFC 3339 in UTC: `2026-08-06T14:22:31Z`. Durations are seconds, as numbers.
Sizes are bytes, as numbers — never "1.2 GB", which is a rendering decision for the client.

## Responses

### Reads

```http
GET /api/v1/apps/jellyfin
```

```json
{
  "id": "jellyfin",
  "name": "Jellyfin",
  "state": "running",
  "health": "healthy",
  "version": "10.9.11",
  "installed_at": "2026-07-14T09:02:11Z"
}
```

### Writes

Every mutating endpoint returns `202 Accepted` with a job, never the finished result:

```json
{
  "job_id": "job_01HQ8X2K3M",
  "state": "queued",
  "created_at": "2026-08-06T14:22:31Z"
}
```

Uniformly — even for operations that are usually instant. A client that has to know which
operations are fast is a client that breaks the first time a "fast" one is not. See
[jobs](jobs.md).

### Collections

```json
{
  "items": [...],
  "total": 42,
  "next_cursor": "eyJpZCI6ImpvYl8wMUhROFgySzNNIn0"
}
```

Cursor-based. Offsets skip or duplicate entries when the underlying set changes between
requests, which for a job list is normal rather than exceptional.

## Errors

One envelope, everywhere:

```json
{
  "error": {
    "code": "storage.disk_not_found",
    "message": "The backup disk is not connected.",
    "detail": "No disk with serial WD-WCC4N0304 is currently attached.",
    "recoverable": true,
    "recovery": "Reconnect the backup disk and try again.",
    "request_id": "req_01HQ8X2K3M",
    "documentation_url": "https://homebase.dev/errors/storage.disk_not_found"
  }
}
```

| Field | Purpose |
|---|---|
| `code` | Stable, machine-readable, `domain.reason`. Never changes once shipped. |
| `message` | One sentence, for a person who does not know what a mount point is. |
| `detail` | Specifics, for someone diagnosing. May name paths and devices. Never secrets. |
| `recoverable` | Whether retrying could plausibly work |
| `recovery` | What the user can actually do |
| `request_id` | Correlates with logs and the audit trail |

`code` is the contract; `message` is not. Clients switch on `code`. Translating or improving
a `message` is a compatible change; altering what a `code` means is not.

### Codes

`domain.reason`, where domain matches the API area:

```text
auth.invalid_credentials      auth.session_expired
apps.not_found                apps.already_installed
apps.health_check_failed      apps.image_pull_failed
storage.disk_not_found        storage.insufficient_space
storage.mount_failed          storage.disk_in_use
network.no_connectivity       network.dns_resolution_failed
system.operation_not_permitted
jobs.not_found                jobs.conflict
```

The registry is generated from `api/openapi.yaml`; that file is the source of truth.

### Status codes

| Status | Meaning |
|---|---|
| `200` | Read succeeded |
| `202` | Mutation accepted; poll the job |
| `400` | Malformed request |
| `401` | Not authenticated |
| `403` | Authenticated, but not permitted |
| `404` | No such resource |
| `409` | Conflicts with current state (job already running, app installed) |
| `422` | Well-formed but semantically invalid (disk too small) |
| `429` | Rate limited |
| `500` | Bug in Homebase |
| `503` | Temporarily unavailable (`hostd` unreachable, still starting) |

The distinction between `400` and `422` matters for an automated client: `400` means the
request was built wrong and retrying it unchanged is pointless, `422` means the request was
built correctly but the world is not in a state where it can be satisfied.

## Authentication

Session cookies for the dashboard — `HttpOnly`, `Secure`, `SameSite=Lax`. API tokens for the
CLI and, later, the AI operator, as `Authorization: Bearer <token>`.

Tokens carry a permission set and are individually revocable. The Stage 2 operator gets its
own token with a deliberately narrow set, so that revoking the AI's access does not mean
revoking the user's.

## Permissions

Checked in `core`, before any request reaches `hostd`:

```text
system.read      system.manage
apps.read        apps.manage
storage.read     storage.modify
network.diagnose network.modify
backup.read      backup.run
media.play
```

Read and write are separate throughout. That split is what makes Stage 2A — an AI that can
explain the server but change nothing — expressible as a token rather than as a promise.

## Idempotency

Mutating requests should carry `Idempotency-Key`. A repeated key returns the original job.
See [jobs](jobs.md#idempotency).

## Rate limiting

Applied per token. The limits are generous, and exist to contain a client stuck in a loop
rather than to ration legitimate use — a home server has one household on it.

Rate-limited responses carry `Retry-After`.

## Documentation is the contract

[`api/openapi.yaml`](https://github.com/HusnuOkanCakir/homebase/blob/main/api/openapi.yaml)
is authoritative. It is validated on every pull request, and the reference documentation is
generated from it rather than written alongside it — hand-written API docs drift, and a
drifted API document is worse than none, because it is believed.
