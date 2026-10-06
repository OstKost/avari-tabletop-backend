package handler

import (
	"context"
	"errors"
	"html"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	tabletopai "github.com/ostkost/avari-tabletop-backend"
	"github.com/ostkost/avari-tabletop-backend/internal/bgg"
	"github.com/ostkost/avari-tabletop-backend/internal/middleware"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
	"github.com/stretchr/testify/require"
)

type collectionHTTPRepo struct {
	items  []model.CollectionWithGame
	err    error
	writes int
	reads  int
	userID uuid.UUID
	gameID uuid.UUID
	status model.CollectionStatus
	rating *int
	notes  string
}

func (f *collectionHTTPRepo) Add(_ context.Context, user, game uuid.UUID, status model.CollectionStatus) (model.Collection, error) {
	f.writes++
	f.userID = user
	f.gameID = game
	f.status = status
	return model.Collection{}, f.err
}
func (f *collectionHTTPRepo) Update(_ context.Context, user, game uuid.UUID, status model.CollectionStatus, rating *int, notes string) (model.Collection, error) {
	f.writes++
	f.userID = user
	f.gameID = game
	f.status = status
	f.rating = rating
	f.notes = notes
	return model.Collection{}, f.err
}
func (f *collectionHTTPRepo) Remove(_ context.Context, user, game uuid.UUID) error {
	f.writes++
	f.userID = user
	f.gameID = game
	if f.err != nil {
		return f.err
	}
	f.items = nil
	return nil
}
func (f *collectionHTTPRepo) ListByUser(_ context.Context, user uuid.UUID, status model.CollectionStatus) ([]model.CollectionWithGame, error) {
	f.reads++
	f.userID = user
	f.status = status
	return f.items, f.err
}
func (f *collectionHTTPRepo) GetByUserAndGame(context.Context, uuid.UUID, uuid.UUID) (model.Collection, error) {
	return model.Collection{}, repository.ErrNotFound
}

type collectionHTTPGames struct {
	upserts int
	game    model.Game
	err     error
	reads   int
}

func (f *collectionHTTPGames) GetByBGGID(context.Context, int) (model.Game, error) {
	f.reads++
	return f.game, f.err
}
func (f *collectionHTTPGames) GetByID(context.Context, uuid.UUID) (model.Game, error) {
	return f.game, f.err
}
func (f *collectionHTTPGames) Upsert(context.Context, model.Game) (model.Game, error) {
	f.upserts++
	return f.game, f.err
}

func realCollectionRenderer(t *testing.T) *Renderer {
	t.Helper()
	webFS, err := fs.Sub(tabletopai.Web, "web")
	require.NoError(t, err)
	renderer, err := NewRenderer(webFS)
	require.NoError(t, err)
	return renderer
}
func collectionRequest(method, target string, form url.Values, user uuid.UUID, htmx bool) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		r.Header.Set("HX-Request", "true")
	}
	return r.WithContext(middleware.ContextWithUserID(r.Context(), user))
}
func collectionRoute(h *CollectionHandler) http.Handler {
	router := chi.NewRouter()
	router.Get("/collection", h.GetCollection)
	router.Post("/collection", h.PostCollection)
	router.Put("/collection/{gameID}", h.PutCollection)
	router.Delete("/collection/{gameID}", h.DeleteCollection)
	return router
}

func TestCollectionBrowseHTTPAndHTMX(t *testing.T) {
	user := uuid.New()
	game := uuid.New()
	notes := "Quote ' \" & <script>alert(1)</script>\nSecond line"
	for _, htmx := range []bool{false, true} {
		t.Run(map[bool]string{false: "page", true: "partial"}[htmx], func(t *testing.T) {
			repo := &collectionHTTPRepo{items: []model.CollectionWithGame{
				{Collection: model.Collection{UserID: user, GameID: game, Status: model.StatusWishlist, Notes: notes}, Game: model.Game{ID: game, BGGID: 42, Name: "Азул"}},
				{Game: model.Game{Name: "Catan"}},
			}}
			handler := NewCollectionHandler(service.NewCollectionService(repo), nil, realCollectionRenderer(t))
			w := httptest.NewRecorder()
			handler.GetCollection(w, collectionRequest("GET", "/collection?status=wishlist&q=%D0%90%D0%97&sort=rating", nil, user, htmx))
			require.Equal(t, 200, w.Code)
			require.Equal(t, user, repo.userID)
			require.Equal(t, model.StatusWishlist, repo.status)
			body := w.Body.String()
			require.Contains(t, body, "Азул")
			require.NotContains(t, body, "Catan")
			require.Contains(t, body, `name="q" value="АЗ"`)
			require.Contains(t, body, `value="rating" selected`)
			require.Contains(t, body, `aria-current="page"`)
			require.Contains(t, body, `name="rating" type="number" min="1" max="10" step="1" x-ref="rating" x-model="rating" value=""`)
			require.Contains(t, html.UnescapeString(body), notes)
			require.NotContains(t, body, "<script>alert(1)</script>")
			require.NotContains(t, body, "notes: 'Quote")
			require.Contains(t, body, "collectionChanged from:body")
			require.Contains(t, body, "status=wishlist")
			require.NotContains(t, body, "hx-include")
			require.Contains(t, body, "</form>")
			if htmx {
				require.NotContains(t, body, "<!DOCTYPE html>")
			} else {
				require.Contains(t, body, "<!DOCTYPE html>")
			}
		})
	}
}

func TestCollectionGetErrorsAndEmptyStates(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		err          error
		code         int
		message      string
	}{
		{"empty", "/collection", nil, 200, "No games here yet"},
		{"no match", "/collection?q=Azul", nil, 200, "No games match these filters"},
		{"bad status", "/collection?status=invalid", nil, 400, "Choose a valid collection status"},
		{"bad sort", "/collection?sort=invalid", nil, 400, "Choose a valid collection sort order"},
		{"storage", "/collection", errors.New("private database details"), 500, "Unable to load or save"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, htmx := range []bool{false, true} {
				repo := &collectionHTTPRepo{err: tc.err}
				handler := NewCollectionHandler(service.NewCollectionService(repo), nil, realCollectionRenderer(t))
				w := httptest.NewRecorder()
				handler.GetCollection(w, collectionRequest("GET", tc.target, nil, uuid.New(), htmx))
				require.Equal(t, tc.code, w.Code)
				require.Contains(t, w.Body.String(), tc.message)
				require.NotContains(t, w.Body.String(), "private database details")
				if tc.code != 200 {
					require.NotContains(t, w.Body.String(), "No games here yet")
					require.NotContains(t, w.Body.String(), "No games match")
				}
				if tc.code == 400 {
					require.Zero(t, repo.reads)
				}
			}
		})
	}
}

func TestCollectionUpdateRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct{ status, rating string }{
		{"owned", "0"}, {"owned", "11"}, {"owned", "1.5"}, {"owned", "NaN"}, {"owned", "-1"}, {"owned", "+1"}, {"owned", "999999999999999999999999999999999"}, {"", "5"}, {"invalid", "5"},
	} {
		t.Run(tc.status+"/"+tc.rating, func(t *testing.T) {
			repo := &collectionHTTPRepo{}
			h := collectionRoute(NewCollectionHandler(service.NewCollectionService(repo), nil, realCollectionRenderer(t)))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, collectionRequest("PUT", "/collection/"+uuid.NewString(), url.Values{"status": {tc.status}, "rating": {tc.rating}}, uuid.New(), true))
			require.Equal(t, 400, w.Code)
			require.Zero(t, repo.writes)
			require.Empty(t, w.Header().Get("HX-Trigger"))
		})
	}
}

func TestCollectionMutationResultsAndCurrentUser(t *testing.T) {
	user := uuid.New()
	game := uuid.New()
	for _, method := range []string{"PUT", "DELETE"} {
		for _, tc := range []struct {
			name string
			err  error
			code int
		}{
			{"success", nil, 200}, {"missing", repository.ErrNotFound, 404}, {"storage", errors.New("private storage details"), 500},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				for _, htmx := range []bool{false, true} {
					repo := &collectionHTTPRepo{err: tc.err}
					h := collectionRoute(NewCollectionHandler(service.NewCollectionService(repo), nil, realCollectionRenderer(t)))
					w := httptest.NewRecorder()
					h.ServeHTTP(w, collectionRequest(method, "/collection/"+game.String()+"?status=wishlist", url.Values{"status": {"owned"}, "rating": {""}, "notes": {"saved notes"}}, user, htmx))
					code := tc.code
					if tc.err == nil && !htmx {
						code = 303
					}
					require.Equal(t, code, w.Code)
					require.Equal(t, user, repo.userID)
					require.Equal(t, game, repo.gameID)
					require.NotContains(t, w.Body.String(), "private storage details")
					if tc.err == nil && htmx {
						require.Equal(t, "collectionChanged", w.Header().Get("HX-Trigger"))
						require.Empty(t, w.Body.String())
					} else {
						require.Empty(t, w.Header().Get("HX-Trigger"))
					}
					if method == "PUT" {
						require.Nil(t, repo.rating)
						require.Equal(t, model.StatusOwned, repo.status)
						require.Equal(t, "saved notes", repo.notes)
					}
				}
			})
		}
	}
}

func TestCollectionAddResults(t *testing.T) {
	user := uuid.New()
	game := uuid.New()
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"success", nil, 200}, {"duplicate", repository.ErrDuplicate, 409}, {"storage", errors.New("private database details"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, htmx := range []bool{false, true} {
				repo := &collectionHTTPRepo{err: tc.err}
				games := &collectionHTTPGames{game: model.Game{ID: game, BGGID: 42, FetchedAt: time.Now()}}
				h := NewCollectionHandler(service.NewCollectionService(repo), service.NewGameService(games, nil), realCollectionRenderer(t))
				w := httptest.NewRecorder()
				h.PostCollection(w, collectionRequest("POST", "/collection?status=wishlist", url.Values{"bgg_id": {"42"}}, user, htmx))
				code := tc.code
				if tc.err == nil && !htmx {
					code = 303
				}
				require.Equal(t, code, w.Code)
				require.Equal(t, user, repo.userID)
				require.Equal(t, game, repo.gameID)
				require.Equal(t, model.StatusOwned, repo.status)
				require.NotContains(t, w.Body.String(), "private database details")
				if tc.err == nil && htmx {
					require.Contains(t, w.Body.String(), "✓ Added")
					require.Equal(t, "collectionChanged", w.Header().Get("HX-Trigger"))
				} else if tc.err != nil {
					require.NotContains(t, w.Body.String(), "✓ Added")
					require.Empty(t, w.Header().Get("HX-Trigger"))
				}
			}
		})
	}
}

func TestCollectionAddValidationBeforeGameFetch(t *testing.T) {
	for _, tc := range []struct{ id, status string }{
		{"0", "owned"}, {"-1", "owned"}, {"+1", "owned"}, {"999999999999999999999999", "owned"}, {"abc", "owned"}, {"", "owned"}, {"42", "invalid"},
	} {
		t.Run(tc.id+"/"+tc.status, func(t *testing.T) {
			repo := &collectionHTTPRepo{}
			games := &collectionHTTPGames{}
			h := NewCollectionHandler(service.NewCollectionService(repo), service.NewGameService(games, nil), realCollectionRenderer(t))
			w := httptest.NewRecorder()
			h.PostCollection(w, collectionRequest("POST", "/collection", url.Values{"bgg_id": {tc.id}, "status": {tc.status}}, uuid.New(), true))
			require.Equal(t, 400, w.Code)
			require.Zero(t, games.reads)
			require.Zero(t, repo.writes)
		})
	}
}

func TestCollectionDeleteLastItemRefreshesEmptyState(t *testing.T) {
	user := uuid.New()
	game := uuid.New()
	repo := &collectionHTTPRepo{items: []model.CollectionWithGame{{Collection: model.Collection{GameID: game}}}}
	h := collectionRoute(NewCollectionHandler(service.NewCollectionService(repo), nil, realCollectionRenderer(t)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, collectionRequest("DELETE", "/collection/"+game.String(), nil, user, true))
	require.Equal(t, 200, w.Code)
	require.Equal(t, "collectionChanged", w.Header().Get("HX-Trigger"))
	refreshed := httptest.NewRecorder()
	h.ServeHTTP(refreshed, collectionRequest("GET", "/collection", nil, user, true))
	require.Contains(t, refreshed.Body.String(), "No games here yet")
	require.NotContains(t, refreshed.Body.String(), "card-"+game.String())
}

func TestCollectionBoostedNavigationRendersFullPage(t *testing.T) {
	repo := &collectionHTTPRepo{}
	handler := NewCollectionHandler(service.NewCollectionService(repo), nil, realCollectionRenderer(t))
	r := collectionRequest("GET", "/collection", nil, uuid.New(), true)
	r.Header.Set("HX-Boosted", "true")
	w := httptest.NewRecorder()
	handler.GetCollection(w, r)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "<!DOCTYPE html>")
	require.Contains(t, w.Body.String(), "My Collection")
	require.Contains(t, w.Body.String(), "</html>")
}

func TestCollectionAddNonexistentBGGGame(t *testing.T) {
	for _, htmx := range []bool{false, true} {
		t.Run(map[bool]string{false: "page", true: "htmx"}[htmx], func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				require.Equal(t, "/thing", r.URL.Path)
				require.Equal(t, "99999", r.URL.Query().Get("id"))
				w.Header().Set("Content-Type", "application/xml")
				_, _ = w.Write([]byte(`<items/>`))
			}))
			defer server.Close()
			repo := &collectionHTTPRepo{}
			games := &collectionHTTPGames{err: repository.ErrNotFound}
			handler := NewCollectionHandler(service.NewCollectionService(repo), service.NewGameService(games, bgg.NewClientWithBaseURL(server.URL)), realCollectionRenderer(t))
			w := httptest.NewRecorder()
			handler.PostCollection(w, collectionRequest("POST", "/collection", url.Values{"bgg_id": {"99999"}, "status": {"owned"}}, uuid.New(), htmx))
			require.Equal(t, http.StatusNotFound, w.Code)
			require.Equal(t, int32(1), requests.Load())
			require.Equal(t, 1, games.reads)
			require.Zero(t, games.upserts)
			require.Zero(t, repo.writes)
			require.Empty(t, w.Header().Get("HX-Trigger"))
			require.NotContains(t, w.Body.String(), "✓ Added")
		})
	}
}
