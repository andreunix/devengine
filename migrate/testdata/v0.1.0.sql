-- Exact persistent schema published by devengine v0.1.0.
CREATE TABLE IF NOT EXISTS devengine_jobs (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	payload JSONB,
	run_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	attempt INT NOT NULL DEFAULT 0,
	max_attempts INT NOT NULL DEFAULT 5,
	last_error TEXT,
	locked_until TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- v0.1.0 attempted a partial index using NOW(), which PostgreSQL rejects in
-- index predicates. It was therefore not part of any successfully created
-- v0.1.0 database schema.

CREATE TABLE IF NOT EXISTS outbox_messages (
	id            TEXT        PRIMARY KEY,
	event_type    TEXT        NOT NULL,
	aggregate_id  TEXT,
	aggregate_type TEXT,
	payload       JSONB,
	schema_version INT         NOT NULL DEFAULT 0,
	occurred_at   TIMESTAMPTZ NOT NULL,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	attempt       INT         NOT NULL DEFAULT 0,
	max_attempts  INT         NOT NULL DEFAULT 5,
	process_after TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
	processed_at  TIMESTAMPTZ,
	failed_at     TIMESTAMPTZ,
	last_error    TEXT
);
