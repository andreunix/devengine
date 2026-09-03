package jobs

import (
	"embed"

	"github.com/andreunix/devengine/migrate"
)

// migrations contains the versioned schema owned by the persistent jobs queue.
//
//go:embed migrations/*.up.sql
var migrations embed.FS

// Migrations returns the migrations required by the persistent delayed jobs
// queue. It does not install Outbox infrastructure.
func Migrations() []migrate.Source {
	return []migrate.Source{{Kind: migrate.EngineSource, FS: migrations, Dir: "migrations"}}
}
