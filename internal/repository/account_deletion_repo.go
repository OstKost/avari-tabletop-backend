package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrDeletionConflict = errors.New("deletion conflict")

type DeletionJob struct {
	ID     uuid.UUID
	UserID *uuid.UUID
	Status string
}
type AccountDeletionRepository interface {
	Accept(context.Context, uuid.UUID, string, string, uuid.UUID, string, bool, func(string) bool) (uuid.UUID, error)
	Status(context.Context, uuid.UUID, string) (DeletionJob, error)
	Work(context.Context, func(context.Context, uuid.UUID) error) error
}
type pgAccountDeletionRepo struct{ pool *pgxpool.Pool }

func NewAccountDeletionRepository(pool *pgxpool.Pool) AccountDeletionRepository {
	return &pgAccountDeletionRepo{pool}
}

// User lock serializes freeze with refresh/reset/session creation. An advisory
// lock serializes retries even after the user and all their sessions are gone.
func (r *pgAccountDeletionRepo) Accept(ctx context.Context, uid uuid.UUID, replay, body string, id uuid.UUID, receipt string, allowNew bool, check func(string) bool) (uuid.UUID, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, replay); err != nil {
		return uuid.Nil, err
	}
	var previous uuid.UUID
	var previousBody string
	err = tx.QueryRow(ctx, `SELECT id,body_hash FROM account_deletions WHERE replay_hash=$1 AND (completed_at IS NULL OR completed_at>NOW()-INTERVAL '7 days')`, replay).Scan(&previous, &previousBody)
	if err == nil {
		if previousBody != body {
			return uuid.Nil, ErrDeletionConflict
		}
		return previous, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	if !allowNew {
		return uuid.Nil, ErrSessionUnauthorized
	}
	var passwordHash string
	err = tx.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1 AND deletion_requested_at IS NULL FOR UPDATE`, uid).Scan(&passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrSessionUnauthorized
	}
	if err != nil {
		return uuid.Nil, err
	}
	if !check(passwordHash) {
		return uuid.Nil, ErrSessionUnauthorized
	}
	if _, err = tx.Exec(ctx, `INSERT INTO account_deletions(id,user_id,replay_hash,body_hash,receipt_hash) VALUES($1,$2,$3,$4,$5)`, id, uid, replay, body, receipt); err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET deletion_requested_at=NOW(),auth_version=auth_version+1 WHERE id=$1`, uid); err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE mobile_sessions SET revoked_at=COALESCE(revoked_at,NOW()) WHERE user_id=$1`, uid); err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM mobile_refresh_tokens WHERE session_id IN (SELECT id FROM mobile_sessions WHERE user_id=$1)`, uid); err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM password_reset_tokens WHERE user_id=$1`, uid); err != nil {
		return uuid.Nil, err
	}
	return id, tx.Commit(ctx)
}
func (r *pgAccountDeletionRepo) Status(ctx context.Context, id uuid.UUID, receipt string) (DeletionJob, error) {
	var job DeletionJob
	err := r.pool.QueryRow(ctx, `SELECT id,status FROM account_deletions WHERE id=$1 AND receipt_hash=$2 AND (completed_at IS NULL OR completed_at>NOW()-INTERVAL '7 days')`, id, receipt).Scan(&job.ID, &job.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return job, ErrNotFound
	}
	return job, err
}

// The transaction holds a work lease; competing workers skip locked jobs. SQL
// completion commits only after every registered external cleanup succeeds.
func (r *pgAccountDeletionRepo) Work(ctx context.Context, cleanup func(context.Context, uuid.UUID) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var job DeletionJob
	err = tx.QueryRow(ctx, `SELECT id,user_id FROM account_deletions WHERE status<>'completed' AND next_attempt_at<=NOW() ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&job.ID, &job.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `DELETE FROM account_deletions WHERE completed_at<NOW()-INTERVAL '7 days'`)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if job.UserID == nil {
		return errors.New("incomplete deletion without user")
	}
	// Acquire the user lock before cleanup so no session/reset can be recreated.
	if _, err = tx.Exec(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, *job.UserID); err != nil {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = cleanup(cleanupCtx, *job.UserID); err != nil {
		if _, updateErr := tx.Exec(ctx, `UPDATE account_deletions SET status='failed',attempts=attempts+1,next_attempt_at=NOW()+INTERVAL '1 minute' WHERE id=$1`, job.ID); updateErr != nil {
			return updateErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return commitErr
		}
		return err
	}
	// FK cascades cover collection, chat, sessions, resets, snapshot and replay.
	if _, err = tx.Exec(ctx, `DELETE FROM users WHERE id=$1`, *job.UserID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE account_deletions SET status='completed',completed_at=NOW(),user_id=NULL,attempts=attempts+1 WHERE id=$1`, job.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
