package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	tabletop "github.com/ostkost/avari-tabletop-backend"
	"github.com/ostkost/avari-tabletop-backend/internal/bgg"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
	"github.com/stretchr/testify/require"
)

type collectionFixture struct {
	*mobileFixture
	collection              repository.MobileCollectionRepository
	svc                     *service.MobileCollectionService
	searchCalls, thingCalls atomic.Int32
	provider                *httptest.Server
}

func newCollectionFixture(t *testing.T) *collectionFixture {
	f := &collectionFixture{mobileFixture: newMobileFixture(t)}
	f.provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			f.searchCalls.Add(1)
			if r.URL.Query().Get("query") == "failure" {
				w.WriteHeader(503)
				return
			}
			count := 3
			if r.URL.Query().Get("query") == "truncate" {
				count = 101
			}
			fmt.Fprint(w, `<items>`)
			for i := 1; i <= count; i++ {
				fmt.Fprintf(w, `<item id="%d"><name type="primary" value="Игра %d"/><yearpublished value="2020"/></item>`, i, i)
			}
			fmt.Fprint(w, `</items>`)
		case "/thing":
			f.thingCalls.Add(1)
			id := r.URL.Query().Get("id")
			if id == "404" {
				fmt.Fprint(w, `<items/>`)
				return
			}
			if id == "502" {
				w.WriteHeader(503)
				return
			}
			fmt.Fprintf(w, `<items><item id="%s"><name type="primary" value="Игра %s"/><description>Описание</description><yearpublished value="2020"/><minplayers value="1"/><maxplayers value="4"/><playingtime value="60"/><statistics><ratings><average value="7.5"/></ratings></statistics></item></items>`, id, id)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.provider.Close)
	f.collection = repository.NewMobileCollectionRepository(f.pool)
	f.svc = service.NewMobileCollectionService(f.collection, service.NewGameService(repository.NewGameRepository(f.pool), bgg.NewClientWithBaseURL(f.provider.URL)), mobileTestSecret)
	f.router = NewMobileAuthHandler(f.mobile, f.svc).Router()
	return f
}
func (f *collectionFixture) request(method, path string, payload any, access string, headers map[string]string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	if access != "" {
		req.Header.Set("Authorization", "Bearer "+access)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}
func (f *collectionFixture) add(t *testing.T, s service.MobileAuthSession, id int) model.CollectionEntryDTO {
	t.Helper()
	rec := f.request("POST", "/collection", map[string]any{"bgg_id": id}, s.AccessToken, map[string]string{"Idempotency-Key": uuid.NewString()})
	require.Equal(t, 201, rec.Code)
	var e model.CollectionEntryDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e))
	return e
}
func decodeCollectionPage(t *testing.T, rec *httptest.ResponseRecorder) service.CollectionPageDTO {
	t.Helper()
	require.Equal(t, 200, rec.Code)
	var p service.CollectionPageDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	return p
}
func TestMobileCollectionLifecyclePostgres(t *testing.T) {
	f := newCollectionFixture(t)
	s := f.register(t)
	require.Equal(t, 401, f.call("GET", "/collection", nil, "").Code)
	page := decodeCollectionPage(t, f.call("GET", "/collection", nil, s.AccessToken))
	require.Empty(t, page.Items)
	require.Contains(t, f.call("GET", "/collection", nil, s.AccessToken).Body.String(), `"items":[]`)
	e := f.add(t, s, 1)
	require.Equal(t, model.StatusOwned, e.Status)
	require.Nil(t, e.Rating)
	require.NotZero(t, e.Version)
	require.NotZero(t, e.UpdatedAt)
	path := "/collection/" + e.GameID.String()
	get := f.call("GET", path, nil, s.AccessToken)
	require.Equal(t, collectionETag(e), get.Header().Get("ETag"))
	require.NotContains(t, get.Body.String(), "user_id")
	require.NotContains(t, get.Body.String(), "description")
	require.Equal(t, 428, f.request("PATCH", path, map[string]any{"rating": 8}, s.AccessToken, nil).Code)
	headers := map[string]string{"If-Match": collectionETag(e)}
	updated := f.request("PATCH", path, map[string]any{"rating": 8, "notes": "Привет\nмир", "status": "played"}, s.AccessToken, headers)
	require.Equal(t, 200, updated.Code)
	var current model.CollectionEntryDTO
	require.NoError(t, json.Unmarshal(updated.Body.Bytes(), &current))
	require.Greater(t, current.Version, e.Version)
	require.Equal(t, "Привет\nмир", current.Notes)
	require.Equal(t, 8, *current.Rating)
	stale := f.request("PATCH", path, map[string]any{"notes": "lost"}, s.AccessToken, headers)
	require.Equal(t, 412, stale.Code)
	require.Contains(t, stale.Body.String(), `"current"`)
	require.Equal(t, collectionETag(current), stale.Header().Get("ETag"))
	require.Equal(t, 412, f.request("DELETE", path, nil, s.AccessToken, headers).Code)
	clear := f.request("PATCH", path, map[string]any{"rating": nil, "notes": ""}, s.AccessToken, map[string]string{"If-Match": collectionETag(current)})
	require.Equal(t, 200, clear.Code)
	require.NoError(t, json.Unmarshal(clear.Body.Bytes(), &current))
	require.Nil(t, current.Rating)
	require.Empty(t, current.Notes)
	require.Equal(t, model.StatusPlayed, current.Status)
	// Existing web update hits the shared trigger; its version invalidates API ETags.
	web, err := repository.NewCollectionRepository(f.pool).Update(context.Background(), s.User.ID, e.GameID, model.StatusWishlist, nil, "Из веба")
	require.NoError(t, err)
	require.Greater(t, web.Version, current.Version)
	require.Equal(t, 412, f.request("PATCH", path, map[string]any{"notes": "lost"}, s.AccessToken, map[string]string{"If-Match": collectionETag(current)}).Code)
	current, err = f.collection.Get(context.Background(), s.User.ID, e.GameID)
	require.NoError(t, err)
	del := f.request("DELETE", path, nil, s.AccessToken, map[string]string{"If-Match": collectionETag(current)})
	require.Equal(t, 204, del.Code)
	require.Empty(t, del.Body.Bytes())
	require.Equal(t, 404, f.call("GET", path, nil, s.AccessToken).Code)
	readded := f.add(t, s, 1)
	require.Greater(t, readded.Version, current.Version, "re-add must not reuse a previous ETag")
	require.Equal(t, 412, f.request("DELETE", path, nil, s.AccessToken, map[string]string{"If-Match": collectionETag(current)}).Code)
}
func TestMobileCollectionIdempotencyConcurrentPostgres(t *testing.T) {
	f := newCollectionFixture(t)
	s := f.register(t)
	key := uuid.NewString()
	headers := map[string]string{"Idempotency-Key": key}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.request("POST", "/collection", map[string]any{"bgg_id": 1}, s.AccessToken, headers)
		}()
	}
	wg.Wait()
	close(results)
	var canonical string
	for rec := range results {
		require.Equal(t, 201, rec.Code)
		if canonical == "" {
			canonical = rec.Body.String()
		} else {
			require.Equal(t, canonical, rec.Body.String())
		}
	}
	require.EqualValues(t, 1, f.thingCalls.Load())
	mismatch := f.request("POST", "/collection", map[string]any{"bgg_id": 2}, s.AccessToken, headers)
	require.Equal(t, 409, mismatch.Code)
	require.Contains(t, mismatch.Body.String(), "idempotency_mismatch")
	var entry model.CollectionEntryDTO
	require.NoError(t, json.Unmarshal([]byte(canonical), &entry))
	updated := f.request("PATCH", "/collection/"+entry.GameID.String(), map[string]any{"notes": "new"}, s.AccessToken, map[string]string{"If-Match": collectionETag(entry)})
	require.Equal(t, 200, updated.Code)
	replay := f.request("POST", "/collection", map[string]any{"bgg_id": 1, "status": "owned"}, s.AccessToken, headers)
	require.Equal(t, canonical, replay.Body.String(), "default status and explicit owned are equivalent")
	dup := f.request("POST", "/collection", map[string]any{"bgg_id": 1}, s.AccessToken, map[string]string{"Idempotency-Key": "11111111-1111-4111-8111-111111111111"})
	require.Equal(t, 409, dup.Code)
	require.Contains(t, dup.Body.String(), "already_in_collection")
	require.Contains(t, dup.Body.String(), `"notes":"new"`)
	dupReplay := f.request("POST", "/collection", map[string]any{"bgg_id": 1}, s.AccessToken, map[string]string{"Idempotency-Key": "11111111-1111-4111-8111-111111111111"})
	require.Equal(t, dup.Body.String(), dupReplay.Body.String())
	_, err := f.pool.Exec(context.Background(), `UPDATE collection_add_replays SET expires_at=NOW()-INTERVAL '1 second'`)
	require.NoError(t, err)
	require.NoError(t, f.svc.Cleanup(context.Background()))
	var count int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM collection_add_replays`).Scan(&count))
	require.Zero(t, count)
	// An expired key can start a new operation with another body.
	require.Equal(t, 201, f.request("POST", "/collection", map[string]any{"bgg_id": 2}, s.AccessToken, headers).Code)
}
func TestMobileCollectionScopeAndSearchPostgres(t *testing.T) {
	f := newCollectionFixture(t)
	s := f.register(t)
	e := f.add(t, s, 1)
	otherUser, err := f.auth.Register(context.Background(), "other@example.invalid", "Другой", "synthetic-password")
	require.NoError(t, err)
	other, err := f.mobile.Login(context.Background(), otherUser.Email, "synthetic-password")
	require.NoError(t, err)
	page := decodeCollectionPage(t, f.call("GET", "/collection", nil, other.AccessToken))
	require.Empty(t, page.Items)
	require.Equal(t, 404, f.call("GET", "/collection/"+e.GameID.String(), nil, other.AccessToken).Code)
	require.Equal(t, 404, f.request("PATCH", "/collection/"+e.GameID.String(), map[string]any{"notes": "foreign"}, other.AccessToken, map[string]string{"If-Match": collectionETag(e)}).Code)
	search := f.call("GET", "/games/search?q="+url.QueryEscape("  Игра  "), nil, s.AccessToken)
	require.Equal(t, 200, search.Code)
	var found service.SearchPageDTO
	require.NoError(t, json.Unmarshal(search.Body.Bytes(), &found))
	require.Len(t, found.Items, 3)
	require.True(t, found.Items[0].InCollection)
	require.Equal(t, e.GameID, *found.Items[0].GameID)
	require.False(t, found.Items[1].InCollection)
	require.Nil(t, found.Items[1].GameID)
	require.EqualValues(t, 1, f.searchCalls.Load())
	require.EqualValues(t, 1, f.thingCalls.Load(), "search must not fetch details")
	otherSearch := f.call("GET", "/games/search?q=games", nil, other.AccessToken)
	require.NoError(t, json.Unmarshal(otherSearch.Body.Bytes(), &found))
	require.False(t, found.Items[0].InCollection)
	truncated := f.call("GET", "/games/search?q=truncate", nil, s.AccessToken)
	require.NoError(t, json.Unmarshal(truncated.Body.Bytes(), &found))
	require.Len(t, found.Items, 100)
	require.True(t, found.Truncated)
	require.Equal(t, 400, f.call("GET", "/games/search?q="+url.QueryEscape("Я"), nil, s.AccessToken).Code)
	detail := f.call("GET", "/games/bgg/1", nil, other.AccessToken)
	require.Equal(t, 200, detail.Code)
	require.Contains(t, detail.Body.String(), `"collection":null`)
	require.Equal(t, 404, f.call("GET", "/games/bgg/404", nil, s.AccessToken).Code)
	require.Equal(t, 502, f.call("GET", "/games/bgg/502", nil, s.AccessToken).Code)
	require.Equal(t, 502, f.call("GET", "/games/search?q=failure", nil, s.AccessToken).Code)
	// Membership read failure must never promise false membership.
	_, err = f.pool.Exec(context.Background(), `ALTER TABLE collections RENAME TO unavailable_collections`)
	require.NoError(t, err)
	require.Equal(t, 503, f.call("GET", "/games/search?q=games", nil, s.AccessToken).Code)
	_, err = f.pool.Exec(context.Background(), `ALTER TABLE unavailable_collections RENAME TO collections`)
	require.NoError(t, err)
	// A stale cached detail is explicitly flagged when BGG is unavailable.
	_, err = f.pool.Exec(context.Background(), `UPDATE games SET fetched_at=NOW()-INTERVAL '8 days' WHERE id=$1`, e.GameID)
	require.NoError(t, err)
	f.provider.Close()
	stale := f.call("GET", "/games/bgg/1", nil, s.AccessToken)
	require.Equal(t, 200, stale.Code)
	require.Contains(t, stale.Body.String(), `"stale":true`)

}
func TestMobileCollectionSQLPaginationPostgres(t *testing.T) {
	f := newCollectionFixture(t)
	s := f.register(t)
	names := []string{"Ящик", "ёжик", "Билет", "Билет", "100%_игра"}
	for i, name := range names {
		g, err := repository.NewGameRepository(f.pool).Upsert(context.Background(), model.Game{BGGID: i + 1, Name: name})
		require.NoError(t, err)
		c, err := repository.NewCollectionRepository(f.pool).Add(context.Background(), s.User.ID, g.ID, model.StatusOwned)
		require.NoError(t, err)
		if i == 0 || i == 2 {
			rating := 9
			if i == 0 {
				rating = 3
			}
			_, err = repository.NewCollectionRepository(f.pool).Update(context.Background(), s.User.ID, g.ID, model.StatusPlayed, &rating, "")
			require.NoError(t, err)
		}
		_, err = f.pool.Exec(context.Background(), `UPDATE collections SET added_at='2026-01-01T00:00:00Z' WHERE id=$1`, c.ID)
		require.NoError(t, err)
	}
	all := decodeCollectionPage(t, f.call("GET", "/collection?sort=name", nil, s.AccessToken))
	require.Len(t, all.Items, 5)
	require.Equal(t, "100%_игра", all.Items[0].Game.Name)
	require.Equal(t, "Билет", all.Items[1].Game.Name)
	require.Equal(t, "Ящик", all.Items[3].Game.Name)
	// Frozen fixture ties use the UUID order used by Go's web Browse.
	web, err := service.NewCollectionService(repository.NewCollectionRepository(f.pool)).Browse(context.Background(), s.User.ID, service.CollectionBrowseOptions{Sort: "name"})
	require.NoError(t, err)
	for i, e := range all.Items {
		require.Equal(t, web[i].GameID, e.GameID)
	}
	cursor := ""
	var ids []uuid.UUID
	for i := 0; i < 3; i++ {
		path := "/collection?sort=name&limit=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		p := decodeCollectionPage(t, f.call("GET", path, nil, s.AccessToken))
		for _, e := range p.Items {
			ids = append(ids, e.GameID)
		}
		if p.NextCursor == nil {
			break
		}
		cursor = *p.NextCursor
	}
	require.Len(t, ids, 5)
	for i := range ids {
		require.Equal(t, all.Items[i].GameID, ids[i])
	}
	first := decodeCollectionPage(t, f.call("GET", "/collection?sort=name&limit=2", nil, s.AccessToken))
	require.NotNil(t, first.NextCursor)
	for _, path := range []string{"/collection?sort=rating&limit=2&cursor=", "/collection?sort=name&limit=3&cursor=", "/collection?q=other&sort=name&limit=2&cursor="} {
		require.Equal(t, 400, f.call("GET", path+url.QueryEscape(*first.NextCursor), nil, s.AccessToken).Code)
	}
	require.Equal(t, 400, f.call("GET", "/collection?sort=name&limit=2&cursor="+url.QueryEscape(*first.NextCursor+"x"), nil, s.AccessToken).Code)
	for _, q := range []string{"БИЛ", "ЁЖ", "%_"} {
		p := decodeCollectionPage(t, f.call("GET", "/collection?q="+url.QueryEscape(q), nil, s.AccessToken))
		require.NotEmpty(t, p.Items)
	}
	rated := decodeCollectionPage(t, f.call("GET", "/collection?sort=rating", nil, s.AccessToken))
	require.Equal(t, 9, *rated.Items[0].Rating)
	require.Equal(t, 3, *rated.Items[1].Rating)
	require.Nil(t, rated.Items[2].Rating)
	filtered := decodeCollectionPage(t, f.call("GET", "/collection?status=played", nil, s.AccessToken))
	require.Len(t, filtered.Items, 2)
	for _, q := range []string{"limit=0", "limit=101", "limit=wrong", "sort=invalid", "status=invalid"} {
		require.Equal(t, 400, f.call("GET", "/collection?"+q, nil, s.AccessToken).Code)
	}
	// Cursor binding applies even when another account has matching filters.
	u, err := f.auth.Register(context.Background(), "cursor@example.invalid", "Cursor", "synthetic-password")
	require.NoError(t, err)
	other, err := f.mobile.Login(context.Background(), u.Email, "synthetic-password")
	require.NoError(t, err)
	require.Equal(t, 400, f.call("GET", "/collection?sort=name&limit=2&cursor="+url.QueryEscape(*first.NextCursor), nil, other.AccessToken).Code)
}
func TestMobileCollectionSnapshotImmutablePostgres(t *testing.T) {
	f := newCollectionFixture(t)
	s := f.register(t)
	f.add(t, s, 1)
	second := f.add(t, s, 2)
	rec := f.call("GET", "/collection/snapshot?limit=1", nil, s.AccessToken)
	require.Equal(t, 200, rec.Code)
	var snapshot service.CollectionSnapshotDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snapshot))
	require.False(t, snapshot.Complete)
	require.NotNil(t, snapshot.NextCursor)
	require.WithinDuration(t, snapshot.CapturedAt.Add(5*time.Minute), snapshot.ExpiresAt, time.Millisecond)
	expected, err := f.collection.Snapshot(context.Background(), s.User.ID, snapshot.SnapshotID)
	require.NoError(t, err)
	// Change and delete rows after capture, add an unrelated row.
	_, err = repository.NewCollectionRepository(f.pool).Update(context.Background(), s.User.ID, expected.Items[1].GameID, model.StatusPlayed, nil, "changed after capture")
	require.NoError(t, err)
	require.NoError(t, repository.NewCollectionRepository(f.pool).Remove(context.Background(), s.User.ID, second.GameID))
	f.add(t, s, 3)
	next := f.call("GET", "/collection/snapshot?limit=1&cursor="+url.QueryEscape(*snapshot.NextCursor), nil, s.AccessToken)
	require.Equal(t, 200, next.Code)
	var final service.CollectionSnapshotDTO
	require.NoError(t, json.Unmarshal(next.Body.Bytes(), &final))
	require.True(t, final.Complete)
	require.Nil(t, final.NextCursor)
	require.Equal(t, expected.Items[1], final.Items[0])
	require.Equal(t, snapshot.SnapshotID, final.SnapshotID)
	_, err = f.pool.Exec(context.Background(), `UPDATE collection_snapshots SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, snapshot.SnapshotID)
	require.NoError(t, err)
	require.Equal(t, 410, f.call("GET", "/collection/snapshot?limit=1&cursor="+url.QueryEscape(*snapshot.NextCursor), nil, s.AccessToken).Code)
	require.NoError(t, f.svc.Cleanup(context.Background()))
	require.Equal(t, 410, f.call("GET", "/collection/snapshot?limit=1&cursor="+url.QueryEscape(*snapshot.NextCursor), nil, s.AccessToken).Code)
	require.Equal(t, 400, f.call("GET", "/collection/snapshot?status=owned", nil, s.AccessToken).Code)
}
func TestMobileCollectionValidationAndConcurrentPatchPostgres(t *testing.T) {
	f := newCollectionFixture(t)
	s := f.register(t)
	e := f.add(t, s, 1)
	path := "/collection/" + e.GameID.String()
	headers := map[string]string{"If-Match": collectionETag(e)}
	for _, p := range []any{map[string]any{}, map[string]any{"status": nil}, map[string]any{"notes": nil}, map[string]any{"rating": 0}, map[string]any{"rating": 1.5}, map[string]any{"notes": strings.Repeat("Я", 10001)}, map[string]any{"user_id": uuid.NewString()}} {
		require.Equal(t, 400, f.request("PATCH", path, p, s.AccessToken, headers).Code)
	}
	for _, tag := range []string{"*", "v1", `W/"v1"`, `"v01"`, `"v-1"`, `"v1", "v2"`} {
		require.Equal(t, 400, f.request("DELETE", path, nil, s.AccessToken, map[string]string{"If-Match": tag}).Code)
	}
	require.Equal(t, 400, f.request("POST", "/collection", map[string]any{"bgg_id": 1}, s.AccessToken, nil).Code)
	require.Equal(t, 400, f.request("POST", "/collection", map[string]any{"bgg_id": 1, "status": nil}, s.AccessToken, map[string]string{"Idempotency-Key": uuid.NewString()}).Code)
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.request("PATCH", path, map[string]any{"notes": strings.Repeat("Я", 10000)}, s.AccessToken, headers).Code
		}()
	}
	wg.Wait()
	close(results)
	success, stale := 0, 0
	for code := range results {
		if code == 200 {
			success++
		} else if code == 412 {
			stale++
		} else {
			t.Errorf("unexpected status %d", code)
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 7, stale)
}
func TestMobileCollectionMigrationRoundtripPostgres(t *testing.T) {
	f := newCollectionFixture(t)
	s := f.register(t)
	e := f.add(t, s, 1)
	down, err := tabletop.Migrations.ReadFile("migrations/005_mobile_collection.down.sql")
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), string(down))
	require.NoError(t, err)
	var count int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM collections WHERE id=$1`, e.ID).Scan(&count))
	require.Equal(t, 1, count)
	up, err := tabletop.Migrations.ReadFile("migrations/005_mobile_collection.up.sql")
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), string(up))
	require.NoError(t, err)
	current, err := f.collection.Get(context.Background(), s.User.ID, e.GameID)
	require.NoError(t, err)
	require.Equal(t, e.GameID, current.GameID)
	require.NotZero(t, current.Version)
}

// Export only synthetic collection/game responses, never sessions or credentials.
// The mobile repository validates these against its pinned full OpenAPI schema.
func TestMobileCollectionContractPostgres(t *testing.T) {
	f := newCollectionFixture(t)
	session := f.register(t)
	type sample struct {
		Path    string            `json:"path"`
		Method  string            `json:"method"`
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body,omitempty"`
	}
	var samples []sample
	record := func(method, path, target string, payload any, headers map[string]string, status int) *httptest.ResponseRecorder {
		t.Helper()
		rec := f.request(method, target, payload, session.AccessToken, headers)
		require.Equal(t, status, rec.Code)
		require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		require.Empty(t, rec.Header().Values("Set-Cookie"))
		h := map[string]string{"Content-Type": rec.Header().Get("Content-Type"), "Cache-Control": rec.Header().Get("Cache-Control")}
		if v := rec.Header().Get("ETag"); v != "" {
			h["ETag"] = v
		}
		sample := sample{Path: path, Method: method, Status: status, Headers: h}
		if status == 204 {
			require.Empty(t, rec.Body.Bytes())
		} else {
			require.True(t, json.Valid(rec.Body.Bytes()))
			sample.Body = append(json.RawMessage(nil), rec.Body.Bytes()...)
		}
		samples = append(samples, sample)
		return rec
	}
	record("GET", "/collection", "/collection", nil, nil, 200)
	record("GET", "/games/search", "/games/search?q="+url.QueryEscape("Билет"), nil, nil, 200)
	record("GET", "/games/bgg/{bggId}", "/games/bgg/1", nil, nil, 200)
	added := record("POST", "/collection", "/collection", map[string]any{"bgg_id": 1}, map[string]string{"Idempotency-Key": uuid.NewString()}, 201)
	var entry model.CollectionEntryDTO
	require.NoError(t, json.Unmarshal(added.Body.Bytes(), &entry))
	target := "/collection/" + entry.GameID.String()
	record("GET", "/collection/{gameId}", target, nil, nil, 200)
	record("PATCH", "/collection/{gameId}", target, map[string]any{"rating": 8, "notes": "Кириллица\nи перенос", "status": "played"}, map[string]string{"If-Match": collectionETag(entry)}, 200)
	record("PATCH", "/collection/{gameId}", target, map[string]any{"rating": 1}, map[string]string{"If-Match": collectionETag(entry)}, 412)
	record("PATCH", "/collection/{gameId}", target, map[string]any{"rating": 1}, nil, 428)
	record("POST", "/collection", "/collection", map[string]any{"bgg_id": 1}, map[string]string{"Idempotency-Key": uuid.NewString()}, 409)
	record("GET", "/collection", "/collection?sort=rating", nil, nil, 200)
	record("GET", "/collection/snapshot", "/collection/snapshot", nil, nil, 200)
	current, err := f.collection.Get(context.Background(), session.User.ID, entry.GameID)
	require.NoError(t, err)
	record("PATCH", "/collection/{gameId}", target, map[string]any{"rating": nil}, map[string]string{"If-Match": collectionETag(current)}, 200)
	current, err = f.collection.Get(context.Background(), session.User.ID, entry.GameID)
	require.NoError(t, err)
	record("DELETE", "/collection/{gameId}", target, nil, map[string]string{"If-Match": collectionETag(current)}, 204)
	record("GET", "/collection/{gameId}", target, nil, nil, 404)
	if dir := os.Getenv("B02_CONTRACT_DIR"); dir != "" {
		require.True(t, filepath.IsAbs(dir), "contract output must be an absolute scratch path")
		require.NoError(t, os.MkdirAll(dir, 0700))
		data, err := json.MarshalIndent(samples, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "responses.json"), data, 0600))
	}
}

func TestMobileCollectionSingleConnectionPostgres(t *testing.T) {
	f := newCollectionFixture(t)
	session := f.register(t)
	cfg := f.pool.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	defer pool.Close()
	svc := service.NewMobileCollectionService(repository.NewMobileCollectionRepository(pool), service.NewGameService(repository.NewGameRepository(pool), bgg.NewClientWithBaseURL(f.provider.URL)), mobileTestSecret)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := svc.Add(ctx, session.User.ID, uuid.New(), 13, model.StatusOwned)
	require.NoError(t, err)
	require.Equal(t, 201, result.Status)
}
func TestMobileCollectionSnapshotQuotaAndBindingsPostgres(t *testing.T) {
	f := newCollectionFixture(t)
	session := f.register(t)
	f.add(t, session, 1)
	f.add(t, session, 2)
	first, err := f.svc.Snapshot(context.Background(), session.User.ID, 1, "")
	require.NoError(t, err)
	require.NotNil(t, first.NextCursor)
	_, err = f.svc.Snapshot(context.Background(), uuid.New(), 1, *first.NextCursor)
	require.ErrorIs(t, err, service.ErrCollectionInput)
	_, err = f.svc.Snapshot(context.Background(), session.User.ID, 2, *first.NextCursor)
	require.ErrorIs(t, err, service.ErrCollectionInput)
	var wg sync.WaitGroup
	errors := make(chan error, 15)
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.Snapshot(context.Background(), session.User.ID, 1, "")
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	successful, limited := 0, 0
	for err := range errors {
		if err == nil {
			successful++
		} else {
			require.ErrorIs(t, err, repository.ErrSnapshotQuota)
			limited++
		}
	}
	require.Equal(t, 9, successful)
	require.Equal(t, 6, limited)
	// A new service instance verifies the signed cursor and loads the durable page.
	svc := service.NewMobileCollectionService(repository.NewMobileCollectionRepository(f.pool), service.NewGameService(repository.NewGameRepository(f.pool), bgg.NewClientWithBaseURL(f.provider.URL)), mobileTestSecret)
	last, err := svc.Snapshot(context.Background(), session.User.ID, 1, *first.NextCursor)
	require.NoError(t, err)
	require.True(t, last.Complete)
}
func TestMobileCollectionProviderTimeoutPostgres(t *testing.T) {
	f := newCollectionFixture(t)
	session := f.register(t)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer slow.Close()
	svc := service.NewMobileCollectionService(f.collection, service.NewGameService(repository.NewGameRepository(f.pool), bgg.NewClientWithBaseURL(slow.URL)), mobileTestSecret)
	handler := NewMobileAuthHandler(f.mobile, svc).Router()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", "/games/search?q=timeout", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, 504, rec.Code)
	require.NotContains(t, rec.Body.String(), slow.URL)
}
