package handler

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	tabletopai "github.com/ostkost/avari-tabletop-backend"
	"github.com/ostkost/avari-tabletop-backend/internal/bgg"
	"github.com/ostkost/avari-tabletop-backend/internal/middleware"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type searchGameRepo struct {
	repository.GameRepository
	reads, writes int
}

func (r *searchGameRepo) GetByBGGID(context.Context, int) (model.Game, error) {
	r.reads++
	return model.Game{}, repository.ErrNotFound
}

func (r *searchGameRepo) Upsert(_ context.Context, game model.Game) (model.Game, error) {
	r.writes++
	return game, nil
}

type searchCollectionRepo struct {
	repository.CollectionRepository
	entries        []model.CollectionWithGame
	err            error
	lists, lookups int
	user           uuid.UUID
	status         model.CollectionStatus
}

func (r *searchCollectionRepo) ListByUser(_ context.Context, user uuid.UUID, status model.CollectionStatus) ([]model.CollectionWithGame, error) {
	r.lists++
	r.user, r.status = user, status
	var entries []model.CollectionWithGame
	for _, entry := range r.entries {
		if entry.UserID == user {
			entries = append(entries, entry)
		}
	}
	return entries, r.err
}

func (r *searchCollectionRepo) GetByUserAndGame(context.Context, uuid.UUID, uuid.UUID) (model.Collection, error) {
	r.lookups++
	return model.Collection{}, repository.ErrNotFound
}

func searchEmbeddedRenderer(t *testing.T) *Renderer {
	t.Helper()
	webFS, err := fs.Sub(tabletopai.Web, "web")
	require.NoError(t, err)
	renderer, err := NewRenderer(webFS)
	require.NoError(t, err)
	return renderer
}

func TestGameSearchCollectionMarkers(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(map[bool]string{false: "page", true: "htmx"}[htmx], func(t *testing.T) {
			user, otherUser := uuid.New(), uuid.New()
			gameID := uuid.New()
			games := &searchGameRepo{}
			collections := &searchCollectionRepo{entries: []model.CollectionWithGame{
				{Collection: model.Collection{UserID: user, GameID: gameID}, Game: model.Game{ID: gameID, BGGID: 13}},
				{Collection: model.Collection{UserID: otherUser}, Game: model.Game{ID: uuid.New(), BGGID: 42}},
			}}
			var searches, details atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/search" {
					details.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				searches.Add(1)
				assert.Equal(t, "Катан", r.URL.Query().Get("query"))
				_, _ = w.Write([]byte(`<items><item id="13"><name type="primary" value="Catan"/></item><item id="42"><name type="primary" value="&lt;script&gt;alert(1)&lt;/script&gt;"/></item></items>`))
			}))
			defer srv.Close()
			h := NewGameHandler(service.NewGameService(games, bgg.NewClientWithBaseURL(srv.URL)), service.NewCollectionService(collections), searchEmbeddedRenderer(t))
			req := httptest.NewRequest(http.MethodGet, "/games/search?q="+url.QueryEscape("  Катан  "), nil)
			req = req.WithContext(middleware.ContextWithUserID(req.Context(), user))
			if htmx {
				req.Header.Set("HX-Request", "true")
			}
			w := httptest.NewRecorder()
			h.GetSearch(w, req)
			require.Equal(t, http.StatusOK, w.Code)
			body := w.Body.String()
			assert.Equal(t, int32(1), searches.Load())
			assert.Zero(t, details.Load())
			assert.Zero(t, games.reads)
			assert.Zero(t, games.writes)
			assert.Zero(t, collections.lookups)
			assert.Equal(t, 1, collections.lists)
			assert.Equal(t, user, collections.user)
			assert.Empty(t, collections.status)
			assert.Equal(t, 1, strings.Count(body, "✓ Added"))
			assert.Equal(t, 1, strings.Count(body, "+ Add"))
			assert.Contains(t, body, `"bgg_id": "42"`)
			assert.NotContains(t, body, `"bgg_id": "13"`)
			assert.Contains(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;")
			assert.NotContains(t, body, "<script>alert(1)</script>")
			if htmx {
				assert.NotContains(t, body, "<!DOCTYPE html>")
			} else {
				assert.Contains(t, body, "<!DOCTYPE html>")
				assert.Contains(t, body, `value="Катан"`)
			}
		})
	}
}

func TestGameSearchFailuresAndSkippedReads(t *testing.T) {
	for _, tc := range []struct {
		name, query, response, expected string
		status                          int
		anonymous                       bool
		collectionErr                   error
		searches, lists                 int
	}{
		{name: "empty", query: "  ", expected: "Type a game name", status: 200},
		{name: "one rune", query: "Я", expected: "Search failed", status: 200},
		{name: "empty results", query: "zz", response: `<items/>`, expected: "No games found", status: 200, searches: 1},
		{name: "anonymous", query: "ca", response: `<items><item id="13"><name value="Catan"/></item></items>`, expected: "+ Add", anonymous: true, status: 200, searches: 1},
		{name: "BGG failure", query: "ca", expected: "Search failed", status: 503, searches: 1},
		{name: "bad XML", query: "ca", response: `<items>`, expected: "Search failed", status: 200, searches: 1},
		{name: "collection failure", query: "ca", response: `<items><item id="13"><name value="Catan"/></item></items>`, expected: "+ Add", status: 200, collectionErr: errors.New("db unavailable"), searches: 1, lists: 1},
		{name: "query escaping", query: `<img src=x onerror=alert(1)>`, response: `<items/>`, expected: "&lt;img src=x onerror=alert(1)&gt;", status: 200, searches: 1},
	} {
		for _, htmx := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/page", true: "/htmx"}[htmx], func(t *testing.T) {
				var searches, details atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/search" {
						searches.Add(1)
					} else {
						details.Add(1)
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.response))
				}))
				defer srv.Close()
				games := &searchGameRepo{}
				collections := &searchCollectionRepo{err: tc.collectionErr}
				h := NewGameHandler(service.NewGameService(games, bgg.NewClientWithBaseURL(srv.URL)), service.NewCollectionService(collections), searchEmbeddedRenderer(t))
				req := httptest.NewRequest(http.MethodGet, "/games/search?q="+url.QueryEscape(tc.query), nil)
				if !tc.anonymous {
					req = req.WithContext(middleware.ContextWithUserID(req.Context(), uuid.New()))
				}
				if htmx {
					req.Header.Set("HX-Request", "true")
				}
				w := httptest.NewRecorder()
				h.GetSearch(w, req)
				assert.Equal(t, http.StatusOK, w.Code)
				assert.Contains(t, w.Body.String(), tc.expected)
				assert.Equal(t, int32(tc.searches), searches.Load())
				assert.Zero(t, details.Load())
				assert.Equal(t, tc.lists, collections.lists)
				assert.Zero(t, collections.lookups)
				assert.Zero(t, games.reads)
				assert.Zero(t, games.writes)
			})
		}
	}
}
