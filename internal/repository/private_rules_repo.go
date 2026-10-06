package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
)

var ErrRulesStale = errors.New("rules precondition failed")

type PrivateRulesRepository interface {
	Get(context.Context, uuid.UUID, uuid.UUID, string) (model.PrivateRule, error)
	GetByID(context.Context, uuid.UUID, uuid.UUID) (model.PrivateRule, error)
	Save(context.Context, uuid.UUID, uuid.UUID, string, string, string, int64) (model.PrivateRule, error)
	Delete(context.Context, uuid.UUID, uuid.UUID, string, int64) error
	Reindex(context.Context, uuid.UUID, uuid.UUID, int64, uuid.UUID) (model.RulesIndexStatus, error)
	NextOwner(context.Context) (uuid.UUID, error)
	WithOwnerLock(context.Context, uuid.UUID, bool, func() error) error
	Claim(context.Context, uuid.UUID) (model.PrivateRule, error)
	Finish(context.Context, model.PrivateRule, bool) (bool, error)
	Deleted(context.Context, uuid.UUID) ([]uuid.UUID, error)
	PurgeDeleted(context.Context, uuid.UUID, uuid.UUID) error
	DeleteOwner(context.Context, uuid.UUID) error
	CleanReplays(context.Context) error
}
type pgPrivateRulesRepo struct{ pool *pgxpool.Pool }

func NewPrivateRulesRepository(pool *pgxpool.Pool) PrivateRulesRepository {
	return &pgPrivateRulesRepo{pool}
}

const privateRuleColumns = `r.id,r.user_id,r.game_id,r.language,r.content,r.source,r.revision,r.active_revision,r.index_status,r.updated_at`
const privateRuleVisible = ` FROM user_game_rules r JOIN users u ON u.id=r.user_id WHERE r.deleted_at IS NULL AND u.deletion_requested_at IS NULL AND EXISTS(SELECT 1 FROM collections c WHERE c.user_id=r.user_id AND c.game_id=r.game_id) `

func scanPrivateRule(row pgx.Row) (model.PrivateRule, error) {
	var r model.PrivateRule
	err := row.Scan(&r.ID, &r.UserID, &r.GameID, &r.Language, &r.Content, &r.Source, &r.Revision, &r.ActiveRevision, &r.IndexStatus, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return r, err
}
func (r *pgPrivateRulesRepo) Get(ctx context.Context, user, game uuid.UUID, lang string) (model.PrivateRule, error) {
	return scanPrivateRule(r.pool.QueryRow(ctx, `SELECT `+privateRuleColumns+privateRuleVisible+`AND r.user_id=$1 AND r.game_id=$2 AND r.language=$3`, user, game, lang))
}
func (r *pgPrivateRulesRepo) GetByID(ctx context.Context, user, id uuid.UUID) (model.PrivateRule, error) {
	return scanPrivateRule(r.pool.QueryRow(ctx, `SELECT `+privateRuleColumns+privateRuleVisible+`AND r.user_id=$1 AND r.id=$2`, user, id))
}

// Serialize writes with account freeze and other writes for this owner. The
// collection lock protects membership for the duration of the mutation.
func lockRulesOwner(ctx context.Context, tx pgx.Tx, user, game uuid.UUID) error {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND deletion_requested_at IS NULL FOR UPDATE`, user).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if game != uuid.Nil {
		err = tx.QueryRow(ctx, `SELECT id FROM collections WHERE user_id=$1 AND game_id=$2 FOR KEY SHARE`, user, game).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
	}
	return err
}
func (r *pgPrivateRulesRepo) Save(ctx context.Context, user, game uuid.UUID, lang, content, source string, expected int64) (model.PrivateRule, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return model.PrivateRule{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockRulesOwner(ctx, tx, user, game); err != nil {
		return model.PrivateRule{}, err
	}
	existing, err := scanPrivateRule(tx.QueryRow(ctx, `SELECT `+privateRuleColumns+` FROM user_game_rules r WHERE user_id=$1 AND game_id=$2 AND language=$3 AND deleted_at IS NULL FOR UPDATE`, user, game, lang))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return existing, err
	}
	if (errors.Is(err, ErrNotFound) && expected != 0) || (err == nil && (expected == 0 || existing.Revision != expected)) {
		return existing, ErrRulesStale
	}
	id := existing.ID
	if expected == 0 {
		id = uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO user_game_rules(id,user_id,game_id,language,content,source) VALUES($1,$2,$3,$4,$5,$6)`, id, user, game, lang, content, source)
	} else {
		_, err = tx.Exec(ctx, `UPDATE user_game_rules SET content=$2,source=$3,revision=revision+1,index_status='pending',updated_at=NOW() WHERE id=$1`, id, content, source)
	}
	if err != nil {
		return existing, err
	}
	result, err := scanPrivateRule(tx.QueryRow(ctx, `SELECT `+privateRuleColumns+` FROM user_game_rules r WHERE id=$1`, id))
	if err != nil {
		return result, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO rules_index_jobs(rule_id,revision) VALUES($1,$2)`, id, result.Revision)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
func (r *pgPrivateRulesRepo) Delete(ctx context.Context, user, game uuid.UUID, lang string, expected int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockRulesOwner(ctx, tx, user, game); err != nil {
		return err
	}
	rule, err := scanPrivateRule(tx.QueryRow(ctx, `SELECT `+privateRuleColumns+` FROM user_game_rules r WHERE user_id=$1 AND game_id=$2 AND language=$3 AND deleted_at IS NULL FOR UPDATE`, user, game, lang))
	if err != nil {
		return err
	}
	if rule.Revision != expected {
		return ErrRulesStale
	}
	_, err = tx.Exec(ctx, `UPDATE user_game_rules SET deleted_at=NOW(),content='',source='',active_revision=NULL WHERE id=$1`, rule.ID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *pgPrivateRulesRepo) Reindex(ctx context.Context, user, id uuid.UUID, expected int64, key uuid.UUID) (model.RulesIndexStatus, error) {
	var status model.RulesIndexStatus
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return status, err
	}
	defer tx.Rollback(ctx)
	if err = lockRulesOwner(ctx, tx, user, uuid.Nil); err != nil {
		return status, err
	}
	var priorID uuid.UUID
	var priorVersion int64
	var response []byte
	err = tx.QueryRow(ctx, `SELECT rule_id,revision,response FROM rules_reindex_replays WHERE user_id=$1 AND key=$2 AND expires_at>NOW()`, user, key).Scan(&priorID, &priorVersion, &response)
	if err == nil {
		if priorID != id || priorVersion != expected {
			return status, ErrIdempotencyMismatch
		}
		// Do not expose a replay after document deletion or membership removal.
		var visible bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1`+privateRuleVisible+`AND r.user_id=$1 AND r.id=$2)`, user, id).Scan(&visible)
		if err != nil {
			return status, err
		}
		if !visible {
			return status, ErrNotFound
		}
		err = json.Unmarshal(response, &status)
		return status, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return status, err
	}
	rule, err := scanPrivateRule(tx.QueryRow(ctx, `SELECT `+privateRuleColumns+privateRuleVisible+`AND r.user_id=$1 AND r.id=$2 FOR UPDATE OF r`, user, id))
	if err != nil {
		return status, err
	}
	if rule.Revision != expected {
		return status, ErrRulesStale
	}
	if rule.IndexStatus == "failed" {
		_, err = tx.Exec(ctx, `UPDATE rules_index_jobs SET status='pending',lease_until=NULL WHERE rule_id=$1 AND revision=$2`, id, expected)
		if err != nil {
			return status, err
		}
		_, err = tx.Exec(ctx, `UPDATE user_game_rules SET index_status='pending' WHERE id=$1`, id)
		if err != nil {
			return status, err
		}
		rule.IndexStatus = "pending"
	}
	status = rule.Status()
	response, err = json.Marshal(status)
	if err != nil {
		return status, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO rules_reindex_replays(user_id,key,rule_id,revision,response) VALUES($1,$2,$3,$4,$5) ON CONFLICT(user_id,key) DO UPDATE SET rule_id=EXCLUDED.rule_id,revision=EXCLUDED.revision,response=EXCLUDED.response,expires_at=EXCLUDED.expires_at WHERE rules_reindex_replays.expires_at<=NOW()`, user, key, id, expected, response)
	if err != nil {
		return status, err
	}
	return status, tx.Commit(ctx)
}
func (r *pgPrivateRulesRepo) NextOwner(ctx context.Context) (uuid.UUID, error) {
	var owner uuid.UUID
	err := r.pool.QueryRow(ctx, `SELECT r.user_id FROM user_game_rules r JOIN users u ON u.id=r.user_id WHERE u.deletion_requested_at IS NULL AND (r.deleted_at IS NOT NULL OR EXISTS(SELECT 1 FROM rules_index_jobs j WHERE j.rule_id=r.id AND (j.status='pending' OR (j.status='processing' AND j.lease_until<NOW())))) ORDER BY r.updated_at LIMIT 1`).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return owner, err
}

// A session advisory lock spans embedding, publication and external cleanup.
// It is independent of the user row lock, avoiding lock inversion with deletion.
// PostgreSQL releases it on worker death. Failed unlock destroys the connection.
func (r *pgPrivateRulesRepo) WithOwnerLock(ctx context.Context, owner uuid.UUID, wait bool, fn func() error) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	held := false
	defer func() {
		if held {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var unlocked bool
			if e := conn.QueryRow(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, "private-rules:"+owner.String()).Scan(&unlocked); e != nil || !unlocked {
				_ = conn.Hijack().Close(unlockCtx)
				return
			}
		}
		conn.Release()
	}()
	if wait {
		_, err = conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, "private-rules:"+owner.String())
		held = err == nil
	} else {
		err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, "private-rules:"+owner.String()).Scan(&held)
	}
	if err != nil {
		return err
	}
	if !held {
		return nil
	}
	return fn()
}
func (r *pgPrivateRulesRepo) Claim(ctx context.Context, user uuid.UUID) (model.PrivateRule, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return model.PrivateRule{}, err
	}
	defer tx.Rollback(ctx)
	// Old revisions are never indexed from the current revision's raw text.
	_, err = tx.Exec(ctx, `UPDATE rules_index_jobs j SET status='obsolete' FROM user_game_rules r WHERE r.id=j.rule_id AND r.user_id=$1 AND (r.deleted_at IS NOT NULL OR j.revision<>r.revision OR NOT EXISTS(SELECT 1 FROM collections c WHERE c.user_id=r.user_id AND c.game_id=r.game_id)) AND j.status IN ('pending','processing')`, user)
	if err != nil {
		return model.PrivateRule{}, err
	}
	rule, err := scanPrivateRule(tx.QueryRow(ctx, `SELECT `+privateRuleColumns+privateRuleVisible+`AND r.user_id=$1 AND EXISTS(SELECT 1 FROM rules_index_jobs j WHERE j.rule_id=r.id AND j.revision=r.revision AND (j.status='pending' OR (j.status='processing' AND j.lease_until<NOW()))) ORDER BY r.updated_at LIMIT 1`, user))
	if errors.Is(err, ErrNotFound) {
		if e := tx.Commit(ctx); e != nil {
			return rule, e
		}
		return rule, err
	}
	if err != nil {
		return rule, err
	}
	_, err = tx.Exec(ctx, `UPDATE rules_index_jobs SET status='processing',attempts=attempts+1,lease_until=NOW()+INTERVAL '5 minutes' WHERE rule_id=$1 AND revision=$2`, rule.ID, rule.Revision)
	if err != nil {
		return rule, err
	}
	_, err = tx.Exec(ctx, `UPDATE user_game_rules SET index_status='processing' WHERE id=$1 AND revision=$2 AND deleted_at IS NULL`, rule.ID, rule.Revision)
	if err != nil {
		return rule, err
	}
	rule.IndexStatus = "processing"
	return rule, tx.Commit(ctx)
}
func (r *pgPrivateRulesRepo) Finish(ctx context.Context, rule model.PrivateRule, success bool) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	status := "failed"
	if success {
		status = "ready"
	}
	tag, err := tx.Exec(ctx, `UPDATE user_game_rules r SET index_status=$3,active_revision=CASE WHEN $4 THEN $2 ELSE active_revision END WHERE r.id=$1 AND r.revision=$2 AND r.deleted_at IS NULL AND EXISTS(SELECT 1 FROM users u WHERE u.id=r.user_id AND u.deletion_requested_at IS NULL) AND EXISTS(SELECT 1 FROM collections c WHERE c.user_id=r.user_id AND c.game_id=r.game_id)`, rule.ID, rule.Revision, status, success)
	if err != nil {
		return false, err
	}
	current := tag.RowsAffected() == 1
	if !current {
		status = "obsolete"
	}
	_, err = tx.Exec(ctx, `UPDATE rules_index_jobs SET status=$3,lease_until=NULL WHERE rule_id=$1 AND revision=$2`, rule.ID, rule.Revision, status)
	if err != nil {
		return false, err
	}
	return current, tx.Commit(ctx)
}
func (r *pgPrivateRulesRepo) Deleted(ctx context.Context, user uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `SELECT id FROM user_game_rules WHERE user_id=$1 AND deleted_at IS NOT NULL`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (r *pgPrivateRulesRepo) PurgeDeleted(ctx context.Context, user, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM user_game_rules WHERE user_id=$1 AND id=$2 AND deleted_at IS NOT NULL`, user, id)
	return err
}
func (r *pgPrivateRulesRepo) DeleteOwner(ctx context.Context, user uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM user_game_rules WHERE user_id=$1`, user)
	return err
}
func (r *pgPrivateRulesRepo) CleanReplays(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM rules_reindex_replays WHERE expires_at<NOW()`)
	return err
}
