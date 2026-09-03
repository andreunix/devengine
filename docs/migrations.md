# Capability migrations

Persistent infrastructure is opt-in. Build `migrate.Runner.Sources` with only
the capabilities used by the service, then append application migrations
(versions `1000+`).

```go
sources := append([]migrate.Source{}, jobs.Migrations()...)
sources = append(sources, outbox.Migrations()...)
sources = append(sources, migrate.Source{Kind: migrate.AppSource, FS: appMigrations})

err := (migrate.Runner{Pool: db.Pool(), Sources: sources}).Apply(ctx)
```

- An HTTP-only service supplies no engine migrations and receives neither
  `devengine_jobs` nor `outbox_messages`.
- A Jobs worker uses only `jobs.Migrations()`.
- An Outbox consumer uses only `outbox.Migrations()`.

`migrate.EngineSources()` remains available for compatibility and installs both
capabilities; prefer capability-specific APIs in new code. The runner preserves
global version ordering, checksums, and the advisory lock even when sources come
from separate packages.

Schemas published in `v0.1.0` are supported as an upgrade path: current
migrations add claim ownership without recreating existing tables. The exact
upgrade fixture lives in `migrate/testdata/v0.1.0.sql`.
