package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	tabletop "github.com/ostkost/avari-tabletop-backend"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
	"github.com/stretchr/testify/require"
)

type privateEmbedder struct {
	mu        sync.Mutex
	fail      bool
	dimension int
	blockText string
	started   chan struct{}
	release   chan struct{}
	calls     int
}

func (e *privateEmbedder) Dims() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dimension == 0 {
		return 3
	}
	return e.dimension
}
func (e *privateEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	e.calls++
	failed := e.fail
	dimension := e.dimension
	started, release := e.started, e.release
	blocked := started != nil && strings.Contains(text, e.blockText)
	if blocked {
		e.started = nil
	}
	e.mu.Unlock()
	if blocked {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if failed {
		return nil, errors.New("synthetic embedding failure")
	}
	if dimension == 0 {
		dimension = 3
	}
	vec := make([]float32, dimension)
	vec[0] = 1
	return vec, nil
}
func (e *privateEmbedder) setFail(fail bool) { e.mu.Lock(); e.fail = fail; e.mu.Unlock() }
func (e *privateEmbedder) block(text string) (<-chan struct{}, chan struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.blockText = text
	e.started = make(chan struct{})
	e.release = make(chan struct{})
	return e.started, e.release
}

type privateRulesFixture struct {
	*mobileFixture
	svc      *service.PrivateRulesService
	repo     repository.PrivateRulesRepository
	store    service.PrivateVectorStore
	embedder *privateEmbedder
	a, b     service.MobileAuthSession
	game     uuid.UUID
	root     string
	kind     string
}

func newPrivateRulesFixture(t *testing.T, kind string) *privateRulesFixture {
	t.Helper()
	f := newMobileFixture(t)
	ctx := context.Background()
	embedder := &privateEmbedder{}
	var store service.PrivateVectorStore
	root := t.TempDir()
	if kind == "pgvector" {
		_, err := f.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public`)
		require.NoError(t, err)
		migrations, err := fs.Sub(tabletop.Migrations, "migrations")
		require.NoError(t, err)
		require.NoError(t, repository.RunMigrations(ctx, f.pool, migrations, "003_pgvector"))
		store = service.NewPrivatePgvectorStore(f.pool, embedder)
	} else {
		var err error
		store, err = service.NewPrivateChromemStore(root, embedder)
		require.NoError(t, err)
	}
	repo := repository.NewPrivateRulesRepository(f.pool)
	svc := service.NewPrivateRulesService(repo, store)
	a := f.register(t)
	b, err := f.mobile.Register(ctx, "second@example.invalid", "Другой", "synthetic-password")
	require.NoError(t, err)
	var game uuid.UUID
	require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO games(bgg_id,name) VALUES(2147483002,'Synthetic private rules') RETURNING id`).Scan(&game))
	_, err = f.pool.Exec(ctx, `INSERT INTO collections(user_id,game_id) VALUES($1,$3),($2,$3)`, a.User.ID, b.User.ID, game)
	require.NoError(t, err)
	h := NewMobileAuthHandler(f.mobile, NewMobileRulesHandler(svc, repository.NewCollectionRepository(f.pool), nil))
	f.router = h.Router()
	return &privateRulesFixture{f, svc, repo, store, embedder, a, b, game, root, kind}
}
func (f *privateRulesFixture) callRules(method, path string, payload any, access, match, none, key string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:9988"
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	if match != "" {
		req.Header.Set("If-Match", match)
	}
	if none != "" {
		req.Header.Set("If-None-Match", none)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if dir := os.Getenv("B03_EVIDENCE_DIR"); dir != "" {
		// Only synthetic fixture responses, never Authorization/session material.
		evidence, _ := json.Marshal(map[string]any{"method": method, "path": path, "status": w.Code, "etag": w.Header().Get("ETag"), "body": w.Body.String()})
		if err := os.MkdirAll(dir, 0700); err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(dir, uuid.NewString()+".json"), evidence, 0600); err != nil {
			panic(err)
		}
	}
	return w
}
func (f *privateRulesFixture) path(lang string) string {
	return "/games/" + f.game.String() + "/rules/" + lang
}
func (f *privateRulesFixture) save(t *testing.T, user service.MobileAuthSession, lang, text string, version int64) model.PrivateRule {
	t.Helper()
	match, none := "", "*"
	if version > 0 {
		match = rulesETag(version)
		none = ""
	}
	w := f.callRules("PUT", f.path(lang), map[string]string{"content": text, "source": "synthetic"}, user.AccessToken, match, none, "")
	require.Equal(t, 202, w.Code, w.Body.String())
	var rule model.PrivateRule
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rule))
	require.Equal(t, rulesETag(rule.Revision), w.Header().Get("ETag"))
	return rule
}
func scopeOf(rule model.PrivateRule, user uuid.UUID, version int64) service.RulesScope {
	return service.RulesScope{UserID: user, GameID: rule.GameID, RuleID: rule.ID, Language: rule.Language, Revision: version}
}
func waitPrivateWorker(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("private rules worker timed out")
	}
}
func TestPrivateRulesIsolationLifecyclePostgres(t *testing.T) {
	for _, kind := range []string{"chromem", "pgvector"} {
		t.Run(kind, func(t *testing.T) {
			f := newPrivateRulesFixture(t, kind)
			ctx := context.Background()
			require.NoError(t, repository.NewRulesRepository(f.pool).Upsert(ctx, f.game, "ru", "LEGACY OWNERLESS", "legacy"))
			require.Equal(t, 404, f.callRules("GET", f.path("ru"), nil, f.a.AccessToken, "", "", "").Code)
			aRU := f.save(t, f.a, "ru", "Синтетические правила A русский", 0)
			aEN := f.save(t, f.a, "en", "Synthetic rules A English", 0)
			bRU := f.save(t, f.b, "ru", "Синтетические правила B русский", 0)
			require.NotEqual(t, aRU.ID, bRU.ID)
			for i := 0; i < 3; i++ {
				require.NoError(t, f.svc.Work(ctx))
			}
			for _, tc := range []struct {
				user       service.MobileAuthSession
				lang, text string
			}{{f.a, "ru", aRU.Content}, {f.a, "en", aEN.Content}, {f.b, "ru", bRU.Content}} {
				docs, err := f.svc.Retrieve(ctx, tc.user.User.ID, f.game, tc.lang, "synthetic query", 10)
				require.NoError(t, err)
				require.Len(t, docs, 1)
				require.Equal(t, tc.text, docs[0].Content)
				require.Equal(t, tc.user.User.ID, docs[0].Scope.UserID)
				require.Equal(t, int64(1), docs[0].Scope.Revision)
			}
			require.Equal(t, 404, f.callRules("GET", "/rules/"+aRU.ID.String()+"/index-status", nil, f.b.AccessToken, "", "", "").Code)
			f.embedder.setFail(true)
			updated := f.save(t, f.a, "ru", "Новый сохранённый текст; failed embedding", 1)
			require.Equal(t, int64(2), updated.Revision)
			require.Error(t, f.svc.Work(ctx))
			f.embedder.setFail(false)
			saved, err := f.svc.Get(ctx, f.a.User.ID, f.game, "ru")
			require.NoError(t, err)
			require.Equal(t, updated.Content, saved.Content)
			require.Equal(t, "failed", saved.IndexStatus)
			require.Equal(t, int64(1), *saved.ActiveRevision)
			docs, err := f.svc.Retrieve(ctx, f.a.User.ID, f.game, "ru", "query", 3)
			require.NoError(t, err)
			require.Equal(t, aRU.Content, docs[0].Content)
			require.Equal(t, int64(1), docs[0].Scope.Revision)
			key := uuid.NewString()
			retryPath := "/rules/" + aRU.ID.String() + "/reindex"
			retry := f.callRules("POST", retryPath, nil, f.a.AccessToken, `"v2"`, "", key)
			require.Equal(t, 202, retry.Code, retry.Body.String())
			replay := f.callRules("POST", retryPath, nil, f.a.AccessToken, `"v2"`, "", key)
			require.Equal(t, retry.Body.String(), replay.Body.String())
			require.Equal(t, 409, f.callRules("POST", retryPath, nil, f.a.AccessToken, `"v1"`, "", key).Code)
			require.NoError(t, f.svc.Work(ctx))
			saved, err = f.svc.Get(ctx, f.a.User.ID, f.game, "ru")
			require.NoError(t, err)
			require.Equal(t, "ready", saved.IndexStatus)
			require.Equal(t, int64(2), *saved.ActiveRevision)
			docs, err = f.store.Search(ctx, scopeOf(aRU, f.a.User.ID, 1), "query", 3)
			require.NoError(t, err)
			require.Empty(t, docs)
			require.NoError(t, f.svc.Delete(ctx, f.a.User.ID, f.game, "en", 1))
			docs, err = f.svc.Retrieve(ctx, f.a.User.ID, f.game, "en", "query", 3)
			require.NoError(t, err)
			require.Empty(t, docs)
			require.NoError(t, f.svc.Work(ctx))
			docs, err = f.store.Search(ctx, scopeOf(aEN, f.a.User.ID, 1), "query", 3)
			require.NoError(t, err)
			require.Empty(t, docs)
			require.Equal(t, 404, f.callRules("GET", "/rules/"+aEN.ID.String()+"/index-status", nil, f.a.AccessToken, "", "", "").Code)
			docs, err = f.svc.Retrieve(ctx, f.b.User.ID, f.game, "ru", "query", 3)
			require.NoError(t, err)
			require.Equal(t, bRU.Content, docs[0].Content)
			// Persisted chromem reopen is a real filesystem check.
			if kind == "chromem" {
				reopened, err := service.NewPrivateChromemStore(f.root, f.embedder)
				require.NoError(t, err)
				docs, err = reopened.Search(ctx, scopeOf(updated, f.a.User.ID, 2), "query", 3)
				require.NoError(t, err)
				require.Len(t, docs, 1)
			}
			legacy, err := repository.NewRulesRepository(f.pool).Get(ctx, f.game, "ru")
			require.NoError(t, err)
			require.Equal(t, "LEGACY OWNERLESS", legacy.Content)
		})
	}
}
func TestPrivateRulesDeleteAndSupersedeInFlightPostgres(t *testing.T) {
	for _, kind := range []string{"chromem", "pgvector"} {
		for _, remove := range []bool{true, false} {
			name := "supersede"
			if remove {
				name = "delete"
			}
			t.Run(kind+"/"+name, func(t *testing.T) {
				f := newPrivateRulesFixture(t, kind)
				ctx := context.Background()
				first := f.save(t, f.a, "ru", "Первый готовый текст", 0)
				require.NoError(t, f.svc.Work(ctx))
				second := f.save(t, f.a, "ru", "BLOCK новый второй текст", 1)
				started, release := f.embedder.block("BLOCK")
				done := make(chan error, 1)
				go func() { done <- f.svc.Work(ctx) }()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("embedding did not start")
				}
				// A second worker cannot duplicate the running embedding.
				require.NoError(t, f.svc.Work(ctx))
				status, err := f.svc.Status(ctx, f.a.User.ID, second.ID)
				require.NoError(t, err)
				require.Equal(t, "processing", status.IndexStatus)
				if remove {
					require.NoError(t, f.svc.Delete(ctx, f.a.User.ID, f.game, "ru", 2))
					_, err = f.svc.Get(ctx, f.a.User.ID, f.game, "ru")
					require.ErrorIs(t, err, repository.ErrNotFound)
				} else {
					f.save(t, f.a, "ru", "Третий актуальный текст", 2)
				}
				close(release)
				waitPrivateWorker(t, done)
				require.NoError(t, f.svc.Work(ctx))
				docs, err := f.store.Search(ctx, scopeOf(second, f.a.User.ID, 2), "query", 3)
				require.NoError(t, err)
				require.Empty(t, docs)
				if remove {
					docs, err = f.store.Search(ctx, scopeOf(first, f.a.User.ID, 1), "query", 3)
					require.NoError(t, err)
					require.Empty(t, docs)
					replacement := f.save(t, f.a, "ru", "Новый документ после удаления", 0)
					require.NotEqual(t, first.ID, replacement.ID)
				} else {
					docs, err = f.svc.Retrieve(ctx, f.a.User.ID, f.game, "ru", "query", 3)
					require.NoError(t, err)
					require.Len(t, docs, 1)
					require.Equal(t, int64(3), docs[0].Scope.Revision)
				}
			})
		}
	}
}
func TestPrivateRulesFreezeCleanupInFlightPostgres(t *testing.T) {
	for _, kind := range []string{"chromem", "pgvector"} {
		t.Run(kind, func(t *testing.T) {
			f := newPrivateRulesFixture(t, kind)
			ctx := context.Background()
			b := f.save(t, f.b, "ru", "Other account survives", 0)
			require.NoError(t, f.svc.Work(ctx))
			a := f.save(t, f.a, "ru", "BLOCK private owner", 0)
			started, release := f.embedder.block("BLOCK")
			work := make(chan error, 1)
			go func() { work <- f.svc.Work(ctx) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("embedding did not start")
			}
			deletion := service.NewAccountDeletionService(repository.NewAccountDeletionRepository(f.pool), mobileTestSecret, f.svc.CleanupOwner)
			accepted, err := deletion.Request(ctx, f.a.User.ID, uuid.New(), "synthetic-password", true)
			require.NoError(t, err)
			docs, err := f.svc.Retrieve(ctx, f.a.User.ID, f.game, "ru", "query", 3)
			require.NoError(t, err)
			require.Empty(t, docs)
			cleanup := make(chan error, 1)
			go func() { cleanup <- deletion.Work(ctx) }()
			close(release)
			waitPrivateWorker(t, work)
			waitPrivateWorker(t, cleanup)
			status, err := deletion.Status(ctx, accepted.RequestID, accepted.Receipt)
			require.NoError(t, err)
			require.Equal(t, "completed", status.Status)
			docs, err = f.store.Search(ctx, scopeOf(a, f.a.User.ID, 1), "query", 3)
			require.NoError(t, err)
			require.Empty(t, docs)
			docs, err = f.store.Search(ctx, scopeOf(b, f.b.User.ID, 1), "query", 3)
			require.NoError(t, err)
			require.Len(t, docs, 1)
			var count int
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_game_rules WHERE user_id=$1`, f.a.User.ID).Scan(&count))
			require.Zero(t, count)
		})
	}
}
func TestPrivateRulesQueryDeleteFencePostgres(t *testing.T) {
	for _, kind := range []string{"chromem", "pgvector"} {
		t.Run(kind, func(t *testing.T) {
			f := newPrivateRulesFixture(t, kind)
			ctx := context.Background()
			f.save(t, f.a, "ru", "Ready private text", 0)
			require.NoError(t, f.svc.Work(ctx))
			started, release := f.embedder.block("probe")
			done := make(chan []service.PrivateDocument, 1)
			errs := make(chan error, 1)
			go func() {
				docs, err := f.svc.Retrieve(ctx, f.a.User.ID, f.game, "ru", "probe", 3)
				done <- docs
				errs <- err
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("query did not start")
			}
			require.NoError(t, f.svc.Delete(ctx, f.a.User.ID, f.game, "ru", 1))
			close(release)
			require.Empty(t, <-done)
			require.NoError(t, <-errs)
		})
	}
}
func TestPrivateRulesHTTPValidationPostgres(t *testing.T) {
	f := newPrivateRulesFixture(t, "chromem")
	input := map[string]string{"content": "Синтетический текст"}
	for _, tc := range []struct {
		method, path, match, none string
		payload                   any
		status                    int
	}{
		{"PUT", f.path("ru"), "", "", input, 428}, {"PUT", f.path("ru"), `"v1"`, "*", input, 400}, {"PUT", f.path("ru"), "", "bogus", input, 400},
		{"PUT", f.path("de"), "", "*", input, 400}, {"GET", f.path("de"), "", "", nil, 400},
		{"PUT", f.path("ru"), "", "*", map[string]string{"content": " "}, 400},
		{"PUT", f.path("ru"), "", "*", map[string]string{"content": strings.Repeat("я", (1<<19)+1)}, 413},
		{"PUT", f.path("ru"), "", "*", map[string]string{"content": "valid", "source": strings.Repeat("я", 501)}, 400},
		{"PUT", f.path("ru"), "", "*", map[string]string{"content": "valid", "user_id": f.b.User.ID.String()}, 400},
	} {
		w := f.callRules(tc.method, tc.path, tc.payload, f.a.AccessToken, tc.match, tc.none, "")
		require.Equal(t, tc.status, w.Code, w.Body.String())
	}
	first := f.save(t, f.a, "ru", "saved", 0)
	require.Equal(t, 412, f.callRules("PUT", f.path("ru"), input, f.a.AccessToken, "", "*", "").Code)
	require.Equal(t, 412, f.callRules("PUT", f.path("ru"), input, f.a.AccessToken, `"v2"`, "", "").Code)
	require.Equal(t, 428, f.callRules("DELETE", f.path("ru"), nil, f.a.AccessToken, "", "", "").Code)
	require.Equal(t, 404, f.callRules("POST", "/rules/"+first.ID.String()+"/reindex", nil, f.b.AccessToken, `"v1"`, "", uuid.NewString()).Code)
	for _, tc := range []struct {
		body, contentType string
		status            int
	}{{"%PDF-1.7", "application/pdf", 415}, {"multipart", "multipart/form-data", 415}, {"{\"content\":\"\xff\"}", "application/json", 400}, {strings.Repeat(" ", 2<<20+1), "application/json", 413}} {
		req := httptest.NewRequest("PUT", f.path("en"), strings.NewReader(tc.body))
		req.Header.Set("Content-Type", tc.contentType)
		req.Header.Set("Authorization", "Bearer "+f.a.AccessToken)
		req.Header.Set("If-None-Match", "*")
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)
		require.Equal(t, tc.status, w.Code, w.Body.String())
	}
}

func TestPrivateRulesRecoveryDimensionsAndConcurrentCreatePostgres(t *testing.T) {
	for _, kind := range []string{"chromem", "pgvector"} {
		t.Run(kind, func(t *testing.T) {
			f := newPrivateRulesFixture(t, kind)
			ctx := context.Background()
			codes := make(chan int, 8)
			var workers sync.WaitGroup
			for i := 0; i < 8; i++ {
				workers.Add(1)
				go func() {
					defer workers.Done()
					codes <- f.callRules("PUT", f.path("ru"), map[string]string{"content": "Concurrent text"}, f.a.AccessToken, "", "*", "").Code
				}()
			}
			workers.Wait()
			close(codes)
			accepted := 0
			for code := range codes {
				if code == 202 {
					accepted++
				} else {
					require.Equal(t, 412, code)
				}
			}
			require.Equal(t, 1, accepted)
			// Simulate process death after durable claim, then recover an expired lease.
			rule, err := f.repo.Claim(ctx, f.a.User.ID)
			require.NoError(t, err)
			_, err = f.pool.Exec(ctx, `UPDATE rules_index_jobs SET lease_until=NOW()-INTERVAL '1 second' WHERE rule_id=$1`, rule.ID)
			require.NoError(t, err)
			require.NoError(t, f.svc.Work(ctx))
			saved, err := f.svc.Get(ctx, f.a.User.ID, f.game, "ru")
			require.NoError(t, err)
			require.Equal(t, "ready", saved.IndexStatus)
			f.embedder.mu.Lock()
			f.embedder.dimension = 4
			f.embedder.mu.Unlock()
			_, err = f.svc.Retrieve(ctx, f.a.User.ID, f.game, "ru", "query changed dimensions", 3)
			require.Error(t, err)
			f.embedder.mu.Lock()
			f.embedder.dimension = 3
			f.embedder.mu.Unlock()
			// Losing collection membership stops reads and superseded jobs are retired.
			f.save(t, f.a, "ru", "Never index after membership removal", 1)
			_, err = f.pool.Exec(ctx, `DELETE FROM collections WHERE user_id=$1 AND game_id=$2`, f.a.User.ID, f.game)
			require.NoError(t, err)
			require.NoError(t, f.svc.Work(ctx))
			docs, err := f.svc.Retrieve(ctx, f.a.User.ID, f.game, "ru", "query", 3)
			require.NoError(t, err)
			require.Empty(t, docs)
			var pending int
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM rules_index_jobs WHERE status='pending'`).Scan(&pending))
			require.Zero(t, pending)
		})
	}
}

type failingPrivateCleanupStore struct {
	service.PrivateVectorStore
	fail bool
}

func (s *failingPrivateCleanupStore) DeleteOwner(ctx context.Context, user uuid.UUID) error {
	if s.fail {
		return errors.New("synthetic cleanup failure")
	}
	return s.PrivateVectorStore.DeleteOwner(ctx, user)
}
func TestPrivateRulesCleanupFailureKeepsReceiptPendingPostgres(t *testing.T) {
	for _, kind := range []string{"chromem", "pgvector"} {
		t.Run(kind, func(t *testing.T) {
			f := newPrivateRulesFixture(t, kind)
			ctx := context.Background()
			rule := f.save(t, f.a, "ru", "Synthetic private document", 0)
			require.NoError(t, f.svc.Work(ctx))
			store := &failingPrivateCleanupStore{f.store, true}
			svc := service.NewPrivateRulesService(f.repo, store)
			deletion := service.NewAccountDeletionService(repository.NewAccountDeletionRepository(f.pool), mobileTestSecret, svc.CleanupOwner)
			accepted, err := deletion.Request(ctx, f.a.User.ID, uuid.New(), "synthetic-password", true)
			require.NoError(t, err)
			require.Error(t, deletion.Work(ctx))
			status, err := deletion.Status(ctx, accepted.RequestID, accepted.Receipt)
			require.NoError(t, err)
			require.Equal(t, "failed", status.Status)
			require.True(t, status.Retryable)
			var count int
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE id=$1`, f.a.User.ID).Scan(&count))
			require.Equal(t, 1, count)
			store.fail = false
			_, err = f.pool.Exec(ctx, `UPDATE account_deletions SET next_attempt_at=NOW() WHERE id=$1`, accepted.RequestID)
			require.NoError(t, err)
			require.NoError(t, deletion.Work(ctx))
			status, err = deletion.Status(ctx, accepted.RequestID, accepted.Receipt)
			require.NoError(t, err)
			require.Equal(t, "completed", status.Status)
			docs, err := f.store.Search(ctx, scopeOf(rule, f.a.User.ID, 1), "query", 3)
			require.NoError(t, err)
			require.Empty(t, docs)
		})
	}
}

func TestPrivateRulesWebAndAPIAdaptersPostgres(t *testing.T) {
	f := newPrivateRulesFixture(t, "chromem")
	ctx := context.Background()
	webFS, err := fs.Sub(tabletop.Web, "web")
	require.NoError(t, err)
	renderer, err := NewRenderer(webFS)
	require.NoError(t, err)
	h := NewPrivateRulesHandler(f.svc, repository.NewCollectionRepository(f.pool), renderer)
	require.NoError(t, repository.NewRulesRepository(f.pool).Upsert(ctx, f.game, "ru", "LEGACY OWNERLESS", "legacy"))
	get := rulesRequest("GET", f.game.String(), f.a.User.ID, nil)
	w := httptest.NewRecorder()
	h.GetRules(w, get)
	require.Equal(t, 200, w.Code)
	require.NotContains(t, w.Body.String(), "LEGACY OWNERLESS")
	body := url.Values{"lang": {"ru"}, "document_lang": {"ru"}, "revision": {"0"}, "content": {"Only A private rules"}}
	post := rulesRequest("POST", f.game.String(), f.a.User.ID, body)
	w = httptest.NewRecorder()
	h.PostRules(w, post)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "Only A private rules")
	require.Contains(t, w.Body.String(), "pending")
	// Both normal and HTMX rendering use exactly the same private ownership.
	for _, htmx := range []bool{false, true} {
		req := rulesRequest("GET", f.game.String(), f.b.User.ID, nil)
		if htmx {
			req.Header.Set("HX-Request", "true")
		}
		w = httptest.NewRecorder()
		h.GetRules(w, req)
		require.Equal(t, 200, w.Code)
		require.NotContains(t, w.Body.String(), "Only A private rules")
	}
	api := f.callRules("GET", f.path("ru"), nil, f.a.AccessToken, "", "", "")
	require.Equal(t, 200, api.Code)
	var rule model.PrivateRule
	require.NoError(t, json.Unmarshal(api.Body.Bytes(), &rule))
	status := f.callRules("GET", "/rules/"+rule.ID.String()+"/index-status", nil, f.a.AccessToken, "", "", "")
	require.Equal(t, 200, status.Code)
	body.Set("revision", "0")
	w = httptest.NewRecorder()
	h.PostRules(w, rulesRequest("POST", f.game.String(), f.a.User.ID, body))
	require.Equal(t, 412, w.Code)
	require.NoError(t, f.svc.Work(ctx))
	// Deletion has a conditional API response and makes the web reader empty.
	deleted := f.callRules("DELETE", f.path("ru"), nil, f.a.AccessToken, `"v1"`, "", "")
	require.Equal(t, 204, deleted.Code)
	require.Empty(t, deleted.Body.String())
	w = httptest.NewRecorder()
	h.GetRules(w, get)
	require.Equal(t, 200, w.Code)
	require.NotContains(t, w.Body.String(), "Only A private rules")
}

func TestPrivateRulesAccountCleanupBothBackendsPostgres(t *testing.T) {
	f := newPrivateRulesFixture(t, "pgvector")
	ctx := context.Background()
	rule := f.save(t, f.a, "ru", "Synthetic rules copied to historical backend", 0)
	require.NoError(t, f.svc.Work(ctx))
	files, err := service.NewPrivateChromemStore(f.root, f.embedder)
	require.NoError(t, err)
	scope := scopeOf(rule, f.a.User.ID, 1)
	require.NoError(t, files.Replace(ctx, scope, []service.PrivateDocument{{ID: "synthetic-history", Content: "Synthetic historical files", Scope: scope}}))
	svc := service.NewPrivateRulesService(f.repo, f.store, files)
	deletion := service.NewAccountDeletionService(repository.NewAccountDeletionRepository(f.pool), mobileTestSecret, svc.CleanupOwner)
	accepted, err := deletion.Request(ctx, f.a.User.ID, uuid.New(), "synthetic-password", true)
	require.NoError(t, err)
	require.NoError(t, deletion.Work(ctx))
	status, err := deletion.Status(ctx, accepted.RequestID, accepted.Receipt)
	require.NoError(t, err)
	require.Equal(t, "completed", status.Status)
	for _, store := range []service.PrivateVectorStore{files, f.store} {
		docs, err := store.Search(ctx, scope, "query", 3)
		require.NoError(t, err)
		require.Empty(t, docs)
	}
}

func TestPrivateRulesMigrationsRoundTripPostgres(t *testing.T) {
	f := newPrivateRulesFixture(t, "pgvector")
	ctx := context.Background()
	require.NoError(t, repository.NewRulesRepository(f.pool).Upsert(ctx, f.game, "ru", "Legacy data retained", "legacy"))
	migrations, err := fs.Sub(tabletop.Migrations, "migrations")
	require.NoError(t, err)
	// Also create the real legacy vector table to prove rollback does not drop it.
	require.NoError(t, repository.RunMigrations(ctx, f.pool, migrations))
	for _, name := range []string{"008_private_pgvector.down.sql", "007_private_rules.down.sql"} {
		sql, err := fs.ReadFile(migrations, name)
		require.NoError(t, err)
		_, err = f.pool.Exec(ctx, string(sql))
		require.NoError(t, err)
	}
	var retained bool
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT to_regclass('game_rules') IS NOT NULL AND to_regclass('rules_chunks') IS NOT NULL AND to_regclass('user_game_rules') IS NULL AND to_regclass('private_rules_chunks') IS NULL`).Scan(&retained))
	require.True(t, retained)
	legacy, err := repository.NewRulesRepository(f.pool).Get(ctx, f.game, "ru")
	require.NoError(t, err)
	require.Equal(t, "Legacy data retained", legacy.Content)
	_, err = f.pool.Exec(ctx, `DELETE FROM schema_migrations WHERE filename IN ('007_private_rules.up.sql','008_private_pgvector.up.sql')`)
	require.NoError(t, err)
	require.NoError(t, repository.RunMigrations(ctx, f.pool, migrations))
	f.save(t, f.a, "ru", "Private data after rollback/reapply", 0)
	require.NoError(t, f.svc.Work(ctx))
}
