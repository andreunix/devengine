# Versioning policy

devengine uses Semantic Versioning. Until `v1.0.0`, minor releases may change
experimental APIs; stable-candidate APIs receive the same compatibility review
intended for v1 and should only break when the release notes include a migration
path. Patch releases must remain backward compatible.

The current release line is `v0.4.x`. Consumers should pin an explicit tagged
version in `go.mod`; `main` is development state and is not a release channel.

## Package status

| Status | Packages |
| --- | --- |
| Stable candidate | `engine`, `postgres`, `migrate`, `health`, `httpx`, `httpx/clientip`, `httpx/middleware`, `httpx/problem`, `httpx/requestid` |
| Supporting | `config`, `id`, `testutil/postgres`, `cmd/devengine` |
| Experimental | `events`, `jobs`, `mail`, `outbox`, `schema`, `secrets`, `telemetry`, `telemetry/otel` |

Experimental does not mean unsafe; it means the public API may still change in
a pre-v1 minor release. Every breaking change must be called out in
`CHANGELOG.md`. Packages are promoted only after use by real consumers shows
that common operations do not require workaround adapters.

The API compatibility workflow compares exported Go declarations with the most
recent reachable tag. An intentional pre-v1 experimental break requires an
explicit release decision and changelog entry; stable-candidate breaks should
be fixed before merge.
