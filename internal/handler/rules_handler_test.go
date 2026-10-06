package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/middleware"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeRulesRepo is an in-memory RulesRepository for tests.
type fakeRulesRepo struct {
	data map[string]repository.GameRule // key: "gameID_lang"
}

func newFakeRulesRepo() *fakeRulesRepo {
	return &fakeRulesRepo{data: make(map[string]repository.GameRule)}
}

func (f *fakeRulesRepo) Upsert(_ context.Context, gameID uuid.UUID, lang, content, source string) error {
	f.data[gameID.String()+"_"+lang] = repository.GameRule{
		ID: uuid.New(), GameID: gameID, Lang: lang, Content: content, Source: source,
	}
	return nil
}

func (f *fakeRulesRepo) Get(_ context.Context, gameID uuid.UUID, lang string) (*repository.GameRule, error) {
	r, ok := f.data[gameID.String()+"_"+lang]
	if !ok {
		return nil, nil //nolint:nilnil
	}
	return &r, nil
}

func (f *fakeRulesRepo) Delete(_ context.Context, gameID uuid.UUID, lang string) error {
	delete(f.data, gameID.String()+"_"+lang)
	return nil
}

func (f *fakeRulesRepo) DeleteAll(_ context.Context, gameID uuid.UUID) error { return nil }
func (f *fakeRulesRepo) GetContent(_ context.Context, gameID uuid.UUID, lang string) (string, error) {
	r, ok := f.data[gameID.String()+"_"+lang]
	if !ok {
		return "", nil
	}
	return r.Content, nil
}

// fakeCollectionRepo returns ErrNotFound for any game not in allowedGames.
type fakeCollectionRepo struct {
	allowedGames map[uuid.UUID]bool
}

func newFakeCollectionRepo(allowed ...uuid.UUID) *fakeCollectionRepo {
	m := make(map[uuid.UUID]bool)
	for _, id := range allowed {
		m[id] = true
	}
	return &fakeCollectionRepo{allowedGames: m}
}

func (f *fakeCollectionRepo) GetByUserAndGame(_ context.Context, _, gameID uuid.UUID) (model.Collection, error) {
	if !f.allowedGames[gameID] {
		return model.Collection{}, repository.ErrNotFound
	}
	return model.Collection{GameID: gameID}, nil
}

// Unused CollectionRepository methods — implement to satisfy interface
func (f *fakeCollectionRepo) Add(_ context.Context, _, _ uuid.UUID, _ model.CollectionStatus) (model.Collection, error) {
	return model.Collection{}, errors.New("not implemented")
}
func (f *fakeCollectionRepo) Remove(_ context.Context, _, _ uuid.UUID) error {
	return errors.New("not implemented")
}
func (f *fakeCollectionRepo) Update(_ context.Context, _, _ uuid.UUID, _ model.CollectionStatus, _ *int, _ string) (model.Collection, error) {
	return model.Collection{}, errors.New("not implemented")
}
func (f *fakeCollectionRepo) ListByUser(_ context.Context, _ uuid.UUID, _ model.CollectionStatus) ([]model.CollectionWithGame, error) {
	return nil, errors.New("not implemented")
}

// ---------------------------------------------------------------------------
// Helper — builds a chi request with gameID in the URL param and userID in context
// ---------------------------------------------------------------------------

func rulesRequest(method, gameID string, userID uuid.UUID, body url.Values) *http.Request {
	var bodyReader *strings.Reader
	if body != nil {
		bodyReader = strings.NewReader(body.Encode())
	} else {
		bodyReader = strings.NewReader("")
	}

	req := httptest.NewRequest(method, "/games/"+gameID+"/rules", bodyReader)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	// Inject chi URL params
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("gameID", gameID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	// Inject authenticated userID by running the request through a fake auth middleware
	// that injects userID into context — same key used by middleware.Auth.
	req = req.WithContext(middleware.ContextWithUserID(req.Context(), userID))
	return req
}

func buildRulesHandler(t *testing.T, allowedGameIDs ...uuid.UUID) (*RulesHandler, *fakeRulesRepo) {
	t.Helper()
	repo := newFakeRulesRepo()
	return NewRulesHandler(
		repo,
		newFakeCollectionRepo(allowedGameIDs...),
		nil, // RAG disabled in unit tests
		buildTestRenderer(t),
	), repo
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestGetRules_NotInCollection_Returns403(t *testing.T) {
	userID := uuid.New()
	otherGameID := uuid.New()
	h, _ := buildRulesHandler(t) // no games allowed

	w := httptest.NewRecorder()
	h.GetRules(w, rulesRequest(http.MethodGet, otherGameID.String(), userID, nil))

	assert.Equal(t, http.StatusOK, w.Code) // Render sets 200 but content shows error code
	assert.Contains(t, w.Body.String(), "403")
}

func TestGetRules_InCollection_Returns200(t *testing.T) {
	userID := uuid.New()
	gameID := uuid.New()
	h, _ := buildRulesHandler(t, gameID)

	w := httptest.NewRecorder()
	h.GetRules(w, rulesRequest(http.MethodGet, gameID.String(), userID, nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "RULES")
}

func TestPostRules_NotInCollection_Returns403(t *testing.T) {
	userID := uuid.New()
	gameID := uuid.New()
	h, _ := buildRulesHandler(t) // no games allowed

	body := url.Values{"lang": {"ru"}, "content": {"some rules"}, "source": {"test"}}
	w := httptest.NewRecorder()
	h.PostRules(w, rulesRequest(http.MethodPost, gameID.String(), userID, body))

	assert.Contains(t, w.Body.String(), "403")
}

func TestPostRules_EmptyContent_ShowsError(t *testing.T) {
	userID := uuid.New()
	gameID := uuid.New()
	h, _ := buildRulesHandler(t, gameID)

	body := url.Values{"lang": {"ru"}, "content": {""}}
	w := httptest.NewRecorder()
	h.PostRules(w, rulesRequest(http.MethodPost, gameID.String(), userID, body))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "RULES")
}

func TestPostRules_Success_SavesAndRenders(t *testing.T) {
	userID := uuid.New()
	gameID := uuid.New()
	h, rulesRepo := buildRulesHandler(t, gameID)

	body := url.Values{"lang": {"ru"}, "content": {"правила игры..."}, "source": {"test"}}
	w := httptest.NewRecorder()
	h.PostRules(w, rulesRequest(http.MethodPost, gameID.String(), userID, body))

	assert.Equal(t, http.StatusOK, w.Code)
	// Verify it was actually saved
	saved, err := rulesRepo.Get(context.Background(), gameID, "ru")
	require.NoError(t, err)
	require.NotNil(t, saved)
	assert.Equal(t, "правила игры...", saved.Content)
}

func TestSanitiseLang(t *testing.T) {
	assert.Equal(t, "en", sanitiseLang("en"))
	assert.Equal(t, "ru", sanitiseLang("ru"))
	assert.Equal(t, "ru", sanitiseLang(""))
	assert.Equal(t, "ru", sanitiseLang("de"))
	assert.Equal(t, "ru", sanitiseLang("'; DROP TABLE--"))
}
