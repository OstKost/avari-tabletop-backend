package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	tabletop "github.com/ostkost/avari-tabletop-backend"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
	"github.com/stretchr/testify/require"
)

const mobileTestSecret = "synthetic-b01-jwt-secret-for-tests-only"

type testResetMailer struct {
	mu       sync.Mutex
	token    string
	calls    int
	fail     bool
	disabled bool
}

func (m *testResetMailer) Enabled() bool { return !m.disabled }
func (m *testResetMailer) SendReset(_ context.Context, _ string, token string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.token = token
	m.calls++
	if m.fail {
		return errors.New("synthetic delivery failure")
	}
	return nil
}

type mobileFixture struct {
	pool   *pgxpool.Pool
	auth   *service.AuthService
	mobile *service.MobileAuthService
	repo   repository.MobileAuthRepository
	router http.Handler
	mail   *testResetMailer
}

func newMobileFixture(t *testing.T) *mobileFixture {
	t.Helper()
	dsn := os.Getenv("B01_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("B01_TEST_DATABASE_URL is required for isolated PostgreSQL checks")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	require.Equal(t, "avari_b01_test", cfg.ConnConfig.Database, "only the dedicated B01 test database is allowed")
	admin, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	// Extension objects belong to the stable public schema, never to a fixture
	// schema that teardown drops (including concurrent test processes).
	_, err = admin.Exec(context.Background(), `CREATE EXTENSION IF NOT EXISTS "uuid-ossp" WITH SCHEMA public`)
	require.NoError(t, err)
	schema := "b01_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(context.Background(), `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})
	migrations, err := fs.Sub(tabletop.Migrations, "migrations")
	require.NoError(t, err)
	require.NoError(t, repository.RunMigrations(context.Background(), pool, migrations, "003_pgvector", "008_private_pgvector"))
	users := repository.NewUserRepository(pool)
	auth := service.NewAuthService(users, mobileTestSecret)
	repo := repository.NewMobileAuthRepository(pool)
	mail := &testResetMailer{}
	mobile, err := service.NewMobileAuthService(auth, users, repo, mobileTestSecret, mail)
	require.NoError(t, err)
	return &mobileFixture{pool, auth, mobile, repo, NewMobileAuthHandler(mobile).Router(), mail}
}
func (f *mobileFixture) call(method, path string, payload any, access string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	if access != "" {
		req.Header.Set("Authorization", "Bearer "+access)
	}
	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, req)
	return recorder
}
func (f *mobileFixture) register(t *testing.T) service.MobileAuthSession {
	t.Helper()
	rec := f.call("POST", "/auth/register", map[string]string{"email": "player@example.invalid", "username": "Игрок", "password": "synthetic-password"}, "")
	require.Equal(t, 201, rec.Code)
	var session service.MobileAuthSession
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &session))
	return session
}

func TestMobileAuthLifecyclePostgres(t *testing.T) {
	f := newMobileFixture(t)
	session := f.register(t)
	rec := f.call("GET", "/me", nil, session.AccessToken)
	require.Equal(t, 200, rec.Code)
	var user map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &user))
	require.Len(t, user, 4)
	require.NotContains(t, user, "password_hash")
	require.NotContains(t, user, "auth_version")
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Empty(t, rec.Header().Values("Set-Cookie"))
	require.Equal(t, 401, f.call("GET", "/me", nil, "").Code)
	web, err := f.auth.Login(context.Background(), "player@example.invalid", "synthetic-password")
	require.NoError(t, err)
	require.Equal(t, 401, f.call("GET", "/me", nil, web).Code)
	_, err = f.auth.ValidateToken(session.AccessToken)
	require.Error(t, err)
	rec = f.call("POST", "/auth/logout", nil, session.AccessToken)
	require.Equal(t, 204, rec.Code)
	require.Empty(t, rec.Body.Bytes())
	require.Equal(t, 401, f.call("GET", "/me", nil, session.AccessToken).Code)
	_, err = f.mobile.Refresh(context.Background(), session.RefreshToken, uuid.New(), "peer")
	require.ErrorIs(t, err, repository.ErrSessionUnauthorized)
}

func TestMobileRefreshConcurrentReplayAndReusePostgres(t *testing.T) {
	f := newMobileFixture(t)
	initial := f.register(t)
	attempt := uuid.New()
	responses := make(chan service.MobileAuthSession, 12)
	failures := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := f.mobile.Refresh(context.Background(), initial.RefreshToken, attempt, "peer")
			responses <- response
			failures <- err
		}()
	}
	wg.Wait()
	close(responses)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	var canonical service.MobileAuthSession
	for response := range responses {
		if canonical.AccessToken == "" {
			canonical = response
		} else {
			require.True(t, canonical.AccessToken == response.AccessToken && canonical.RefreshToken == response.RefreshToken, "concurrent replay changed its response")
		}
	}
	var count int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM mobile_refresh_tokens`).Scan(&count))
	require.Equal(t, 2, count)
	var cached []byte
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT replay_ciphertext FROM mobile_refresh_tokens WHERE consumed_at IS NOT NULL`).Scan(&cached))
	require.False(t, bytes.Contains(cached, []byte(canonical.RefreshToken)), "refresh token must be encrypted in replay storage")
	// Another process has the same signing/encryption key and sees the durable replay.
	second, err := service.NewMobileAuthService(f.auth, repository.NewUserRepository(f.pool), f.repo, mobileTestSecret, f.mail)
	require.NoError(t, err)
	replay, err := second.Refresh(context.Background(), initial.RefreshToken, attempt, "other-peer")
	require.NoError(t, err)
	require.True(t, replay.RefreshToken == canonical.RefreshToken)
	_, err = second.Refresh(context.Background(), initial.RefreshToken, uuid.New(), "other-peer")
	require.ErrorIs(t, err, repository.ErrSessionUnauthorized)
	require.Equal(t, 401, f.call("GET", "/me", nil, canonical.AccessToken).Code)
	_, err = f.mobile.Refresh(context.Background(), canonical.RefreshToken, uuid.New(), "peer")
	require.ErrorIs(t, err, repository.ErrSessionUnauthorized)
}

func TestMobilePasswordResetRevokesWebAndAllMobilePostgres(t *testing.T) {
	f := newMobileFixture(t)
	first := f.register(t)
	second, err := f.mobile.Login(context.Background(), "player@example.invalid", "synthetic-password")
	require.NoError(t, err)
	web, err := f.auth.Login(context.Background(), "player@example.invalid", "synthetic-password")
	require.NoError(t, err)
	legacy, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": first.User.ID.String(), "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte(mobileTestSecret))
	require.NoError(t, err)
	_, err = f.auth.ValidateToken(legacy)
	require.NoError(t, err)
	known := f.call("POST", "/auth/password-reset/request", map[string]string{"email": "player@example.invalid"}, "")
	unknown := f.call("POST", "/auth/password-reset/request", map[string]string{"email": "unknown@example.invalid"}, "")
	require.Equal(t, 202, known.Code)
	require.Equal(t, 202, unknown.Code)
	require.True(t, bytes.Equal(known.Body.Bytes(), unknown.Body.Bytes()))
	require.Equal(t, 1, f.mail.calls)
	var accepted map[string]string
	require.NoError(t, json.Unmarshal(known.Body.Bytes(), &accepted))
	require.Equal(t, "accepted", accepted["status"])
	var hash string
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT token_hash FROM password_reset_tokens`).Scan(&hash))
	require.Len(t, hash, 64)
	require.False(t, hash == f.mail.token)
	confirm := map[string]string{"reset_token": f.mail.token, "new_password": "new-synthetic-password"}
	require.Equal(t, 204, f.call("POST", "/auth/password-reset/confirm", confirm, "").Code)
	require.Equal(t, 400, f.call("POST", "/auth/password-reset/confirm", confirm, "").Code)
	require.Equal(t, 401, f.call("GET", "/me", nil, first.AccessToken).Code)
	require.Equal(t, 401, f.call("GET", "/me", nil, second.AccessToken).Code)
	_, err = f.auth.ValidateToken(web)
	require.Error(t, err)
	_, err = f.auth.ValidateToken(legacy)
	require.Error(t, err)
	_, err = f.mobile.Login(context.Background(), "player@example.invalid", "synthetic-password")
	require.ErrorIs(t, err, service.ErrInvalidCredentials)
	_, err = f.mobile.Login(context.Background(), "player@example.invalid", "new-synthetic-password")
	require.NoError(t, err)
}

func TestMobileExpiredDeletedAndCleanupPostgres(t *testing.T) {
	f := newMobileFixture(t)
	initial := f.register(t)
	rotated, err := f.mobile.Refresh(context.Background(), initial.RefreshToken, uuid.New(), "peer")
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), `UPDATE mobile_refresh_tokens SET replay_until=NOW()-interval '1 second' WHERE consumed_at IS NOT NULL`)
	require.NoError(t, err)
	require.NoError(t, f.mobile.Cleanup(context.Background()))
	var count int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM mobile_refresh_tokens WHERE replay_ciphertext IS NOT NULL`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM mobile_refresh_tokens`).Scan(&count))
	require.Equal(t, 2, count)
	_, err = f.pool.Exec(context.Background(), `UPDATE mobile_sessions SET expires_at=NOW()-interval '1 second' WHERE id=$1`, rotated.SessionID)
	require.NoError(t, err)
	require.Equal(t, 401, f.call("GET", "/me", nil, rotated.AccessToken).Code)
	other, err := f.mobile.Login(context.Background(), "player@example.invalid", "synthetic-password")
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, initial.User.ID)
	require.NoError(t, err)
	require.Equal(t, 401, f.call("GET", "/me", nil, other.AccessToken).Code)
	_, err = f.auth.Login(context.Background(), "player@example.invalid", "synthetic-password")
	require.ErrorIs(t, err, service.ErrInvalidCredentials)
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM mobile_sessions`).Scan(&count))
	require.Zero(t, count)
}

func TestMobileJSONValidationRateLimitAndMailboxPostgres(t *testing.T) {
	f := newMobileFixture(t)
	f.register(t)
	f.mail.fail = true
	require.Equal(t, 202, f.call("POST", "/auth/password-reset/request", map[string]string{"email": "player@example.invalid"}, "").Code)
	require.Equal(t, 202, f.call("POST", "/auth/password-reset/request", map[string]string{"email": "unknown@example.invalid"}, "").Code)
	f.mail.disabled = true
	require.Equal(t, 503, f.call("POST", "/auth/password-reset/request", map[string]string{"email": "player@example.invalid"}, "").Code)
	require.Equal(t, 503, f.call("POST", "/auth/password-reset/request", map[string]string{"email": "unknown@example.invalid"}, "").Code)
	for _, path := range []string{"/missing", "/collection"} {
		rec := f.call("GET", path, nil, "")
		require.Equal(t, 404, rec.Code)
		require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
		require.Empty(t, rec.Header().Get("Location"))
	}
	require.Equal(t, 405, f.call("PUT", "/auth/login", nil, "").Code)
	for i := 0; i < 12; i++ {
		req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{"email":"player@example.invalid","password":"bad","extra":true}`))
		req.RemoteAddr = "127.0.0.2:2345"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", uuid.NewString())
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		if i < 10 {
			require.Equal(t, 400, rec.Code)
		} else {
			require.Equal(t, 429, rec.Code)
			require.Equal(t, "60", rec.Header().Get("Retry-After"))
		}
	}
}

func TestMobileMigrationDownUpPreservesUsersPostgres(t *testing.T) {
	f := newMobileFixture(t)
	user := f.register(t).User
	down, err := tabletop.Migrations.ReadFile("migrations/004_mobile_auth.down.sql")
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), string(down))
	require.NoError(t, err)
	var count int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM users WHERE id=$1`, user.ID).Scan(&count))
	require.Equal(t, 1, count)
	up, err := tabletop.Migrations.ReadFile("migrations/004_mobile_auth.up.sql")
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), string(up))
	require.NoError(t, err)
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT auth_version FROM users WHERE id=$1`, user.ID).Scan(&count))
	require.Zero(t, count)
}

func TestMobileResetAndRefreshRacePostgres(t *testing.T) {
	f := newMobileFixture(t)
	initial := f.register(t)
	require.NoError(t, f.mobile.RequestReset(context.Background(), "player@example.invalid"))
	var rotated service.MobileAuthSession
	var rotateErr, resetErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		rotated, rotateErr = f.mobile.Refresh(context.Background(), initial.RefreshToken, uuid.New(), "peer")
	}()
	go func() {
		defer wg.Done()
		resetErr = f.mobile.ConfirmReset(context.Background(), f.mail.token, "new-synthetic-password")
	}()
	wg.Wait()
	require.NoError(t, resetErr)
	if rotateErr != nil {
		require.ErrorIs(t, rotateErr, repository.ErrSessionUnauthorized)
	} else {
		require.Equal(t, 401, f.call("GET", "/me", nil, rotated.AccessToken).Code)
	}
	require.Equal(t, 401, f.call("GET", "/me", nil, initial.AccessToken).Code)
}

func TestMobilePasswordResetExpirationAndUTF8Postgres(t *testing.T) {
	f := newMobileFixture(t)
	initial := f.register(t)
	require.NoError(t, f.mobile.RequestReset(context.Background(), "player@example.invalid"))
	_, err := f.pool.Exec(context.Background(), `UPDATE password_reset_tokens SET expires_at=NOW()-interval '1 second'`)
	require.NoError(t, err)
	require.ErrorIs(t, f.mobile.ConfirmReset(context.Background(), f.mail.token, "new-synthetic-password"), repository.ErrResetInvalid)
	require.Equal(t, 200, f.call("GET", "/me", nil, initial.AccessToken).Code)
	_, err = f.mobile.Register(context.Background(), "other@example.invalid", "Other", strings.Repeat("я", 37))
	require.ErrorIs(t, err, service.ErrAuthInput)
	_, err = f.mobile.Register(context.Background(), "other@example.invalid", "Other", "пароль")
	require.ErrorIs(t, err, service.ErrAuthInput)
	_, err = f.mobile.Register(context.Background(), "other@example.invalid", "Другой", "парольдлинный")
	require.NoError(t, err)
}
