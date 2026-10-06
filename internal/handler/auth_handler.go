package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/ostkost/avari-tabletop-backend/internal/service"
)

type AuthHandler struct {
	authSvc    *service.AuthService
	renderer   *Renderer
	isProduction bool
}

func NewAuthHandler(authSvc *service.AuthService, renderer *Renderer, isProduction bool) *AuthHandler {
	return &AuthHandler{authSvc: authSvc, renderer: renderer, isProduction: isProduction}
}

type authPageData struct {
	Error    string
	Username string
	Email    string
}

func (h *AuthHandler) GetLogin(w http.ResponseWriter, r *http.Request) {
	h.renderer.Render(w, "login", authPageData{})
}

func (h *AuthHandler) PostLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderer.Render(w, "login", authPageData{Error: "Invalid form data"})
		return
	}

	email := r.FormValue("email")
	password := r.FormValue("password")

	token, err := h.authSvc.Login(r.Context(), email, password)
	if err != nil {
		data := authPageData{Error: "Invalid email or password", Email: email}
		if isHTMX(r) {
			w.Header().Set("HX-Retarget", "#form-errors")
			w.WriteHeader(http.StatusUnprocessableEntity)
			h.renderer.RenderPartial(w, "toast", toastData{Message: data.Error, Type: "error"})
			return
		}
		h.renderer.Render(w, "login", data)
		return
	}

	h.setTokenCookie(w, token)
	w.Header().Set("HX-Redirect", "/collection")
	http.Redirect(w, r, "/collection", http.StatusSeeOther)
}

func (h *AuthHandler) GetRegister(w http.ResponseWriter, r *http.Request) {
	h.renderer.Render(w, "register", authPageData{})
}

func (h *AuthHandler) PostRegister(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderer.Render(w, "register", authPageData{Error: "Invalid form data"})
		return
	}

	email := r.FormValue("email")
	username := r.FormValue("username")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm_password")

	if password != confirm {
		data := authPageData{Error: "Passwords do not match", Email: email, Username: username}
		h.renderAuthError(w, r, "register", data)
		return
	}

	_, err := h.authSvc.Register(r.Context(), email, username, password)
	if err != nil {
		msg := friendlyAuthError(err)
		data := authPageData{Error: msg, Email: email, Username: username}
		h.renderAuthError(w, r, "register", data)
		return
	}

	// Auto-login after registration
	token, err := h.authSvc.Login(r.Context(), email, password)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	h.setTokenCookie(w, token)
	w.Header().Set("HX-Redirect", "/collection")
	http.Redirect(w, r, "/collection", http.StatusSeeOther)
}

func (h *AuthHandler) PostLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:    "token",
		Value:   "",
		MaxAge:  -1,
		Path:    "/",
		Expires: time.Unix(0, 0),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *AuthHandler) setTokenCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    token,
		Path:     "/",
		MaxAge:   86400, // 24h
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.isProduction,
	})
}

func (h *AuthHandler) renderAuthError(w http.ResponseWriter, r *http.Request, page string, data authPageData) {
	if isHTMX(r) {
		w.Header().Set("HX-Retarget", "#form-errors")
		w.WriteHeader(http.StatusUnprocessableEntity)
		h.renderer.RenderPartial(w, "toast", toastData{Message: data.Error, Type: "error"})
		return
	}
	h.renderer.Render(w, page, data)
}

func friendlyAuthError(err error) string {
	switch {
	case errors.Is(err, service.ErrEmailTaken):
		return "Email already registered"
	case errors.Is(err, service.ErrUsernameTaken):
		return "Username already taken"
	case errors.Is(err, service.ErrWeakPassword):
		return "Password must be at least 8 characters"
	case errors.Is(err, service.ErrInvalidEmail):
		return "Invalid email address"
	default:
		return "Registration failed, please try again"
	}
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

type toastData struct {
	Message string
	Type    string // "success" | "error" | "info"
}
