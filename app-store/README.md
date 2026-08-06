# app-store/ — curated application catalogue

One manifest per installable application, validated against
[`schemas/app-manifest.schema.json`](../schemas/app-manifest.schema.json) in CI.

Homebase deliberately ships a **small, tested catalogue** rather than an open app store. Each
manifest is a claim that the Homebase project has verified this application installs, starts,
stops, survives a reboot, backs up and uninstalls cleanly — a claim we can only make about
applications we actually test.

The first release targets three:

| App | Why |
|---|---|
| `hello-homebase` | Trivial internal test app. Exercises the whole lifecycle without a large image download, and is the fixture the app-management tests run against. |
| `jellyfin` | The realistic case: large image, user-selected media storage, optional GPU, real health endpoint. |
| `filebrowser` | Small, useful immediately, and gives users a way to get files onto the server before anything else exists. |

## Adding an application

A new manifest is not a documentation change. It requires evidence that install, start,
stop, restart, reboot-persistence, backup, restore and uninstall were all exercised — see
[docs/development/testing.md](../docs/development/testing.md). Manifests describing software
we have not tested do not belong here, however popular the software is.
