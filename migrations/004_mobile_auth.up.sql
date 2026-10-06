ALTER TABLE users ADD COLUMN auth_version BIGINT NOT NULL DEFAULT 0;

CREATE TABLE mobile_sessions (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    auth_version BIGINT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_mobile_sessions_user ON mobile_sessions(user_id);
CREATE TABLE mobile_refresh_tokens (
    token_hash TEXT PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES mobile_sessions(id) ON DELETE CASCADE,
    consumed_at TIMESTAMPTZ,
    attempt_id UUID,
    replay_until TIMESTAMPTZ,
    replay_ciphertext BYTEA,
    CHECK ((consumed_at IS NULL) = (attempt_id IS NULL))
);
CREATE INDEX idx_mobile_refresh_session ON mobile_refresh_tokens(session_id);
CREATE TABLE password_reset_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ
);
CREATE INDEX idx_password_reset_user ON password_reset_tokens(user_id);
