package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
)

var ErrSessionUnauthorized = errors.New("session unauthorized")
var ErrResetInvalid = errors.New("reset invalid")

type MobileRotation struct {
	NextHash   string
	Ciphertext []byte
}

type MobileAuthRepository interface {
	CreateSession(context.Context, uuid.UUID, model.User, string, time.Time) error
	Rotate(context.Context, string, uuid.UUID, time.Time, func(model.User, uuid.UUID) (MobileRotation, error)) ([]byte, error)
	SessionUser(context.Context, uuid.UUID, uuid.UUID, int64, time.Time) (model.User, error)
	Revoke(context.Context, uuid.UUID, time.Time) error
	CreateReset(context.Context, string, uuid.UUID, time.Time) error
	ConsumeReset(context.Context, string, string, time.Time) error
	Cleanup(context.Context, time.Time) error
}

type pgMobileAuthRepo struct{ pool *pgxpool.Pool }

func NewMobileAuthRepository(pool *pgxpool.Pool) MobileAuthRepository { return &pgMobileAuthRepo{pool} }

func (r *pgMobileAuthRepo) CreateSession(ctx context.Context, sid uuid.UUID, user model.User, hash string, expires time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var version int64
	err = tx.QueryRow(ctx, `SELECT auth_version FROM users WHERE id=$1 AND deletion_requested_at IS NULL FOR UPDATE`, user.ID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && version != user.AuthVersion) {
		return ErrSessionUnauthorized
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO mobile_sessions(id,user_id,auth_version,expires_at) VALUES($1,$2,$3,$4)`, sid, user.ID, version, expires)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO mobile_refresh_tokens(token_hash,session_id) VALUES($1,$2)`, hash, sid)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *pgMobileAuthRepo) Rotate(ctx context.Context, hash string, attempt uuid.UUID, now time.Time, build func(model.User, uuid.UUID) (MobileRotation, error)) ([]byte, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var sid, uid uuid.UUID
	err = tx.QueryRow(ctx, `SELECT s.id,s.user_id FROM mobile_refresh_tokens t JOIN mobile_sessions s ON s.id=t.session_id WHERE t.token_hash=$1`, hash).Scan(&sid, &uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSessionUnauthorized
	}
	if err != nil {
		return nil, err
	}
	// Same lock order as password reset: user, session, then token.
	var user model.User
	err = tx.QueryRow(ctx, `SELECT id,email,username,created_at,auth_version FROM users WHERE id=$1 AND deletion_requested_at IS NULL FOR UPDATE`, uid).Scan(&user.ID, &user.Email, &user.Username, &user.CreatedAt, &user.AuthVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSessionUnauthorized
	}
	if err != nil {
		return nil, err
	}
	var version int64
	var expires time.Time
	var revoked *time.Time
	err = tx.QueryRow(ctx, `SELECT auth_version,expires_at,revoked_at FROM mobile_sessions WHERE id=$1 FOR UPDATE`, sid).Scan(&version, &expires, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSessionUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if revoked != nil || !now.Before(expires) || version != user.AuthVersion {
		return nil, ErrSessionUnauthorized
	}
	var consumed, replayUntil *time.Time
	var storedAttempt *uuid.UUID
	var cached []byte
	err = tx.QueryRow(ctx, `SELECT consumed_at,attempt_id,replay_until,replay_ciphertext FROM mobile_refresh_tokens WHERE token_hash=$1 FOR UPDATE`, hash).Scan(&consumed, &storedAttempt, &replayUntil, &cached)
	if err != nil {
		return nil, err
	}
	if consumed != nil {
		if storedAttempt != nil && *storedAttempt == attempt && replayUntil != nil && now.Before(*replayUntil) && len(cached) > 0 {
			return cached, nil
		}
		if err = revokeTx(ctx, tx, sid, now); err != nil {
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, ErrSessionUnauthorized
	}
	rotation, err := build(user, sid)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE mobile_refresh_tokens SET consumed_at=$2,attempt_id=$3,replay_until=$4,replay_ciphertext=$5 WHERE token_hash=$1`, hash, now, attempt, now.Add(time.Minute), rotation.Ciphertext)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO mobile_refresh_tokens(token_hash,session_id) VALUES($1,$2)`, rotation.NextHash, sid)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return rotation.Ciphertext, nil
}

func (r *pgMobileAuthRepo) SessionUser(ctx context.Context, sid, uid uuid.UUID, version int64, now time.Time) (model.User, error) {
	var user model.User
	err := r.pool.QueryRow(ctx, `SELECT u.id,u.email,u.username,u.created_at,u.auth_version FROM mobile_sessions s JOIN users u ON u.id=s.user_id WHERE s.id=$1 AND u.id=$2 AND s.auth_version=$3 AND u.auth_version=$3 AND s.revoked_at IS NULL AND u.deletion_requested_at IS NULL AND s.expires_at>$4`, sid, uid, version, now).Scan(&user.ID, &user.Email, &user.Username, &user.CreatedAt, &user.AuthVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, ErrSessionUnauthorized
	}
	return user, err
}
func revokeTx(ctx context.Context, tx pgx.Tx, sid uuid.UUID, now time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE mobile_sessions SET revoked_at=COALESCE(revoked_at,$2) WHERE id=$1`, sid, now); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE mobile_refresh_tokens SET replay_ciphertext=NULL WHERE session_id=$1`, sid)
	return err
}
func (r *pgMobileAuthRepo) Revoke(ctx context.Context, sid uuid.UUID, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Lock the family before its tokens, matching Rotate.
	if _, err = tx.Exec(ctx, `SELECT id FROM mobile_sessions WHERE id=$1 FOR UPDATE`, sid); err != nil {
		return err
	}
	if err = revokeTx(ctx, tx, sid, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *pgMobileAuthRepo) CreateReset(ctx context.Context, hash string, uid uuid.UUID, expires time.Time) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO password_reset_tokens(token_hash,user_id,expires_at) VALUES($1,$2,$3)`, hash, uid, expires)
	return err
}
func (r *pgMobileAuthRepo) ConsumeReset(ctx context.Context, hash, passwordHash string, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var uid uuid.UUID
	err = tx.QueryRow(ctx, `SELECT user_id FROM password_reset_tokens WHERE token_hash=$1`, hash).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrResetInvalid
	}
	if err != nil {
		return err
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND deletion_requested_at IS NULL FOR UPDATE`, uid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrResetInvalid
	}
	if err != nil {
		return err
	}
	var expires time.Time
	var consumed *time.Time
	err = tx.QueryRow(ctx, `SELECT expires_at,consumed_at FROM password_reset_tokens WHERE token_hash=$1 FOR UPDATE`, hash).Scan(&expires, &consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrResetInvalid
	}
	if err != nil {
		return err
	}
	if consumed != nil || !now.Before(expires) {
		return ErrResetInvalid
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=$2,auth_version=auth_version+1,updated_at=$3 WHERE id=$1`, uid, passwordHash, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE password_reset_tokens SET consumed_at=$2 WHERE user_id=$1 AND consumed_at IS NULL`, uid, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE mobile_sessions SET revoked_at=COALESCE(revoked_at,$2) WHERE user_id=$1`, uid, now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE mobile_refresh_tokens t SET replay_ciphertext=NULL FROM mobile_sessions s WHERE t.session_id=s.id AND s.user_id=$1`, uid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *pgMobileAuthRepo) Cleanup(ctx context.Context, now time.Time) error {
	// Independent statements release locks between phases; family deletion uses
	// the same session-before-token ordering as rotation/revoke.
	if _, err := r.pool.Exec(ctx, `DELETE FROM mobile_sessions WHERE expires_at<=$1`, now); err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx, `UPDATE mobile_refresh_tokens SET replay_ciphertext=NULL WHERE replay_until<=$1 AND replay_ciphertext IS NOT NULL`, now); err != nil {
		return err
	}
	_, err := r.pool.Exec(ctx, `DELETE FROM password_reset_tokens WHERE expires_at<=$1`, now)
	return err
}
