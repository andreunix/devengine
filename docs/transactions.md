# PostgreSQL transactions

`postgres.DB.WithTransaction` executes its callback in a pgx transaction. The
callback context carries the ambient transaction; pass it to queries and
`db.Querier(txCtx)` so all repositories share one unit of work.

```go
err := db.WithTransaction(ctx, func(txCtx context.Context, tx pgx.Tx) error {
	if _, err := db.Querier(txCtx).Exec(txCtx, `INSERT INTO orders (id) VALUES ($1)`, id); err != nil {
		return err
	}
	return outbox.Enqueue(txCtx, tx, event)
})
```

## Retries and external side effects

By default, `WithTransaction` retries PostgreSQL `40P01` (deadlock) errors up
to three attempts. The callback can therefore execute more than once.

It must not send email, publish webhooks, call HTTP services, acknowledge a
broker message, or perform another external side effect. Persist the intent in
the same transaction through Outbox or Jobs instead. Their handlers perform
the external action after commit and must be idempotent.

Business errors and context cancellation or deadline errors are not retried.
`RetryConfig` customizes attempts, SQLSTATE codes, and backoff.

## pgx options

Use `WithTransactionOptions` to pass `pgx.TxOptions` directly:

```go
err := db.WithTransactionOptions(ctx, pgx.TxOptions{
	IsoLevel:       pgx.Serializable,
	AccessMode:     pgx.ReadOnly,
	DeferrableMode: pgx.Deferrable,
}, func(txCtx context.Context, tx pgx.Tx) error {
	return reports.Build(txCtx, tx)
})
```

Inside an ambient transaction, `WithTransaction` creates a savepoint.
Isolation, access, and deferrable modes belong to the outer transaction and
cannot change at a savepoint. Consequently, `WithTransactionOptions` accepts
only zero options in that case and rejects non-zero options explicitly.
