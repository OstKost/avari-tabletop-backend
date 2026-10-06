package handler

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/bgg"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
	"github.com/stretchr/testify/require"
)

// HTTP/HTML regression through the actual router, auth cookie middleware and SQL.
// This complements browser tests; it does not claim to test browser JS/CSS.
func TestR1WebMobileCollectionAndDeletionPostgres(t *testing.T) {
	ctx := context.Background()
	f := newCollectionFixture(t)
	s := f.register(t)
	other, err := f.mobile.Register(ctx, "other-r1@example.invalid", "Other R1", "synthetic-password")
	require.NoError(t, err)
	token, err := f.auth.Login(ctx, s.User.Email, "synthetic-password")
	require.NoError(t, err)
	otherToken, err := f.auth.Login(ctx, other.User.Email, "synthetic-password")
	require.NoError(t, err)
	renderer := realCollectionRenderer(t)
	games := service.NewGameService(repository.NewGameRepository(f.pool), bgg.NewClientWithBaseURL(f.provider.URL))
	mobile := NewMobileAuthHandler(f.mobile, f.svc)
	deletion := service.NewAccountDeletionService(repository.NewAccountDeletionRepository(f.pool), mobileTestSecret)
	mobile.SetDeletion(deletion)
	router := NewRouter(&Handlers{MobileAuth: mobile, Auth: NewAuthHandler(f.auth, renderer, false), Collection: NewCollectionHandler(service.NewCollectionService(repository.NewCollectionRepository(f.pool)), games, renderer), Game: NewGameHandler(games, service.NewCollectionService(repository.NewCollectionRepository(f.pool)), renderer), Chat: &ChatHandler{}, Rules: &RulesHandler{}, Renderer: renderer}, f.auth, fs.FS(fstest.MapFS{}))
	web := func(method, path string, form url.Values, cookie string, htmx bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.RemoteAddr = "127.0.0.9:4444"
		if cookie != "" {
			request.AddCookie(&http.Cookie{Name: "token", Value: cookie})
		}
		if htmx {
			request.Header.Set("HX-Request", "true")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	require.Equal(t, 303, web("GET", "/collection", nil, "", false).Code)
	added := f.request("POST", "/collection", map[string]any{"bgg_id": 1}, s.AccessToken, map[string]string{"Idempotency-Key": uuid.NewString()})
	require.Equal(t, 201, added.Code)
	var entry model.CollectionEntryDTO
	require.NoError(t, json.Unmarshal(added.Body.Bytes(), &entry))
	page := web("GET", "/collection", nil, token, false)
	require.Equal(t, 200, page.Code)
	require.Contains(t, page.Body.String(), "Игра 1")
	require.Contains(t, page.Body.String(), "<html")
	fragment := web("GET", "/collection", nil, token, true)
	require.Equal(t, 200, fragment.Code)
	require.Contains(t, fragment.Body.String(), "Игра 1")
	require.NotContains(t, fragment.Body.String(), "<html")
	otherPage := web("GET", "/collection", nil, otherToken, false)
	require.Equal(t, 200, otherPage.Code)
	require.NotContains(t, otherPage.Body.String(), "Игра 1")
	path := "/collection/" + entry.GameID.String()
	require.Equal(t, 200, web("PUT", path, url.Values{"status": {"played"}, "rating": {"9"}, "notes": {"Web заметка\n<script>never-run</script>"}}, token, true).Code)
	current, err := f.collection.Get(ctx, s.User.ID, entry.GameID)
	require.NoError(t, err)
	require.Greater(t, current.Version, entry.Version)
	require.Equal(t, "Web заметка\n<script>never-run</script>", current.Notes)
	stale := f.request("PATCH", path, map[string]any{"notes": "outdated"}, s.AccessToken, map[string]string{"If-Match": collectionETag(entry)})
	require.Equal(t, 412, stale.Code)
	require.Equal(t, 404, web("DELETE", path, nil, otherToken, true).Code)
	invalid := web("PUT", path, url.Values{"status": {"owned"}, "rating": {"99"}}, token, true)
	require.Equal(t, 400, invalid.Code)
	unchanged, err := f.collection.Get(ctx, s.User.ID, entry.GameID)
	require.NoError(t, err)
	require.Equal(t, current.Version, unchanged.Version)
	require.Equal(t, 200, web("GET", "/account/delete", nil, "", false).Code)
	f.router = mobile.Router()
	acceptedDeletion(t, deleteCall(f.mobileFixture, s.AccessToken, uuid.NewString(), "synthetic-password"))
	for _, htmx := range []bool{false, true} {
		frozen := web("GET", "/collection", nil, token, htmx)
		require.Equal(t, 303, frozen.Code)
		require.Equal(t, "/login", frozen.Header().Get("Location"))
		require.NotContains(t, frozen.Body.String(), "Игра 1")
	}
	require.Equal(t, 200, web("GET", "/collection", nil, otherToken, false).Code)
	require.NoError(t, deletion.Work(ctx))
	require.Equal(t, 303, web("GET", "/collection", nil, token, false).Code)
}
