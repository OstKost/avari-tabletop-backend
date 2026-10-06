package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
)

type CollectionRepository interface {
	Add(ctx context.Context, userID, gameID uuid.UUID, status model.CollectionStatus) (model.Collection, error)
	Remove(ctx context.Context, userID, gameID uuid.UUID) error
	Update(ctx context.Context, userID, gameID uuid.UUID, status model.CollectionStatus, rating *int, notes string) (model.Collection, error)
	ListByUser(ctx context.Context, userID uuid.UUID, status model.CollectionStatus) ([]model.CollectionWithGame, error)
	GetByUserAndGame(ctx context.Context, userID, gameID uuid.UUID) (model.Collection, error)
}

type pgCollectionRepo struct {
	pool *pgxpool.Pool
}

func NewCollectionRepository(pool *pgxpool.Pool) CollectionRepository {
	return &pgCollectionRepo{pool: pool}
}

func (r *pgCollectionRepo) Add(ctx context.Context, userID, gameID uuid.UUID, status model.CollectionStatus) (model.Collection, error) {
	var c model.Collection
	err := r.pool.QueryRow(ctx, `
		INSERT INTO collections (user_id, game_id, status)
		VALUES ($1, $2, $3)
		RETURNING id, user_id, game_id, status, rating, notes, added_at, updated_at, version
	`, userID, gameID, status).Scan(
		&c.ID, &c.UserID, &c.GameID, &c.Status, &c.Rating, &c.Notes, &c.AddedAt, &c.UpdatedAt, &c.Version,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return model.Collection{}, ErrDuplicate
		}
		return model.Collection{}, fmt.Errorf("add to collection: %w", err)
	}
	return c, nil
}

func (r *pgCollectionRepo) Remove(ctx context.Context, userID, gameID uuid.UUID) error {
	result, err := r.pool.Exec(ctx, `
		DELETE FROM collections WHERE user_id = $1 AND game_id = $2
	`, userID, gameID)
	if err != nil {
		return fmt.Errorf("remove from collection: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *pgCollectionRepo) Update(ctx context.Context, userID, gameID uuid.UUID, status model.CollectionStatus, rating *int, notes string) (model.Collection, error) {
	var c model.Collection
	err := r.pool.QueryRow(ctx, `
		UPDATE collections
		SET status = $3, rating = $4, notes = $5
		WHERE user_id = $1 AND game_id = $2
		RETURNING id, user_id, game_id, status, rating, notes, added_at, updated_at, version
	`, userID, gameID, status, rating, notes).Scan(
		&c.ID, &c.UserID, &c.GameID, &c.Status, &c.Rating, &c.Notes, &c.AddedAt, &c.UpdatedAt, &c.Version,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Collection{}, ErrNotFound
	}
	if err != nil {
		return model.Collection{}, fmt.Errorf("update collection: %w", err)
	}
	return c, nil
}

func (r *pgCollectionRepo) ListByUser(ctx context.Context, userID uuid.UUID, status model.CollectionStatus) ([]model.CollectionWithGame, error) {
	query := `
		SELECT c.id, c.user_id, c.game_id, c.status, c.rating, c.notes, c.added_at, c.updated_at, c.version,
		       g.id, g.bgg_id, g.name, g.description, g.image_url, g.thumbnail_url,
		       g.min_players, g.max_players, g.playing_time, g.year_published,
		       g.average_rating, g.fetched_at
		FROM collections c
		JOIN games g ON g.id = c.game_id
		WHERE c.user_id = $1
	`
	args := []any{userID}

	if status != "" {
		query += " AND c.status = $2"
		args = append(args, status)
	}
	query += " ORDER BY c.added_at DESC"

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list collection: %w", err)
	}
	defer rows.Close()

	var results []model.CollectionWithGame
	for rows.Next() {
		var cg model.CollectionWithGame
		err := rows.Scan(
			&cg.Collection.ID, &cg.Collection.UserID, &cg.Collection.GameID,
			&cg.Collection.Status, &cg.Collection.Rating, &cg.Collection.Notes, &cg.Collection.AddedAt, &cg.Collection.UpdatedAt, &cg.Collection.Version,
			&cg.Game.ID, &cg.Game.BGGID, &cg.Game.Name, &cg.Game.Description,
			&cg.Game.ImageURL, &cg.Game.ThumbnailURL,
			&cg.Game.MinPlayers, &cg.Game.MaxPlayers, &cg.Game.PlayingTime,
			&cg.Game.YearPublished, &cg.Game.AverageRating, &cg.Game.FetchedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan collection row: %w", err)
		}
		results = append(results, cg)
	}
	return results, rows.Err()
}

func (r *pgCollectionRepo) GetByUserAndGame(ctx context.Context, userID, gameID uuid.UUID) (model.Collection, error) {
	var c model.Collection
	err := r.pool.QueryRow(ctx, `
		SELECT id, user_id, game_id, status, rating, notes, added_at, updated_at, version
		FROM collections WHERE user_id = $1 AND game_id = $2
	`, userID, gameID).Scan(
		&c.ID, &c.UserID, &c.GameID, &c.Status, &c.Rating, &c.Notes, &c.AddedAt, &c.UpdatedAt, &c.Version,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Collection{}, ErrNotFound
	}
	if err != nil {
		return model.Collection{}, fmt.Errorf("get collection entry: %w", err)
	}
	return c, nil
}
