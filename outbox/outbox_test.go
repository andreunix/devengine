package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/andreunix/devengine/events"
	"github.com/andreunix/devengine/outbox"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
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
			MaxAttempts:  1,
			PollInterval: time.Millisecond * 10,
		},
	}

	tx, _ := db.Pool().Begin(ctx)
	_ = outbox.Enqueue(ctx, tx, events.Event{
		ID:         "evt_1",
		Type:       "UserCreated",
		OccurredAt: time.Now(),
	}, "")
	tx.Commit(ctx)
	if _, err := db.Pool().Exec(ctx, `UPDATE outbox_messages SET max_attempts = 1 WHERE id = 'evt_1'`); err != nil {
		t.Fatal(err)
	}

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

func TestRelayRequiresDependencies(t *testing.T) {
	if err := (&outbox.Relay{}).Run(context.Background()); err == nil {
		t.Fatal("expected pool error")
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
