# dashboard/ — web interface

React + TypeScript single-page application. Lands in Milestone 2.

The dashboard is an **ordinary API client with no special privileges**. It authenticates the
same way any other client does, it can only do what the signed-in user's permissions allow,
and it never talks to Docker, systemd or the filesystem directly.

That constraint is worth stating plainly because it is easy to erode: any time the dashboard
needs something the API cannot express, the correct fix is to add the capability to the API —
not to give the browser a side channel. The Stage 2 AI operator will be a second client of
that same API, and every shortcut taken here becomes a hole there.

Requires Node 20+ (this repository's development machine currently has Node 12; upgrading is
part of Milestone 2 setup).
