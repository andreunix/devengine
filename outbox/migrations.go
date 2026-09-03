package outbox

import (
	"embed"

	"github.com/andreunix/devengine/migrate"
)

// migrations contains the versioned schema owned by the transactional outbox.
//
//go:embed migrations/*.up.sql
var migrations embed.FS

// Migrations returns the migrations required by the transactional outbox. It
// does not install Jobs infrastructure.
func Migrations() []migrate.Source {
	return []migrate.Source{{Kind: migrate.EngineSource, FS: migrations, Dir: "migrations"}}
}
