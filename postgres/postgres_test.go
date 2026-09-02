package postgres_test

import (
	"context"
	"testing"
	"time"

	testpostgres "github.com/andreunix/devengine/testutil/postgres"
)

func TestDBConnectAndPing(t *testing.T) {
	db := testpostgres.Open(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := db.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
}

func TestReadyCheck(t *testing.T) {
	db := testpostgres.Open(t)

	check := db.ReadyCheck()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := check(ctx); err != nil {
		t.Fatalf("ReadyCheck failed: %v", err)
	}
}
