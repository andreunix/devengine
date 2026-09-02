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
go run ./cmd/server
```

## CI

The project uses GitHub Actions with `gofmt`, `go vet ./...`, and `go test -race ./...` on every push and pull request. Baseline: **Go 1.27**.

## License

MIT
