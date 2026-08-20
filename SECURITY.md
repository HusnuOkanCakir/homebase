# Security policy

## Reporting a vulnerability

**Please do not open a public issue.**

Report privately through GitHub:
[**Report a vulnerability**](https://github.com/HusnuOkanCakir/homebase/security/advisories/new).

If GitHub is not workable for you, email `security@example.com` with `[homebase-security]`
in the subject.

Please include:

- What the problem is and roughly how bad you think it is
- Steps to reproduce, or a proof of concept
- The version or commit you tested
- Whether you intend to publish, and when

You will get an acknowledgement within **7 days**. If you have not heard anything in 14
days, please chase — a missed notification is far more likely than a decision to ignore you.

### Disclosure

Coordinated disclosure, **90 days** by default from acknowledgement. If a fix needs longer,
we will explain why and agree a date with you rather than let the clock run out silently.
You are credited in the advisory unless you prefer otherwise.

We have no bug bounty. This is a personal open-source project.

## Supported versions

| Version | Supported |
|---|---|
| pre-1.0 | **No guarantee.** Fixes land on `main` and in the next release. There are no backports. |

Homebase is pre-alpha and has never had a release. It is not ready to hold data you cannot
afford to lose. That is a statement about maturity, not a disclaimer against fixing things —
please still report what you find.

## Scope

### In scope

- **The privilege boundary.** Anything that lets `core`, the dashboard, an installed
  application, or a network client reach a privileged operation it should not — especially
  anything that gets arbitrary command execution out of `hostd`. See
  [ADR-0006](docs/decisions/0006-privilege-split.md).
- **Authentication and authorisation.** Session handling, password storage, permission
  checks, privilege escalation between users.
- **The update path.** Signature verification, downgrade attacks, tampering with update
  metadata or artifacts. A compromise here reaches every installation at once, so it is
  treated as the most serious category regardless of exploit difficulty.
- **Secret handling.** Credentials leaking into logs, API responses, backups or diagnostic
  bundles.
- **Network exposure.** Anything reachable from the LAN that should not be, or that responds
  before authentication.
- **Application isolation.** An installed app reaching another app's data or the host.
- **Installer and storage.** Data destruction beyond what the user confirmed.
- **Supply chain.** Our CI, release pipeline and dependency handling.

### Out of scope

- **Attacks requiring physical access to the machine or its disk.** Homebase installs by
  erasing a whole disk and assumes the server is physically trusted. Full-disk encryption is
  not implemented, so an attacker holding the drive reads everything — this is a known and
  documented limitation, not a vulnerability. See
  [docs/security/threat-model.md](docs/security/threat-model.md).
- Vulnerabilities in third-party applications from the catalogue. Report those upstream; do
  tell us if our manifest makes an upstream issue meaningfully worse.
- Missing hardening with no demonstrated impact ("header X is absent"). Explain the attack.
- Denial of service by an already-authenticated administrator. They can reboot the machine.
- Social engineering, and results from automated scanners without a working exploit.

## For Stage 2 (the local AI operator)

Homebase will eventually run a local model that administers the server. When that lands,
these are explicitly **in scope** — and we would rather hear about them early:

- Prompt injection that reaches a privileged capability, especially via content the server
  ingested rather than the user typed — a filename, a media title, a document, an app
  description
- Any path where the model's output is trusted without policy evaluation
- Secrets reaching the model's context
- Audit records that can be suppressed, forged, or omitted for an action that occurred

The architecture is built to make these hard: the model proposes an intention, a policy
engine decides, a typed capability executes, everything is logged. A report showing that
chain broken is the most valuable thing you can send us.

## What we ask of you

Test against your own installation. Do not access other people's data, do not degrade
anyone's service, and do not run scans against machines you do not own. Good-faith research
within these bounds will not be met with legal action.
