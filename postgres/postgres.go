// Package postgres contains database/sql helpers for PostgreSQL applications.
// The engine intentionally does not select a driver. Applications may use pgx/stdlib
// or another PostgreSQL database/sql driver and pass the resulting *sql.DB here.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Options struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

func Configure(db *sql.DB, options Options) error {
	if db == nil {
		return errors.New("postgres: nil database")
	}
	if options.MaxOpenConns > 0 {
		db.SetMaxOpenConns(options.MaxOpenConns)
	}
	if options.MaxIdleConns > 0 {
		db.SetMaxIdleConns(options.MaxIdleConns)
	}
	if options.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(options.ConnMaxLifetime)
	}
	if options.ConnMaxIdleTime > 0 {
		db.SetConnMaxIdleTime(options.ConnMaxIdleTime)
	}
	return nil
}

func ReadyCheck(db *sql.DB) func(context.Context) error {
	return func(ctx context.Context) error {
		if db == nil {
			return errors.New("postgres: database unavailable")
		}
		return db.PingContext(ctx)
	}
}

func WithTx(ctx context.Context, db *sql.DB, options *sql.TxOptions, fn func(*sql.Tx) error) error {
	if db == nil {
		return errors.New("postgres: nil database")
	}
	if fn == nil {
		return errors.New("postgres: nil transaction callback")
	}
	tx, err := db.BeginTx(ctx, options)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
