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
- [ ] **No assertion that depends on chance.** A test whose expectation only usually holds
      is one that fails on somebody else's branch, for reasons that have nothing to do with
      their change

`make check` runs everything the pull-request checks run, Go formatting and `go vet`
included. If something is worth failing CI over, it is worth failing before the push — a
check you have to remember to run separately catches things afterwards.

### A test that passes is not a product that works

Two of the worst bugs this project has had were found by using it, not by
running the suite, and both had the same shape: **a test was made to pass by
changing the test's machine rather than the product.**

`hostd` shells out to `sqlite3` when it backs up. Nothing declared that
dependency, so backup failed on every machine except one installed from the ISO
— and the VM test installed `sqlite3` itself, which made the symptom disappear
on the only machine anybody looked at. The line was added to make a Milestone 5
test go green. It did.

The session cookie was marked `Secure`, which browsers discard over plain HTTP
and accept from localhost. Every browser test reaches the server through
loopback port-forwarding, so all thirty-three passed while the dashboard was
unusable from any real network.

Neither is a gap in coverage — both paths were exercised. They are gaps in
*fidelity*: the tests ran against a machine subtly unlike the one a user has.
When a test needs something installed to pass, that is a question about the
product, not a step in the test.

The `Secure` cookie one is now closed at the source rather than asserted around.
Every browser test reaches the server over HTTPS at its real port with
`ignoreHTTPSErrors`, which is the "proceed once" a person clicks — so the origin
under test is the one a household uses, and the loopback exemption that hid the
bug is no longer available to hide the next one.

### Re-run the suites the change could reach, not the one it was about

Milestone 7 moved core from `127.0.0.1:8080` to 80 and 443. The network suite
was written for it and passed. Three other suites talked to the old port, and
were left broken for a day because the change did not look like it touched them
— the package suite reached `8080` from inside the VM, and the browser journey
pointed Playwright at a port that now answers a redirect.

`make check` cannot catch this: the VM suites are too slow to run on every
commit, which is exactly why they are the ones that quietly rot. The rule is
about the blast radius rather than the diff. **A change to how the product is
reached — a port, a scheme, a path, a unit file — reaches every suite that
reaches the product**, and the cost of finding out is a few minutes each.

**A fourth suite was broken by that same change and stayed broken for a
milestone.** `vm-test-core` had not been touched since Milestone 2 and had not
been run since; it was found at the end of Milestone 8 by running every suite
rather than the ones that looked relevant. Three suites were fixed at the time
because somebody thought of three. That is the argument for the run below rather
than for a better memory:

```sh
for t in hostd core packages apps storage backup network update dashboard; do
    make vm-test-$t || echo "FAILED: $t"
done
```

Roughly twenty minutes for the lot. Worth it before closing a milestone, and
worth it after anything that changes how the product is reached or installed.
The installer suite is left out of that loop deliberately — it takes fifteen
minutes on its own and boots a real Ubuntu ISO.

### A check that can only answer "no"

The internet check lived in `hostd`, whose unit sets
`RestrictAddressFamilies=AF_UNIX AF_NETLINK`. It could not open a socket, so it
returned `false` on every machine that ever ran it, for four milestones.

Two tests covered it and neither could fail.

The unit tests injected a fake dialler. They exercised the logic exactly as
intended and never asked whether the process was permitted to do the thing —
which is the one question that mattered.

The VM test asserted `online is False`, and only after taking the interface down.
It passed every time, for a reason that had nothing to do with the interface.

**A check that can only answer "no" passes every test that only asks when the
answer should be no.** When a function has one interesting output, assert the
other one too — and at least once, in the process that will really run it, with
the sandbox it will really have. The VM test now checks `online is True` before
unplugging anything, and there is a test in `internal/api` that opens a real
socket rather than a fake one.

### Reaching it the way the product does, not the way the test can

The application test proved a container serves HTTP by asking Docker for its port
and connecting from inside the VM. It passed for four milestones while installed
applications were unreachable from anywhere: they were bound to loopback on a
port Docker chose, and no part of Homebase reported it.

The test was not wrong about the container. It answered a question nobody had —
"does this image work" — instead of the one that matters, which is whether a
person on the sofa can open it.

**Reach the thing under test through the path a user has.** If that path does not
exist, the test cannot be written, and *that* is the finding.

### Watching somebody use it is a test, and it is the one that finds the most

Milestone 12 was finished, tested from another machine with a client written for
the purpose, and documented. Then one person opened a file manager and none of it
worked: the login refused the name it was created with, there was no way to open
an installed application, `apps stop` and `apps restart` had never worked,
changing an application's disk did nothing, and the server's own name sometimes
resolved to a Docker bridge.

Seven bugs, all found in under an hour, none of them by a suite.

They are not coverage gaps. Every one of those paths was exercised. They are the
difference between *working* and *usable*, and nothing in this repository was
measuring it — because a test knows the prefix on the account name, knows which
field the confirmation goes in, and never has to find the address of the thing it
just installed.

There is no assertion that catches this class. The practice is: **after a
milestone passes, use it as a person would, on the machine somebody actually
has, and write down every point at which you had to already know something.**
Each of those is a bug even when the code is correct.

### A chart that passes its checks and still cannot be read

The colour of a chart is computable — lightness band, chroma floor, separation
under three kinds of colour blindness, contrast against the surface it is drawn
on — so it is computed rather than judged. The palette in the dashboard was run
through a validator, and an ordering that looked more sensible was the one that
failed it.

That check says nothing about layout. Rendering the same charts with a day of
realistic readings and looking at the picture found five things no assertion had:
the card was capped at a width that squeezed five plots into half a screen, the
axis labels ran off the left edge, temperature was drawn from 0 °C so forty
degrees of range lived in the top tenth, the legend keys ran together as one
word, and an axis read `10279k` where it meant `9.8 MB`.

**Render it and look at it.** A screenshot of the real markup with plausible data
is a minute's work and is the only thing that catches geometry.

### A health check that passes while nobody can get in

qBittorrent installed, started, passed its health check and reported itself
running. Every request to it — its own login page included — was answered with a
bare `401 Unauthorized`, with nothing in its log to say why.

The cause was that Homebase published it on 8081 while it listened on 8080.
qBittorrent validates the Host header against the port it is serving on, and
rejects anything else. Its health check passed because that check asks the
container whether it answers, and it did: it answered 401.

**A health check answers "is this process alive", not "can somebody use this".**
Where the two can differ, only using it finds out — and the way this one was
found was a person opening the address in a browser and reading the word
"Unauthorized".

The general lesson is in the schema now: publish an application on the port it
listens on. Where the two must differ, something has to be told, and the thing
that gets told is usually the application.

### Tests that depend on chance

`vm-test-backup` built a deliberately-wrong restore confirmation with `backup_id.upper()`.
A backup id ends in eight hex characters, so roughly one run in forty-four produces one with
no letters in it — and the "wrong" confirmation is then the correct one. The assertion fails,
having performed a real restore on the way.

It passed for weeks. The fix is not to retry it: build the wrong value so that it is
*always* wrong, and assert the case-sensitive property only when changing the case changes
the string.

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

The machine it builds has **two spare disks**, which is not an implementation detail: one
goes to an application, and Homebase refuses to put a backup on a disk an application keeps
files on. A machine with a single spare disk cannot reach a backup at all — the rule working
correctly, and a shape the test has to match rather than work around.

It finishes outside the browser, running `homebasectl` against the database `core` has been
using all journey and spending the code it prints against the live API. That is the only way
to learn whether a second process can open the database while `core` holds it with a
write-ahead log, and a recovery tool that cannot is one that fails at the moment it is
wanted.

**`make vm-test-apps`** installs an application, uses it over HTTP, restarts the machine,
and removes it. The assertion it exists for is that a file written into the application's
data directory is still there afterwards — a test that only checked the container was gone
would pass on an implementation that wiped the disk. It also reads `hostd`'s audit log to
confirm nothing describing a container ever crossed the socket, because
[ADR-0012](../decisions/0012-hostd-owns-the-catalogue.md) is a claim about the socket.

**`make vm-test-storage`** attaches a real disk over QEMU's monitor and **pulls it out
without warning while it is mounted**. That distinction is the test: unmounting is the tidy
case, and the one that destroys data is the device disappearing underneath a filesystem that
is still being written to. It checks that the disk is found again despite the kernel giving
it a different name, that a managed mount survives a reboot, that not even root can write
into the mount point while the disk is absent, and that an application whose disk is gone
refuses to start rather than running without its files.

**`make vm-test-backup`** is the only test that uses two machines, because the milestone's
exit condition is about the second one. The first is set up, used, backed up onto a USB disk
and destroyed; the disk survives; a second machine is created from scratch and the backup is
restored onto it. Restoring onto the machine that made the backup would prove almost nothing
— the files are already there, and half of what a restore has to reconstruct was never lost.

**`make vm-test-packages`** installs, upgrades, reinstalls, reboots and purges the `.deb`s
on a clean machine, with a real administrator account and a real file intact throughout —
including after purge, which deliberately keeps user data. It also reaches the dashboard
**from outside the machine**: everything else in that file talks to the API by running curl
inside the VM, which cannot tell a server the household can use from one that answers only
its own keyboard.

**`make vm-test-installer`** is the slowest and the most faithful. It writes installation
media with `homebasectl installer create`, boots a machine from it, answers Ubuntu's
"Continue with autoinstall?" by pressing keys, and installs onto a disk carrying a real GPT
with Windows' partition types and an NTFS signature. What comes up afterwards is a machine
somebody could use, and the test drives it the rest of the way: create an administrator,
receive a recovery code, install an application.

It is unlike every other VM test in three ways, each learned by watching it fail:

- **The machine has a screen, and the test reads it.** Ubuntu's live-server ISO does not
  redirect its console to serial the way the cloud images do, so with no display the
  installer has nowhere to write and a machine waiting on a question looks exactly like one
  that has crashed.
- **Progress is the target disk growing**, because there is no log to follow. That also
  separates the two failures worth telling apart: crashed, and quietly waiting.
- **"The screen stopped changing" is not "two screenshots are identical."** The cursor
  blinks, so a still console alternates between exactly two images for ever.

It needs about 3.5 GB of free memory and refuses to start without it, because an
out-of-memory kill arrives four minutes in and looks identical to the installer crashing.
`HOMEBASE_KEEP_VM=1` leaves the machine running to be looked at.

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
| `vm-test-storage` | A `0555` mount point does not stop root — and an application container frequently runs as root, so the protection worked against every writer except the most likely one |
| `vm-test-storage` | `hostd` exited `226/NAMESPACE` before running a line of Go, because its unit hard-required a directory *core's* package creates. Installing `homebase-hostd` alone was impossible |
| `vm-test-storage` | …and it then restarted every two seconds, 673 times, with no start limit. Because the socket belongs to systemd, clients connected and hung rather than failing |
| `vm-test-backup` | The database export was never exercised, because `core` was not running so there was no database to export — the most delicate part of a backup, silently untested |
| `vm-test-dashboard` | Rate limiting counted successful sign-ins, so a household signing in over an evening was rationed exactly like somebody guessing. Unit tests sign in once; only a journey that signs in thirty-three times notices |
| `make vm-run`, by hand | Every backup failed on every machine not installed from the ISO: `hostd` shells out to `sqlite3` and no package depended on it. The VM tests installed it themselves, so the gap was invisible from inside them |
| `make vm-run`, by hand | Installing an application that needs a disk downloaded the whole image first — a gigabyte, several minutes at a frozen 10% — and then refused with something true before the first byte |
| `vm-test-installer` | The console account could not run `sudo`. Its password is locked by design, so the recovery path it exists for — `sudo homebasectl recovery-code` — would have failed on every real installation |
| `vm-test-installer` | An installed server listened on `127.0.0.1`, so the dashboard was reachable only from the server's own keyboard. Every machine-side check passed while the one thing the product is for did not work. It was invisible because two *other* places each set the address for their own good reasons |
| `vm-test-dashboard` | Renaming the server wrote to `/etc/hostname` and got "read-only file system". `hostd` runs under `ProtectSystem=strict`, and replacing a file in `/etc` atomically needs the *directory* writable — so the fix was to stop writing it at all and ask systemd-hostnamed, which already owns it |
| `vm-test-dashboard` | …and then the rename worked while the dashboard went on showing the old name for ever. `ProtectHostname=yes` gives the service a private UTS namespace, so `hostd` — the thing that reports the machine's name and checks the restart confirmation against it — was the only part of the system that could not see the machine had been renamed |

The pattern is worth naming: every one is a property of the *deployment* rather than of the
code, and every one is silent. Nothing crashed, no test went red, and `systemctl status`
reported success in three of them.

**Installer tests (Milestone 6)** run against fixtures including a Windows-style disk, because
that is what most target machines actually contain. An installer change is never merged on the
strength of a local test.

**Upgrade tests (Milestone 8)** run a matrix: previous stable → current, previous beta →
current, current → rollback, interrupted update, and backup → clean install → restore.
