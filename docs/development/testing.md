# Testing

A Homebase feature is not finished when it works. It is finished when it **works, fails
comprehensibly, survives a reboot, and can be rolled back.**

That standard is set by what this software does: it runs unattended on a machine that loses
power, holding data its owner cannot replace, administered by someone who cannot read a stack
trace.

## The levels

| Level | Where | Answers |
|---|---|---|
| **Unit** | Beside the source | Is the logic, parsing and validation correct? |
| **Integration** | `tests/integration/` | Do `core`, `hostd`, SQLite and the runtime agree? |
| **VM** | `tests/vm/` | Does it work as real systemd services, across a reboot? |
| **Installer** | `tests/installer/` | Does a blank — or Windows-occupied — disk become a server? |
| **Upgrade** | `tests/upgrade/` | Does the previous release become this one without losing data? |
| **End-to-end** | `tests/e2e/` | Can a user actually do this through the dashboard? |

Go and TypeScript unit tests live beside their source, as is idiomatic in both. `tests/` holds
the levels needing orchestration.

## Milestone 0

There is no product code, so testing is validation of the artifacts that exist:

```sh
make check
```

- Hygiene: encoding, line endings, trailing whitespace, file size
- Markdown lint, internal link resolution including heading anchors
- YAML lint, workflow-security analysis
- Documentation site builds with `--strict`
- OpenAPI and JSON Schema validity, plus fixtures

### Contract fixtures

`schemas/examples/` holds both valid and **invalid** fixtures. The invalid ones matter more:
a schema that accepts everything passes every positive test. Each `invalid-*.json` asserts
that the schema rejects a specific mistake — a missing required field, a bad enumeration
value, an absolute path where a relative one is required.

When you add a schema constraint, add the invalid fixture that proves it bites.

## The tests that earn their keep

Success-path tests are the easy half and catch the least. These are required for any change
touching storage, installation, updates or the privilege boundary:

**Power loss mid-write.** Not exceptional — this is a laptop in a cupboard.

**An update interrupted at each stage.** Download, verify, apply, health check. The machine
must still boot and the data must still be there.

**A USB disk removed while mounted**, then reconnected. Common, and a good way to corrupt
configuration if mount state is assumed.

**A disk that is full**, and a disk that fills *during* an operation.

**A container that fails its health check forever.** The job must fail with a comprehensible
message rather than retry indefinitely.

**A restore onto a different machine** from the one backed up.

**Rollback that itself fails.** The `rollback_failed` state must produce specific recovery
instructions, and must never be retried automatically.

## Reboot behaviour

Anything that touches state on disk must be tested across a reboot. In a VM:

1. Perform the operation
2. Reboot
3. Assert services recovered, state is intact, no job is stuck in `running`

The stuck-job case is easy to miss and bad to ship: a job showing "running, 65 %" with no
process behind it is worse than an honest failure, because the user cannot tell the
difference. See [jobs](../architecture/jobs.md#persistence).

## Failure messages are tested

A failure message is a feature, and it is tested like one.

```text
Bad:  Error: exit status 32
Good: The backup disk is not connected. Reconnect it and try again.
```

Assert on the user-facing message, not only on the error type. A test that accepts any error
will happily accept `exit status 32` reaching the dashboard.

## Before a pull request

```sh
make check
```

Required where applicable to the change:

- [ ] Unit tests for logic, parsing, validation
- [ ] Integration tests where components interact
- [ ] **Failure paths, not only the success path**
- [ ] Reboot behaviour, if this touches state on disk
- [ ] Existing user data survives an upgrade
- [ ] Failure messages are comprehensible to a non-technical user

## The VM tests

These need QEMU/KVM and roughly 40 GB of free disk. Each one creates a machine, exercises
it, and destroys it — including on failure, after collecting diagnostics.

**`make vm-test-hostd`** runs `hostd` under real systemd and checks the things a unit test
cannot: the socket's mode and group, systemd's sandbox actually applying, an unprivileged
user being refused by the *kernel* rather than by our code, and the service surviving the
reboot it performed itself. Each of those is a property of the deployment, and each would
pass a unit test while being wrong in production.

**`make vm-test-dashboard`** drives the milestone exit conditions through a real browser
against a real machine, including a real reboot. It is not a mocked API: those conditions
are phrased as things a person does, and a mock would let every assertion pass while the
thing a user touches was broken.

**`make vm-test-apps`** installs an application, uses it over HTTP, restarts the machine,
and removes it. The assertion it exists for is that a file written into the application's
data directory is still there afterwards — a test that only checked the container was gone
would pass on an implementation that wiped the disk. It also reads `hostd`'s audit log to
confirm nothing describing a container ever crossed the socket, because
[ADR-0012](../decisions/0012-hostd-owns-the-catalogue.md) is a claim about the socket.

**`make vm-test-packages`** installs, upgrades, reinstalls, reboots and purges the `.deb`s
on a clean machine, with a real administrator account and a real file intact throughout —
including after purge, which deliberately keeps user data.

### What they have caught that nothing else would

Not a boast. Each of these passed every unit test in the repository at the time, and the
list is the argument for why these tests are worth their twelve minutes.

| Test | Bug |
|---|---|
| `vm-test-dashboard` | `fetch` has no default timeout, so a half-restarted machine left the page spinning for ever |
| `vm-test-dashboard` | API responses carried no `Cache-Control`, so a polled endpoint could freeze at a cached value |
| `vm-test-dashboard` | An application the user stopped read "Stopped unexpectedly" — Docker records nothing about who stopped a container |
| `vm-test-apps` | `/srv/homebase/apps/<id>` was `0750 root:root`, so `core` could not traverse into it to back the data up. Silent |
| `vm-test-packages` | Restarting `hostd` deleted its own socket, while the socket unit went on reporting itself healthy. **Every upgrade restarts `hostd`** |
| `vm-test-core` | Both units declared `StateDirectory=homebase` as different users, so `core`'s database became root-owned |

The pattern is worth naming: every one is a property of the *deployment* rather than of the
code, and every one is silent. Nothing crashed, no test went red, and `systemctl status`
reported success in three of them.

**Installer tests (Milestone 6)** run against fixtures including a Windows-style disk, because
that is what most target machines actually contain. An installer change is never merged on the
strength of a local test.

**Upgrade tests (Milestone 8)** run a matrix: previous stable → current, previous beta →
current, current → rollback, interrupted update, and backup → clean install → restore.
