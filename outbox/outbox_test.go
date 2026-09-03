package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/andreunix/devengine/events"
	"github.com/andreunix/devengine/outbox"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOutboxUnhandledPolicy(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()

	_, err := db.Pool().Exec(ctx, outbox.Schema)
	if err != nil {
		t.Fatal(err)
	}

	// No registry configured, or registry with no handlers
	registry := &events.Registry{}

	relay := &outbox.Relay{
		Pool:     db.Pool(),
		Registry: registry,
		Config: outbox.RelayConfig{
			BatchSize:    10,
			PollInterval: time.Millisecond * 10,
		},
	}

	tx, _ := db.Pool().Begin(ctx)
	_ = outbox.Enqueue(ctx, tx, events.Event{
		ID:         "evt_1",
		Type:       "UserCreated",
		OccurredAt: time.Now(),
	}, outbox.WithMaxAttempts(1))
	tx.Commit(ctx)

	relayCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- relay.Run(relayCtx) }()
	t.Cleanup(func() { cancel(); <-done })

	// Wait for the persisted outcome instead of racing a short context timeout.
	deadline := time.After(2 * time.Second)
	for {
		var failedAt *time.Time
		var lastError *string
		err = db.Pool().QueryRow(ctx, `SELECT failed_at, last_error FROM outbox_messages WHERE id = 'evt_1'`).Scan(&failedAt, &lastError)
		if err != nil {
			t.Fatal(err)
		}
		if failedAt != nil && lastError != nil && *lastError != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("message was not marked failed")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestEnqueueOptionsPersistMessagePolicy(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool().Exec(ctx, outbox.Schema); err != nil {
		t.Fatal(err)
	}
	processAfter := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	tx, err := db.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := outbox.Enqueue(ctx, tx, events.Event{
		ID: "configured", Type: "UserCreated", OccurredAt: time.Now(),
	}, outbox.WithMaxAttempts(10), outbox.WithProcessAfter(processAfter)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var maxAttempts int
	var gotProcessAfter time.Time
	if err := db.Pool().QueryRow(ctx, `
		SELECT max_attempts, process_after FROM outbox_messages WHERE id = 'configured'
	`).Scan(&maxAttempts, &gotProcessAfter); err != nil {
		t.Fatal(err)
	}
	if maxAttempts != 10 || !gotProcessAfter.Equal(processAfter) {
		t.Fatalf("max_attempts=%d process_after=%s", maxAttempts, gotProcessAfter)
	}
}

func TestEnqueueRejectsInvalidOptions(t *testing.T) {
	if err := outbox.WithMaxAttempts(0)(nil); err == nil {
		t.Fatal("expected invalid max attempts error")
	}
	if err := outbox.WithProcessAfter(time.Time{})(nil); err == nil {
		t.Fatal("expected zero process after error")
	}
}

func TestRelayRequiresDependencies(t *testing.T) {
	if err := (&outbox.Relay{}).Run(context.Background()); err == nil {
		t.Fatal("expected pool error")
	}
	if err := (&outbox.Relay{Pool: &pgxpool.Pool{}}).Run(context.Background()); err == nil {
		t.Fatal("expected registry error")
	}
}

func TestRelayUsesPersistedMaxAttemptsPerMessage(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool().Exec(ctx, outbox.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `
		INSERT INTO outbox_messages (id, event_type, aggregate_id, aggregate_type, occurred_at, max_attempts)
		VALUES ('one', 'missing', '', '', NOW(), 1), ('two', 'missing', '', '', NOW(), 2)
	`); err != nil {
		t.Fatal(err)
	}
	relay := &outbox.Relay{Pool: db.Pool(), Registry: events.NewRegistry(), Config: outbox.RelayConfig{PollInterval: time.Millisecond, InitialBackoff: time.Millisecond}}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- relay.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.After(2 * time.Second)
	for {
		var oneFailed, twoFailed bool
		if err := db.Pool().QueryRow(ctx, `SELECT failed_at IS NOT NULL FROM outbox_messages WHERE id='one'`).Scan(&oneFailed); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool().QueryRow(ctx, `SELECT failed_at IS NOT NULL FROM outbox_messages WHERE id='two'`).Scan(&twoFailed); err != nil {
			t.Fatal(err)
		}
		if oneFailed && twoFailed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("messages did not exhaust retries")
		case <-time.After(10 * time.Millisecond):
		}
	}
	var oneAttempts, twoAttempts int
	if err := db.Pool().QueryRow(ctx, `SELECT attempt FROM outbox_messages WHERE id='one'`).Scan(&oneAttempts); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool().QueryRow(ctx, `SELECT attempt FROM outbox_messages WHERE id='two'`).Scan(&twoAttempts); err != nil {
		t.Fatal(err)
	}
	if oneAttempts != 1 || twoAttempts != 2 {
		t.Fatalf("attempts one=%d two=%d", oneAttempts, twoAttempts)
	}
}

func TestStaleClaimCannotCompleteOutboxMessage(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool().Exec(ctx, outbox.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `INSERT INTO outbox_messages (id, event_type, occurred_at) VALUES ('lease-event', 'test', NOW())`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `UPDATE outbox_messages SET claim_token = 'second', locked_until = NOW() + interval '1 second' WHERE id = 'lease-event'`); err != nil {
		t.Fatal(err)
	}
	result, err := db.Pool().Exec(ctx, `UPDATE outbox_messages SET processed_at = NOW() WHERE id = 'lease-event' AND claim_token = 'first'`)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsAffected() != 0 {
		t.Fatal("stale claim completed outbox message")
	}
	var token string
	if err := db.Pool().QueryRow(ctx, `SELECT claim_token FROM outbox_messages WHERE id = 'lease-event'`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if token != "second" {
		t.Fatalf("claim token = %q", token)
	}
}
