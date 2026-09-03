ALTER TABLE outbox_messages ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ;
ALTER TABLE outbox_messages ADD COLUMN IF NOT EXISTS claim_token TEXT;
