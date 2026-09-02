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

`devengine new -profile http`, `devengine new -profile worker` e `devengine new -profile combined` geram, respectivamente, HTTP, worker e ambos. O profile `http` usa `cmd/server`; `worker` e `combined` usam `cmd/app`. O engine inicia módulos e workers registrados e encerra-os por contexto/sinal.

```go
// HTTP-only: engine.New(engine.WithProfile(engine.ProfileHTTP))
// Worker-only: engine.New(engine.WithProfile(engine.ProfileWorker))
// Combined (padrão): engine.New()
```

```bash
devengine new -module github.com/acme/api -profile http
cd api && go run ./cmd/server

devengine new -module github.com/acme/worker -profile worker
cd worker && go run ./cmd/app

devengine new -module github.com/acme/service -profile combined
cd service && go run ./cmd/app
```

Use a fronteira transacional com o contexto retornado, para que todos os repositórios compartilhem a mesma transação:

```go
err := db.WithTransaction(ctx, func(txCtx context.Context, tx pgx.Tx) error {
  _, err := db.Querier(txCtx).Exec(txCtx, `INSERT INTO users (email) VALUES ($1)`, email)
  return err
})
```

`outbox.Enqueue` e `jobs.Enqueue` devem ser chamados nessa transação. Entrega é at-least-once: handlers precisam ser idempotentes; tokens de lease impedem que um worker stale grave o outcome de um owner novo. Durante handlers ativos, Jobs e Outbox renovam o lease com o respectivo token; `LeaseRenewalInterval` é metade do lease por padrão e pode ser ajustado. Se ownership for perdido, renovações param e o contexto do handler é cancelado.
No outbox, `outbox_messages.max_attempts` é a autoridade por mensagem para retries.

```go
// Dentro de db.WithTransaction: evento e alteração de domínio são atômicos.
err := outbox.Enqueue(txCtx, tx, events.Event{ID: "evt_1", Type: "UserCreated", OccurredAt: time.Now()}, "")
err = jobs.Enqueue(txCtx, tx, jobs.Job{Name: "send_email", Payload: map[string]string{"to": email}})

// Workers usam pools e registries fornecidos pela aplicação.
relay := &outbox.Relay{Pool: db.Pool(), Registry: eventRegistry}
worker := &jobs.Worker{Pool: db.Pool(), Registry: jobRegistry}
```

```go
// Readiness crítica bloqueia /readyz; checks informativos apenas aparecem no diagnóstico.
ready := health.NewRegistry()
ready.Add("postgres", db.ReadyCheck())
ready.AddInformational("cache", cache.ReadyCheck)
```

## Banco, migrations e schema

Migrations da engine usam versões `1–999`; a aplicação usa `1000+`. Todo deploy deve executar primeiro as fontes da engine e depois as fontes da aplicação — `Schema` de jobs/outbox serve apenas para bootstrap efêmero/testes, não para upgrades.

```go
sources := append(migrate.EngineSources(), migrate.Source{Kind: migrate.AppSource, FS: appMigrations})
err := (migrate.Runner{Pool: db.Pool(), Sources: sources}).Apply(ctx)
```

```bash
export TEST_DATABASE_URL='postgres://localhost:5432/postgres?sslmode=disable'
go test -race -count=1 ./...
```

`schema.Capture` produz snapshots `snapshot_version: 2`; `schema.Diff` reporta drift de tabelas, enums e sequences, incluindo configuração de sequence. Snapshots v1 continuam legíveis e, para sequences, são comparados apenas por nome: adições e remoções são detectadas, mas metadata que não existia no formato v1 não gera falso `sequence_changed`.

## Telemetria

Telemetry é noop por padrão. A aplicação configura providers/exporters/propagadores (incluindo W3C Trace Context) e injeta o adapter, sem exporter ou vendor implícito:

```go
adapter := devotel.New(tracerProvider, meterProvider)
app := engine.New(engine.WithTelemetry(adapter.Tracer(), adapter.Meter()))
```

Para propagar W3C Trace Context no HTTP, passe explicitamente o propagador da aplicação. Sem essa opção o middleware não lê nem escreve `traceparent`/`tracestate`.

```go
handler := middleware.Telemetry(adapter.Tracer(), adapter.Meter(),
  middleware.WithPropagator(propagation.TraceContext{}),
)(appHandler)

// Em chamadas de saída (ou para expor o contexto na resposta):
middleware.InjectTraceContext(ctx, request.Header, propagation.TraceContext{})
```

`jobs` é uma fila persistente de jobs atrasáveis, com retry; não é um scheduler cron ou de recorrência.

## Módulo privado

Enquanto privado, configure `GOPRIVATE=github.com/andreunix/*` e credenciais GitHub (SSH ou token) tanto localmente quanto no CI antes de executar `go get`/`go mod download`.

## CI

The project uses GitHub Actions with `gofmt`, `go vet ./...`, and `go test -race ./...` on every push and pull request. Supported PostgreSQL versions: **17 and 18**. Baseline: **Go 1.27**.

## License

MIT
