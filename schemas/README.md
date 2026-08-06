# schemas/ — machine-validated contracts

JSON Schemas that CI enforces on every pull request.

| Schema | Governs |
|---|---|
| [`app-manifest.schema.json`](app-manifest.schema.json) | Installable application manifests in [`app-store/`](../app-store/) |
| [`error.schema.json`](error.schema.json) | The API error envelope; mirrors `Error` in [`api/openapi.yaml`](../api/openapi.yaml) |

## Fixtures

`examples/` holds fixtures for both, and the invalid ones matter more than the valid ones. A
schema that accepts everything passes every positive test ever written; only a rejection test
proves a constraint is doing anything.

Each invalid fixture is registered in
[`examples/expectations.json`](examples/expectations.json) with the JSON pointer and schema
keyword that must reject it:

```json
"invalid-privileged-container.json": {
  "why": "Requests a privileged container...",
  "path": "/permissions/privileged",
  "keyword": "const"
}
```

`scripts/validate_contracts.py` asserts the fixture is rejected **by that specific
constraint**. Checking only that it was rejected somehow would let a fixture keep passing
after a typo made it invalid for an unrelated reason — while the constraint it was written to
protect had quietly stopped working.

## Adding a constraint

Add the invalid fixture that proves it bites, and register it in `expectations.json`.

A constraint without a rejection test is a comment.

## Constraints worth not weakening

Three exist to make a guarantee mechanical rather than a matter of review attention:

- **`permissions.privileged` is `const: false`.** A privileged container is a root shell on
  the host. No catalogue application gets one, and the schema is where that is enforced
- **`container.version` cannot be `latest`.** An application that changes version silently
  underneath a user is one that breaks silently
- **No host paths in `storage`.** Applications declare storage by role; Homebase decides
  where it lives. A host path would let a manifest escape `/srv/homebase/`

```sh
make validate
```
