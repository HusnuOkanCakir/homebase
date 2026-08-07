# dashboard/ — web interface

React + TypeScript, built with Vite, served by `core` from the same origin as the API.

```sh
make dash-install    # npm ci
make dash-build      # typecheck and build
make dash-lint       # typecheck and lint
make dash-dev        # Vite on :5173, proxying /api to a running core
```

## The constraint that shapes everything here

The dashboard is an **ordinary API client with no special privileges**. It authenticates the
same way any client does, is bounded by the signed-in user's permissions, and never touches
Docker, systemd or the filesystem.

When it needs something the API cannot express, the fix is to add the capability to the API —
not to give the browser a side channel. The Stage 2 AI operator will be a second client of
that same API, and every shortcut taken here becomes a hole there.

## Writing for the person reading it

The intended reader has never opened a terminal and does not know what a daemon is. That is a
constraint on the strings, not a tone of voice:

- **No jargon.** Not "mount the volume" but "make the disk available to Jellyfin". The
  browser test asserts the setup screen contains none of *daemon, systemd, sudo, root,
  localhost, socket*.
- **Say what will happen before it happens**, particularly where something is irreversible.
- **Never show a raw Linux error.** `Permission denied on /dev/sdb1` is a bug report, not a
  message to somebody who wants their photographs back. Diagnostics belong in the quiet
  `detail` line, which is present for whoever needs it and does not compete with the
  sentence the reader has to act on.
- **A recoverable error says how to recover.** Reporting that a problem is fixable while
  withholding the fix is worse than saying nothing.

Uptime is rendered as "3 days", not "259384 seconds" — the same fact, expressed so the
reader does not have to do arithmetic to use it.

## Deliberately absent

**No UI framework, no CSS framework, no state library, no router.** The dependency tree here
is the largest supply-chain exposure in the project — it is the only part with a big
third-party graph — so each dependency has to earn its place. Four screens and plain `fetch`
do not need one. A router arrives when there are enough addressable pages to want one.

The whole bundle is about **60 kB gzipped**, and CI fails if it passes 400 kB. This runs on
whatever old laptop the user had spare.

## Confirmation is not a yes/no

Restarting asks you to type the server's name. That is not friction for its own sake: the
API requires the target to be *named*, so a confirmation obtained for one machine cannot be
spent on another — which matters once a Stage 2 operator is the thing proposing restarts.
Typing the name is also what makes a person notice *which* machine they are about to
restart.

## Tests

```sh
make vm-test-dashboard
```

The browser journey runs against a **real Homebase in a VM**, not a mock. The milestone's
exit condition is phrased as something a person does — "opens the dashboard, creates an
administrator, sees accurate system information, reboots the machine" — and a mocked API
would let every assertion pass while the thing a user touches was broken.

It performs a real reboot of a real machine, which is the only way to exercise what the
dashboard does while the server is away. Two bugs were found that way and could not have
been found otherwise: `fetch` has no default timeout, so a half-restarted machine that
accepts a connection and never answers left the page spinning forever; and API responses
carried no `Cache-Control`, so a polled endpoint could be served from cache.

The e2e suite is not in CI yet — it needs KVM, which GitHub's hosted runners do not offer.
`ci/dashboard` runs typecheck, lint, build, a bundle-size gate and `npm audit`.
