package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
)

type GameRepository interface {
	Upsert(ctx context.Context, g model.Game) (model.Game, error)
	GetByBGGID(ctx context.Context, bggID int) (model.Game, error)
	GetByID(ctx context.Context, id uuid.UUID) (model.Game, error)
}

type pgGameRepo struct {
	pool interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}
}

func NewGameRepository(pool *pgxpool.Pool) GameRepository {
	return &pgGameRepo{pool: pool}
}

func (r *pgGameRepo) Upsert(ctx context.Context, g model.Game) (model.Game, error) {
	var out model.Game
	err := r.pool.QueryRow(ctx, `
		INSERT INTO games (bgg_id, name, description, image_url, thumbnail_url,
		                   min_players, max_players, playing_time, year_published,
		                   average_rating, fetched_at, name_fold)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW(),$11)
		ON CONFLICT (bgg_id) DO UPDATE SET
			name          = EXCLUDED.name,
 name_fold = EXCLUDED.name_fold,
			description   = EXCLUDED.description,
			image_url     = EXCLUDED.image_url,
			thumbnail_url = EXCLUDED.thumbnail_url,
			min_players   = EXCLUDED.min_players,
			max_players   = EXCLUDED.max_players,
			playing_time  = EXCLUDED.playing_time,
			year_published= EXCLUDED.year_published,
			average_rating= EXCLUDED.average_rating,
			fetched_at    = NOW()
		RETURNING id, bgg_id, name, description, image_url, thumbnail_url,
		          min_players, max_players, playing_time, year_published,
		          average_rating, fetched_at
	`, g.BGGID, g.Name, g.Description, g.ImageURL, g.ThumbnailURL,
		g.MinPlayers, g.MaxPlayers, g.PlayingTime, g.YearPublished, g.AverageRating, strings.ToLower(g.Name),
	).Scan(
		&out.ID, &out.BGGID, &out.Name, &out.Description, &out.ImageURL, &out.ThumbnailURL,
		&out.MinPlayers, &out.MaxPlayers, &out.PlayingTime, &out.YearPublished,
		&out.AverageRating, &out.FetchedAt,
	)
	if err != nil {
		return model.Game{}, fmt.Errorf("upsert game: %w", err)
	}
	return out, nil
}

func (r *pgGameRepo) GetByBGGID(ctx context.Context, bggID int) (model.Game, error) {
	var g model.Game
	err := r.pool.QueryRow(ctx, `
		SELECT id, bgg_id, name, description, image_url, thumbnail_url,
		       min_players, max_players, playing_time, year_published,
		       average_rating, fetched_at
		FROM games WHERE bgg_id = $1
	`, bggID).Scan(
		&g.ID, &g.BGGID, &g.Name, &g.Description, &g.ImageURL, &g.ThumbnailURL,
		&g.MinPlayers, &g.MaxPlayers, &g.PlayingTime, &g.YearPublished,
		&g.AverageRating, &g.FetchedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Game{}, ErrNotFound
	}
	if err != nil {
		return model.Game{}, fmt.Errorf("get game by bgg_id: %w", err)
	}
	return g, nil
}

func (r *pgGameRepo) GetByID(ctx context.Context, id uuid.UUID) (model.Game, error) {
	var g model.Game
	err := r.pool.QueryRow(ctx, `
		SELECT id, bgg_id, name, description, image_url, thumbnail_url,
		       min_players, max_players, playing_time, year_published,
		       average_rating, fetched_at
		FROM games WHERE id = $1
	`, id).Scan(
		&g.ID, &g.BGGID, &g.Name, &g.Description, &g.ImageURL, &g.ThumbnailURL,
		&g.MinPlayers, &g.MaxPlayers, &g.PlayingTime, &g.YearPublished,
		&g.AverageRating, &g.FetchedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Game{}, ErrNotFound
	}
	if err != nil {
		return model.Game{}, fmt.Errorf("get game by id: %w", err)
	}
	return g, nil
}
