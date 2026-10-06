-- Private documents are deliberately separate from ownerless legacy game_rules.
CREATE TABLE user_game_rules (
 id UUID PRIMARY KEY,
 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 game_id UUID NOT NULL REFERENCES games(id) ON DELETE CASCADE,
 language TEXT NOT NULL CHECK(language IN ('ru','en')),
 content TEXT NOT NULL CHECK(octet_length(content)<=1048576),
 source TEXT NOT NULL CHECK(char_length(source)<=500),
 revision BIGINT NOT NULL DEFAULT 1 CHECK(revision>0),
 active_revision BIGINT CHECK(active_revision>0 AND active_revision<=revision),
 index_status TEXT NOT NULL DEFAULT 'pending' CHECK(index_status IN ('pending','processing','ready','failed')),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 deleted_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX user_game_rules_live ON user_game_rules(user_id,game_id,language) WHERE deleted_at IS NULL;
CREATE INDEX user_game_rules_cleanup ON user_game_rules(user_id) WHERE deleted_at IS NOT NULL;
CREATE TABLE rules_index_jobs (
 rule_id UUID NOT NULL REFERENCES user_game_rules(id) ON DELETE CASCADE,
 revision BIGINT NOT NULL CHECK(revision>0),
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','processing','ready','failed','obsolete')),
 lease_until TIMESTAMPTZ,
 attempts INTEGER NOT NULL DEFAULT 0,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(rule_id,revision)
);
CREATE INDEX rules_index_jobs_work ON rules_index_jobs(created_at) WHERE status IN ('pending','processing');
CREATE TABLE rules_reindex_replays (
 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 key UUID NOT NULL,
 rule_id UUID NOT NULL REFERENCES user_game_rules(id) ON DELETE CASCADE,
 revision BIGINT NOT NULL,
 response JSONB NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW()+INTERVAL '1 day',
 PRIMARY KEY(user_id,key)
);
