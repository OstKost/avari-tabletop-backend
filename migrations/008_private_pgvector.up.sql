-- Applied only for pgvector deployments. Dynamic model dimensions are checked by
-- the adapter; this separate table never reads or rewrites legacy rules_chunks.
CREATE EXTENSION IF NOT EXISTS vector;
CREATE TABLE private_rules_chunks (
 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 rule_id UUID NOT NULL REFERENCES user_game_rules(id) ON DELETE CASCADE,
 game_id UUID NOT NULL REFERENCES games(id) ON DELETE CASCADE,
 language TEXT NOT NULL CHECK(language IN ('ru','en')),
 revision BIGINT NOT NULL CHECK(revision>0),
 chunk_id TEXT NOT NULL,
 content TEXT NOT NULL,
 dimension INTEGER NOT NULL CHECK(dimension>0),
 embedding vector NOT NULL,
 CHECK(vector_dims(embedding)=dimension),
 PRIMARY KEY(rule_id,revision,chunk_id)
);
CREATE INDEX private_rules_chunks_scope ON private_rules_chunks(user_id,game_id,language,rule_id,revision);
