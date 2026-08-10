# ADR-0015 — A recovery code the user holds, and a console reset behind it

- **Status:** Accepted
- **Date:** 2026-08-10
- **Milestone:** 5 — Backup and restore (brought forward from 6 and 8)
- **Related:** [ADR-0006](0006-privilege-split.md), [ADR-0014](0014-backups-are-readable-without-homebase.md)

## Context

Until now a forgotten password meant a lost server. There was no way back in: no email to
send a link to, no second administrator, no support desk. The account was the only door and
it had one key.

Milestone 5 made that worse rather than better. A restored backup brings the accounts back
exactly as they were, password hashes included — so somebody who restores *because* they
were locked out restores the account they cannot sign into. The recovery story and the
backup story are the same story, which is why this is settled here rather than left in
Milestone 8 where "credential reset" was listed.

Three constraints shape the answer, and they pull against each other.

**There is no second channel.** Every consumer product recovers an account by proving you
control something else — an email address, a phone, another signed-in device. Homebase has
none of these. It is a box on a shelf that may never have touched the internet, and it must
keep working with the internet unplugged. Anything resembling "we sent you a link" is
unavailable, and building it would mean asking a non-technical user to configure SMTP
before they are allowed to be forgetful.

**The promise is that they never need a terminal.** A recovery path that begins "SSH into
the server" is not a recovery path for the person this is built for. It is a recovery path
for me.

**Physical access is already total.** The threat model says so explicitly: pre-1.0 Homebase
does not defend against an attacker with physical access to the disk. Whoever is holding the
machine can already read every file on it, including the database. Any recovery mechanism
gated on physical presence therefore gives away nothing that was being protected.

The uncomfortable part is that a recovery mechanism *is* an authentication bypass. It is a
second way in, deliberately built. The question is not whether to weaken the door but which
weakening is honest about who it lets through.

## Decision

**Two paths, one for each way of being locked out.**

### A recovery code, held by the user

At first-run setup, Homebase generates a recovery code, shows it once, and asks the user to
write it down or print it. It is the only thing shown on that screen that they are asked to
keep.

- **125 bits of entropy** — twenty-five characters from a 32-character alphabet with no
  ambiguous glyphs (no `I`, `L`, `O` or `U`), shown as five groups of five. Transcribed by
  hand from paper, so it is compared case-insensitively with separators ignored, and `O`,
  `I` and `L` are folded onto `0` and `1` on input. That is the mistake everyone makes, and
  refusing it teaches nobody anything.
- **Stored as an argon2id hash**, with the same parameters as a password. Never stored in a
  form that can be shown again. A code Homebase can display is a code an attacker who reads
  the database can use.
- **Single use.** Using it sets a new password, invalidates it, and issues a replacement
  which is shown once. Recovering therefore always ends with the user holding a valid code
  again — the alternative is a user who recovers once and is then in exactly the position
  they started in, minus the paper.
- **Every session is destroyed** when it is used. Recovery is what somebody does when they
  suspect they have lost control of the account, and leaving existing sessions alive makes
  it useless in precisely that case.
- A signed-in administrator can **generate a fresh code** at any time, which invalidates the
  old one. This is the answer to lost paper, and the reason the code needs no plaintext copy
  anywhere.

### A console command that issues a new code, for when the paper is gone too

`homebasectl recovery-code`, run as root on the machine itself, prints a fresh code for a
named account and invalidates the previous one.

It deliberately does **not** set a password. Typing one at a terminal puts it in scrollback
and shell history, needs its own confirmation and validation, and would be a second
implementation of a thing the browser already does correctly. Issuing a code instead means
the console does the one part that requires being at the machine, and the part the user
already knows how to do happens where it always does. The printed code is single-use and
about to be spent, which is a far better thing to leave on a screen than a password.

It grants nothing that root on that machine did not already have — the binary opens the same
SQLite file that root could already edit with `sqlite3` — but it does the job correctly,
applying migrations, hashing with current parameters and writing the event.

Both paths **record a loud, non-recoverable event**. A password reset the owner did not
perform is the single most important thing Homebase can tell them, and it is visible on the
dashboard without going looking for it.

### Rate limiting, which this makes unavoidable

The recovery endpoint is unauthenticated and verifies an argon2id hash, which is 64 MiB of
memory per attempt by design. Without a limit, that is a memory exhaustion attack that needs
no credentials — and the same was already true of the login endpoint.

So sign-in, setup and recovery are limited per client address: a small burst, then a
widening delay, and a hard ceiling. The limiter is in front of the hash, not behind it,
because the cost being defended against is the hash itself.

## Consequences

### What this costs

**A recovery code is a bearer credential on a piece of paper.** Anybody who finds it can
take the server, and it does not expire. This is the real price, and it is paid because the
alternative — a user permanently locked out of their own photographs — is worse and far more
likely. Household risk is not the threat being defended against; a lost password is.

**It is only as good as the writing-down.** A user who skips past the setup screen has no
recovery code, and will discover this at the worst moment. So the screen requires an
explicit acknowledgement rather than a "next" button, and the dashboard says plainly, on the
security screen, whether a code exists and when it was issued.

**Restoring a backup restores the recovery code**, because it lives in the database. This is
correct and is the point: the code written on paper still opens the machine that was rebuilt
from the disk. It also means a backup disk and a written code together are full access to
the server, which is now stated in the backup README, the user guide and the manifest.

**The console path needs a terminal.** For the target user that is a failure, mitigated by
being the second path rather than the first, and by Milestone 6's installer being able to
show the code again at the console during first boot.

### What was rejected

**Security questions.** Guessable by the people most likely to be nearby, forgotten as
readily as passwords, and an additional low-entropy secret to store. They fail both ways.

**A reset that requires only physical presence — hold a key at boot.** Attractive because it
matches the threat model exactly, and rejected because a home server lives in a cupboard
without a keyboard or a monitor. The console path is this idea, without pretending the
hardware is there.

**No recovery at all, and lean on backups.** Coherent, and wrong: restoring a backup restores
the same hash. Backups protect against a lost machine, not a lost password. It was believing
these were the same problem that left this gap open for five milestones.

**Emailed reset links.** No SMTP on an appliance that must work offline, and configuring one
is a technical task placed in front of a non-technical user at the exact moment they are
already stuck.

**Encrypting the code so it can be shown again later.** The key would have to live on the
machine, next to the thing it protects, which makes it decoration. Regenerating a fresh code
solves the real problem — lost paper — without a reversible secret at rest.
