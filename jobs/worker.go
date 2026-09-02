package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/andreunix/devengine/id"
	"github.com/andreunix/devengine/telemetry"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkerConfig configures the jobs Worker.
type WorkerConfig struct {
	BatchSize      int
	PollInterval   time.Duration
	InitialBackoff time.Duration
	// LeaseDuration is the maximum time a worker owns a claimed job.
	LeaseDuration time.Duration
	// LeaseRenewalInterval controls how often an active handler renews its
	// lease. Zero renews at half LeaseDuration. Values at or above the lease
	// duration are clamped to half the lease to prevent expiry before renewal.
	LeaseRenewalInterval time.Duration
}

func (c *WorkerConfig) batchSize() int {
	if c.BatchSize > 0 {
		return c.BatchSize
	}
	return 50
}
func (c *WorkerConfig) pollInterval() time.Duration {
	if c.PollInterval > 0 {
		return c.PollInterval
	}
	return 2 * time.Second
}
func (c *WorkerConfig) initialBackoff() time.Duration {
	if c.InitialBackoff > 0 {
		return c.InitialBackoff
	}
	return 500 * time.Millisecond
}
func (c *WorkerConfig) leaseDuration() time.Duration {
	if c.LeaseDuration > 0 {
		return c.LeaseDuration
	}
	return 5 * time.Minute
}

func (c *WorkerConfig) leaseRenewalInterval() time.Duration {
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

// Worker is a background worker that polls the jobs table. Jobs use leased
// claims: active handlers renew their lease, and only the holder of the
// current claim token can renew or persist an outcome. Handlers must remain
// idempotent because delivery remains at-least-once (for example after a
// process crash or a lost lease).
type Worker struct {
	Pool     *pgxpool.Pool
	Registry *Registry
	Logger   *slog.Logger
	Config   WorkerConfig
	Tracer   telemetry.Tracer
	Meter    telemetry.Meter
}

// Name implements engine.Worker.
func (w *Worker) Name() string { return "jobs-worker" }

// Run starts the polling loop.
func (w *Worker) Run(ctx context.Context) error {
	if w.Pool == nil {
		return errors.New("jobs: worker pool is required")
	}
	if w.Registry == nil {
		return errors.New("jobs: worker registry is required")
	}
	logger := w.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if w.Tracer == nil {
		w.Tracer = telemetry.NoopTracer
	}
	if w.Meter == nil {
		w.Meter = telemetry.NoopMeter
	}
	ticker := time.NewTicker(w.Config.pollInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.processBatch(ctx, logger); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("jobs worker batch error", "error", err)
			}
		}
	}
}

type jobRow struct {
	id          string
	name        string
	payload     []byte
	attempt     int
	maxAttempts int
	claimToken  string
}

func (w *Worker) processBatch(ctx context.Context, logger *slog.Logger) error {
	ctx, span := w.Tracer.Start(ctx, "jobs.processBatch")
	defer span.End()

	batch := w.Config.batchSize()
	leaseDuration := w.Config.leaseDuration()
	claimPrefix := id.MustUUIDv7()

	rows, err := w.Pool.Query(ctx, fmt.Sprintf(`
		UPDATE devengine_jobs
		SET locked_until = NOW() + $2::interval, claim_token = $3 || ':' || id
		WHERE id IN (
			SELECT id
			FROM devengine_jobs
			WHERE run_at <= NOW()
			  AND (locked_until IS NULL OR locked_until <= NOW())
			ORDER BY run_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, name, payload, attempt, max_attempts, claim_token
	`), batch, postgresInterval(leaseDuration), claimPrefix)
	if err != nil {
		return fmt.Errorf("jobs: claim batch: %w", err)
	}

	var jobs []jobRow
	for rows.Next() {
		var j jobRow
		if err := rows.Scan(&j.id, &j.name, &j.payload, &j.attempt, &j.maxAttempts, &j.claimToken); err != nil {
			rows.Close()
			return fmt.Errorf("jobs: scan claim row: %w", err)
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("jobs: iterate claim rows: %w", err)
	}

	for _, j := range jobs {
		handlerCtx, stopRenewal := w.startLeaseRenewal(ctx, logger, j, leaseDuration)
		jobCtx, jobSpan := w.Tracer.Start(handlerCtx, "jobs.deliver")
		jobSpan.SetAttribute("job.id", j.id)
		jobSpan.SetAttribute("job.name", j.name)

		handler := w.Registry.HandlerFor(j.name)
		var processErr error
		if handler == nil {
			processErr = fmt.Errorf("no handler for job %q", j.name)
		} else {
			processErr = handler.Handle(jobCtx, j.payload)
		}
		stopRenewal()

		if processErr != nil {
			jobSpan.RecordError(processErr)
		}
		jobSpan.End()

		nextAttempt := j.attempt + 1
		var tag pgconn.CommandTag

		if processErr == nil {
			tag, err = w.Pool.Exec(ctx, `DELETE FROM devengine_jobs WHERE id = $1 AND claim_token = $2`, j.id, j.claimToken)
		} else {
			errMsg := processErr.Error()
			if len(errMsg) > 1024 {
				errMsg = errMsg[:1024]
			}
			if nextAttempt >= j.maxAttempts {
				logger.Error("jobs: job failed permanently",
					"id", j.id, "name", j.name, "attempts", nextAttempt, "error", processErr)
				tag, err = w.Pool.Exec(ctx, `
					UPDATE devengine_jobs SET attempt = $2, last_error = $3, locked_until = 'infinity' WHERE id = $1 AND claim_token = $4
				`, j.id, nextAttempt, errMsg, j.claimToken)
			} else {
				backoff := w.backoff(nextAttempt)
				logger.Warn("jobs: job failed, will retry",
					"id", j.id, "name", j.name, "attempt", nextAttempt, "retry_after", backoff)
				tag, err = w.Pool.Exec(ctx, `
					UPDATE devengine_jobs SET attempt = $2, run_at = NOW() + $3::interval, locked_until = NULL, claim_token = NULL, last_error = $4 WHERE id = $1 AND claim_token = $5
				`, j.id, nextAttempt,
					fmt.Sprintf("%d milliseconds", int(backoff.Milliseconds())),
					errMsg, j.claimToken)
			}
		}
		if err != nil {
			logger.Error("jobs: failed to update job outcome", "id", j.id, "error", err)
		} else if tag.RowsAffected() != 1 {
			logger.Warn("jobs: claim lost", "id", j.id, "name", j.name)
			w.Meter.Int64Counter("jobs.processed").Add(ctx, 1, map[string]string{"status": "claim_lost", "name": j.name})
		} else {
			status := "processed"
			if processErr != nil {
				if nextAttempt >= j.maxAttempts {
					status = "failed"
				} else {
					status = "retried"
				}
			}
			w.Meter.Int64Counter("jobs.processed").Add(ctx, 1, map[string]string{
				"status": status,
				"name":   j.name,
			})
		}
	}

	return nil
}

func (w *Worker) startLeaseRenewal(parent context.Context, logger *slog.Logger, job jobRow, lease time.Duration) (context.Context, func()) {
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
		ticker := time.NewTicker(w.Config.leaseRenewalInterval())
		defer ticker.Stop()
		for {
			select {
			case <-renewalCtx.Done():
				return
			case <-ticker.C:
				tag, err := w.Pool.Exec(renewalCtx, `
					UPDATE devengine_jobs
					SET locked_until = NOW() + $3::interval
					WHERE id = $1 AND claim_token = $2 AND locked_until > NOW()
				`, job.id, job.claimToken, postgresInterval(lease))
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						logger.Warn("jobs: lease renewal failed", "id", job.id, "error", err)
					}
					continue
				}
				if tag.RowsAffected() != 1 {
					logger.Warn("jobs: lease ownership lost", "id", job.id, "name", job.name)
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

func (w *Worker) backoff(attempt int) time.Duration {
	base := w.Config.initialBackoff()
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
