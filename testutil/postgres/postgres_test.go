package testpostgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewIsolatedDatabaseCreatesDistinctDatabasesInParallel(t *testing.T) {
	results := make(chan string, 2)
	start := make(chan struct{})

	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			<-start

			db := NewIsolatedDatabase(t)
			var databaseName string
			if err := db.Pool().QueryRow(context.Background(), `SELECT current_database()`).Scan(&databaseName); err != nil {
				t.Errorf("get isolated database name: %v", err)
				return
			}
			results <- databaseName
		})
	}

	close(start)
	t.Cleanup(func() {
		seen := make(map[string]struct{}, 2)
		for range 2 {
			select {
			case databaseName := <-results:
				seen[databaseName] = struct{}{}
			case <-time.After(10 * time.Second):
				t.Error("parallel isolated database creation did not complete")
				return
			}
		}
		if len(seen) != 2 {
			t.Errorf("expected two distinct databases from parallel calls, got %v", seen)
		}
	})
}

func TestNewIsolatedDatabaseUsesDistinctDatabases(t *testing.T) {
	first := NewIsolatedDatabase(t)
	second := NewIsolatedDatabase(t)
	ctx := context.Background()

	var firstName, secondName string
	if err := first.Pool().QueryRow(ctx, `SELECT current_database()`).Scan(&firstName); err != nil {
		t.Fatalf("get first database name: %v", err)
	}
	if err := second.Pool().QueryRow(ctx, `SELECT current_database()`).Scan(&secondName); err != nil {
		t.Fatalf("get second database name: %v", err)
	}
	if firstName == secondName {
		t.Fatalf("expected physically distinct databases, both connected to %q", firstName)
	}

	tableName := "isolation_proof"
	if _, err := first.Pool().Exec(ctx, fmt.Sprintf(`CREATE TABLE %s (id INT PRIMARY KEY)`, tableName)); err != nil {
		t.Fatalf("create table in first database: %v", err)
	}

	var exists bool
	if err := second.Pool().QueryRow(ctx, `SELECT to_regclass('public.isolation_proof') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("check table in second database: %v", err)
	}
	if exists {
		t.Fatalf("table created in %q leaked into %q", firstName, secondName)
	}
}

func TestNewIsolatedDatabaseCleansUp(t *testing.T) {
	var databaseName string
	t.Run("database", func(t *testing.T) {
		db := NewIsolatedDatabase(t)
		if err := db.Pool().QueryRow(context.Background(), `SELECT current_database()`).Scan(&databaseName); err != nil {
			t.Fatalf("get isolated database name: %v", err)
		}
	})

	ctx := context.Background()
	adminConfig, err := pgxpool.ParseConfig(databaseURL(t))
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	t.Cleanup(adminPool.Close)

	var exists bool
	if err := adminPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, databaseName).Scan(&exists); err != nil {
		t.Fatalf("check isolated database cleanup: %v", err)
	}
	if exists {
		t.Fatalf("isolated database %q was not removed after cleanup", databaseName)
	}
}
