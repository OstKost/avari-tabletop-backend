package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
)

func (h *MobileAuthHandler) SetDeletion(s *service.AccountDeletionService) { h.deletion = s }
func (h *MobileAuthHandler) RequestDeletion(w http.ResponseWriter, r *http.Request) {
	if !h.publicLimit(w, r) {
		return
	}
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || len(header) > 8192 {
		h.failure(w, r, repository.ErrSessionUnauthorized)
		return
	}
	access := strings.TrimPrefix(header, "Bearer ")
	uid, err := h.service.DeletionIdentity(access)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	key, err := uuid.Parse(r.Header.Get("Idempotency-Key"))
	if err != nil {
		h.failure(w, r, service.ErrAuthInput)
		return
	}
	_, authErr := h.service.ValidateAccess(r.Context(), access)
	if authErr != nil && !errors.Is(authErr, repository.ErrSessionUnauthorized) {
		h.failure(w, r, authErr)
		return
	}
	result, err := h.deletion.Request(r.Context(), uid, key, input.Password, authErr == nil)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, 202, result)
}
func (h *MobileAuthHandler) DeletionStatus(w http.ResponseWriter, r *http.Request) {
	if !h.publicLimit(w, r) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "requestId"))
	if err != nil {
		apiError(w, r, 404, "not_found", "Deletion status unavailable", false)
		return
	}
	result, err := h.deletion.Status(r.Context(), id, r.Header.Get("X-Deletion-Receipt"))
	if errors.Is(err, repository.ErrNotFound) {
		apiError(w, r, 404, "not_found", "Deletion status unavailable", false)
		return
	}
	if err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}
