package jobs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andreunix/devengine/jobs"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
)

func TestRepositoryManagesPermanentlyFailedJobs(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool().Exec(ctx, jobs.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `
		INSERT INTO devengine_jobs
			(id, name, payload, run_at, attempt, max_attempts, last_error, locked_until, claim_token)
		VALUES
			('failed-a', 'email', '{"to":"a@example.com"}', NOW(), 5, 5, 'delivery failed', 'infinity', 'old-claim'),
			('ready-a', 'email', '{}', NOW(), 0, 5, NULL, NULL, NULL)
	`); err != nil {
		t.Fatal(err)
	}

	repository := jobs.NewRepository(db.Pool())
	count, err := repository.CountFailed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("failed count = %d, want 1", count)
	}

	failed, err := repository.ListFailed(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].ID != "failed-a" || failed[0].LastError != "delivery failed" {
		t.Fatalf("failed jobs = %+v", failed)
	}
	inspected, err := repository.GetFailed(ctx, "failed-a")
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Name != "email" || inspected.Attempt != 5 || string(inspected.Payload) != `{"to": "a@example.com"}` {
		t.Fatalf("inspected job = %+v", inspected)
	}
	if _, err := repository.GetFailed(ctx, "ready-a"); !errors.Is(err, jobs.ErrFailedJobNotFound) {
		t.Fatalf("inspect ready job error = %v", err)
	}

	runAt := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	if err := repository.RequeueFailed(ctx, "failed-a", runAt); err != nil {
		t.Fatal(err)
	}
	var storedRunAt time.Time
	var attempt int
	var lastError, lockedUntil, claimToken *string
	if err := db.Pool().QueryRow(ctx, `
		SELECT run_at, attempt, last_error, locked_until::text, claim_token
		FROM devengine_jobs WHERE id = 'failed-a'
	`).Scan(&storedRunAt, &attempt, &lastError, &lockedUntil, &claimToken); err != nil {
		t.Fatal(err)
	}
	if !storedRunAt.Equal(runAt) || attempt != 0 || lastError != nil || lockedUntil != nil || claimToken != nil {
		t.Fatalf("requeued state: run_at=%s attempt=%d last_error=%v locked_until=%v claim_token=%v", storedRunAt, attempt, lastError, lockedUntil, claimToken)
	}
	if err := repository.RequeueFailed(ctx, "ready-a", time.Time{}); !errors.Is(err, jobs.ErrFailedJobNotFound) {
		t.Fatalf("requeue ready job error = %v", err)
	}

	if _, err := db.Pool().Exec(ctx, `
		INSERT INTO devengine_jobs (id, name, locked_until, last_error)
		VALUES ('failed-b', 'cleanup', 'infinity', 'permanent')
	`); err != nil {
		t.Fatal(err)
	}
	if err := repository.DiscardFailed(ctx, "failed-b"); err != nil {
		t.Fatal(err)
	}
	if err := repository.DiscardFailed(ctx, "failed-b"); !errors.Is(err, jobs.ErrFailedJobNotFound) {
		t.Fatalf("discard missing job error = %v", err)
	}
	var exists bool
	if err := db.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM devengine_jobs WHERE id = 'failed-b')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("discarded failed job still exists")
	}
}

func TestRepositoryRequiresPool(t *testing.T) {
	repository := jobs.NewRepository(nil)
	if _, err := repository.CountFailed(context.Background()); err == nil {
		t.Fatal("expected missing pool error")
	}
}
