package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GameRule holds the raw rules text for a game in a given language.
type GameRule struct {
	ID      uuid.UUID
	GameID  uuid.UUID
	Lang    string
	Content string
	Source  string
}

// RulesStore is the interface used by handlers and services.
// Allows easy fake/mock in tests.
type RulesStore interface {
	Upsert(ctx context.Context, gameID uuid.UUID, lang, content, source string) error
	Get(ctx context.Context, gameID uuid.UUID, lang string) (*GameRule, error)
	Delete(ctx context.Context, gameID uuid.UUID, lang string) error
	DeleteAll(ctx context.Context, gameID uuid.UUID) error
	GetContent(ctx context.Context, gameID uuid.UUID, lang string) (string, error)
}

// RulesRepository implements RulesStore against PostgreSQL.
type RulesRepository struct {
	pool *pgxpool.Pool
}

func NewRulesRepository(pool *pgxpool.Pool) *RulesRepository {
	return &RulesRepository{pool: pool}
}

// Upsert inserts or replaces the rules for a game+language pair.
func (r *RulesRepository) Upsert(ctx context.Context, gameID uuid.UUID, lang, content, source string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO game_rules (game_id, language, content, source, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (game_id, language)
		DO UPDATE SET content = EXCLUDED.content, source = EXCLUDED.source, updated_at = NOW()
	`, gameID, lang, content, source)
	if err != nil {
		return fmt.Errorf("upsert game rules: %w", err)
	}
	return nil
}

// Get returns the rules for a game+language pair, or nil if not found.
func (r *RulesRepository) Get(ctx context.Context, gameID uuid.UUID, lang string) (*GameRule, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, game_id, language, content, source
		FROM game_rules
		WHERE game_id = $1 AND language = $2
	`, gameID, lang)

	var gr GameRule
	err := row.Scan(&gr.ID, &gr.GameID, &gr.Lang, &gr.Content, &gr.Source)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get game rules: %w", err)
	}
	return &gr, nil
}

// Delete removes rules for a game+language pair.
func (r *RulesRepository) Delete(ctx context.Context, gameID uuid.UUID, lang string) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM game_rules WHERE game_id = $1 AND language = $2
	`, gameID, lang)
	return err
}

// DeleteAll removes all rules for a game (all languages).
func (r *RulesRepository) DeleteAll(ctx context.Context, gameID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM game_rules WHERE game_id = $1`, gameID)
	return err
}

// GetContent returns just the rules text for a game+language pair (implements service.RulesLoader).
func (r *RulesRepository) GetContent(ctx context.Context, gameID uuid.UUID, lang string) (string, error) {
	gr, err := r.Get(ctx, gameID, lang)
	if err != nil {
		return "", err
	}
	if gr == nil {
		return "", nil
	}
	return gr.Content, nil
}
