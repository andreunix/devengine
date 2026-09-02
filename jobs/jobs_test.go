package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/andreunix/devengine/jobs"
	testpostgres "github.com/andreunix/devengine/testutil/postgres"
)

func TestJobsExecution(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	db := testpostgres.NewIsolatedDatabase(t)
	defer db.Close()

	_, err := db.Pool().Exec(context.Background(), jobs.Schema)
	if err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	registry := jobs.NewRegistry()
	handled := make(chan string, 1)

	registry.Register("send_email", jobs.HandlerFunc(func(ctx context.Context, payload []byte) error {
		var p map[string]string
		_ = json.Unmarshal(payload, &p)
		handled <- p["email"]
		return nil
	}))

	worker := &jobs.Worker{
		Pool:     db.Pool(),
		Registry: registry,
		Config: jobs.WorkerConfig{
			PollInterval: 10 * time.Millisecond,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)

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

	// Verify job is deleted
	var count int
	_ = db.Pool().QueryRow(context.Background(), `SELECT COUNT(*) FROM devengine_jobs`).Scan(&count)
	if count != 0 {
		t.Errorf("expected job to be deleted, got %d", count)
	}
}

func TestJobsRetry(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	db := testpostgres.NewIsolatedDatabase(t)
	defer db.Close()

	_, err := db.Pool().Exec(context.Background(), jobs.Schema)
	if err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	registry := jobs.NewRegistry()
	attempts := 0

	registry.Register("failing_job", jobs.HandlerFunc(func(ctx context.Context, payload []byte) error {
		attempts++
		return errors.New("temporary failure")
	}))

	worker := &jobs.Worker{
		Pool:     db.Pool(),
		Registry: registry,
		Config: jobs.WorkerConfig{
			PollInterval:   10 * time.Millisecond,
			InitialBackoff: 10 * time.Millisecond,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)

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

	// Wait enough time for 2 attempts
	time.Sleep(500 * time.Millisecond)

	if attempts != 2 {
		t.Errorf("expected exactly 2 attempts, got %d", attempts)
	}

	// Verify job is marked as failed permanently
	var lastError string
	var lockedUntil *time.Time
	err = db.Pool().QueryRow(context.Background(), `SELECT last_error, locked_until FROM devengine_jobs`).Scan(&lastError, &lockedUntil)
	if err != nil {
		t.Fatal(err)
	}
	if lastError != "temporary failure" {
		t.Errorf("expected last_error to be 'temporary failure', got %q", lastError)
	}
	if lockedUntil == nil {
		t.Error("expected job to be permanently locked ('infinity'), but got NULL")
	}
}
