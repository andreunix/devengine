// Package postgres provides test helpers for integration tests that require a
// real PostgreSQL database. It is intended to be imported only from _test.go
// files or test binaries.
//
// Usage:
//
//	func TestSomething(t *testing.T) {
//	    db := testpostgres.Open(t)
//	    // db is closed and all connections released when t ends.
//	}
package testpostgres

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andreunix/devengine/migrate"
	"github.com/andreunix/devengine/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// EnvDatabaseURL is the environment variable used to locate the test database.
	// Set it to a postgres:// DSN before running integration tests.
	// Example: postgres://user:pass@localhost:5432/testdb?sslmode=disable
	EnvDatabaseURL = "TEST_DATABASE_URL"
)

// Open opens a *postgres.DB for the test database specified by TEST_DATABASE_URL.
// If the environment variable is not set the test is skipped.
// The connection is automatically closed when t ends.
func Open(t *testing.T) *postgres.DB {
	t.Helper()
	connStr := databaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := postgres.Open(ctx, connStr, postgres.Options{
		MaxConns: 5,
		MinConns: 1,
	})
	if err != nil {
		t.Fatalf("testpostgres: open: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

// NewIsolatedDatabase creates a fresh PostgreSQL database for the test and
// returns a pool connected to it. The database is dropped when t ends.
//
// This gives each test a clean, isolated schema so tests can run in parallel
// without interfering with each other.
func NewIsolatedDatabase(t *testing.T) *postgres.DB {
	t.Helper()
	connStr := databaseURL(t)

	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("testpostgres: admin pool: %v", err)
	}

	// Generate a unique database name to avoid collisions in parallel runs.
	dbName := fmt.Sprintf("test_%s_%d", sanitize(t.Name()), rand.Int64())
	if _, err := adminPool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, dbName)); err != nil {
		adminPool.Close()
		t.Fatalf("testpostgres: create database %q: %v", dbName, err)
	}

	t.Cleanup(func() {
		defer adminPool.Close()
		dropCtx := context.Background()
		// Terminate connections before dropping to avoid "database in use" errors.
		_, _ = adminPool.Exec(dropCtx,
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1`, dbName)
		if _, err := adminPool.Exec(dropCtx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, dbName)); err != nil {
			t.Logf("testpostgres: drop database %q: %v", dbName, err)
		}
	})

	// Replace the database name in the DSN.
	testConnStr := replaceDatabaseName(connStr, dbName)
	testCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := postgres.Open(testCtx, testConnStr, postgres.Options{MaxConns: 5})
	if err != nil {
		t.Fatalf("testpostgres: open isolated db: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

// RunMigrations applies migrations to db using the provided sources.
// It fails the test immediately if migrations fail.
func RunMigrations(t *testing.T, db *postgres.DB, sources []migrate.Source) {
	t.Helper()
	runner := migrate.Runner{
		Pool:    db.Pool(),
		Sources: sources,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runner.Apply(ctx); err != nil {
		t.Fatalf("testpostgres: run migrations: %v", err)
	}
}

func databaseURL(t *testing.T) string {
	t.Helper()
	url := strings.TrimSpace(os.Getenv(EnvDatabaseURL))
	if url == "" {
		t.Skipf("skipping integration test: %s not set", EnvDatabaseURL)
	}
	return url
}

// sanitize replaces characters that are not safe in a PostgreSQL identifier.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	result := b.String()
	if len(result) > 40 {
		result = result[:40]
	}
	return result
}

// replaceDatabaseName replaces the database component of a postgres:// DSN.
func replaceDatabaseName(connStr, dbName string) string {
	pool, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return connStr
	}
	pool.ConnConfig.Database = dbName
	return pool.ConnConfig.ConnString()
}
