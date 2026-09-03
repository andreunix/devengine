package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrFailedJobNotFound indicates that no permanently failed job matched the
// requested operation.
var ErrFailedJobNotFound = errors.New("jobs: permanently failed job not found")

// FailedJob is a permanently failed job available for operational inspection.
type FailedJob struct {
	ID          string
	Name        string
	Payload     json.RawMessage
	RunAt       time.Time
	Attempt     int
	MaxAttempts int
	LastError   string
	CreatedAt   time.Time
}

// Repository provides PostgreSQL-native administrative operations for jobs.
type Repository struct {
	Pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{Pool: pool}
}

// CountFailed returns the number of permanently failed jobs.
func (r *Repository) CountFailed(ctx context.Context) (int64, error) {
	if err := r.validate(); err != nil {
		return 0, err
	}
	var count int64
	if err := r.Pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM devengine_jobs
		WHERE locked_until = 'infinity'::timestamptz
	`).Scan(&count); err != nil {
		return 0, fmt.Errorf("jobs: count failed: %w", err)
	}
	return count, nil
}

// ListFailed returns permanently failed jobs ordered from oldest to newest.
// A non-positive limit defaults to 100.
func (r *Repository) ListFailed(ctx context.Context, limit int) ([]FailedJob, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.Pool.Query(ctx, `
		SELECT id, name, payload, run_at, attempt, max_attempts, COALESCE(last_error, ''), created_at
		FROM devengine_jobs
		WHERE locked_until = 'infinity'::timestamptz
		ORDER BY created_at, id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("jobs: list failed: %w", err)
	}
	defer rows.Close()

	failed := make([]FailedJob, 0)
	for rows.Next() {
		job, err := scanFailedJob(rows)
		if err != nil {
			return nil, err
		}
		failed = append(failed, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: list failed rows: %w", err)
	}
	return failed, nil
}

// GetFailed returns one permanently failed job for inspection.
func (r *Repository) GetFailed(ctx context.Context, id string) (FailedJob, error) {
	if err := r.validate(); err != nil {
		return FailedJob{}, err
	}
	job, err := scanFailedJob(r.Pool.QueryRow(ctx, `
		SELECT id, name, payload, run_at, attempt, max_attempts, COALESCE(last_error, ''), created_at
		FROM devengine_jobs
		WHERE id = $1 AND locked_until = 'infinity'::timestamptz
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return FailedJob{}, fmt.Errorf("%w: %s", ErrFailedJobNotFound, id)
	}
	return job, err
}

// RequeueFailed makes a permanently failed job eligible for delivery again.
// It resets its attempt history and schedules it at runAt; a zero runAt means now.
func (r *Repository) RequeueFailed(ctx context.Context, id string, runAt time.Time) error {
	if err := r.validate(); err != nil {
		return err
	}
	if runAt.IsZero() {
		runAt = time.Now()
	}
	tag, err := r.Pool.Exec(ctx, `
		UPDATE devengine_jobs
		SET run_at = $2, attempt = 0, last_error = NULL,
			locked_until = NULL, claim_token = NULL
		WHERE id = $1 AND locked_until = 'infinity'::timestamptz
	`, id, runAt)
	if err != nil {
		return fmt.Errorf("jobs: requeue failed job: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: %s", ErrFailedJobNotFound, id)
	}
	return nil
}

// DiscardFailed permanently deletes a failed job.
func (r *Repository) DiscardFailed(ctx context.Context, id string) error {
	if err := r.validate(); err != nil {
		return err
	}
	tag, err := r.Pool.Exec(ctx, `
		DELETE FROM devengine_jobs
		WHERE id = $1 AND locked_until = 'infinity'::timestamptz
	`, id)
	if err != nil {
		return fmt.Errorf("jobs: discard failed job: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: %s", ErrFailedJobNotFound, id)
	}
	return nil
}

func (r *Repository) validate() error {
	if r == nil || r.Pool == nil {
		return errors.New("jobs: repository pool is required")
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanFailedJob(row rowScanner) (FailedJob, error) {
	var job FailedJob
	if err := row.Scan(
		&job.ID,
		&job.Name,
		&job.Payload,
		&job.RunAt,
		&job.Attempt,
		&job.MaxAttempts,
		&job.LastError,
		&job.CreatedAt,
	); err != nil {
		return FailedJob{}, fmt.Errorf("jobs: scan failed job: %w", err)
	}
	return job, nil
}
