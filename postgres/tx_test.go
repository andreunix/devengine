package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

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
		nestedErr := db.WithTransaction(txCtx, func(nestedCtx context.Context, nestedTx pgx.Tx) error {
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

func TestWithTransactionDeadlockRetry(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()

	_, err := db.Pool().Exec(ctx, `CREATE TABLE tx_test (id INT PRIMARY KEY, val TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool().Exec(ctx, `INSERT INTO tx_test (id, val) VALUES (1, 'A'), (2, 'B')`)
	if err != nil {
		t.Fatal(err)
	}

	// Wait channels to coordinate the deadlock
	ready1 := make(chan struct{})
	ready2 := make(chan struct{})

	var err1, err2 error
	done := make(chan struct{}, 2)

	// Goroutine 1 locks row 1, waits for Goroutine 2 to lock row 2, then tries to lock row 2
	go func() {
		err1 = db.WithTransaction(ctx, func(txCtx context.Context, tx pgx.Tx) error {
			_, err := db.Querier(txCtx).Exec(txCtx, `SELECT val FROM tx_test WHERE id = 1 FOR UPDATE`)
			if err != nil {
				return err
			}
			// Signal we got row 1, but only on the first attempt so we don't block retries forever
			select {
			case ready1 <- struct{}{}:
			default:
			}
			// Wait for goroutine 2 to get row 2
			<-ready2
			// Now try to get row 2 -> DEADLOCK
			_, err = db.Querier(txCtx).Exec(txCtx, `SELECT val FROM tx_test WHERE id = 2 FOR UPDATE`)
			return err
		})
		done <- struct{}{}
	}()

	// Goroutine 2 locks row 2, waits for Goroutine 1 to lock row 1, then tries to lock row 1
	go func() {
		err2 = db.WithTransaction(ctx, func(txCtx context.Context, tx pgx.Tx) error {
			<-ready1
			_, err := db.Querier(txCtx).Exec(txCtx, `SELECT val FROM tx_test WHERE id = 2 FOR UPDATE`)
			if err != nil {
				return err
			}
			close(ready2)
			// Small sleep to ensure goroutine 1 gets blocked on row 2 before we try row 1
			time.Sleep(50 * time.Millisecond)
			// Now try to get row 1 -> DEADLOCK
			_, err = db.Querier(txCtx).Exec(txCtx, `SELECT val FROM tx_test WHERE id = 1 FOR UPDATE`)
			return err
		})
		done <- struct{}{}
	}()

	<-done
	<-done

	// One of them should have succeeded (due to retry), the other might have succeeded or failed depending on timing,
	// but basically Postgres resolves the deadlock by aborting one, and our retry logic should catch it.
	// Since both are using WithTransaction, they might BOTH eventually succeed if retries are sufficient.
	if err1 != nil {
		t.Errorf("Goroutine 1 failed: %v", err1)
	}
	if err2 != nil {
		t.Errorf("Goroutine 2 failed: %v", err2)
	}
}
