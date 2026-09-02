# devengine

**devengine** is a Go runtime infrastructure library for building production-ready services. It provides the structural foundation for HTTP servers, background workers, health checks, migrations, and PostgreSQL access — without owning any business domain logic.

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
| `events`, `outbox`, `jobs` | Eventos transacionais e workers at-least-once |
| `schema` | Snapshot versionado, diff e detecção de drift PostgreSQL |
| `httpx`, `httpx/clientip`, `httpx/middleware`, `httpx/problem`, `httpx/requestid` | Utilitários HTTP seguros |
| `telemetry`, `telemetry/otel` | Abstração e adapter opcional OpenTelemetry |
| `testutil/postgres` | Bancos isolados para testes de integração |
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

## Profiles e lifecycle

`devengine new -profile http`, `devengine new -profile worker` e `devengine new -profile combined` geram, respectivamente, HTTP, worker e ambos em `cmd/app`. O engine inicia módulos e workers registrados e encerra-os por contexto/sinal.

```go
// HTTP-only: engine.New(engine.WithProfile(engine.ProfileHTTP))
// Worker-only: engine.New(engine.WithProfile(engine.ProfileWorker))
// Combined (padrão): engine.New()
```

Use a fronteira transacional com o contexto retornado, para que todos os repositórios compartilhem a mesma transação:

```go
err := db.WithTransaction(ctx, func(txCtx context.Context, _ pgx.Tx) error {
  _, err := db.Querier(txCtx).Exec(txCtx, `INSERT INTO users (email) VALUES ($1)`, email)
  return err
})
```

`outbox.Enqueue` e `jobs.Enqueue` devem ser chamados nessa transação. Entrega é at-least-once: handlers precisam ser idempotentes; tokens de lease impedem que um worker stale grave o outcome de um owner novo.

## Banco, migrations e schema

Migrations da engine usam versões `1–999`; a aplicação usa `1000+`. Para integration tests:

```bash
export TEST_DATABASE_URL='postgres://localhost:5432/postgres?sslmode=disable'
go test -race -count=1 ./...
```

`schema.Capture` produz snapshots `snapshot_version: 2`; `schema.Diff` reporta drift de tabelas, enums e sequences, incluindo configuração de sequence. Snapshots v1 continuam legíveis; como o módulo está em `< v1`, mudanças incompatíveis seguem SemVer pré-1.0.

## Telemetria

Telemetry é noop por padrão. A aplicação configura providers/exporters/propagadores (incluindo W3C Trace Context) e injeta o adapter, sem exporter ou vendor implícito:

```go
adapter := devotel.New(tracerProvider, meterProvider)
app := engine.New(engine.WithTelemetry(adapter.Tracer(), adapter.Meter()))
```

## Módulo privado

Enquanto privado, configure `GOPRIVATE=github.com/andreunix/*` e credenciais GitHub (SSH ou token) tanto localmente quanto no CI antes de executar `go get`/`go mod download`.

## CI

The project uses GitHub Actions with `gofmt`, `go vet ./...`, and `go test -race ./...` on every push and pull request. Baseline: **Go 1.27**.

## License

MIT
