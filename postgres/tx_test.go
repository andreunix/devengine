package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andreunix/devengine/postgres"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
	"github.com/jackc/pgx/v5"
)

func TestWithTransactionCommit(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()

	_, err := db.Pool().Exec(ctx, `CREATE TABLE tx_test (id INT PRIMARY KEY, val TEXT)`)
	if err != nil {
		t.Fatal(err)
	}

	err = db.WithTransaction(ctx, func(txCtx context.Context, tx pgx.Tx) error {
		_, err := db.Querier(txCtx).Exec(txCtx, `INSERT INTO tx_test (id, val) VALUES (1, 'A')`)
		return err
	})
	if err != nil {
		t.Fatalf("WithTransaction failed: %v", err)
	}

	var val string
	err = db.Pool().QueryRow(ctx, `SELECT val FROM tx_test WHERE id = 1`).Scan(&val)
	if err != nil {
		t.Fatalf("QueryRow failed: %v", err)
	}
	if val != "A" {
		t.Fatalf("Expected A, got %s", val)
	}
}

func TestWithTransactionRollback(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()

	_, err := db.Pool().Exec(ctx, `CREATE TABLE tx_test (id INT PRIMARY KEY, val TEXT)`)
	if err != nil {
		t.Fatal(err)
	}

	expectedErr := errors.New("business error")
	err = db.WithTransaction(ctx, func(txCtx context.Context, tx pgx.Tx) error {
		_, err := db.Querier(txCtx).Exec(txCtx, `INSERT INTO tx_test (id, val) VALUES (2, 'B')`)
		if err != nil {
			return err
		}
		return expectedErr
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("Expected business error, got %v", err)
	}

	var count int
	err = db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM tx_test WHERE id = 2`).Scan(&count)
	if err != nil {
		t.Fatalf("QueryRow failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("Expected 0 rows due to rollback, got %d", count)
	}
}

func TestWithTransactionSavepoint(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()

	_, err := db.Pool().Exec(ctx, `CREATE TABLE tx_test (id INT PRIMARY KEY, val TEXT)`)
	if err != nil {
		t.Fatal(err)
	}

	err = db.WithTransaction(ctx, func(txCtx context.Context, tx pgx.Tx) error {
		_, err := db.Querier(txCtx).Exec(txCtx, `INSERT INTO tx_test (id, val) VALUES (3, 'C')`)
		if err != nil {
			return err
		}

		// Nested transaction (savepoint) that will be rolled back
		expectedErr := errors.New("nested error")
		nestedErr := db.WithTransactionOptions(txCtx, pgx.TxOptions{}, func(nestedCtx context.Context, nestedTx pgx.Tx) error {
			_, err := db.Querier(nestedCtx).Exec(nestedCtx, `INSERT INTO tx_test (id, val) VALUES (4, 'D')`)
			if err != nil {
				return err
			}
			return expectedErr
		})

		if !errors.Is(nestedErr, expectedErr) {
			t.Fatalf("Expected nested error, got %v", nestedErr)
		}

		// The outer transaction continues
		return nil
	})
	if err != nil {
		t.Fatalf("Outer WithTransaction failed: %v", err)
	}

	var count int
	err = db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM tx_test`).Scan(&count)
	if err != nil {
		t.Fatalf("QueryRow failed: %v", err)
	}
	// Only 'C' should have been committed, 'D' was rolled back via savepoint
	if count != 1 {
		t.Fatalf("Expected 1 row, got %d", count)
	}
}

func TestWithTransactionContextCancellation(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := db.WithTransaction(ctx, func(txCtx context.Context, tx pgx.Tx) error {
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Expected context.Canceled, got %v", err)
	}
}

func TestWithTransactionDoesNotRetryContextCancellation(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32

	err := db.WithTransaction(ctx, func(txCtx context.Context, tx pgx.Tx) error {
		attempts.Add(1)
		cancel()
		return txCtx.Err()
	}, postgres.RetryConfig{MaxAttempts: 3})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestWithTransactionOptions(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()

	err := db.WithTransactionOptions(ctx, pgx.TxOptions{
		IsoLevel:       pgx.Serializable,
		AccessMode:     pgx.ReadOnly,
		DeferrableMode: pgx.Deferrable,
	}, func(txCtx context.Context, tx pgx.Tx) error {
		var isolation, readOnly, deferrable string
		if err := tx.QueryRow(txCtx, "SHOW transaction_isolation").Scan(&isolation); err != nil {
			return err
		}
		if err := tx.QueryRow(txCtx, "SHOW transaction_read_only").Scan(&readOnly); err != nil {
			return err
		}
		if err := tx.QueryRow(txCtx, "SHOW transaction_deferrable").Scan(&deferrable); err != nil {
			return err
		}
		if isolation != "serializable" || readOnly != "on" || deferrable != "on" {
			return fmt.Errorf("transaction settings = %q, %q, %q", isolation, readOnly, deferrable)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTransactionOptions failed: %v", err)
	}
}

func TestWithTransactionOptionsPreservesNestedSavepoints(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool().Exec(ctx, `CREATE TABLE tx_options_savepoint (id INT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}

	err := db.WithTransactionOptions(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(txCtx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(txCtx, `INSERT INTO tx_options_savepoint(id) VALUES (1)`); err != nil {
			return err
		}
		nestedErr := db.WithTransaction(txCtx, func(nestedCtx context.Context, nestedTx pgx.Tx) error {
			if _, err := nestedTx.Exec(nestedCtx, `INSERT INTO tx_options_savepoint(id) VALUES (2)`); err != nil {
				return err
			}
			return errors.New("rollback nested savepoint")
		})
		if nestedErr == nil {
			return errors.New("expected nested savepoint error")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("outer transaction failed: %v", err)
	}

	var count int
	if err := db.Pool().QueryRow(ctx, `SELECT count(*) FROM tx_options_savepoint`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows after nested rollback = %d, want 1", count)
	}
}

func TestWithTransactionOptionsRejectsOptionsForAmbientTransaction(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	err := db.WithTransaction(context.Background(), func(txCtx context.Context, tx pgx.Tx) error {
		return db.WithTransactionOptions(txCtx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(context.Context, pgx.Tx) error {
			t.Fatal("callback must not run when options cannot apply to ambient transaction")
			return nil
		})
	})
	if err == nil || !strings.Contains(err.Error(), "ambient transaction") {
		t.Fatalf("expected ambient transaction options error, got %v", err)
	}
}

func TestWithTransactionDeadlockRetry(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := db.Pool().Exec(ctx, `CREATE TABLE tx_test (id INT PRIMARY KEY, val TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool().Exec(ctx, `INSERT INTO tx_test (id, val) VALUES (1, 'A'), (2, 'B')`)
	if err != nil {
		t.Fatal(err)
	}

	firstLocked := make(chan struct{})
	secondLocked := make(chan struct{})
	results := make(chan error, 2)
	var firstAttempts, secondAttempts atomic.Int32

	// The first attempt from each transaction takes locks in inverse order and
	// produces a real PostgreSQL deadlock. Retries use a stable lock order, so
	// they do not consume the one-shot coordination channels or block forever.
	go func() {
		results <- db.WithTransaction(ctx, func(txCtx context.Context, tx pgx.Tx) error {
			attempt := firstAttempts.Add(1)
			if attempt > 1 {
				_, err := db.Querier(txCtx).Exec(txCtx, `SELECT val FROM tx_test WHERE id IN (1, 2) ORDER BY id FOR UPDATE`)
				return err
			}

			_, err := db.Querier(txCtx).Exec(txCtx, `SELECT val FROM tx_test WHERE id = 1 FOR UPDATE`)
			if err != nil {
				return err
			}
			close(firstLocked)
			select {
			case <-secondLocked:
			case <-txCtx.Done():
				return txCtx.Err()
			}
			// Now try to get row 2 -> DEADLOCK.
			_, err = db.Querier(txCtx).Exec(txCtx, `SELECT val FROM tx_test WHERE id = 2 FOR UPDATE`)
			return err
		})
	}()

	go func() {
		results <- db.WithTransaction(ctx, func(txCtx context.Context, tx pgx.Tx) error {
			attempt := secondAttempts.Add(1)
			if attempt > 1 {
				_, err := db.Querier(txCtx).Exec(txCtx, `SELECT val FROM tx_test WHERE id IN (1, 2) ORDER BY id FOR UPDATE`)
				return err
			}

			select {
			case <-firstLocked:
			case <-txCtx.Done():
				return txCtx.Err()
			}
			_, err := db.Querier(txCtx).Exec(txCtx, `SELECT val FROM tx_test WHERE id = 2 FOR UPDATE`)
			if err != nil {
				return err
			}
			close(secondLocked)
			// Now try to get row 1 -> DEADLOCK.
			_, err = db.Querier(txCtx).Exec(txCtx, `SELECT val FROM tx_test WHERE id = 1 FOR UPDATE`)
			return err
		})
	}()

	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Errorf("transaction failed: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("transactions did not complete: %v", ctx.Err())
		}
	}
	if firstAttempts.Load() < 2 && secondAttempts.Load() < 2 {
		t.Fatal("expected one transaction to retry after the deadlock")
	}
}
