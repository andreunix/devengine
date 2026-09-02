CREATE TABLE IF NOT EXISTS outbox_messages (
 id TEXT PRIMARY KEY, event_type TEXT NOT NULL, aggregate_id TEXT, aggregate_type TEXT,
 payload JSONB, schema_version INT NOT NULL DEFAULT 0, occurred_at TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP, attempt INT NOT NULL DEFAULT 0,
 max_attempts INT NOT NULL DEFAULT 5, process_after TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 processed_at TIMESTAMPTZ, failed_at TIMESTAMPTZ, last_error TEXT
);
