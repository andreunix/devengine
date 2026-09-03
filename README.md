# devengine

**devengine** is a Go runtime infrastructure library for building production-ready services. It provides the structural foundation for HTTP servers, background workers, health checks, migrations, and PostgreSQL access — without owning any business domain logic.

English is the primary language for project documentation and public API
contracts.

## Overview

The engine deliberately does **not** include domain repositories, authorization rules, or business models. Consuming applications wire their own domain and register it with the engine's runtime surface.

## Packages

| Package | Description |
|---------|-------------|
| `engine` | Core lifecycle: HTTP server, modules, workers, signal handling |
| `postgres` | PostgreSQL connection management (pgx/v5 + pgxpool) |
| `migrate` | Ordered SQL migration runner with advisory lock and SHA-256 checksums |
| `health` | Liveness (`/healthz`) and readiness (`/readyz`) registry |
| `httpx/middleware` | RequestID, logging, panic recovery, security headers |
| `config` | Environment variable loading |
| `id` | UUIDv7 generation |
| `secrets` | Opaque-secret generation, SHA-256 digests, and AES-256-GCM encryption at rest |
| `mail` | SMTP transport with TLS, authentication, timeouts, and header sanitization |
| `events`, `outbox`, `jobs` | Transactional events and at-least-once workers |
| `schema` | Versioned PostgreSQL snapshots, diffing, and drift detection |
| `httpx`, `httpx/clientip`, `httpx/middleware`, `httpx/problem`, `httpx/requestid` | Safe HTTP utilities |
| `telemetry`, `telemetry/otel` | Telemetry abstraction and optional OpenTelemetry adapter |
| `testutil/postgres` | Isolated databases for integration tests |
| `cmd/devengine` | Scaffold CLI for bootstrapping new applications |

## Migration Version Ranges

| Range | Owner |
|-------|-------|
| 1–999 | devengine infrastructure |
| 1000+ | Application |

## Getting Started

```bash
go install github.com/andreunix/devengine/cmd/devengine@latest
devengine new -module github.com/your-org/my-app
cd my-app && go mod tidy
go run ./cmd/app
```

## Profiles and lifecycle

`devengine new -profile http`, `worker`, and `combined` generate HTTP-only,
worker-only, and combined applications respectively. The engine starts
registered modules and workers and shuts them down on context cancellation or
an operating-system signal. `WithWorkerShutdownTimeout` bounds the wait for a
non-cooperative worker and reports its name; returning before shutdown is an
unexpected worker exit.

```go
// HTTP-only: engine.New(engine.WithProfile(engine.ProfileHTTP))
// Worker-only: engine.New(engine.WithProfile(engine.ProfileWorker))
// Combined (default): engine.New()
```

```bash
devengine new -module github.com/acme/api -profile http
cd api && go run ./cmd/server

devengine new -module github.com/acme/worker -profile worker
cd worker && go run ./cmd/app

devengine new -module github.com/acme/service -profile combined
cd service && go run ./cmd/app
```

Use the returned transaction context so every repository shares the same transaction:

```go
err := db.WithTransaction(ctx, func(txCtx context.Context, tx pgx.Tx) error {
  _, err := db.Querier(txCtx).Exec(txCtx, `INSERT INTO users (email) VALUES ($1)`, email)
  return err
})
```

Transaction callbacks may be retried and must not perform external side
effects. Persist intent through Jobs or Outbox instead. Use
`WithTransactionOptions` for explicit `pgx.TxOptions`; see
[`docs/transactions.md`](docs/transactions.md).

Call `outbox.Enqueue` and `jobs.Enqueue` inside that transaction. Delivery is
at least once, so handlers must be idempotent. Lease tokens prevent a stale
worker from recording an outcome owned by a newer worker. Losing ownership
stops renewal and cancels the handler context. Each Outbox message owns its
retry limit through `max_attempts`.

```go
// Inside WithTransaction, the domain change and event are atomic.
err := outbox.Enqueue(txCtx, tx, events.Event{ID: "evt_1", Type: "UserCreated", OccurredAt: time.Now()})
err = jobs.Enqueue(txCtx, tx, jobs.Job{Name: "send_email", Payload: map[string]string{"to": email}})

// Workers use pools and registries supplied by the application.
relay := &outbox.Relay{Pool: db.Pool(), Registry: eventRegistry}
worker := &jobs.Worker{Pool: db.Pool(), Registry: jobRegistry}
```

```go
// Critical readiness blocks /readyz; informational checks are diagnostic only.
ready := health.NewRegistry()
ready.Add("postgres", db.ReadyCheck())
ready.AddInformational("cache", cache.ReadyCheck)
ready.SetTelemetry(adapter.Meter())
snapshot := ready.Snapshot() // latest programmatic results
```

## Database, migrations, and schema

Engine migrations use versions `1–999`; applications use `1000+`. Deployments
apply selected engine capability sources before application sources. Jobs and
Outbox `Schema` constants are only for ephemeral test bootstrapping, not
production upgrades.

```go
sources := append([]migrate.Source{}, jobs.Migrations()...)
sources = append(sources, outbox.Migrations()...)
sources = append(sources, migrate.Source{Kind: migrate.AppSource, FS: appMigrations})
err := (migrate.Runner{Pool: db.Pool(), Sources: sources}).Apply(ctx)
```

```bash
export TEST_DATABASE_URL='postgres://localhost:5432/postgres?sslmode=disable'
go test -race -count=1 ./...
```

`schema.Capture` produces `snapshot_version: 2`; `schema.Diff` reports drift in
tables, enums, and sequences. Version 1 snapshots remain readable. Sequence
additions and removals are detected by name without inventing changes for
metadata that did not exist in v1.

## Telemetry

Telemetry is a no-op by default. The application owns providers, exporters,
resources, and propagators, then injects the adapter explicitly:

```go
adapter := devotel.New(tracerProvider, meterProvider)
app := engine.New(engine.WithTelemetry(adapter.Tracer(), adapter.Meter()))
```

Pass the application's propagator explicitly for W3C Trace Context. Without it,
the middleware neither reads nor writes `traceparent` or `tracestate`.

```go
handler := middleware.Telemetry(adapter.Tracer(), adapter.Meter(),
  middleware.WithPropagator(propagation.TraceContext{}),
)(appHandler)

// For outbound calls (or exposing context in a response):
middleware.InjectTraceContext(ctx, request.Header, propagation.TraceContext{})
```

`jobs` is a persistent delayed queue with retries, not a cron or recurring-task
scheduler.

## Client IP behind trusted proxies

`httpx/clientip` reads forwarding headers only when the direct peer belongs to
an explicitly trusted CIDR. Header priority is explicit; a malformed selected
header fails closed to the direct peer.

```go
resolver, err := clientip.New(
  []string{"10.0.0.0/8", "fd00::/8"},
  []string{
    clientip.HeaderCFConnectingIP,
    clientip.HeaderForwarded,
    clientip.HeaderXForwardedFor,
    clientip.HeaderXRealIP,
  },
)
ip := resolver.Resolve(request)
```

For a Cloudflare → reverse proxy → service deployment, trust only the network
used by the reverse proxy to reach the service. Configure the header order to
match headers that this trusted boundary overwrites. See
[`docs/client-ip.md`](docs/client-ip.md) and the executable
[`examples/clientip`](examples/clientip).

## Private module access

While the module is private, configure `GOPRIVATE=github.com/andreunix/*` and
GitHub credentials locally and in CI before `go get` or `go mod download`.

## CI

The project uses GitHub Actions with module hygiene, `gofmt`, `go vet`, race
tests, vulnerability scanning, API compatibility checks, and PostgreSQL 17/18
integration tests. Baseline: **Go 1.27**.

Runnable, compile-checked examples live under [`examples/`](examples). Version
stability, platform support, and the release process are documented in
[`docs/versioning.md`](docs/versioning.md),
[`docs/compatibility.md`](docs/compatibility.md), and
[`docs/releasing.md`](docs/releasing.md). Repository rulesets and GitHub secret
scanning remain administrator-managed settings; see the release guide for the
required controls.

## License

MIT
