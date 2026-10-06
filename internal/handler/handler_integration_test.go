package handler

// Integration-level handler tests that catch the class of bugs found in production:
//  - Template loading failures (missing partials/pages in renderer)
//  - 422 on duplicate registration via HTMX
//  - 422 on wrong password via HTMX
//  - register → login → collection flow
//  - search page renders without error

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	tabletopai "github.com/ostkost/avari-tabletop-backend"
	"github.com/ostkost/avari-tabletop-backend/internal/config"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Renderer smoke test — uses the REAL embedded templates
// Catches: missing {{define}} names, missing included partials, etc.
// ---------------------------------------------------------------------------

func TestNewRenderer_LoadsAllTemplates(t *testing.T) {
	webFS, err := fs.Sub(tabletopai.Web, "web")
	require.NoError(t, err)
	_, err = NewRenderer(webFS)
	require.NoError(t, err, "NewRenderer must load all templates without error")
}

func TestRenderer_RenderEachPage(t *testing.T) {
	webFS, err := fs.Sub(tabletopai.Web, "web")
	require.NoError(t, err)
	r, err := NewRenderer(webFS)
	require.NoError(t, err)

	pages := []struct {
		name string
		data any
	}{
		{"home", nil},
		{"login", authPageData{}},
		{"register", authPageData{}},
		{"error", errorData{Code: 404, Message: "not found"}},
		{"rules_upload", rulesPageData{}},
		{"search", searchPageData{}},
		{"collection", collectionPageData{}},
	}

	for _, p := range pages {
		t.Run(p.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.Render(w, p.name, p.data)
			// Must not return 500
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"page %q should render without 500", p.name)
		})
	}
}

func TestRenderer_RenderEachPartial(t *testing.T) {
	webFS, err := fs.Sub(tabletopai.Web, "web")
	require.NoError(t, err)
	r, err := NewRenderer(webFS)
	require.NoError(t, err)

	partials := []struct {
		name string
		data any
	}{
		{"toast", toastData{Message: "test", Type: "error"}},
		{"search_results", struct {
			Query   string
			Results []searchResultItem
			Error   string
		}{}},
		{"collection_list", collectionPageData{}},
		{"add_button", searchResultItem{}},
		{"chat_message", model.ChatMessage{Role: model.RoleUser, Content: "hello"}},
	}

	for _, p := range partials {
		t.Run(p.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.RenderPartial(w, p.name, p.data)
			assert.NotEqual(t, http.StatusInternalServerError, w.Code,
				"partial %q should render without 500", p.name)
		})
	}
}

// ---------------------------------------------------------------------------
// Auth handler — full register/login/logout flow
// ---------------------------------------------------------------------------

func buildRealAuthHandler(t *testing.T) (*AuthHandler, *service.AuthService) {
	t.Helper()
	cfg := config.Load()
	cfg.JWTSecret = "test-secret-32-chars-minimum!!"
	userRepo := repository.NewInMemoryUserRepository()
	authSvc := service.NewAuthService(userRepo, cfg.JWTSecret)
	return NewAuthHandler(authSvc, buildTestRenderer(t), false), authSvc
}

func TestRegister_DuplicateEmail_NonHTMX(t *testing.T) {
	h, authSvc := buildRealAuthHandler(t)
	_, err := authSvc.Register(context.Background(), "dup@test.com", "firstuser", "password123")
	require.NoError(t, err)

	form := url.Values{
		"email": {"dup@test.com"}, "username": {"seconduser"},
		"password": {"password123"}, "confirm_password": {"password123"},
	}
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.PostRegister(w, req)

	// Non-HTMX: should re-render the page with an error message (200, not 422)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "REGISTER")
}

func TestRegister_DuplicateEmail_HTMX_Returns422(t *testing.T) {
	h, authSvc := buildRealAuthHandler(t)
	_, err := authSvc.Register(context.Background(), "dup@test.com", "firstuser", "password123")
	require.NoError(t, err)

	form := url.Values{
		"email": {"dup@test.com"}, "username": {"seconduser"},
		"password": {"password123"}, "confirm_password": {"password123"},
	}
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	h.PostRegister(w, req)

	// HTMX: must return 422 with toast content, NOT 500
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	body := w.Body.String()
	assert.NotContains(t, body, "partial not found", "toast partial must render successfully")
	assert.Contains(t, body, "already", "error message should mention email is taken")
}

func TestRegister_PasswordMismatch_HTMX_Returns422(t *testing.T) {
	h, _ := buildRealAuthHandler(t)
	form := url.Values{
		"email": {"new@test.com"}, "username": {"newuser"},
		"password": {"password123"}, "confirm_password": {"different456"},
	}
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	h.PostRegister(w, req)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.NotContains(t, w.Body.String(), "partial not found")
}

func TestLogin_WrongPassword_HTMX_Returns422(t *testing.T) {
	h, authSvc := buildRealAuthHandler(t)
	_, err := authSvc.Register(context.Background(), "user@test.com", "testuser", "password123")
	require.NoError(t, err)

	form := url.Values{"email": {"user@test.com"}, "password": {"wrongpass"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	h.PostLogin(w, req)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.NotContains(t, w.Body.String(), "partial not found")
}

func TestRegister_WeakPassword_Returns422(t *testing.T) {
	h, _ := buildRealAuthHandler(t)
	form := url.Values{
		"email": {"new@test.com"}, "username": {"newuser"},
		"password": {"123"}, "confirm_password": {"123"},
	}
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	h.PostRegister(w, req)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

func TestRegister_ThenLogin_FullFlow(t *testing.T) {
	h, _ := buildRealAuthHandler(t)

	// Step 1: Register
	form := url.Values{
		"email": {"flow@test.com"}, "username": {"flowuser"},
		"password": {"password123"}, "confirm_password": {"password123"},
	}
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.PostRegister(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code, "register should redirect")
	require.Equal(t, "/collection", w.Header().Get("Location"))

	// Token cookie must be set
	var token string
	for _, c := range w.Result().Cookies() {
		if c.Name == "token" {
			token = c.Value
		}
	}
	require.NotEmpty(t, token, "token cookie must be set after registration")

	// Step 2: Logout
	req2 := httptest.NewRequest(http.MethodPost, "/logout", nil)
	w2 := httptest.NewRecorder()
	h.PostLogout(w2, req2)
	assert.Equal(t, http.StatusSeeOther, w2.Code)

	// Step 3: Login again
	form3 := url.Values{"email": {"flow@test.com"}, "password": {"password123"}}
	req3 := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form3.Encode()))
	req3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w3 := httptest.NewRecorder()
	h.PostLogin(w3, req3)
	assert.Equal(t, http.StatusSeeOther, w3.Code)
	assert.Equal(t, "/collection", w3.Header().Get("Location"))
}
