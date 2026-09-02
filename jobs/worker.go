package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/andreunix/devengine/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkerConfig configures the jobs Worker.
type WorkerConfig struct {
	BatchSize      int
	PollInterval   time.Duration
	InitialBackoff time.Duration
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

// Worker is a background worker that polls the jobs table.
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
}

func (w *Worker) processBatch(ctx context.Context, logger *slog.Logger) error {
	ctx, span := w.Tracer.Start(ctx, "jobs.processBatch")
	defer span.End()

	batch := w.Config.batchSize()
	leaseDuration := 5 * time.Minute

	rows, err := w.Pool.Query(ctx, fmt.Sprintf(`
		UPDATE devengine_jobs
		SET locked_until = NOW() + $2::interval
		WHERE id IN (
			SELECT id
			FROM devengine_jobs
			WHERE run_at <= NOW()
			  AND (locked_until IS NULL OR locked_until <= NOW())
			ORDER BY run_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, name, payload, attempt, max_attempts
	`), batch, fmt.Sprintf("%d seconds", int(leaseDuration.Seconds())))
	if err != nil {
		return fmt.Errorf("jobs: claim batch: %w", err)
	}

	var jobs []jobRow
	for rows.Next() {
		var j jobRow
		if err := rows.Scan(&j.id, &j.name, &j.payload, &j.attempt, &j.maxAttempts); err != nil {
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
		jobCtx, jobSpan := w.Tracer.Start(ctx, "jobs.deliver")
		jobSpan.SetAttribute("job.id", j.id)
		jobSpan.SetAttribute("job.name", j.name)

		handler := w.Registry.HandlerFor(j.name)
		var processErr error
		if handler == nil {
			processErr = fmt.Errorf("no handler for job %q", j.name)
		} else {
			processErr = handler.Handle(jobCtx, j.payload)
		}

		if processErr != nil {
			jobSpan.RecordError(processErr)
		}
		jobSpan.End()

		nextAttempt := j.attempt + 1

		if processErr == nil {
			_, err = w.Pool.Exec(ctx, `DELETE FROM devengine_jobs WHERE id = $1`, j.id)
		} else {
			errMsg := processErr.Error()
			if len(errMsg) > 1024 {
				errMsg = errMsg[:1024]
			}
			if nextAttempt >= j.maxAttempts {
				logger.Error("jobs: job failed permanently",
					"id", j.id, "name", j.name, "attempts", nextAttempt, "error", processErr)
				_, err = w.Pool.Exec(ctx, `
					UPDATE devengine_jobs SET attempt = $2, last_error = $3, locked_until = 'infinity' WHERE id = $1
				`, j.id, nextAttempt, errMsg)
			} else {
				backoff := w.backoff(nextAttempt)
				logger.Warn("jobs: job failed, will retry",
					"id", j.id, "name", j.name, "attempt", nextAttempt, "retry_after", backoff)
				_, err = w.Pool.Exec(ctx, `
					UPDATE devengine_jobs SET attempt = $2, run_at = NOW() + $3::interval, locked_until = NULL, last_error = $4 WHERE id = $1
				`, j.id, nextAttempt,
					fmt.Sprintf("%d milliseconds", int(backoff.Milliseconds())),
					errMsg)
			}
		}
		if err != nil {
			logger.Error("jobs: failed to update job outcome", "id", j.id, "error", err)
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
