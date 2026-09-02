package migrate_test

import (
	"context"
	"github.com/andreunix/devengine/migrate"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
	"testing"
)

func TestEngineSourcesFreshInstallAndLegacyUpgrade(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "legacy"}[legacy], func(t *testing.T) {
			db := testpostgres.NewIsolatedDatabase(t)
			ctx := context.Background()
			if legacy {
				if _, err := db.Pool().Exec(ctx, `CREATE TABLE outbox_messages (id TEXT PRIMARY KEY, event_type TEXT NOT NULL, occurred_at TIMESTAMPTZ NOT NULL); CREATE TABLE devengine_jobs (id TEXT PRIMARY KEY, name TEXT NOT NULL, run_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), locked_until TIMESTAMPTZ);`); err != nil {
					t.Fatal(err)
				}
			}
			runner := migrate.Runner{Pool: db.Pool(), Sources: migrate.EngineSources()}
			if err := runner.Apply(ctx); err != nil {
				t.Fatal(err)
			}
			for _, column := range []struct{ table, name string }{{"outbox_messages", "locked_until"}, {"outbox_messages", "claim_token"}, {"devengine_jobs", "claim_token"}} {
				var exists bool
				if err := db.Pool().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND column_name=$2)`, column.table, column.name).Scan(&exists); err != nil || !exists {
					t.Fatalf("column %s.%s missing: %v", column.table, column.name, err)
				}
			}
			if err := runner.Apply(ctx); err != nil {
				t.Fatalf("reapply: %v", err)
			}
		})
	}
}
