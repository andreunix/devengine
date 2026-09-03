package migrate_test

import (
	"context"
	_ "embed"
	"testing"
	"time"

	"github.com/andreunix/devengine/jobs"
	"github.com/andreunix/devengine/migrate"
	"github.com/andreunix/devengine/outbox"
	"github.com/andreunix/devengine/postgres"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
)

// v010Schema is the exact persistent table schema successfully created by the
// v0.1.0 release. See the note in the fixture about its rejected index.
//
//go:embed testdata/v0.1.0.sql
var v010Schema string

func TestCapabilityMigrationsAreOptIn(t *testing.T) {
	tests := []struct {
		name       string
		sources    []migrate.Source
		wantJobs   bool
		wantOutbox bool
	}{
		{name: "http_only"},
		{name: "jobs_only", sources: jobs.Migrations(), wantJobs: true},
		{name: "outbox_only", sources: outbox.Migrations(), wantOutbox: true},
		{name: "jobs_and_outbox", sources: capabilitySources(), wantJobs: true, wantOutbox: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := testpostgres.NewIsolatedDatabase(t)
			ctx := context.Background()
			runner := migrate.Runner{Pool: db.Pool(), Sources: test.sources}
			if err := runner.Apply(ctx); err != nil {
				t.Fatal(err)
			}
			if got := tableExists(t, ctx, db, "devengine_jobs"); got != test.wantJobs {
				t.Fatalf("devengine_jobs exists = %v, want %v", got, test.wantJobs)
			}
			if got := tableExists(t, ctx, db, "outbox_messages"); got != test.wantOutbox {
				t.Fatalf("outbox_messages exists = %v, want %v", got, test.wantOutbox)
			}
		})
	}
}

func TestCapabilityMigrationsFreshInstallAndV010Upgrade(t *testing.T) {
	for _, initialSchema := range []struct {
		name string
		sql  string
	}{
		{name: "fresh"},
		{name: "v0_1_0", sql: v010Schema},
	} {
		t.Run(initialSchema.name, func(t *testing.T) {
			db := testpostgres.NewIsolatedDatabase(t)
			ctx := context.Background()
			if initialSchema.sql != "" {
				if _, err := db.Pool().Exec(ctx, initialSchema.sql); err != nil {
					t.Fatalf("install v0.1.0 fixture: %v", err)
				}
			}

			runner := migrate.Runner{Pool: db.Pool(), Sources: capabilitySources()}
			if err := runner.Apply(ctx); err != nil {
				t.Fatal(err)
			}
			assertCurrentCapabilitySchema(t, ctx, db)

			status, err := runner.Status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(status) != 4 {
				t.Fatalf("migration count = %d, want 4", len(status))
			}
			for i, migration := range status {
				if want := i + 1; migration.Version != want || migration.Checksum == "" || migration.AppliedAt == nil {
					t.Fatalf("status[%d] = %+v, want applied version %d with checksum", i, migration, want)
				}
			}

			// Reapplying verifies stored checksums and the global version ordering.
			if err := runner.Apply(ctx); err != nil {
				t.Fatalf("reapply: %v", err)
			}
		})
	}
}

func TestEngineSourcesRemainCompatibleAggregate(t *testing.T) {
	type migrationIdentity struct {
		name, checksum string
	}
	statuses := make([][]migrationIdentity, 0, 2)
	for _, sources := range [][]migrate.Source{capabilitySources(), migrate.EngineSources()} {
		db := testpostgres.NewIsolatedDatabase(t)
		ctx := context.Background()
		runner := migrate.Runner{Pool: db.Pool(), Sources: sources}
		if err := runner.Apply(ctx); err != nil {
			t.Fatal(err)
		}
		assertCurrentCapabilitySchema(t, ctx, db)
		status, err := runner.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		identities := make([]migrationIdentity, len(status))
		for i, item := range status {
			identities[i] = migrationIdentity{name: item.Name, checksum: item.Checksum}
		}
		statuses = append(statuses, identities)
	}
	if len(statuses[0]) != len(statuses[1]) {
		t.Fatalf("capability migrations = %d, aggregate = %d", len(statuses[0]), len(statuses[1]))
	}
	for i := range statuses[0] {
		if statuses[0][i] != statuses[1][i] {
			t.Fatalf("migration %d differs: capability=%+v aggregate=%+v", i, statuses[0][i], statuses[1][i])
		}
	}
}

func TestCapabilityMigrationsSerializeConcurrentApply(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			errs <- (migrate.Runner{Pool: db.Pool(), Sources: capabilitySources()}).Apply(ctx)
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent apply: %v", err)
		}
	}
	assertCurrentCapabilitySchema(t, ctx, db)
}

func capabilitySources() []migrate.Source {
	sources := append([]migrate.Source{}, jobs.Migrations()...)
	return append(sources, outbox.Migrations()...)
}

func tableExists(t *testing.T, ctx context.Context, db *postgres.DB, name string) bool {
	t.Helper()
	var exists bool
	if err := db.Pool().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`, name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func assertCurrentCapabilitySchema(t *testing.T, ctx context.Context, db *postgres.DB) {
	t.Helper()
	for _, column := range []struct{ table, name string }{
		{"outbox_messages", "locked_until"},
		{"outbox_messages", "claim_token"},
		{"devengine_jobs", "locked_until"},
		{"devengine_jobs", "claim_token"},
	} {
		var exists bool
		if err := db.Pool().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2)`, column.table, column.name).Scan(&exists); err != nil || !exists {
			t.Fatalf("column %s.%s missing: %v", column.table, column.name, err)
		}
	}
	if _, err := db.Pool().Exec(ctx, `INSERT INTO devengine_jobs(id, name, claim_token) VALUES ('job', 'current', 'owner'); INSERT INTO outbox_messages(id, event_type, occurred_at, claim_token) VALUES ('event', 'current', NOW(), 'owner');`); err != nil {
		t.Fatalf("current Jobs/Outbox schema is not operational: %v", err)
	}
}
