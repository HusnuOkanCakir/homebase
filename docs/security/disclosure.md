# Vulnerability disclosure

The policy of record is
[SECURITY.md](https://github.com/HusnuOkanCakir/homebase/blob/main/SECURITY.md). This page
covers how a report is handled once it arrives.

## Reporting

[**Report a vulnerability**](https://github.com/HusnuOkanCakir/homebase/security/advisories/new)
— private, and never a public issue.

If GitHub is not workable, email `4hg92s4k@anonaddy.me` with `[homebase-security]` in the
subject.

## What happens next

| When | What |
|---|---|
| Within 7 days | Acknowledgement |
| Within 14 days | Initial assessment and severity, or an explanation of what is still unclear |
| Within 90 days | Fix released, advisory published |

If you have heard nothing in 14 days, please chase. A missed notification is far more likely
than a decision to ignore you.

## Severity

| Severity | Meaning | Target |
|---|---|---|
| **Critical** | Update channel compromise; unauthenticated remote code execution; unauthenticated data destruction | 7 days |
| **High** | Privilege escalation across the `core` → `hostd` boundary; authentication bypass; credential disclosure | 30 days |
| **Moderate** | Authenticated privilege escalation; information disclosure; app isolation bypass | 60 days |
| **Low** | Limited impact, or requiring unlikely preconditions | Next release |

Two categories are treated as more serious than their technical difficulty suggests: anything
touching the **update channel**, because it reaches every installation at once, and anything
that **destroys user data**, because it cannot be undone.

## Disclosure

Coordinated, 90 days by default from acknowledgement. If a fix needs longer we will explain
why and agree a date with you — rather than let the clock run out quietly.

You are credited in the advisory unless you prefer otherwise. Anonymous reports are welcome.

There is no bug bounty. This is a personal open-source project, and pretending otherwise
would waste your time.

## Testing safely

Test against your own installation. Please do not access other people's data, degrade
anyone's service, or scan machines you do not own.

Good-faith research within those bounds will not be met with legal action.

## Especially valuable reports

Reports demonstrating that a designed guarantee does not hold are worth more than a list of
missing headers:

- The `core` → `hostd` boundary crossed — any path from an unprivileged component to
  arbitrary execution ([ADR-0006](../decisions/0006-privilege-split.md))
- Update signature verification bypassed, or a downgrade forced
- Secrets reaching a log, an API response, a backup or a diagnostic bundle
- An audit record suppressed, forged, or missing for an action that occurred
- **Stage 2**, when it exists: content the server ingested — a filename, a media title, a
  document — reaching a privileged capability

## Out of scope

- Attacks requiring physical access ([threat model](threat-model.md#an-attacker-with-physical-access))
- Vulnerabilities in catalogued third-party applications, unless our manifest makes them worse
- Missing hardening with no demonstrated impact
- Denial of service by an authenticated administrator
- Social engineering
- Scanner output without a working exploit
