package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/bgg"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGameSearchQueryValidation(t *testing.T) {
	for _, tc := range []struct {
		query, trimmed string
		valid          bool
	}{
		{query: "", valid: false},
		{query: "  \t\n", valid: false},
		{query: " a ", valid: false},
		{query: " Я ", valid: false},
		{query: " 🎲 ", valid: false},
		{query: "  ab  ", trimmed: "ab", valid: true},
		{query: " \tЯя\n", trimmed: "Яя", valid: true},
	} {
		t.Run(tc.query, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Equal(t, tc.trimmed, r.URL.Query().Get("query"))
				_, _ = w.Write([]byte(`<items/>`))
			}))
			defer srv.Close()
			svc := NewGameService(nil, bgg.NewClientWithBaseURL(srv.URL))
			results, err := svc.SearchBGG(context.Background(), tc.query)
			if tc.valid {
				require.NoError(t, err)
				assert.Empty(t, results)
				assert.Equal(t, int32(1), requests.Load())
			} else {
				require.Error(t, err)
				assert.Zero(t, requests.Load())
			}
		})
	}
}

type gameFetchRepo struct {
	repository.GameRepository
	cached model.Game
	err    error
	writes int
}

func (r *gameFetchRepo) GetByBGGID(context.Context, int) (model.Game, error) {
	return r.cached, r.err
}

func (r *gameFetchRepo) Upsert(_ context.Context, game model.Game) (model.Game, error) {
	r.writes++
	return game, nil
}

func TestGameFetchBGGAbsenceAndStaleFallback(t *testing.T) {
	stale := model.Game{ID: uuid.New(), BGGID: 13, Name: "Cached Catan", FetchedAt: time.Now().Add(-cacheTTL - time.Hour)}
	for _, tc := range []struct {
		name         string
		cached       model.Game
		repoErr      error
		response     string
		status       int
		wantNotFound bool
	}{
		{name: "uncached absent game", repoErr: repository.ErrNotFound, response: `<items/>`, status: http.StatusOK, wantNotFound: true},
		{name: "stale game removed from BGG", cached: stale, response: `<items/>`, status: http.StatusOK, wantNotFound: true},
		{name: "stale fallback during outage", cached: stale, status: http.StatusServiceUnavailable},
		{name: "stale fallback during malformed response", cached: stale, response: `<items>`, status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Equal(t, "/thing", r.URL.Path)
				assert.Equal(t, "13", r.URL.Query().Get("id"))
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.response))
			}))
			defer srv.Close()
			repo := &gameFetchRepo{cached: tc.cached, err: tc.repoErr}
			svc := NewGameService(repo, bgg.NewClientWithBaseURL(srv.URL))
			game, err := svc.GetOrFetch(context.Background(), 13)
			if tc.wantNotFound {
				require.ErrorIs(t, err, repository.ErrNotFound)
				assert.Equal(t, model.Game{}, game)
			} else {
				require.NoError(t, err)
				assert.Equal(t, stale, game)
			}
			assert.Equal(t, int32(1), requests.Load())
			assert.Zero(t, repo.writes)
		})
	}
}
