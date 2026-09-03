package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andreunix/devengine/jobs"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSchemaIsValidAndIdempotent(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()

	for range 2 {
		if _, err := db.Pool().Exec(ctx, jobs.Schema); err != nil {
			t.Fatalf("apply jobs schema: %v", err)
		}
	}

	var definition string
	err := db.Pool().QueryRow(ctx, `
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = 'public' AND indexname = 'idx_devengine_jobs_schedule'
	`).Scan(&definition)
	if err != nil {
		t.Fatalf("read jobs schedule index: %v", err)
	}

	definition = strings.ToLower(definition)
	if strings.Contains(definition, " where ") || strings.Contains(definition, "now(") {
		t.Fatalf("jobs schedule index must not have a runtime predicate: %s", definition)
	}
	if !strings.Contains(definition, "(run_at, locked_until)") {
		t.Fatalf("jobs schedule index has unexpected columns: %s", definition)
	}
}

func TestWorkerRequiresDependencies(t *testing.T) {
	if err := (&jobs.Worker{}).Run(context.Background()); err == nil {
		t.Fatal("expected pool error")
	}
	if err := (&jobs.Worker{Pool: &pgxpool.Pool{}}).Run(context.Background()); err == nil {
		t.Fatal("expected registry error")
	}
}

func TestStaleClaimCannotCompleteJob(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)
	ctx := context.Background()
	if _, err := db.Pool().Exec(ctx, jobs.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool().Exec(ctx, `INSERT INTO devengine_jobs (id, name) VALUES ('lease-job', 'test')`); err != nil {
		t.Fatal(err)
	}
	var first, second string
	if err := db.Pool().QueryRow(ctx, `UPDATE devengine_jobs SET locked_until = NOW() + interval '1 second', claim_token = 'first' WHERE id = 'lease-job' RETURNING claim_token`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool().QueryRow(ctx, `UPDATE devengine_jobs SET locked_until = NOW() + interval '1 second', claim_token = 'second' WHERE id = 'lease-job' AND claim_token = $1 RETURNING claim_token`, first).Scan(&second); err != nil {
		t.Fatal(err)
	}
	result, err := db.Pool().Exec(ctx, `DELETE FROM devengine_jobs WHERE id = 'lease-job' AND claim_token = $1`, first)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsAffected() != 0 {
		t.Fatal("stale claim deleted job")
	}
	var token string
	if err := db.Pool().QueryRow(ctx, `SELECT claim_token FROM devengine_jobs WHERE id = 'lease-job'`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if token != second {
		t.Fatalf("claim token = %q, want %q", token, second)
	}
}

func TestJobsExecution(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)

	_, err := db.Pool().Exec(context.Background(), jobs.Schema)
	if err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	registry := jobs.NewRegistry()
	handled := make(chan string, 1)

	if err := registry.Register("send_email", jobs.HandlerFunc(func(ctx context.Context, payload []byte) error {
		var p map[string]string
		_ = json.Unmarshal(payload, &p)
		handled <- p["email"]
		return nil
	})); err != nil {
		t.Fatal(err)
	}

	worker := &jobs.Worker{
		Pool:     db.Pool(),
		Registry: registry,
		Config: jobs.WorkerConfig{
			PollInterval: 10 * time.Millisecond,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-workerDone:
			if err != nil {
				t.Errorf("jobs worker stopped with error: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("jobs worker did not stop after cancellation")
		}
	})

	tx, err := db.Pool().Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = jobs.Enqueue(context.Background(), tx, jobs.Job{
		Name:    "send_email",
		Payload: map[string]string{"email": "test@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tx.Commit(context.Background())

	select {
	case email := <-handled:
		if email != "test@example.com" {
			t.Errorf("expected test@example.com, got %v", email)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for job to be processed")
	}

	deadline := time.After(2 * time.Second)
	for {
		var count int
		if err := db.Pool().QueryRow(context.Background(), `SELECT COUNT(*) FROM devengine_jobs`).Scan(&count); err != nil {
			t.Fatalf("count jobs: %v", err)
		}
		if count == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("expected job to be deleted, got %d", count)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestJobsRetry(t *testing.T) {
	db := testpostgres.NewIsolatedDatabase(t)

	_, err := db.Pool().Exec(context.Background(), jobs.Schema)
	if err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	registry := jobs.NewRegistry()
	var attempts atomic.Int32

	if err := registry.Register("failing_job", jobs.HandlerFunc(func(ctx context.Context, payload []byte) error {
		attempts.Add(1)
		return errors.New("temporary failure")
	})); err != nil {
		t.Fatal(err)
	}

	worker := &jobs.Worker{
		Pool:     db.Pool(),
		Registry: registry,
		Config: jobs.WorkerConfig{
			PollInterval:   10 * time.Millisecond,
			InitialBackoff: 10 * time.Millisecond,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-workerDone:
			if err != nil {
				t.Errorf("jobs worker stopped with error: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("jobs worker did not stop after cancellation")
		}
	})

	tx, err := db.Pool().Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = jobs.Enqueue(context.Background(), tx, jobs.Job{
		Name:        "failing_job",
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	tx.Commit(context.Background())

	deadline := time.After(2 * time.Second)
	for attempts.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("expected 2 attempts, got %d", attempts.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("expected exactly 2 attempts, got %d", got)
	}

	// Verify job is marked as failed permanently
	var lastError string
	var permanentlyLocked bool
	err = db.Pool().QueryRow(context.Background(), `
		SELECT last_error, locked_until = 'infinity'::timestamptz
		FROM devengine_jobs
	`).Scan(&lastError, &permanentlyLocked)
	if err != nil {
		t.Fatal(err)
	}
	if lastError != "temporary failure" {
		t.Errorf("expected last_error to be 'temporary failure', got %q", lastError)
	}
	if !permanentlyLocked {
		t.Error("expected job to be permanently locked ('infinity')")
	}
}
