# packaging/ — distribution artifacts

Debian packaging for `core` and `hostd`, plus their systemd units. Lands in Milestone 2,
hardened in Milestone 8 (signing, SBOMs, attestations).

| Package | Contents |
|---|---|
| `homebase-core` | `core` binary, systemd unit, default config, `homebase` system user |
| `homebase-hostd` | `hostd` binary, systemd unit and socket, socket permissions |
| `homebase-dashboard` | Built static assets |

## Packaging is a security boundary

The socket permissions, the unprivileged user, and the systemd hardening directives
(`ProtectSystem`, `PrivateTmp`, `NoNewPrivileges`, capability bounding sets) are what turn
the privilege split from an architectural intention into something the kernel enforces. A
change here can silently undo [ADR-0006](../docs/decisions/0006-privilege-split.md) without
touching a line of Go.

Changes require the `risk/security` label and security-maintainer review.

## Upgrades must never destroy data

Package upgrades run on machines holding the user's only copy of their photographs. Postinst
scripts must be idempotent, must migrate rather than recreate, and must be tested by the
upgrade CI matrix against the previous release before merge.
