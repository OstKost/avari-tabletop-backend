CREATE TABLE IF NOT EXISTS game_rules (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    game_id UUID NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    language VARCHAR(5) NOT NULL DEFAULT 'ru',
    content TEXT NOT NULL,
    source VARCHAR(500) NOT NULL DEFAULT 'manual',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(game_id, language)
);

CREATE INDEX IF NOT EXISTS game_rules_game_id_idx ON game_rules(game_id);
