ALTER TABLE users ADD COLUMN deletion_requested_at TIMESTAMPTZ;
CREATE TABLE account_deletions (
 id UUID PRIMARY KEY,
 user_id UUID UNIQUE REFERENCES users(id) ON DELETE SET NULL,
 replay_hash TEXT UNIQUE NOT NULL,
 body_hash TEXT NOT NULL,
 receipt_hash TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','failed','completed')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 completed_at TIMESTAMPTZ,
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 attempts INTEGER NOT NULL DEFAULT 0,
 CHECK ((status = 'completed') = (completed_at IS NOT NULL))
);
CREATE INDEX account_deletions_work ON account_deletions(next_attempt_at) WHERE status <> 'completed';
