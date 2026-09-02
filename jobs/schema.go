package jobs

// Schema is the SQL used to create the jobs table. Consumers may embed and
// apply it via devengine/migrate.
const Schema = `
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

CREATE INDEX IF NOT EXISTS idx_devengine_jobs_run_at 
ON devengine_jobs(run_at) 
WHERE locked_until IS NULL OR locked_until <= NOW();
`
