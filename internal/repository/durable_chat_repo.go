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

var ErrChatActive = errors.New("chat generation already active")
var ErrChatQuota = errors.New("chat quota exceeded")
var ErrChatInactive = errors.New("chat request no longer running")
var ErrChatReplayExpired = errors.New("chat event replay expired")
var ErrChatEventLimit = errors.New("chat event budget exceeded")

type DurableChatRepository interface {
	Submit(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, string, string, int) (model.ChatRequest, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (model.ChatRequest, error)
	Claim(context.Context, time.Duration) (model.ChatRequest, error)
	Running(context.Context, uuid.UUID) (bool, error)
	Start(context.Context, model.ChatRequest, string) error
	Delta(context.Context, model.ChatRequest, string, int, int) error
	Finish(context.Context, model.ChatRequest, string, *model.ChatResult, string) (model.ChatRequest, error)
	Cancel(context.Context, uuid.UUID, uuid.UUID) (model.ChatRequest, error)
	Events(context.Context, uuid.UUID, uuid.UUID, int64) ([]model.ChatEvent, model.ChatRequest, error)
	History(context.Context, uuid.UUID, uuid.UUID, int, int64) ([]model.DurableChatMessage, error)
	ClearHistory(context.Context, uuid.UUID, uuid.UUID) error
	Report(context.Context, uuid.UUID, uuid.UUID, string) error
	Cleanup(context.Context) error
	CleanupOwner(context.Context, uuid.UUID) error
}
type pgDurableChatRepo struct{ pool *pgxpool.Pool }

func NewDurableChatRepository(pool *pgxpool.Pool) DurableChatRepository {
	return &pgDurableChatRepo{pool}
}

const chatRequestColumns = `r.id,r.user_id,r.game_id,r.client_request_id,r.status,r.created_at,r.result,r.error_code,r.input_message,r.last_sequence,r.event_bytes,r.completed_at,r.deadline_at`
const chatRequestVisible = ` FROM chat_requests r JOIN users u ON u.id=r.user_id WHERE r.deleted_at IS NULL AND u.deletion_requested_at IS NULL AND EXISTS(SELECT 1 FROM collections c WHERE c.user_id=r.user_id AND c.game_id=r.game_id) `

func scanChatRequest(row pgx.Row) (model.ChatRequest, error) {
	var r model.ChatRequest
	var result []byte
	err := row.Scan(&r.ID, &r.UserID, &r.GameID, &r.ClientRequestID, &r.Status, &r.CreatedAt, &result, &r.ErrorCode, &r.Input, &r.LastSequence, &r.EventBytes, &r.CompletedAt, &r.DeadlineAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if result != nil {
		r.Result = &model.ChatResult{}
		err = json.Unmarshal(result, r.Result)
	}
	return r, err
}
func (r *pgDurableChatRepo) Get(ctx context.Context, user, id uuid.UUID) (model.ChatRequest, error) {
	return scanChatRequest(r.pool.QueryRow(ctx, `SELECT `+chatRequestColumns+chatRequestVisible+`AND r.user_id=$1 AND r.id=$2`, user, id))
}
func (r *pgDurableChatRepo) Submit(ctx context.Context, user, game, client, key uuid.UUID, body, message string, quota int) (model.ChatRequest, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return model.ChatRequest{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockRulesOwner(ctx, tx, user, game); err != nil {
		return model.ChatRequest{}, err
	}
	var id uuid.UUID
	var savedBody string
	var deleted *time.Time
	err = tx.QueryRow(ctx, `SELECT r.id,r.body_hash,r.deleted_at FROM chat_requests r JOIN chat_request_keys k ON k.request_id=r.id WHERE k.user_id=$1 AND k.key=$2`, user, key).Scan(&id, &savedBody, &deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id,body_hash,deleted_at FROM chat_requests WHERE user_id=$1 AND game_id=$2 AND client_request_id=$3`, user, game, client).Scan(&id, &savedBody, &deleted)
	}
	if err == nil {
		if savedBody != body {
			return model.ChatRequest{}, ErrIdempotencyMismatch
		}
		if deleted != nil {
			return model.ChatRequest{}, ErrNotFound
		}
		_, err = tx.Exec(ctx, `INSERT INTO chat_request_keys(user_id,key,request_id) VALUES($1,$2,$3) ON CONFLICT(user_id,key) DO NOTHING`, user, key, id)
		if err != nil {
			return model.ChatRequest{}, err
		}
		request, err := scanChatRequest(tx.QueryRow(ctx, `SELECT `+chatRequestColumns+` FROM chat_requests r WHERE id=$1`, id))
		if err != nil {
			return request, err
		}
		return request, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.ChatRequest{}, err
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat_requests WHERE user_id=$1 AND game_id=$2 AND status IN ('pending','running'))`, user, game).Scan(&active)
	if err != nil {
		return model.ChatRequest{}, err
	}
	if active {
		current, err := scanChatRequest(tx.QueryRow(ctx, `SELECT `+chatRequestColumns+` FROM chat_requests r WHERE r.user_id=$1 AND r.game_id=$2 AND r.status IN ('pending','running')`, user, game))
		if err != nil {
			return current, err
		}
		return current, ErrChatActive
	}
	var count int
	err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM chat_requests WHERE user_id=$1 AND created_at>NOW()-INTERVAL '24 hours'`, user).Scan(&count)
	if err != nil {
		return model.ChatRequest{}, err
	}
	if count >= quota {
		return model.ChatRequest{}, ErrChatQuota
	}
	id = uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO chat_requests(id,user_id,game_id,client_request_id,body_hash,input_message) VALUES($1,$2,$3,$4,$5,$6)`, id, user, game, client, body, message)
	if err != nil {
		return model.ChatRequest{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO chat_request_keys(user_id,key,request_id) VALUES($1,$2,$3)`, user, key, id)
	if err != nil {
		return model.ChatRequest{}, err
	}
	result, err := scanChatRequest(tx.QueryRow(ctx, `SELECT `+chatRequestColumns+` FROM chat_requests r WHERE id=$1`, id))
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
func (r *pgDurableChatRepo) Claim(ctx context.Context, budget time.Duration) (model.ChatRequest, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return model.ChatRequest{}, err
	}
	defer tx.Rollback(ctx)
	request, err := scanChatRequest(tx.QueryRow(ctx, `SELECT `+chatRequestColumns+chatRequestVisible+`AND r.status='pending' ORDER BY r.created_at LIMIT 1 FOR UPDATE OF r SKIP LOCKED`))
	if err != nil {
		return request, err
	}
	_, err = tx.Exec(ctx, `UPDATE chat_requests SET status='running',started_at=NOW(),deadline_at=NOW()+make_interval(secs=>$2) WHERE id=$1`, request.ID, budget.Seconds())
	if err != nil {
		return request, err
	}
	request.Status = "running"
	deadline := time.Now().Add(budget)
	request.DeadlineAt = &deadline
	return request, tx.Commit(ctx)
}
func (r *pgDurableChatRepo) Running(ctx context.Context, id uuid.UUID) (bool, error) {
	var running bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1`+chatRequestVisible+`AND r.id=$1 AND r.status='running')`, id).Scan(&running)
	return running, err
}
func appendChatEvent(ctx context.Context, tx pgx.Tx, request *model.ChatRequest, kind string, payload any, terminal bool) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request.LastSequence++
	_, err = tx.Exec(ctx, `INSERT INTO chat_events(request_id,sequence,kind,payload,terminal) VALUES($1,$2,$3,$4,$5)`, request.ID, request.LastSequence, kind, data, terminal)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE chat_requests SET last_sequence=$2,event_bytes=event_bytes+$3 WHERE id=$1`, request.ID, request.LastSequence, len(data))
	return err
}
func (r *pgDurableChatRepo) Start(ctx context.Context, request model.ChatRequest, mode string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := scanChatRequest(tx.QueryRow(ctx, `SELECT `+chatRequestColumns+chatRequestVisible+`AND r.id=$1 FOR UPDATE OF r`, request.ID))
	if err != nil {
		return err
	}
	if current.Status != "running" {
		return ErrChatInactive
	}
	if current.LastSequence > 0 {
		return nil
	}
	err = appendChatEvent(ctx, tx, &current, "started", map[string]any{"request_id": current.ID, "status": "running", "mode": mode}, false)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *pgDurableChatRepo) Delta(ctx context.Context, request model.ChatRequest, text string, maxBytes, maxEvents int) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := scanChatRequest(tx.QueryRow(ctx, `SELECT `+chatRequestColumns+chatRequestVisible+`AND r.id=$1 FOR UPDATE OF r`, request.ID))
	if err != nil {
		return err
	}
	if current.Status != "running" {
		return ErrChatInactive
	}
	encoded, err := json.Marshal(map[string]any{"request_id": current.ID, "sequence": current.LastSequence + 1, "text": text})
	if err != nil {
		return err
	}
	if current.EventBytes+len(encoded) > maxBytes || current.LastSequence >= int64(maxEvents-1) {
		return ErrChatEventLimit
	}
	err = appendChatEvent(ctx, tx, &current, "delta", map[string]any{"request_id": current.ID, "sequence": current.LastSequence + 1, "text": text}, false)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func finishChatRequest(ctx context.Context, tx pgx.Tx, request model.ChatRequest, status string, result *model.ChatResult, code string) (model.ChatRequest, error) {
	if request.Terminal() {
		return request, nil
	}
	var raw any
	var errorCode any
	if result != nil {
		encoded, err := json.Marshal(result)
		if err != nil {
			return request, err
		}
		raw = encoded
	}
	if code != "" {
		errorCode = code
	}
	_, err := tx.Exec(ctx, `UPDATE chat_requests SET status=$2,result=$3,error_code=$4,input_message='',completed_at=NOW() WHERE id=$1`, request.ID, status, raw, errorCode)
	if err != nil {
		return request, err
	}
	request.Status = status
	request.Result = result
	request.Input = ""
	if code != "" {
		request.ErrorCode = &code
	}
	now := time.Now()
	request.CompletedAt = &now
	var payload any = request
	if status == "cancelled" {
		payload = map[string]any{"request_id": request.ID, "status": "cancelled"}
	}
	if status == "failed" {
		payload = map[string]any{"request_id": request.ID, "status": "failed", "error": map[string]any{"code": code, "retryable": code != "output_limit"}}
	}
	err = appendChatEvent(ctx, tx, &request, status, payload, true)
	return request, err
}
func (r *pgDurableChatRepo) Finish(ctx context.Context, request model.ChatRequest, status string, result *model.ChatResult, code string) (model.ChatRequest, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return request, err
	}
	defer tx.Rollback(ctx)
	// user -> request lock ordering matches account freeze/cleanup and submit.
	if err = lockRulesOwner(ctx, tx, request.UserID, request.GameID); err != nil {
		return request, err
	}
	current, err := scanChatRequest(tx.QueryRow(ctx, `SELECT `+chatRequestColumns+` FROM chat_requests r WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, request.ID))
	if err != nil {
		return current, err
	}
	if current.Terminal() {
		return current, nil
	}
	if status == "completed" {
		if result == nil {
			return current, errors.New("completion missing result")
		}
		for _, message := range []struct {
			role string
			text string
		}{{"user", current.Input}, {"assistant", result.Text}} {
			_, err = tx.Exec(ctx, `INSERT INTO chat_messages(user_id,game_id,request_id,role,content,created_at) VALUES($1,$2,$3,$4,$5,clock_timestamp())`, current.UserID, current.GameID, current.ID, message.role, message.text)
			if err != nil {
				return current, err
			}
		}
	}
	current, err = finishChatRequest(ctx, tx, current, status, result, code)
	if err != nil {
		return current, err
	}
	return current, tx.Commit(ctx)
}
func (r *pgDurableChatRepo) Cancel(ctx context.Context, user, id uuid.UUID) (model.ChatRequest, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return model.ChatRequest{}, err
	}
	defer tx.Rollback(ctx)
	current, err := scanChatRequest(tx.QueryRow(ctx, `SELECT `+chatRequestColumns+chatRequestVisible+`AND r.user_id=$1 AND r.id=$2 FOR UPDATE OF r`, user, id))
	if err != nil {
		return current, err
	}
	if current.Terminal() {
		return current, nil
	}
	current, err = finishChatRequest(ctx, tx, current, "cancelled", nil, "")
	if err != nil {
		return current, err
	}
	return current, tx.Commit(ctx)
}
func (r *pgDurableChatRepo) Events(ctx context.Context, user, id uuid.UUID, after int64) ([]model.ChatEvent, model.ChatRequest, error) {
	request, err := r.Get(ctx, user, id)
	if err != nil {
		return nil, request, err
	}
	if request.CompletedAt != nil && time.Since(*request.CompletedAt) > 10*time.Minute {
		return nil, request, ErrChatReplayExpired
	}
	if after < 0 || after > request.LastSequence {
		return nil, request, ErrRulesStale
	}
	rows, err := r.pool.Query(ctx, `SELECT sequence,kind,payload FROM chat_events WHERE request_id=$1 AND sequence>$2 ORDER BY sequence LIMIT 256`, id, after)
	if err != nil {
		return nil, request, err
	}
	defer rows.Close()
	events := make([]model.ChatEvent, 0)
	for rows.Next() {
		var e model.ChatEvent
		if err = rows.Scan(&e.Sequence, &e.Kind, &e.Payload); err != nil {
			return nil, request, err
		}
		events = append(events, e)
	}
	return events, request, rows.Err()
}
func (r *pgDurableChatRepo) History(ctx context.Context, user, game uuid.UUID, limit int, before int64) ([]model.DurableChatMessage, error) {
	var owned bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM collections c JOIN users u ON u.id=c.user_id WHERE c.user_id=$1 AND c.game_id=$2 AND u.deletion_requested_at IS NULL)`, user, game).Scan(&owned); err != nil {
		return nil, err
	}
	if !owned {
		return nil, ErrNotFound
	}
	rows, err := r.pool.Query(ctx, `SELECT m.id,m.role,m.content,m.created_at,m.request_id,m.sequence FROM chat_messages m JOIN chat_requests r ON r.id=m.request_id WHERE m.user_id=$1 AND m.game_id=$2 AND r.status='completed' AND r.deleted_at IS NULL AND ($3::bigint=0 OR m.sequence<$3) ORDER BY m.sequence DESC LIMIT $4`, user, game, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.DurableChatMessage, 0)
	for rows.Next() {
		var m model.DurableChatMessage
		if err = rows.Scan(&m.ID, &m.Role, &m.Content, &m.CreatedAt, &m.RequestID, &m.Sequence); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
func (r *pgDurableChatRepo) ClearHistory(ctx context.Context, user, game uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockRulesOwner(ctx, tx, user, game); err != nil {
		return err
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat_requests WHERE user_id=$1 AND game_id=$2 AND status IN ('pending','running'))`, user, game).Scan(&active)
	if err != nil {
		return err
	}
	if active {
		return ErrChatActive
	}
	_, err = tx.Exec(ctx, `DELETE FROM ai_reports WHERE request_id IN (SELECT id FROM chat_requests WHERE user_id=$1 AND game_id=$2)`, user, game)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM chat_events WHERE request_id IN (SELECT id FROM chat_requests WHERE user_id=$1 AND game_id=$2)`, user, game)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM chat_messages WHERE user_id=$1 AND game_id=$2`, user, game)
	if err != nil {
		return err
	}
	// Tombstone keys preserve replay suppression and quota; all content is gone.
	_, err = tx.Exec(ctx, `UPDATE chat_requests SET deleted_at=NOW(),input_message='',result=NULL,error_code=NULL,event_bytes=0 WHERE user_id=$1 AND game_id=$2`, user, game)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *pgDurableChatRepo) Report(ctx context.Context, user, id uuid.UUID, reason string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockRulesOwner(ctx, tx, user, uuid.Nil); err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1`+chatRequestVisible+`AND r.id=$1 AND r.user_id=$2 AND r.status='completed')`, id, user).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrNotFound
	}
	var exists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_reports WHERE user_id=$1 AND request_id=$2 AND reason=$3)`, user, id, reason).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		var count int
		err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM ai_reports WHERE user_id=$1 AND created_at>NOW()-INTERVAL '24 hours'`, user).Scan(&count)
		if err != nil {
			return err
		}
		if count >= 10 {
			return ErrChatQuota
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO ai_reports(user_id,request_id,reason) VALUES($1,$2,$3) ON CONFLICT(user_id,request_id,reason) DO NOTHING`, user, id, reason)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *pgDurableChatRepo) Cleanup(ctx context.Context) error {
	// Membership removal prevents pending jobs from reviving on re-add. Only
	// a failed terminal is written here, so no user FK insert/lock is needed.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	orphanRows, err := tx.Query(ctx, `SELECT `+chatRequestColumns+` FROM chat_requests r WHERE status IN ('pending','running') AND NOT EXISTS(SELECT 1 FROM collections c WHERE c.user_id=r.user_id AND c.game_id=r.game_id) LIMIT 100 FOR UPDATE OF r SKIP LOCKED`)
	if err != nil {
		tx.Rollback(ctx)
		return err
	}
	var abandoned []model.ChatRequest
	for orphanRows.Next() {
		request, e := scanChatRequest(orphanRows)
		if e != nil {
			orphanRows.Close()
			tx.Rollback(ctx)
			return e
		}
		abandoned = append(abandoned, request)
	}
	err = orphanRows.Err()
	orphanRows.Close()
	if err != nil {
		tx.Rollback(ctx)
		return err
	}
	for _, request := range abandoned {
		if _, err = finishChatRequest(ctx, tx, request, "failed", nil, "collection_removed"); err != nil {
			tx.Rollback(ctx)
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	// Expired running workers are failed once; never replay an uncertain LLM call.
	rows, err := r.pool.Query(ctx, `SELECT `+chatRequestColumns+` FROM chat_requests r WHERE status='running' AND deadline_at<NOW() LIMIT 100`)
	if err != nil {
		return err
	}
	var requests []model.ChatRequest
	for rows.Next() {
		request, e := scanChatRequest(rows)
		if e != nil {
			rows.Close()
			return e
		}
		requests = append(requests, request)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, request := range requests {
		if _, err = r.Finish(ctx, request, "failed", nil, "worker_interrupted"); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	_, err = r.pool.Exec(ctx, `DELETE FROM chat_events WHERE request_id IN (SELECT id FROM chat_requests WHERE completed_at<NOW()-INTERVAL '10 minutes')`)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `DELETE FROM ai_reports WHERE expires_at<NOW()`)
	return err
}
func (r *pgDurableChatRepo) CleanupOwner(ctx context.Context, user uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM chat_requests WHERE user_id=$1`, user)
	return err
}
