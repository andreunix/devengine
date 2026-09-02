// Package postgres provides PostgreSQL access built on pgx/v5 and pgxpool.
// It exposes a DB type that wraps *pgxpool.Pool and implements the Querier
// interface. The legacy database/sql API is intentionally not exposed.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier abstracts the common row-level operations available on both a pool
// and a transaction. Repositories should depend on Querier, not on *DB or
// *pgxpool.Pool directly.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Options configures the connection pool.
type Options struct {
	// MaxConns is the maximum number of connections in the pool.
	// Defaults to pgxpool default (4 × NumCPU).
	MaxConns int32
	// MinConns keeps at least this many connections alive.
	MinConns int32
	// MaxConnLifetime is the maximum age of a connection before recycling.
	MaxConnLifetime time.Duration
	// MaxConnIdleTime is how long an idle connection is kept before closing.
	MaxConnIdleTime time.Duration
	// HealthCheckPeriod is how often the pool pings idle connections.
	HealthCheckPeriod time.Duration
}

// DB wraps a *pgxpool.Pool and provides the devengine PostgreSQL surface.
type DB struct {
	pool *pgxpool.Pool
}

// Open parses connString, applies opts and returns a connected DB.
// connString may be a DSN (key=value) or a postgres:// URL.
func Open(ctx context.Context, connString string, opts Options) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse config: %w", err)
	}
	return OpenConfig(ctx, cfg, opts)
}

// OpenConfig applies opts to cfg and returns a connected DB. cfg must have
// been created by pgxpool.ParseConfig. The provided config is not mutated.
func OpenConfig(ctx context.Context, cfg *pgxpool.Config, opts Options) (*DB, error) {
	if cfg == nil {
		return nil, errors.New("postgres: config is nil")
	}

	poolConfig := cfg.Copy()
	applyOptions(poolConfig, opts)

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Close shuts down the connection pool gracefully.
func (db *DB) Close() {
	if db != nil && db.pool != nil {
		db.pool.Close()
	}
}

// Ping verifies the pool can reach the database.
func (db *DB) Ping(ctx context.Context) error {
	if db == nil || db.pool == nil {
		return errors.New("postgres: database unavailable")
	}
	return db.pool.Ping(ctx)
}

// ReadyCheck returns a func suitable for health.Registry.Register.
func (db *DB) ReadyCheck() func(context.Context) error {
	return func(ctx context.Context) error {
		return db.Ping(ctx)
	}
}

// Pool returns the underlying *pgxpool.Pool for callers that need raw pgx
// features (COPY, LISTEN/NOTIFY, batch, etc.). Prefer Querier for normal use.
func (db *DB) Pool() *pgxpool.Pool {
	return db.pool
}

// Exec implements Querier against the pool.
func (db *DB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return db.pool.Exec(ctx, sql, args...)
}

// Query implements Querier against the pool.
func (db *DB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return db.pool.Query(ctx, sql, args...)
}

// QueryRow implements Querier against the pool.
func (db *DB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return db.pool.QueryRow(ctx, sql, args...)
}

// PgError unwraps err to a *pgconn.PgError if possible. Returns nil otherwise.
// Use this to inspect SQLSTATE codes without hiding the original error.
func PgError(err error) *pgconn.PgError {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr
	}
	return nil
}

// IsConstraintViolation reports whether err is a PostgreSQL SQLSTATE 23xxx
// (integrity constraint violation family).
func IsConstraintViolation(err error) bool {
	pg := PgError(err)
	return pg != nil && len(pg.Code) >= 2 && pg.Code[:2] == "23"
}

// IsUniqueViolation reports whether err is SQLSTATE 23505 (unique_violation).
func IsUniqueViolation(err error) bool {
	pg := PgError(err)
	return pg != nil && pg.Code == "23505"
}

func applyOptions(cfg *pgxpool.Config, opts Options) {
	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}
	if opts.MinConns > 0 {
		cfg.MinConns = opts.MinConns
	}
	if opts.MaxConnLifetime > 0 {
		cfg.MaxConnLifetime = opts.MaxConnLifetime
	}
	if opts.MaxConnIdleTime > 0 {
		cfg.MaxConnIdleTime = opts.MaxConnIdleTime
	}
	if opts.HealthCheckPeriod > 0 {
		cfg.HealthCheckPeriod = opts.HealthCheckPeriod
	}
}
