# Versioning

[Semantic versioning](https://semver.org/), with one pre-1.0 caveat that matters more than
the usual one.

## The whole appliance has one version

Homebase releases as a single versioned product. A user installs *Homebase 0.4.2*, not a
compatible set of components ([ADR-0001](../decisions/0001-monorepo.md)).

That is also the version they will quote in a bug report, which is worth more than the
flexibility of independent component versions.

## Pre-1.0

**Minor releases may contain breaking changes.** Patch releases contain only compatible
fixes.

The usual convention — but here is what it does **not** excuse:

!!! danger "Every release must migrate existing installations"

    A user who installed 0.3.0 must reach 0.4.0 with their photographs, applications and
    configuration intact.

    Breaking an API before 1.0 is acceptable. Breaking somebody's server is not, at any
    version number.

The API contract can change. The promise that user data survives cannot.

## After 1.0

| Increment | For |
|---|---|
| **Major** | Breaking API change, on-disk format change requiring intervention, dropped hardware support |
| **Minor** | New capability, new endpoint, new application, compatible schema change |
| **Patch** | Bug fix, security fix, documentation |

## Prereleases

```text
v0.2.0-alpha.1     Feature-complete for the milestone, expect problems
v0.2.0-beta.1      Promoted alpha, tested on hardware
v0.2.0             Stable
v0.2.1             Patch
```

Prerelease identifiers match the channel the artifact is promoted through — see
[process](process.md).

## Upgrade paths

Every release records the oldest version it can upgrade from:

```yaml
version: 0.4.0
minimum_upgrade_from: 0.2.0
```

A machine on 0.1.0 is told to install 0.2.0 first. This is enforced rather than advised,
because a migration nobody tested is exactly the situation that loses data.

The upgrade CI matrix tests the paths that are claimed to work. A path that is not tested is
not offered.

## Schema versions

Three versions change independently of the release version and are recorded in release
metadata:

| Version | Covers | Changing it means |
|---|---|---|
| **Database schema** | SQLite structure | A migration, tested against real upgrades |
| **App manifest schema** | `schemas/app-manifest.schema.json` | Catalogue revalidation |
| **API version** | `/api/v1/` | See [API conventions](../architecture/api-conventions.md#versioning) |

## Deprecation

Before 1.0, a breaking change ships in a minor release with the migration documented in the
changelog.

After 1.0: mark deprecated with a replacement documented, keep it working for at least two
minor releases, warn in responses and logs, then remove in the next major.

Nothing is removed in a patch release. Ever.
