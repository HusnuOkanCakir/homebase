# Changelog

All notable changes to Homebase are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versioning
follows [Semantic Versioning](https://semver.org/) — with one pre-1.0 caveat spelled out
below.

## Pre-1.0 versioning

Until 1.0.0, **minor releases may contain breaking changes**. Patch releases contain only
compatible fixes.

This is the usual pre-1.0 convention, but it is worth being explicit about what it does
*not* excuse: every release, breaking or not, must migrate existing installations without
destroying data. A user who installed 0.3.0 must be able to reach 0.4.0 with their photos,
their applications and their configuration intact. Breaking an API is acceptable before 1.0;
breaking somebody's server is not.

Each release records the minimum version it can upgrade from. See
[docs/release/versioning.md](docs/release/versioning.md).

## [Unreleased]

### Added

- Repository skeleton, Apache-2.0 licence, editor and line-ending conventions
- Contributing guide, security policy, code of conduct, roadmap
- Pull request and issue templates, CODEOWNERS

[Unreleased]: https://github.com/HusnuOkanCakir/homebase/commits/main
