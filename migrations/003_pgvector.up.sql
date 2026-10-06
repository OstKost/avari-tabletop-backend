-- pgvector scale-path: only apply when VECTOR_STORE=pgvector
-- Requires pgvector extension in Postgres (pgvector/pgvector:pg16 image)

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS rules_chunks (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    game_id UUID NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    language VARCHAR(5) NOT NULL DEFAULT 'ru',
    chunk_index INT NOT NULL,
    content TEXT NOT NULL,
    embedding vector(768),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS rules_chunks_game_lang_idx ON rules_chunks(game_id, language);
CREATE INDEX IF NOT EXISTS rules_chunks_embedding_idx ON rules_chunks
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);
