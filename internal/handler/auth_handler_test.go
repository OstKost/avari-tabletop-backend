package handler

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ostkost/avari-tabletop-backend/internal/config"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// In-memory filesystem for templates in unit tests
// ---------------------------------------------------------------------------

type mapFS struct{ files map[string]string }

// ReadFile implements fs.ReadFileFS — avoids Stat() nil panic in template parsing.
func (m *mapFS) ReadFile(name string) ([]byte, error) {
	content, ok := m.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return []byte(content), nil
}

func (m *mapFS) Open(name string) (fs.File, error) {
	content, ok := m.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &mapFile{content: content}, nil
}

type mapFile struct {
	content string
	offset  int
}

func (f *mapFile) Read(p []byte) (int, error) {
	if f.offset >= len(f.content) {
		return 0, io.EOF
	}
	n := copy(p, f.content[f.offset:])
	f.offset += n
	return n, nil
}
func (f *mapFile) Close() error               { return nil }
func (f *mapFile) Stat() (fs.FileInfo, error) { return nil, nil }

func buildTestRenderer(t *testing.T) *Renderer {
	t.Helper()
	files := map[string]string{
		"templates/layouts/base.html":             `{{define "base"}}<!DOCTYPE html><body>{{template "nav" .}}{{block "content" .}}{{end}}</body>{{end}}`,
		"templates/partials/nav.html":             `{{define "nav"}}{{end}}`,
		"templates/partials/toast.html":           `{{define "toast"}}{{.Message}}{{end}}`,
		"templates/pages/login.html":              `{{define "content"}}LOGIN{{if .Error}} ERR:{{.Error}}{{end}}{{end}}`,
		"templates/pages/register.html":           `{{define "content"}}REGISTER{{if .Error}} ERR:{{.Error}}{{end}}{{end}}`,
		"templates/pages/home.html":               `{{define "content"}}HOME{{end}}`,
		"templates/pages/collection.html":         `{{define "content"}}{{template "collection_list" .}}{{end}}`,
		"templates/pages/search.html":             `{{define "content"}}{{template "search_results" .}}{{end}}`,
		"templates/pages/game_detail.html":        `{{define "content"}}GAME{{end}}`,
		"templates/pages/chat.html":               `{{define "content"}}CHAT{{end}}`,
		"templates/pages/error.html":              `{{define "content"}}ERROR {{.Code}}{{end}}`,
		"templates/pages/rules_upload.html":       `{{define "content"}}RULES{{end}}`,
		"templates/partials/search_results.html":  `{{define "search_results"}}RESULTS{{end}}`,
		"templates/partials/collection_list.html": `{{define "collection_list"}}LIST{{end}}`,
		"templates/partials/collection_card.html": `{{define "collection_card"}}CARD{{end}}`,
		"templates/partials/chat_message.html":    `{{define "chat_message"}}MSG{{end}}`,
		"templates/partials/add_button.html":      `{{define "add_button"}}BTN{{end}}`,
	}
	renderer, err := NewRenderer(&mapFS{files: files})
	require.NoError(t, err)
	return renderer
}

func buildTestAuthHandler(t *testing.T) (*AuthHandler, *service.AuthService) {
	t.Helper()
	cfg := config.Load()
	cfg.JWTSecret = "test-secret-32-chars-minimum!!"
	userRepo := repository.NewInMemoryUserRepository()
	authSvc := service.NewAuthService(userRepo, cfg.JWTSecret)
	return NewAuthHandler(authSvc, buildTestRenderer(t), false), authSvc
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestGetLogin(t *testing.T) {
	h, _ := buildTestAuthHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	w := httptest.NewRecorder()
	h.GetLogin(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "LOGIN")
}

func TestPostLogin_Success(t *testing.T) {
	h, authSvc := buildTestAuthHandler(t)
	_, err := authSvc.Register(context.Background(), "user@test.com", "testuser", "password123")
	require.NoError(t, err)

	form := url.Values{"email": {"user@test.com"}, "password": {"password123"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.PostLogin(w, req)

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/collection", w.Header().Get("Location"))

	var tokenCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "token" {
			tokenCookie = c
		}
	}
	require.NotNil(t, tokenCookie, "token cookie must be set")
	assert.NotEmpty(t, tokenCookie.Value)
	assert.True(t, tokenCookie.HttpOnly)
}

func TestPostLogin_WrongPassword(t *testing.T) {
	h, authSvc := buildTestAuthHandler(t)
	_, err := authSvc.Register(context.Background(), "user@test.com", "testuser", "password123")
	require.NoError(t, err)

	form := url.Values{"email": {"user@test.com"}, "password": {"wrongpassword"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.PostLogin(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "LOGIN")
}

func TestPostRegister_Success(t *testing.T) {
	h, _ := buildTestAuthHandler(t)
	form := url.Values{
		"email": {"new@test.com"}, "username": {"newuser"},
		"password": {"password123"}, "confirm_password": {"password123"},
	}
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.PostRegister(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)
}

func TestPostRegister_PasswordMismatch(t *testing.T) {
	h, _ := buildTestAuthHandler(t)
	form := url.Values{
		"email": {"new@test.com"}, "username": {"newuser"},
		"password": {"password123"}, "confirm_password": {"different456"},
	}
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.PostRegister(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "REGISTER")
}

func TestPostLogout(t *testing.T) {
	h, _ := buildTestAuthHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	w := httptest.NewRecorder()
	h.PostLogout(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	for _, c := range w.Result().Cookies() {
		if c.Name == "token" {
			assert.Less(t, c.MaxAge, 0)
		}
	}
}
