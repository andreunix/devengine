// Package outbox implements the transactional outbox pattern for PostgreSQL.
//
// # Pattern
//
// Application code enqueues events inside the same database transaction as the
// domain change. A background Relay worker polls the outbox table, delivers
// events to registered handlers, and marks them as processed — or records
// failures after exhausting retries.
//
// # Guarantees
//
//   - At-least-once delivery: the relay may deliver a message more than once
//     on restart; handlers must be idempotent.
//   - No concurrent delivery: FOR UPDATE SKIP LOCKED ensures two relay workers
//     never process the same message simultaneously.
//   - Atomic enqueue: Enqueue must be called within the domain transaction.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/andreunix/devengine/events"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultTable        = "outbox_messages"
	defaultBatchSize    = 50
	defaultPollInterval = 2 * time.Second
	defaultMaxAttempts  = 5
)

// Schema is the SQL used to create the outbox table. Consumers may embed and
// apply it via devengine/migrate.
const Schema = `CREATE TABLE IF NOT EXISTS outbox_messages (
	id            TEXT        PRIMARY KEY,
	event_type    TEXT        NOT NULL,
	aggregate_id  TEXT,
	aggregate_type TEXT,
	payload       JSONB,
	schema_version INT         NOT NULL DEFAULT 0,
	occurred_at   TIMESTAMPTZ NOT NULL,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	attempt       INT         NOT NULL DEFAULT 0,
	max_attempts  INT         NOT NULL DEFAULT 5,
	process_after TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	processed_at  TIMESTAMPTZ,
	failed_at     TIMESTAMPTZ,
	last_error    TEXT
)`

// RelayConfig configures the Relay worker.
type RelayConfig struct {
	// Table overrides the default "outbox_messages" table name.
	Table string
	// BatchSize is the number of messages fetched per poll cycle.
	BatchSize int
	// PollInterval is how often the relay checks for pending messages.
	PollInterval time.Duration
	// MaxAttempts is the total number of delivery attempts before a message is failed.
	MaxAttempts int
	// InitialBackoff is the base delay before the first retry.
	InitialBackoff time.Duration
}

func (c *RelayConfig) table() string {
	if c.Table != "" {
		return c.Table
	}
	return defaultTable
}

func (c *RelayConfig) batchSize() int {
	if c.BatchSize > 0 {
		return c.BatchSize
	}
	return defaultBatchSize
}

func (c *RelayConfig) pollInterval() time.Duration {
	if c.PollInterval > 0 {
		return c.PollInterval
	}
	return defaultPollInterval
}

func (c *RelayConfig) maxAttempts() int {
	if c.MaxAttempts > 0 {
		return c.MaxAttempts
	}
	return defaultMaxAttempts
}

func (c *RelayConfig) initialBackoff() time.Duration {
	if c.InitialBackoff > 0 {
		return c.InitialBackoff
	}
	return 500 * time.Millisecond
}

// Enqueue writes event into the outbox table within tx. It must be called
// inside the same transaction as the domain change to guarantee atomicity.
func Enqueue(ctx context.Context, tx pgx.Tx, event events.Event, table string) error {
	if table == "" {
		table = defaultTable
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("outbox: marshal payload: %w", err)
	}
	_, err = tx.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, event_type, aggregate_id, aggregate_type, payload, schema_version, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, table),
		event.ID,
		event.Type,
		event.AggregateID,
		event.AggregateType,
		payload,
		event.SchemaVersion,
		event.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("outbox: enqueue %s: %w", event.Type, err)
	}
	return nil
}

// Relay is a background worker that polls the outbox table and delivers events.
type Relay struct {
	Pool     *pgxpool.Pool
	Registry *events.Registry
	Logger   *slog.Logger
	Config   RelayConfig
}

// Name implements engine.Worker.
func (r *Relay) Name() string { return "outbox-relay" }

// Run starts the relay loop. It returns when ctx is cancelled.
func (r *Relay) Run(ctx context.Context) error {
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ticker := time.NewTicker(r.Config.pollInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.processBatch(ctx, logger); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("outbox relay batch error", "error", err)
			}
		}
	}
}

type outboxRow struct {
	id            string
	eventType     string
	aggregateID   string
	aggregateType string
	payload       []byte
	schemaVersion int
	occurredAt    time.Time
	attempt       int
}

func (r *Relay) processBatch(ctx context.Context, logger *slog.Logger) error {
	table := r.Config.table()
	batch := r.Config.batchSize()
	maxAttempts := r.Config.maxAttempts()
	leaseDuration := 5 * time.Minute

	// Claim messages and lease them so other workers don't pick them up.
	rows, err := r.Pool.Query(ctx, fmt.Sprintf(`
		UPDATE %s
		SET process_after = NOW() + $2::interval
		WHERE id IN (
			SELECT id
			FROM %s
			WHERE processed_at IS NULL
			  AND failed_at IS NULL
			  AND process_after <= NOW()
			ORDER BY occurred_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, event_type, aggregate_id, aggregate_type, payload, schema_version, occurred_at, attempt
	`, table, table), batch, fmt.Sprintf("%d seconds", int(leaseDuration.Seconds())))
	if err != nil {
		return fmt.Errorf("outbox: claim batch: %w", err)
	}

	var messages []outboxRow
	for rows.Next() {
		var m outboxRow
		if err := rows.Scan(&m.id, &m.eventType, &m.aggregateID, &m.aggregateType,
			&m.payload, &m.schemaVersion, &m.occurredAt, &m.attempt); err != nil {
			rows.Close()
			return fmt.Errorf("outbox: scan claim row: %w", err)
		}
		messages = append(messages, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("outbox: iterate claim rows: %w", err)
	}

	for _, m := range messages {
		event := events.Event{
			ID:            m.id,
			Type:          m.eventType,
			AggregateID:   m.aggregateID,
			AggregateType: m.aggregateType,
			Payload:       m.payload,
			SchemaVersion: m.schemaVersion,
			OccurredAt:    m.occurredAt,
		}

		deliveryErr := r.deliver(ctx, event)
		nextAttempt := m.attempt + 1

		if deliveryErr == nil {
			_, err = r.Pool.Exec(ctx, fmt.Sprintf(`
				UPDATE %s SET processed_at = NOW() WHERE id = $1
			`, table), m.id)
		} else {
			errMsg := deliveryErr.Error()
			if len(errMsg) > 1024 {
				errMsg = errMsg[:1024]
			}
			if nextAttempt >= maxAttempts {
				logger.Error("outbox: message failed permanently",
					"id", m.id, "type", m.eventType, "attempts", nextAttempt, "error", deliveryErr)
				_, err = r.Pool.Exec(ctx, fmt.Sprintf(`
					UPDATE %s SET failed_at = NOW(), attempt = $2, last_error = $3 WHERE id = $1
				`, table), m.id, nextAttempt, errMsg)
			} else {
				backoff := r.backoff(nextAttempt)
				logger.Warn("outbox: delivery failed, will retry",
					"id", m.id, "type", m.eventType, "attempt", nextAttempt, "retry_after", backoff)
				_, err = r.Pool.Exec(ctx, fmt.Sprintf(`
					UPDATE %s SET attempt = $2, process_after = NOW() + $3::interval, last_error = $4 WHERE id = $1
				`, table), m.id, nextAttempt,
					fmt.Sprintf("%d milliseconds", int(backoff.Milliseconds())),
					errMsg)
			}
		}
		if err != nil {
			logger.Error("outbox: failed to update message outcome", "id", m.id, "error", err)
		}
	}

	return nil
}

func (r *Relay) deliver(ctx context.Context, event events.Event) error {
	if r.Registry == nil {
		return errors.New("outbox: no event registry configured")
	}
	handlers := r.Registry.HandlersFor(event.Type)
	if len(handlers) == 0 {
		return fmt.Errorf("outbox: no handlers registered for event type %q", event.Type)
	}
	var errs []error
	for _, h := range handlers {
		if err := h.Handle(ctx, event); err != nil {
			errs = append(errs, fmt.Errorf("handler %T: %w", h, err))
		}
	}
	return errors.Join(errs...)
}

// backoff returns an exponential jitter delay for attempt n (1-indexed).
func (r *Relay) backoff(attempt int) time.Duration {
	base := r.Config.initialBackoff()
	exp := base
	for i := 1; i < attempt; i++ {
		exp *= 2
	}
	max := exp * 2
	jitter := time.Duration(rand.Int64N(int64(exp)))
	d := exp + jitter
	if max > 0 && d > max {
		d = max
	}
	return d
}
