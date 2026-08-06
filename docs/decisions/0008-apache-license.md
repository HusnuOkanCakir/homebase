# ADR-0008: Apache-2.0 licence

- **Status:** Accepted
- **Date:** 2026-08-06

## Context

Homebase is public from its first commit. The licence determines who can contribute, who can
build on it, and what happens if it becomes popular enough for somebody else to want to sell
it.

The relevant comparisons are the projects in the same space. Home Assistant — the closest
analogue, and the clearest success in self-hosted home software — is Apache-2.0. CasaOS is
Apache-2.0. Nextcloud is AGPL-3.0. Umbrel uses a source-available licence that is not open
source.

Homebase also intends to bundle third-party application manifests and, in Stage 2, to
distribute model weights with their own licence terms. The project's own licence needs to
make those combinations straightforward rather than a question for a lawyer.

## Decision

Apache License 2.0, with a `NOTICE` file. Contributions are accepted under the same licence,
asserted by DCO sign-off rather than a contributor licence agreement.

## Alternatives considered

### AGPL-3.0

The strong-copyleft option, and the one with the most appealing story: anyone offering a
modified Homebase over a network must publish their changes. It would prevent a cloud vendor
from taking this work, hosting it, and giving nothing back.

Rejected on two grounds.

The scenario it protects against barely applies. Homebase's entire premise is that it runs on
hardware you own, on your own network. "Hosted Homebase" is a contradiction — the value is
that nobody else is running it. AGPL's network clause is aimed at a business model this
software is designed not to have.

The costs are real and immediate. Many companies prohibit employee contributions to AGPL
projects outright, which removes contributors from a project whose main scarcity is
contributors. It also makes bundling more delicate: combining AGPL code with model weights
under bespoke licences, and with a catalogue of third-party manifests, raises questions that
Apache-2.0 simply does not.

### MIT

Shortest, most permissive, universally understood.

Rejected for the missing patent grant. Apache-2.0 §3 gives users an explicit licence to any
patents the contributors hold that the work practises, and terminates it for anyone who
brings a patent suit over it. MIT is silent on patents.

For most small projects that is theoretical. It is less theoretical here: Homebase touches
installation, update distribution and device provisioning — areas with a great deal of active
patent activity. An explicit grant costs nothing and removes a question a corporate
contributor's legal team will otherwise ask.

Apache-2.0 also requires preserving attribution and stating changes, which MIT does not.

### Business Source Licence or similar source-available terms

Umbrel's approach: source visible, commercial use restricted, converting to open source after
a delay.

Rejected because it is not open source, and Homebase's proposition is that you own the thing
running on your hardware. A licence reserving the right to restrict what you do with it
undercuts the argument for using it at all.

### Dual licensing with a CLA

Would keep the option of commercial relicensing later. Rejected because a contributor licence
agreement is friction at exactly the wrong moment — a first-time contributor's first pull
request — in exchange for optionality this project has no plan to use.

## Consequences

### What this makes easier

- Corporate contribution, without a legal review that ends in "no"
- Bundling third-party manifests, model weights and dependencies without licence-compatibility
  analysis
- Explicit patent grant and retaliation clause
- Compatible with GPL-3.0, so Homebase code can be used in GPL-3.0 projects
- Same licence as Home Assistant, which sets a familiar expectation in this space

### What this makes harder

- **A vendor could run a modified Homebase as a service and publish nothing.** Unlikely for
  the reasons above, but genuinely permitted
- A hardware vendor could ship a proprietary fork on a device. Also permitted
- Attribution requirements are slightly more involved than MIT — `NOTICE` must be preserved
  in redistribution
- Relicensing later would require every contributor's agreement, which becomes impractical
  quickly. This decision is close to permanent

### Security impact

None directly. Indirectly positive: a permissive licence encourages more eyes on code that
runs as root, and security researchers do not have to think about licence terms before
publishing an analysis.

The dependency-licence policy in `security.yml` denies GPL-3.0, AGPL-3.0 and SSPL-1.0
dependencies — not a judgement on those licences, but a guard against a dependency
inadvertently making the combined work undistributable under Apache-2.0.

### What would make us revisit this

Realistically nothing, and that is worth being clear-eyed about: relicensing requires
agreement from every contributor, so this is effectively permanent once anyone else
contributes.

The scenario that would prompt the conversation is a vendor building a commercial product on
Homebase while contributing nothing and creating support burden for the project. Even then
the answer is more likely trademark policy than a licence change.
