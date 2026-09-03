// Command transaction demonstrates the ambient transaction contract.
package main

import (
	"context"

	"github.com/andreunix/devengine/postgres"
	"github.com/jackc/pgx/v5"
)

func createAccount(ctx context.Context, db *postgres.DB, email string) error {
	return db.WithTransactionOptions(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(txCtx context.Context, _ pgx.Tx) error {
		_, err := db.Querier(txCtx).Exec(txCtx, `INSERT INTO accounts (email) VALUES ($1)`, email)
		return err
	})
}

func main() {}
