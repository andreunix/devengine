// Command outbox demonstrates atomic event enqueueing.
package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/andreunix/devengine/events"
	"github.com/andreunix/devengine/outbox"
	"github.com/jackc/pgx/v5"
)

func enqueueUserCreated(ctx context.Context, tx pgx.Tx, id string) error {
	event := events.Event{
		ID:          id,
		Type:        "user.created",
		Payload:     json.RawMessage(`{"name":"Ada"}`),
		OccurredAt:  time.Now(),
		AggregateID: id,
	}
	return outbox.Enqueue(ctx, tx, event,
		outbox.WithMaxAttempts(10),
		outbox.WithProcessAfter(time.Now().Add(time.Minute)),
	)
}

func main() {}
