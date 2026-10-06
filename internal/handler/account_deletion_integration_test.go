package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	tabletop "github.com/ostkost/avari-tabletop-backend"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
	"github.com/stretchr/testify/require"
)

func deletionFixture(t *testing.T, hooks ...service.AccountCleanup) (*mobileFixture, *service.AccountDeletionService) {
	f := newMobileFixture(t)
	svc := service.NewAccountDeletionService(repository.NewAccountDeletionRepository(f.pool), mobileTestSecret, hooks...)
	h := NewMobileAuthHandler(f.mobile)
	h.SetDeletion(svc)
	f.router = h.Router()
	return f, svc
}
func deleteCall(f *mobileFixture, access, key, password string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"password": password})
	r := httptest.NewRequest("POST", "/me/deletion", bytes.NewReader(body))
	r.RemoteAddr = "127.0.0.1:9988"
	r.Header.Set("Authorization", "Bearer "+access)
	r.Header.Set("Idempotency-Key", key)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}
func statusCall(f *mobileFixture, id uuid.UUID, receipt, access string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/deletions/"+id.String(), nil)
	r.RemoteAddr = "127.0.0.2:9999"
	if receipt != "" {
		r.Header.Set("X-Deletion-Receipt", receipt)
	}
	if access != "" {
		r.Header.Set("Authorization", "Bearer "+access)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}
func acceptedDeletion(t *testing.T, w *httptest.ResponseRecorder) service.DeletionAccepted {
	t.Helper()
	require.Equal(t, 202, w.Code, w.Body.String())
	var result service.DeletionAccepted
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Equal(t, "pending", result.Status)
	require.NotEmpty(t, result.Receipt)
	return result
}
func TestAccountDeletionFreezeReplayScopeAndCleanupPostgres(t *testing.T) {
	ctx := context.Background()
	f, svc := deletionFixture(t)
	s := f.register(t)
	web, err := f.auth.Login(ctx, s.User.Email, "synthetic-password")
	require.NoError(t, err)
	other, err := f.mobile.Register(ctx, "other@example.invalid", "Other", "synthetic-password")
	require.NoError(t, err)
	var gid uuid.UUID
	require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO games(bgg_id,name) VALUES(98765,'Deletion test') RETURNING id`).Scan(&gid))
	_, err = f.pool.Exec(ctx, `INSERT INTO collections(user_id,game_id,notes) VALUES($1,$2,'private note'),($3,$2,'other note')`, s.User.ID, gid, other.User.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `INSERT INTO chat_messages(user_id,game_id,role,content) VALUES($1,$2,'user','private chat'),($3,$2,'user','other chat')`, s.User.ID, gid, other.User.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `INSERT INTO collection_snapshots(id,user_id,expires_at,items) VALUES($1,$2,NOW()+INTERVAL '5 minutes','[]')`, uuid.New(), s.User.ID)
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `INSERT INTO collection_add_replays(user_id,key,body_hash,status,request_id,response,expires_at) VALUES($1,$2,'test',201,$3,'{}',NOW()+INTERVAL '1 day')`, s.User.ID, uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, f.mobile.RequestReset(ctx, s.User.Email))
	key := uuid.NewString()
	a := acceptedDeletion(t, deleteCall(f, s.AccessToken, key, "synthetic-password"))
	require.Equal(t, a, acceptedDeletion(t, deleteCall(f, s.AccessToken, key, "synthetic-password")))
	require.Equal(t, 409, deleteCall(f, s.AccessToken, key, "different-password").Code)
	require.Equal(t, 401, deleteCall(f, s.AccessToken, uuid.NewString(), "synthetic-password").Code)
	require.Equal(t, 401, f.call("GET", "/me", nil, s.AccessToken).Code)
	_, err = f.auth.ValidateTokenContext(ctx, web)
	require.Error(t, err)
	_, err = f.mobile.Login(ctx, s.User.Email, "synthetic-password")
	require.Error(t, err)
	_, err = f.mobile.Refresh(ctx, s.RefreshToken, uuid.New(), "test")
	require.Error(t, err)
	require.Error(t, f.mobile.ConfirmReset(ctx, f.mail.token, "changed-password"))
	require.Equal(t, 404, statusCall(f, a.RequestID, "", other.AccessToken).Code)
	require.Equal(t, 404, statusCall(f, a.RequestID, "wrong", "").Code)
	require.Equal(t, 404, statusCall(f, uuid.New(), a.Receipt, "").Code)
	pending := statusCall(f, a.RequestID, a.Receipt, "")
	require.Equal(t, 200, pending.Code)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(pending.Body.Bytes(), &fields))
	require.Len(t, fields, 3)
	require.Equal(t, "pending", fields["status"])
	require.NoError(t, svc.Work(ctx))
	done := statusCall(f, a.RequestID, a.Receipt, "")
	require.Equal(t, 200, done.Code)
	require.NoError(t, json.Unmarshal(done.Body.Bytes(), &fields))
	require.Len(t, fields, 3)
	require.Equal(t, "completed", fields["status"])
	require.NotContains(t, done.Body.String(), s.User.Email)
	require.NotContains(t, done.Body.String(), s.User.ID.String())
	// Exact replay survives account cleanup; the raw receipt is never in SQL.
	require.Equal(t, a, acceptedDeletion(t, deleteCall(f, s.AccessToken, key, "synthetic-password")))
	for _, table := range []string{"collections", "chat_messages", "mobile_sessions", "password_reset_tokens", "collection_snapshots", "collection_add_replays"} {
		var count int
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+table+` WHERE user_id=$1`, s.User.ID).Scan(&count))
		require.Zero(t, count, table)
	}
	var uid *uuid.UUID
	var hash string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT user_id,receipt_hash FROM account_deletions WHERE id=$1`, a.RequestID).Scan(&uid, &hash))
	require.Nil(t, uid)
	require.NotEqual(t, a.Receipt, hash)
	var otherRows int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM collections WHERE user_id=$1`, other.User.ID).Scan(&otherRows))
	require.Equal(t, 1, otherRows)
	require.Equal(t, 200, f.call("GET", "/me", nil, other.AccessToken).Code)
	_, err = f.pool.Exec(ctx, `UPDATE account_deletions SET completed_at=NOW()-INTERVAL '8 days' WHERE id=$1`, a.RequestID)
	require.NoError(t, err)
	require.Equal(t, 404, statusCall(f, a.RequestID, a.Receipt, "").Code)
	require.NoError(t, svc.Work(ctx))
	var jobs int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_deletions`).Scan(&jobs))
	require.Zero(t, jobs)
}
func TestAccountDeletionCleanupFailureAndConcurrentWorkersPostgres(t *testing.T) {
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls int
	f, svc := deletionFixture(t, func(ctx context.Context, _ uuid.UUID) error {
		calls++
		if calls == 1 {
			return errors.New("synthetic cleanup failure")
		}
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	s := f.register(t)
	a := acceptedDeletion(t, deleteCall(f, s.AccessToken, uuid.NewString(), "synthetic-password"))
	require.Error(t, svc.Work(ctx))
	result, err := svc.Status(ctx, a.RequestID, a.Receipt)
	require.NoError(t, err)
	require.Equal(t, "failed", result.Status)
	require.True(t, result.Retryable)
	var users int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE id=$1`, s.User.ID).Scan(&users))
	require.Equal(t, 1, users)
	_, err = f.pool.Exec(ctx, `UPDATE account_deletions SET next_attempt_at=NOW() WHERE id=$1`, a.RequestID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- svc.Work(ctx) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not enter cleanup")
	}
	// Separate instance must skip the leased job and not prematurely complete it.
	other := service.NewAccountDeletionService(repository.NewAccountDeletionRepository(f.pool), mobileTestSecret, func(context.Context, uuid.UUID) error { t.Error("duplicate cleanup"); return nil })
	require.NoError(t, other.Work(ctx))
	result, err = svc.Status(ctx, a.RequestID, a.Receipt)
	require.NoError(t, err)
	require.NotEqual(t, "completed", result.Status)
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, svc.Work(ctx))
	require.Equal(t, 2, calls)
	result, err = svc.Status(ctx, a.RequestID, a.Receipt)
	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
}
func TestAccountDeletionReauthenticationConcurrentRetryPostgres(t *testing.T) {
	ctx := context.Background()
	f, svc := deletionFixture(t)
	s := f.register(t)
	require.Equal(t, 401, deleteCall(f, s.AccessToken, uuid.NewString(), "wrong-password").Code)
	require.Equal(t, 400, deleteCall(f, s.AccessToken, "not-a-uuid", "synthetic-password").Code)
	require.Equal(t, 200, f.call("GET", "/me", nil, s.AccessToken).Code)
	key := uuid.New()
	var wg sync.WaitGroup
	ids := make(chan uuid.UUID, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := svc.Request(ctx, s.User.ID, key, "synthetic-password", true)
			ids <- a.RequestID
			errs <- err
		}()
	}
	wg.Wait()
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	require.Equal(t, <-ids, <-ids)
	var jobs int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_deletions`).Scan(&jobs))
	require.Equal(t, 1, jobs)
}
func TestAccountDeletionPublicWebResource(t *testing.T) {
	w := httptest.NewRecorder()
	AccountDeletionPage(w, httptest.NewRequest(http.MethodGet, "/account/delete", nil))
	require.Equal(t, 200, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Contains(t, w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")
	require.Contains(t, w.Body.String(), "me/deletion")
	require.Contains(t, w.Body.String(), "X-Deletion-Receipt")
	require.NotContains(t, strings.ToLower(w.Body.String()), "localstorage")
}
func TestAccountDeletionMigrationRoundtripPostgres(t *testing.T) {
	f, _ := deletionFixture(t)
	ctx := context.Background()
	migrations, err := fs.Sub(tabletop.Migrations, "migrations")
	require.NoError(t, err)
	down, err := fs.ReadFile(migrations, "006_account_deletion.down.sql")
	require.NoError(t, err)
	up, err := fs.ReadFile(migrations, "006_account_deletion.up.sql")
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, string(down))
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, string(up))
	require.NoError(t, err)
	s := f.register(t)
	acceptedDeletion(t, deleteCall(f, s.AccessToken, uuid.NewString(), "synthetic-password"))
}
