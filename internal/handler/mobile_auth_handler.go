package handler

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
)

type MobileAuthHandler struct {
	service    *service.MobileAuthService
	deletion   *service.AccountDeletionService
	collection *MobileCollectionHandler
	chat       *MobileChatHandler
	rules      *MobileRulesHandler
}

type MobileHandlersOpt struct {
	Collection *MobileCollectionHandler
	Chat       *MobileChatHandler
	Rules      *MobileRulesHandler
}

func NewMobileAuthHandler(
	s *service.MobileAuthService,
	opts ...any,
) *MobileAuthHandler {
	h := &MobileAuthHandler{
		service: s,
	}
	for _, opt := range opts {
		switch v := opt.(type) {
		case *MobileCollectionHandler:
			h.collection = v
			v.auth = h
		case *service.MobileCollectionService:
			h.collection = NewMobileCollectionHandler(v, h)
		case *MobileChatHandler:
			h.chat = v
			v.auth = h
		case *MobileRulesHandler:
			h.rules = v
			v.auth = h
		}
	}
	return h
}
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}
func apiError(w http.ResponseWriter, r *http.Request, status int, code, message string, retryable bool) {
	id := w.Header().Get("X-Request-ID")
	if id == "" {
		id = uuid.NewString()
		w.Header().Set("X-Request-ID", id)
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "request_id": id, "retryable": retryable}})
}
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	return decodeJSONLimit(w, r, target, 16384)
}
func decodeJSONLimit(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		apiError(w, r, 400, "invalid_input", "Expected JSON input", false)
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		apiError(w, r, 400, "invalid_input", "Invalid JSON input", false)
		return false
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		apiError(w, r, 400, "invalid_input", "Invalid JSON input", false)
		return false
	}
	return true
}
func (h *MobileAuthHandler) failure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidCredentials), errors.Is(err, repository.ErrSessionUnauthorized):
		apiError(w, r, 401, "session_expired", "Sign in again", false)
	case errors.Is(err, repository.ErrResetInvalid):
		apiError(w, r, 400, "invalid_reset_token", "Reset link is invalid or expired", false)
	case errors.Is(err, repository.ErrDeletionConflict):
		apiError(w, r, 409, "idempotency_conflict", "Use the original request", false)
	case errors.Is(err, service.ErrEmailTaken), errors.Is(err, service.ErrUsernameTaken):
		apiError(w, r, 409, "account_conflict", "Email or username is unavailable", false)
	case errors.Is(err, service.ErrAuthInput), errors.Is(err, service.ErrInvalidEmail), errors.Is(err, service.ErrInvalidUsername), errors.Is(err, service.ErrWeakPassword), errors.Is(err, service.ErrLongPassword):
		apiError(w, r, 400, "invalid_input", "Check the submitted fields", false)
	case errors.Is(err, service.ErrAuthRateLimit):
		w.Header().Set("Retry-After", "60")
		apiError(w, r, 429, "rate_limited", "Try again later", true)
	default:
		apiError(w, r, 503, "service_unavailable", "Service is temporarily unavailable", true)
	}
}
func apiPeer(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return peer
}
func (h *MobileAuthHandler) publicLimit(w http.ResponseWriter, r *http.Request) bool {
	if !h.service.AllowPublic(apiPeer(r)) {
		h.failure(w, r, service.ErrAuthRateLimit)
		return false
	}
	return true
}
func (h *MobileAuthHandler) principal(w http.ResponseWriter, r *http.Request) (service.MobilePrincipal, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || len(header) > 8192 {
		h.failure(w, r, repository.ErrSessionUnauthorized)
		return service.MobilePrincipal{}, false
	}
	principal, err := h.service.ValidateAccess(r.Context(), strings.TrimPrefix(header, "Bearer "))
	if err != nil {
		h.failure(w, r, err)
		return service.MobilePrincipal{}, false
	}
	return principal, true
}
func (h *MobileAuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if !h.publicLimit(w, r) {
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	session, err := h.service.Login(r.Context(), input.Email, input.Password)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, 200, session)
}
func (h *MobileAuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	if !h.publicLimit(w, r) {
		return
	}
	var input struct {
		Email    string `json:"email"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	session, err := h.service.Register(r.Context(), input.Email, input.Username, input.Password)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, 201, session)
}
func (h *MobileAuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Refresh string `json:"refresh_token"`
		Attempt string `json:"attempt_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	attempt, err := uuid.Parse(input.Attempt)
	if err != nil || attempt == uuid.Nil {
		h.failure(w, r, service.ErrAuthInput)
		return
	}
	session, err := h.service.Refresh(r.Context(), input.Refresh, attempt, apiPeer(r))
	if err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, 200, session)
}
func (h *MobileAuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	if p, ok := h.principal(w, r); ok {
		writeJSON(w, 200, p.User)
	}
}
func (h *MobileAuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	if err := h.service.Logout(r.Context(), p.SessionID); err != nil {
		h.failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *MobileAuthHandler) RequestReset(w http.ResponseWriter, r *http.Request) {
	if !h.publicLimit(w, r) {
		return
	}
	var input struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := h.service.RequestReset(r.Context(), input.Email); err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]any{"status": "accepted"})
}
func (h *MobileAuthHandler) ConfirmReset(w http.ResponseWriter, r *http.Request) {
	if !h.publicLimit(w, r) {
		return
	}
	var input struct {
		Token    string `json:"reset_token"`
		Password string `json:"new_password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := h.service.ConfirmReset(r.Context(), input.Token, input.Password); err != nil {
		h.failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *MobileAuthHandler) Meta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"api_version": "v1",
		"capabilities": map[string]bool{
			"collection": h.collection != nil,
			"rules":      h.rules != nil,
			"chat":       h.chat != nil,
		},
	})
}
func (h *MobileAuthHandler) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Request-ID", uuid.NewString())
			defer func() {
				if recover() != nil {
					apiError(w, r, 500, "internal_error", "Service is temporarily unavailable", true)
				}
			}()
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/meta", h.Meta)
	r.Post("/auth/register", h.Register)
	r.Post("/auth/login", h.Login)
	r.Post("/auth/refresh", h.Refresh)
	r.Post("/auth/logout", h.Logout)
	r.Get("/me", h.Me)
	if h.deletion != nil {
		r.Post("/me/deletion", h.RequestDeletion)
		r.Get("/deletions/{requestId}", h.DeletionStatus)
	}
	r.Post("/auth/password-reset/request", h.RequestReset)
	r.Post("/auth/password-reset/confirm", h.ConfirmReset)
	if h.collection != nil {
		h.collection.routes(r)
	}
	if h.rules != nil {
		h.rules.routes(r)
	}
	if h.chat != nil {
		h.chat.routes(r)
	}
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		apiError(w, r, 404, "not_found", "API endpoint not found", false)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		apiError(w, r, 405, "method_not_allowed", "Method not allowed", false)
	})
	return r
}
