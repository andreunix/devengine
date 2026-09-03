// Command jobs demonstrates enqueueing a delayed persistent job.
package main

import (
	"context"
	"time"

	"github.com/andreunix/devengine/jobs"
	"github.com/jackc/pgx/v5"
)

func enqueueEmail(ctx context.Context, tx pgx.Tx, recipient string) error {
	return jobs.Enqueue(ctx, tx, jobs.Job{
		Name:        "send-email",
		Payload:     map[string]string{"to": recipient},
		RunAt:       time.Now().Add(time.Minute),
		MaxAttempts: 5,
	})
}

func main() {}
