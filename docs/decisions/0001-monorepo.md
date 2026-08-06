# ADR-0001: Single repository for all components

- **Status:** Accepted
- **Date:** 2026-08-06

## Context

Homebase is an appliance, not a library. Shipping it means shipping an installer image, two
services, a dashboard, a desktop USB-writer application, packaging, an application catalogue
and documentation — and shipping them as one thing, because a user installs a *version of
Homebase*, not a compatible set of components.

Several of these are tightly coupled by definition. The dashboard cannot be newer than the
API it calls. The installer must place the exact packages the release contains. An app
manifest must validate against the schema the running `core` enforces.

## Decision

All components live in one repository, released as one versioned product.

## Alternatives considered

### A repository per component

The conventional choice, and it has real merits: independent release cadence, smaller
checkouts, clear ownership boundaries, and a contributor can work on the dashboard without
cloning an installer they will never touch.

It was rejected because the coupling here is genuine rather than incidental. A change to the
API contract touches `openapi.yaml`, `core`, the dashboard and the documentation. Across four
repositories that is four pull requests, reviewed separately, merged in an order somebody has
to get right, with a window in between where `main` is inconsistent in each of them. In one
repository it is one reviewable change that is either correct or not.

The version-matrix problem is worse. With separate repositories, "does dashboard 0.4.2 work
with core 0.4.1?" becomes a question requiring an answer — a compatibility table, and tests
that exercise it. With one repository it is not a question anyone can ask.

### A monorepo with independently versioned components

Keeps the atomic-change benefit while allowing components to release separately. This is
what large projects converge on, and it may be where Homebase ends up.

Rejected for now because it buys flexibility we cannot use. There is one artifact — an
appliance image — and one thing a user can install. Independent component versions would be
bookkeeping in service of a distribution model that does not exist.

## Consequences

### What this makes easier

- API contract, implementation, client and documentation change together or not at all
- One version number, which is also the one a user reports in a bug
- CI can test the real combination that ships, rather than an approximation
- Shared schemas, error codes and fixtures have one home

### What this makes harder

- Checkout size grows; eventually the installer's image assets will be the bulk of it
- CI must use path filters to avoid running Go tests for a documentation typo
- Access control is per-repository, so it cannot be narrowed by component. `CODEOWNERS`
  mitigates this for review, but not for write access
- Extracting a component later means rewriting history or losing it

### Security impact

Slightly negative and worth naming: a contributor granted write access has it everywhere,
including `installer/` and `cmd/hostd/`. Branch protection and `CODEOWNERS` are what stand in
for per-component permissions, which makes their configuration more load-bearing than it
would be otherwise. See [ADR-0007](0007-trunk-based-development.md).

### What would make us revisit this

- The controller (the desktop USB writer) developing a genuinely independent release cadence
- A component acquiring users outside the appliance — if the Go client became something
  people imported on its own, it would want its own module and repository
- Checkout time becoming an obstacle for contributors who only want to fix the dashboard
