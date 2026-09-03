# Changelog

All notable changes to devengine are documented here. The project follows
[Semantic Versioning](https://semver.org/) and keeps an explicit distinction
between stable-candidate and experimental packages until v1.0.0.

## [Unreleased]

### Added

- A strict, configurable client IP resolver supporting RFC 7239 `Forwarded`,
  `X-Forwarded-For`, `X-Real-IP`, and `CF-Connecting-IP` behind explicitly
  trusted proxy CIDRs.

### Deprecated

- `clientip.ParseTrustedProxies`, `MustParseTrustedProxies`, and `FromRequest`;
  use `clientip.New` and `Resolver.Resolve`. The legacy API remains available
  throughout the v0.2 release line.

## [0.2.0] - 2026-09-02

### Added

- Bounded worker shutdown and explicit long-running worker lifecycle semantics.
- Transaction options, modular Jobs/Outbox migrations, registry validation,
  OpenTelemetry semantic conventions, and health diagnostics.
- Programmatic failed-job inspection, requeue, and discard operations.
- Executable examples, fuzz coverage, PostgreSQL 17/18 concurrency tests, and
  a self-hosted resilience workflow for 100k-row backlog and database restart.
- Dependency, vulnerability, API compatibility, and release automation.

### Changed

- Dependencies were refreshed to current compatible stable releases.
- `jobs.Registry.Register` now returns an error and rejects invalid or duplicate
  registrations; intentional replacement uses `Replace`.
- `events.Registry.Register` now returns an error, rejects invalid or duplicate
  handlers, and supports an explicit startup `Freeze` boundary.
- Outbox uses the fixed `outbox_messages` table. `RelayConfig.Table` and the
  table-name argument to `Enqueue` were removed.
- `outbox.Enqueue` accepts options such as `WithMaxAttempts` and
  `WithProcessAfter`.
- Persistent infrastructure migrations are opt-in through `jobs.Migrations()`
  and `outbox.Migrations()`; `migrate.EngineSources()` is deprecated.

### Upgrade notes

- Update Jobs and Events registration call sites to handle the returned error.
- Remove custom Outbox table configuration and table-name arguments.
- Select required capability migrations explicitly before application
  migrations. Existing migration versions and checksums remain unchanged.
- No destructive database migration is included. Rolling application binaries
  back to `v0.1.0` is supported only when consumers have not adopted the new
  public API signatures.

## [0.1.0] - 2026-09-02

- Initial development release.

[Unreleased]: https://github.com/andreunix/devengine/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/andreunix/devengine/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/andreunix/devengine/releases/tag/v0.1.0
