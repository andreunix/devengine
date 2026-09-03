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
//   - Lease ownership: every claim has a token and only its owner may record an
//     outcome. After lease expiry, delivery can happen again (at-least-once),
//     so handlers must remain idempotent; a stale worker cannot overwrite the
//     newer owner's outcome. Active handlers renew their lease; a lost lease
//     cancels the handler context and stops further renewals.
//   - Atomic enqueue: Enqueue must be called within the domain transaction.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/andreunix/devengine/events"
	"github.com/andreunix/devengine/id"
	"github.com/andreunix/devengine/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultBatchSize    = 50
	defaultPollInterval = 2 * time.Second
	defaultMaxAttempts  = 5
)

// Schema bootstraps the outbox table for ephemeral tests only. Production
// deployments must apply outbox.Migrations() for upgrade-safe evolution.
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
	, locked_until TIMESTAMPTZ
	, claim_token TEXT
)`

// RelayConfig configures the Relay worker.
type RelayConfig struct {
	// BatchSize is the number of messages fetched per poll cycle.
	BatchSize int
	// PollInterval is how often the relay checks for pending messages.
	PollInterval time.Duration
	// InitialBackoff is the base delay before the first retry.
	InitialBackoff time.Duration
	// LeaseDuration is the maximum time a relay owns a claimed message.
	LeaseDuration time.Duration
	// LeaseRenewalInterval controls how often an active handler renews its
	// lease. Zero renews at half LeaseDuration. Values at or above the lease
	// duration are clamped to half the lease to prevent expiry before renewal.
	LeaseRenewalInterval time.Duration
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

func (c *RelayConfig) initialBackoff() time.Duration {
	if c.InitialBackoff > 0 {
		return c.InitialBackoff
	}
	return 500 * time.Millisecond
}
func (c *RelayConfig) leaseDuration() time.Duration {
	if c.LeaseDuration > 0 {
		return c.LeaseDuration
	}
	return 5 * time.Minute
}

func (c *RelayConfig) leaseRenewalInterval() time.Duration {
	lease := c.leaseDuration()
	if c.LeaseRenewalInterval > 0 && c.LeaseRenewalInterval < lease {
		return c.LeaseRenewalInterval
	}
	interval := lease / 2
	if interval <= 0 {
		return time.Nanosecond
	}
	return interval
}

type enqueueConfig struct {
	maxAttempts  int
	processAfter time.Time
}

// EnqueueOption configures persistence of a single outbox message.
type EnqueueOption func(*enqueueConfig) error

// WithMaxAttempts sets the message-owned retry limit.
func WithMaxAttempts(maxAttempts int) EnqueueOption {
	return func(config *enqueueConfig) error {
		if maxAttempts <= 0 {
			return errors.New("outbox: max attempts must be greater than zero")
		}
		config.maxAttempts = maxAttempts
		return nil
	}
}

// WithProcessAfter delays delivery until the given time.
func WithProcessAfter(processAfter time.Time) EnqueueOption {
	return func(config *enqueueConfig) error {
		if processAfter.IsZero() {
			return errors.New("outbox: process after must not be zero")
		}
		config.processAfter = processAfter
		return nil
	}
}

// Enqueue writes event into the official outbox_messages table within tx. It
// must be called inside the same transaction as the domain change.
func Enqueue(ctx context.Context, tx pgx.Tx, event events.Event, options ...EnqueueOption) error {
	config := enqueueConfig{maxAttempts: defaultMaxAttempts, processAfter: time.Now()}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(&config); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("outbox: marshal payload: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_messages (
			id, event_type, aggregate_id, aggregate_type, payload, schema_version,
			occurred_at, max_attempts, process_after
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		event.ID,
		event.Type,
		event.AggregateID,
		event.AggregateType,
		payload,
		event.SchemaVersion,
		event.OccurredAt,
		config.maxAttempts,
		config.processAfter,
	)
	if err != nil {
		return fmt.Errorf("outbox: enqueue %s: %w", event.Type, err)
	}
	return nil
}

// Relay is a background worker that polls the outbox table and delivers events.
// outbox_messages.max_attempts is the sole authority for retry limits.
type Relay struct {
	Pool     *pgxpool.Pool
	Registry *events.Registry
	Logger   *slog.Logger
	Config   RelayConfig
	Tracer   telemetry.Tracer
	Meter    telemetry.Meter
}

// Name implements engine.Worker.
func (r *Relay) Name() string { return "outbox-relay" }

// Run starts the relay loop. It returns when ctx is cancelled.
func (r *Relay) Run(ctx context.Context) error {
	if r.Pool == nil {
		return errors.New("outbox: relay pool is required")
	}
	if r.Registry == nil {
		return errors.New("outbox: relay registry is required")
	}
	r.Registry.Freeze()
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if r.Tracer == nil {
		r.Tracer = telemetry.NoopTracer
	}
	if r.Meter == nil {
		r.Meter = telemetry.NoopMeter
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
	maxAttempts   int
	claimToken    string
}

func (r *Relay) processBatch(ctx context.Context, logger *slog.Logger) error {
	ctx, span := r.Tracer.Start(ctx, "outbox.processBatch")
	defer span.End()

	batch := r.Config.batchSize()
	leaseDuration := r.Config.leaseDuration()
	claimPrefix := id.MustUUIDv7()

	// Claim messages and lease them so other workers don't pick them up.
	rows, err := r.Pool.Query(ctx, `
		UPDATE outbox_messages
		SET process_after = NOW() + $2::interval, locked_until = NOW() + $2::interval, claim_token = $3 || ':' || id
		WHERE id IN (
			SELECT id
			FROM outbox_messages
			WHERE processed_at IS NULL
			  AND failed_at IS NULL
			  AND process_after <= NOW()
			  AND (locked_until IS NULL OR locked_until <= NOW())
			ORDER BY occurred_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, event_type, aggregate_id, aggregate_type, payload, schema_version, occurred_at, attempt, max_attempts, claim_token
	`, batch, postgresInterval(leaseDuration), claimPrefix)
	if err != nil {
		return fmt.Errorf("outbox: claim batch: %w", err)
	}

	var messages []outboxRow
	for rows.Next() {
		var m outboxRow
		if err := rows.Scan(&m.id, &m.eventType, &m.aggregateID, &m.aggregateType,
			&m.payload, &m.schemaVersion, &m.occurredAt, &m.attempt, &m.maxAttempts, &m.claimToken); err != nil {
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

		handlerCtx, stopRenewal := r.startLeaseRenewal(ctx, logger, m, leaseDuration)
		evCtx, evSpan := r.Tracer.Start(handlerCtx, "outbox.deliver")
		evSpan.SetAttribute("event.id", m.id)
		evSpan.SetAttribute("event.type", m.eventType)

		deliveryErr := r.deliver(evCtx, event)
		stopRenewal()
		if deliveryErr != nil {
			evSpan.RecordError(deliveryErr)
		}
		evSpan.End()

		nextAttempt := m.attempt + 1
		var tag pgconn.CommandTag

		if deliveryErr == nil {
			tag, err = r.Pool.Exec(ctx, `
				UPDATE outbox_messages SET processed_at = NOW() WHERE id = $1 AND claim_token = $2
			`, m.id, m.claimToken)
		} else {
			errMsg := deliveryErr.Error()
			errMsg = truncateUTF8(errMsg, 1024)
			if nextAttempt >= m.maxAttempts {
				logger.Error("outbox: message failed permanently",
					"id", m.id, "type", m.eventType, "attempts", nextAttempt, "error", deliveryErr)
				tag, err = r.Pool.Exec(ctx, `
					UPDATE outbox_messages SET failed_at = NOW(), attempt = $2, last_error = $3 WHERE id = $1 AND claim_token = $4
				`, m.id, nextAttempt, errMsg, m.claimToken)
			} else {
				backoff := r.backoff(nextAttempt)
				logger.Warn("outbox: delivery failed, will retry",
					"id", m.id, "type", m.eventType, "attempt", nextAttempt, "retry_after", backoff)
				tag, err = r.Pool.Exec(ctx, `
					UPDATE outbox_messages SET attempt = $2, process_after = NOW() + $3::interval, locked_until = NULL, claim_token = NULL, last_error = $4 WHERE id = $1 AND claim_token = $5
				`, m.id, nextAttempt,
					fmt.Sprintf("%d milliseconds", int(backoff.Milliseconds())),
					errMsg, m.claimToken)
			}
		}
		if err != nil {
			logger.Error("outbox: failed to update message outcome", "id", m.id, "error", err)
		} else if tag.RowsAffected() != 1 {
			logger.Warn("outbox: claim lost", "id", m.id, "type", m.eventType)
			r.Meter.Int64Counter("outbox.events").Add(ctx, 1, map[string]string{"status": "claim_lost", "type": m.eventType})
		} else {
			status := "delivered"
			if deliveryErr != nil {
				if nextAttempt >= m.maxAttempts {
					status = "failed"
				} else {
					status = "retried"
				}
			}
			r.Meter.Int64Counter("outbox.events").Add(ctx, 1, map[string]string{
				"status": status,
				"type":   m.eventType,
			})
		}
	}

	return nil
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func (r *Relay) startLeaseRenewal(parent context.Context, logger *slog.Logger, message outboxRow, lease time.Duration) (context.Context, func()) {
	handlerCtx, cancelHandler := context.WithCancel(parent)
	renewalCtx, cancelRenewal := context.WithCancel(parent)
	done := make(chan struct{})
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancelRenewal()
			<-done
			cancelHandler()
		})
	}

	go func() {
		defer close(done)
		ticker := time.NewTicker(r.Config.leaseRenewalInterval())
		defer ticker.Stop()
		for {
			select {
			case <-renewalCtx.Done():
				return
			case <-ticker.C:
				tag, err := r.Pool.Exec(renewalCtx, `
					UPDATE outbox_messages
					SET process_after = NOW() + $3::interval,
					    locked_until = NOW() + $3::interval
					WHERE id = $1 AND claim_token = $2 AND locked_until > NOW()
				`, message.id, message.claimToken, postgresInterval(lease))
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						logger.Warn("outbox: lease renewal failed", "id", message.id, "error", err)
					}
					continue
				}
				if tag.RowsAffected() != 1 {
					logger.Warn("outbox: lease ownership lost", "id", message.id, "type", message.eventType)
					cancelHandler()
					return
				}
			}
		}
	}()
	return handlerCtx, stop
}

func postgresInterval(duration time.Duration) string {
	return fmt.Sprintf("%.9f seconds", duration.Seconds())
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
		if err := callEventHandler(ctx, h, event); err != nil {
			errs = append(errs, fmt.Errorf("handler %T: %w", h, err))
		}
	}
	return errors.Join(errs...)
}

func callEventHandler(ctx context.Context, handler events.Handler, event events.Event) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("outbox: handler panic: %v", recovered)
		}
	}()
	return handler.Handle(ctx, event)
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
