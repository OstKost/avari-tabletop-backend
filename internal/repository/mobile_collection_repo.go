package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
)

var (
	ErrCollectionStale     = errors.New("collection version conflict")
	ErrIdempotencyMismatch = errors.New("idempotency body mismatch")
	ErrSnapshotExpired     = errors.New("snapshot expired")
	ErrSnapshotQuota       = errors.New("snapshot quota exceeded")
)

type CollectionAddResult struct {
	RequestID uuid.UUID
	Status    int
	Entry     model.CollectionEntryDTO
}
type MobileCollectionRepository interface {
	Membership(context.Context, uuid.UUID, []int) (map[int]uuid.UUID, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (model.CollectionEntryDTO, error)
	List(context.Context, uuid.UUID, model.CollectionFilter, int, int) ([]model.CollectionEntryDTO, error)
	Mutate(context.Context, uuid.UUID, uuid.UUID, int64, *model.CollectionPatch) (model.CollectionEntryDTO, error)
	Add(context.Context, uuid.UUID, uuid.UUID, string, model.CollectionStatus, func(context.Context, GameRepository) (model.Game, error)) (CollectionAddResult, error)
	Snapshot(context.Context, uuid.UUID, uuid.UUID) (model.CollectionSnapshot, error)
	Cleanup(context.Context) error
}
type pgMobileCollectionRepo struct{ pool *pgxpool.Pool }

func NewMobileCollectionRepository(pool *pgxpool.Pool) MobileCollectionRepository {
	return &pgMobileCollectionRepo{pool}
}

// Deliberately excludes game descriptions/images from list and snapshot reads.
const mobileEntryColumns = `c.id,c.game_id,c.status,c.rating,c.notes,c.added_at,c.updated_at,c.version,
 g.id,g.bgg_id,g.name,g.thumbnail_url,g.year_published,g.min_players,g.max_players,g.playing_time,g.average_rating`
const mobileEntryFrom = ` FROM collections c JOIN games g ON g.id=c.game_id `

func scanMobileEntry(row pgx.Row) (model.CollectionEntryDTO, error) {
	var e model.CollectionEntryDTO
	err := row.Scan(&e.ID, &e.GameID, &e.Status, &e.Rating, &e.Notes, &e.AddedAt, &e.UpdatedAt, &e.Version,
		&e.Game.ID, &e.Game.BGGID, &e.Game.Name, &e.Game.ThumbnailURL, &e.Game.YearPublished, &e.Game.MinPlayers, &e.Game.MaxPlayers, &e.Game.PlayingTime, &e.Game.AverageRating)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return e, err
}
func (r *pgMobileCollectionRepo) Membership(ctx context.Context, user uuid.UUID, ids []int) (map[int]uuid.UUID, error) {
	out := make(map[int]uuid.UUID)
	if len(ids) > 100 {
		return nil, fmt.Errorf("membership bound exceeded")
	}
	rows, err := r.pool.Query(ctx, `SELECT g.bgg_id,g.id`+mobileEntryFrom+`WHERE c.user_id=$1 AND g.bgg_id=ANY($2::int[])`, user, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var game uuid.UUID
		if err = rows.Scan(&id, &game); err != nil {
			return nil, err
		}
		out[id] = game
	}
	return out, rows.Err()
}
func (r *pgMobileCollectionRepo) Get(ctx context.Context, user, game uuid.UUID) (model.CollectionEntryDTO, error) {
	return scanMobileEntry(r.pool.QueryRow(ctx, `SELECT `+mobileEntryColumns+mobileEntryFrom+`WHERE c.user_id=$1 AND c.game_id=$2`, user, game))
}
func collectionOrder(sort string) string {
	switch sort {
	case "name":
		return `g.name_fold COLLATE "C" ASC,c.added_at DESC,c.game_id ASC`
	case "rating":
		return `c.rating DESC NULLS LAST,c.added_at DESC,c.game_id ASC`
	default:
		return `c.added_at DESC,c.game_id ASC`
	}
}
func readMobileEntries(rows pgx.Rows) ([]model.CollectionEntryDTO, error) {
	defer rows.Close()
	out := make([]model.CollectionEntryDTO, 0)
	for rows.Next() {
		e, err := scanMobileEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (r *pgMobileCollectionRepo) List(ctx context.Context, user uuid.UUID, f model.CollectionFilter, limit, offset int) ([]model.CollectionEntryDTO, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+mobileEntryColumns+mobileEntryFrom+`WHERE c.user_id=$1 AND ($2='' OR c.status::text=$2) AND strpos(g.name_fold,$3)>0 ORDER BY `+collectionOrder(f.Sort)+` LIMIT $4 OFFSET $5`, user, string(f.Status), f.Query, limit, offset)
	if err != nil {
		return nil, err
	}
	return readMobileEntries(rows)
}
func (r *pgMobileCollectionRepo) Mutate(ctx context.Context, user, game uuid.UUID, version int64, p *model.CollectionPatch) (model.CollectionEntryDTO, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return model.CollectionEntryDTO{}, err
	}
	defer tx.Rollback(ctx)
	current, err := scanMobileEntry(tx.QueryRow(ctx, `SELECT `+mobileEntryColumns+mobileEntryFrom+`WHERE c.user_id=$1 AND c.game_id=$2 FOR UPDATE OF c`, user, game))
	if err != nil {
		return current, err
	}
	if current.Version != version {
		return current, ErrCollectionStale
	}
	if p == nil {
		_, err = tx.Exec(ctx, `DELETE FROM collections WHERE user_id=$1 AND game_id=$2`, user, game)
	} else {
		status, rating, notes := current.Status, current.Rating, current.Notes
		if p.Status != nil {
			status = *p.Status
		}
		if p.HasRating {
			rating = p.Rating
		}
		if p.Notes != nil {
			notes = *p.Notes
		}
		_, err = tx.Exec(ctx, `UPDATE collections SET status=$3,rating=$4,notes=$5 WHERE user_id=$1 AND game_id=$2`, user, game, status, rating, notes)
		if err == nil {
			current, err = scanMobileEntry(tx.QueryRow(ctx, `SELECT `+mobileEntryColumns+mobileEntryFrom+`WHERE c.user_id=$1 AND c.game_id=$2`, user, game))
		}
	}
	if err != nil {
		return current, err
	}
	return current, tx.Commit(ctx)
}

// Serializing the user's adds also serializes replay reservations across instances.
// No remote call is made until a durable replay has been checked under this lock.
func (r *pgMobileCollectionRepo) Add(ctx context.Context, user, key uuid.UUID, hash string, status model.CollectionStatus, fetch func(context.Context, GameRepository) (model.Game, error)) (CollectionAddResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return CollectionAddResult{}, err
	}
	defer tx.Rollback(ctx)
	var locked uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, user).Scan(&locked); err != nil {
		return CollectionAddResult{}, err
	}
	var storedHash string
	var stored CollectionAddResult
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT body_hash,status,response,request_id FROM collection_add_replays WHERE user_id=$1 AND key=$2 AND expires_at>NOW()`, user, key).Scan(&storedHash, &stored.Status, &raw, &stored.RequestID)
	if err == nil {
		if hash != storedHash {
			return stored, ErrIdempotencyMismatch
		}
		err = json.Unmarshal(raw, &stored.Entry)
		return stored, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return stored, err
	}
	game, err := fetch(ctx, &pgGameRepo{pool: tx})
	if err != nil {
		return stored, err
	}
	var inserted uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO collections(user_id,game_id,status) VALUES($1,$2,$3) ON CONFLICT(user_id,game_id) DO NOTHING RETURNING id`, user, game.ID, status).Scan(&inserted)
	stored.Status = 201
	stored.RequestID = uuid.New()
	if errors.Is(err, pgx.ErrNoRows) {
		stored.Status = 409
	} else if err != nil {
		return stored, err
	}
	stored.Entry, err = scanMobileEntry(tx.QueryRow(ctx, `SELECT `+mobileEntryColumns+mobileEntryFrom+`WHERE c.user_id=$1 AND c.game_id=$2`, user, game.ID))
	if err != nil {
		return stored, err
	}
	raw, err = json.Marshal(stored.Entry)
	if err != nil {
		return stored, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO collection_add_replays(user_id,key,body_hash,status,response,request_id,expires_at) VALUES($1,$2,$3,$4,$5,$6,NOW()+INTERVAL '24 hours') ON CONFLICT(user_id,key) DO UPDATE SET body_hash=EXCLUDED.body_hash,status=EXCLUDED.status,response=EXCLUDED.response,request_id=EXCLUDED.request_id,expires_at=EXCLUDED.expires_at`, user, key, hash, stored.Status, raw, stored.RequestID)
	if err != nil {
		return stored, err
	}
	return stored, tx.Commit(ctx)
}
func (r *pgMobileCollectionRepo) Snapshot(ctx context.Context, user, id uuid.UUID) (model.CollectionSnapshot, error) {
	out := model.CollectionSnapshot{ID: id, UserID: user}
	var raw []byte
	if id != uuid.Nil {
		err := r.pool.QueryRow(ctx, `SELECT captured_at,expires_at,items FROM collection_snapshots WHERE id=$1 AND user_id=$2`, id, user).Scan(&out.CapturedAt, &out.ExpiresAt, &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, ErrSnapshotExpired
		}
		if err != nil {
			return out, err
		}
		if !time.Now().Before(out.ExpiresAt) {
			return out, ErrSnapshotExpired
		}
		err = json.Unmarshal(raw, &out.Items)
		return out, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	// An advisory transaction lock makes per-user capture quota safe across replicas.
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 52))`, user.String())
	if err != nil {
		return out, err
	}
	var count int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM collection_snapshots WHERE user_id=$1 AND expires_at>NOW()`, user).Scan(&count)
	if err != nil {
		return out, err
	}
	if count >= 10 {
		return out, ErrSnapshotQuota
	}
	rows, err := tx.Query(ctx, `SELECT `+mobileEntryColumns+mobileEntryFrom+`WHERE c.user_id=$1 ORDER BY c.added_at DESC,c.game_id ASC`, user)
	if err != nil {
		return out, err
	}
	out.Items, err = readMobileEntries(rows)
	if err != nil {
		return out, err
	}
	raw, err = json.Marshal(out.Items)
	if err != nil {
		return out, err
	}
	out.ID = uuid.New()
	err = tx.QueryRow(ctx, `INSERT INTO collection_snapshots(id,user_id,expires_at,items) VALUES($1,$2,NOW()+INTERVAL '5 minutes',$3) RETURNING captured_at,expires_at`, out.ID, user, raw).Scan(&out.CapturedAt, &out.ExpiresAt)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (r *pgMobileCollectionRepo) Cleanup(ctx context.Context) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM collection_snapshots WHERE expires_at<=NOW()`); err != nil {
		return err
	}
	_, err := r.pool.Exec(ctx, `DELETE FROM collection_add_replays WHERE expires_at<=NOW()`)
	return err
}
