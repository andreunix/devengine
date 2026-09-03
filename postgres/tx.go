package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// txKey is the context key used to propagate an ambient transaction.
type txKey struct{}

// RetryConfig controls how WithTransaction retries on transient SQLSTATE errors.
//
// A callback passed to WithTransaction can run more than once. It must not
// perform external side effects such as sending email, publishing a webhook, or
// acknowledging a message broker. Persist an outbox message or a delayed job in
// the transaction instead, and let its handler perform the external action
// after commit.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts (including the first).
	// Zero or negative means no retry (single attempt).
	MaxAttempts int
	// RetryOn is the set of SQLSTATE codes that qualify for a retry.
	// If empty, defaults to {"40P01"} (deadlock_detected).
	RetryOn []string
	// InitialBackoff is the base wait before the first retry.
	// Defaults to 20ms.
	InitialBackoff time.Duration
	// MaxBackoff caps the exponential backoff.
	// Defaults to 500ms.
	MaxBackoff time.Duration
}

var defaultRetryConfig = RetryConfig{
	MaxAttempts:    3,
	RetryOn:        []string{"40P01"},
	InitialBackoff: 20 * time.Millisecond,
	MaxBackoff:     500 * time.Millisecond,
}

// WithTransaction executes fn inside a pgx transaction using default pgx
// transaction options. If ctx already carries
// an ambient transaction (via WithTransaction or InjectTx), fn is called within
// a savepoint instead of a new top-level transaction, implementing nested
// transaction semantics.
//
// The provided fn receives a txCtx which MUST be passed to subsequent
// db.Querier(txCtx) calls. This ensures cross-repository operations participate
// in the same transaction.
//
// Example:
//
//	db.WithTransaction(ctx, func(txCtx context.Context, tx pgx.Tx) error {
//	    if err := userRepo.Create(txCtx, user); err != nil { return err }
//	    if err := logRepo.Record(txCtx, "user_created"); err != nil { return err }
//	    return nil
//	})
//
// Transient errors whose SQLSTATE matches cfg.RetryOn cause automatic retry up
// to cfg.MaxAttempts times with exponential jitter backoff. Consequently fn is
// retryable code: it must not perform external side effects. Store an outbox
// message or a jobs.Job in tx and let the Outbox/Jobs worker run that side
// effect after the transaction commits.
//
// Non-transient errors, context cancellation, and business errors are never
// retried. If fn returns nil, the transaction (or savepoint) is committed.
func (db *DB) WithTransaction(ctx context.Context, fn func(context.Context, pgx.Tx) error, cfgs ...RetryConfig) error {
	return db.withTransaction(ctx, pgx.TxOptions{}, fn, cfgs...)
}

// WithTransactionOptions executes fn using txOptions for a new top-level pgx
// transaction. Its retry and side-effect contract is identical to
// WithTransaction.
//
// PostgreSQL transaction options are fixed when the outer transaction begins.
// Therefore, when ctx has an ambient transaction, zero txOptions uses a
// savepoint and non-zero txOptions returns an error rather than silently
// ignoring the requested isolation/access/deferrable modes.
func (db *DB) WithTransactionOptions(ctx context.Context, txOptions pgx.TxOptions, fn func(context.Context, pgx.Tx) error, cfgs ...RetryConfig) error {
	return db.withTransaction(ctx, txOptions, fn, cfgs...)
}

func (db *DB) withTransaction(ctx context.Context, txOptions pgx.TxOptions, fn func(context.Context, pgx.Tx) error, cfgs ...RetryConfig) error {
	cfg := defaultRetryConfig
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}
	if len(cfg.RetryOn) == 0 {
		cfg.RetryOn = []string{"40P01"}
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = 20 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 500 * time.Millisecond
	}

	// If there's an ambient transaction in ctx, use a savepoint.
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok && tx != nil {
		if txOptions != (pgx.TxOptions{}) {
			return errors.New("postgres: transaction options cannot be applied to an ambient transaction")
		}
		return withSavepoint(ctx, tx, fn)
	}

	var lastErr error
	backoff := cfg.InitialBackoff
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = db.runInTx(ctx, txOptions, fn)
		if lastErr == nil {
			return nil
		}
		if !isRetryable(lastErr, cfg.RetryOn) {
			return lastErr
		}
		if attempt < cfg.MaxAttempts {
			jitter := time.Duration(rand.Int64N(int64(backoff / 2)))
			sleep := backoff + jitter
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sleep):
			}
			backoff = min(backoff*2, cfg.MaxBackoff)
		}
	}
	return fmt.Errorf("postgres: exceeded %d attempts: %w", cfg.MaxAttempts, lastErr)
}

// Querier returns the ambient pgx.Tx stored in ctx, or the pool itself if no
// transaction is active. Use this in repositories to transparently participate
// in an outer transaction.
func (db *DB) Querier(ctx context.Context) Querier {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok && tx != nil {
		return tx
	}
	return db
}

// InjectTx returns a new context carrying tx as the ambient transaction.
// Use this in tests to inject a pre-opened transaction that will be rolled back.
func InjectTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

// runInTx opens a transaction, calls fn, and commits or rolls back.
func (db *DB) runInTx(ctx context.Context, txOptions pgx.TxOptions, fn func(context.Context, pgx.Tx) error) error {
	tx, err := db.pool.BeginTx(ctx, txOptions)
	if err != nil {
		return fmt.Errorf("postgres: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txCtx := context.WithValue(ctx, txKey{}, tx)
	if err := fn(txCtx, tx); err != nil {
		return err
	}
	if err := tx.Commit(txCtx); err != nil {
		return fmt.Errorf("postgres: commit transaction: %w", err)
	}
	return nil
}

// withSavepoint wraps fn in a SAVEPOINT / RELEASE or ROLLBACK TO SAVEPOINT.
func withSavepoint(ctx context.Context, tx pgx.Tx, fn func(context.Context, pgx.Tx) error) error {
	// pgx provides a NestedTransaction helper that manages savepoints.
	nested, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin savepoint: %w", err)
	}
	defer func() { _ = nested.Rollback(ctx) }()

	txCtx := context.WithValue(ctx, txKey{}, nested)
	if err := fn(txCtx, nested); err != nil {
		return err
	}
	if err := nested.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit savepoint: %w", err)
	}
	return nil
}

// isRetryable returns true when err's SQLSTATE is in the allowed set.
// Context errors and non-PgError values are never retryable.
func isRetryable(err error, codes []string) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	for _, code := range codes {
		if pgErr.Code == code {
			return true
		}
	}
	return false
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
