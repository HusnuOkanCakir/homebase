<!--
Title this PR as a Conventional Commit — it becomes the commit message on main.
  feat(core): add system inventory endpoint
See CONTRIBUTING.md.
-->

## What changed?

<!-- What a reviewer needs to know to read the diff. -->

## Why?

<!-- The problem being solved. Link the issue: Closes #123 -->

## How was it tested?

<!--
What you actually ran, not what you intend to run. Include failure paths.
"Not tested" is an acceptable answer for a docs-only change and nowhere else.
-->

## Security impact

<!--
Prose, not a checkbox. "None — documentation only" is fine when true.

It is NOT the right answer if this PR touches: hostd or any privileged operation,
packaging or systemd units, authentication or permissions, the update path, network
exposure, secret handling, or anything parsing input from off the machine.

If a trust boundary moved, say which one and update docs/security/threat-model.md.
-->

## Data and migration impact

<!--
Prose, not a checkbox. "None" is fine when true.

Required if this PR touches: storage, backups, the database schema, on-disk formats,
or package upgrade scripts. Answer this question: what happens to a user who already
has data and applications installed when they take this change?
-->

## Documentation

- [ ] User documentation updated
- [ ] Developer documentation updated
- [ ] API / schema reference updated
- [ ] ADR added or superseded
- [ ] No documentation change needed, because: <!-- explain -->

## Checklist

- [ ] Commits are signed off (`git commit -s`)
- [ ] Unit tests added or updated
- [ ] Integration tests added or updated, where components interact
- [ ] Failure paths tested, not only the success path
- [ ] Reboot behaviour tested, if this touches state on disk
- [ ] Existing user data survives an upgrade
- [ ] No secrets, credentials or generated artifacts committed
- [ ] `make check` passes locally

## Rollback

<!--
If this turns out to be wrong in production, how does a user get back? Reverting the
commit is enough for most changes. Say so if it is not — migrations, on-disk format
changes and packaging changes usually are not.
-->

## Risk labels

<!-- Apply any that fit. See CONTRIBUTING.md. -->

- [ ] `risk/destructive` — can erase or overwrite user data
- [ ] `risk/migration` — changes schema, on-disk format, or the upgrade path
- [ ] `risk/security` — touches the privilege boundary, auth, or the update path
