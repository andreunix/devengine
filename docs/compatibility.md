# Compatibility

## Supported platforms

- Go: 1.27 baseline.
- PostgreSQL: 17 and 18, tested independently in CI.
- Delivery contracts for Jobs and Outbox: at least once; handlers must be
  idempotent.

The module may compile on other PostgreSQL versions, but they are not release
gates. The oldest supported Go version is the version declared by `go.mod` and
used by CI.

## Upgrade policy

Consumers should upgrade one tagged release at a time when release notes carry
migration instructions. Database migrations must run before application code
that depends on them. Never roll application binaries back across a destructive
database migration; use the release-specific rollback notes instead.

The public API check detects source-incompatible exported declaration changes.
It does not replace PostgreSQL migration tests, behavioral tests, or consumer
validation. Experimental packages can change before v1, but those changes are
still reviewed and documented.

The `httpx/clientip` resolver introduced after v0.2.0 is additive. Its legacy
`TrustedProxies`, `ParseTrustedProxies`, `MustParseTrustedProxies`, `Contains`,
and `FromRequest` declarations remain source-compatible during the v0.2 release
line while consumers migrate to `Resolver`.

For private module access, set `GOPRIVATE=github.com/andreunix/*` and configure
Git authentication before `go mod download`.
