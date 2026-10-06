package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
)

type MobileRulesHandler struct {
	service        *service.PrivateRulesService
	collectionRepo repository.CollectionRepository
	auth           *MobileAuthHandler
}

func NewMobileRulesHandler(svc *service.PrivateRulesService, collection repository.CollectionRepository, auth *MobileAuthHandler) *MobileRulesHandler {
	return &MobileRulesHandler{svc, collection, auth}
}
func (h *MobileRulesHandler) failure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		apiError(w, r, 404, "not_found", "Private rules or collection game not found", false)
	case errors.Is(err, repository.ErrRulesStale):
		apiError(w, r, 412, "precondition_failed", "Reload the current rules revision", false)
	case errors.Is(err, repository.ErrIdempotencyMismatch):
		apiError(w, r, 409, "idempotency_conflict", "Use the original reindex request", false)
	case errors.Is(err, service.ErrPrivateRulesSize):
		apiError(w, r, 413, "rules_too_large", "Rules text exceeds 1 MiB", false)
	case errors.Is(err, service.ErrPrivateRulesInput):
		apiError(w, r, 400, "invalid_input", "Check rules text, source and language", false)
	default:
		apiError(w, r, 500, "internal_error", "Private rules operation failed", true)
	}
}
func (h *MobileRulesHandler) ownsGame(w http.ResponseWriter, r *http.Request, user uuid.UUID) (uuid.UUID, bool) {
	game, err := uuid.Parse(chi.URLParam(r, "gameId"))
	if err != nil || game == uuid.Nil {
		h.failure(w, r, service.ErrPrivateRulesInput)
		return uuid.Nil, false
	}
	_, err = h.collectionRepo.GetByUserAndGame(r.Context(), user, game)
	if err != nil {
		h.failure(w, r, err)
		return uuid.Nil, false
	}
	return game, true
}
func rulesETag(revision int64) string { return `"v` + strconv.FormatInt(revision, 10) + `"` }
func rulesPrecondition(w http.ResponseWriter, r *http.Request, create bool) (int64, bool) {
	match, none := r.Header.Get("If-Match"), r.Header.Get("If-None-Match")
	if match == "" && none == "" {
		apiError(w, r, 428, "precondition_required", "Supply the rules version or creation precondition", false)
		return 0, false
	}
	if match != "" && none != "" {
		apiError(w, r, 400, "invalid_input", "Supply exactly one precondition", false)
		return 0, false
	}
	if none != "" {
		if create && none == "*" {
			return 0, true
		}
		apiError(w, r, 400, "invalid_input", "Invalid creation precondition", false)
		return 0, false
	}
	version, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(match, `"v`), `"`), 10, 64)
	if err != nil || version < 1 || match != rulesETag(version) {
		apiError(w, r, 400, "invalid_input", "Invalid rules version", false)
		return 0, false
	}
	return version, true
}
func (h *MobileRulesHandler) GetRules(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	game, ok := h.ownsGame(w, r, p.User.ID)
	if !ok {
		return
	}
	rule, err := h.service.Get(r.Context(), p.User.ID, game, chi.URLParam(r, "lang"))
	if err != nil {
		h.failure(w, r, err)
		return
	}
	w.Header().Set("ETag", rulesETag(rule.Revision))
	writeJSON(w, 200, rule)
}
func (h *MobileRulesHandler) SaveRules(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	game, ok := h.ownsGame(w, r, p.User.ID)
	if !ok {
		return
	}
	version, ok := rulesPrecondition(w, r, true)
	if !ok {
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		apiError(w, r, 415, "unsupported_media_type", "Upload UTF-8 JSON rules text", false)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			apiError(w, r, 413, "rules_too_large", "Request body exceeds 2 MiB", false)
		} else {
			h.failure(w, r, service.ErrPrivateRulesInput)
		}
		return
	}
	if !utf8.Valid(body) {
		h.failure(w, r, service.ErrPrivateRulesInput)
		return
	}
	var input struct {
		Content string `json:"content"`
		Source  string `json:"source"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&input); err != nil {
		h.failure(w, r, service.ErrPrivateRulesInput)
		return
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		h.failure(w, r, service.ErrPrivateRulesInput)
		return
	}
	rule, err := h.service.Save(r.Context(), p.User.ID, game, chi.URLParam(r, "lang"), input.Content, input.Source, version)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	w.Header().Set("ETag", rulesETag(rule.Revision))
	writeJSON(w, 202, rule)
}
func (h *MobileRulesHandler) DeleteRules(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	game, ok := h.ownsGame(w, r, p.User.ID)
	if !ok {
		return
	}
	version, ok := rulesPrecondition(w, r, false)
	if !ok {
		return
	}
	if err := h.service.Delete(r.Context(), p.User.ID, game, chi.URLParam(r, "lang"), version); err != nil {
		h.failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func privateRuleID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "ruleId"))
	if err != nil || id == uuid.Nil {
		apiError(w, r, 404, "not_found", "Private rules not found", false)
		return uuid.Nil, false
	}
	return id, true
}
func (h *MobileRulesHandler) IndexStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	id, ok := privateRuleID(w, r)
	if !ok {
		return
	}
	status, err := h.service.Status(r.Context(), p.User.ID, id)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, 200, status)
}
func (h *MobileRulesHandler) Reindex(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	id, ok := privateRuleID(w, r)
	if !ok {
		return
	}
	version, ok := rulesPrecondition(w, r, false)
	if !ok {
		return
	}
	key, err := uuid.Parse(r.Header.Get("Idempotency-Key"))
	if err != nil || key == uuid.Nil {
		h.failure(w, r, service.ErrPrivateRulesInput)
		return
	}
	status, err := h.service.Reindex(r.Context(), p.User.ID, id, version, key)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, 202, status)
}
func (h *MobileRulesHandler) routes(r chi.Router) {
	r.Get("/games/{gameId}/rules/{lang}", h.GetRules)
	r.Put("/games/{gameId}/rules/{lang}", h.SaveRules)
	r.Delete("/games/{gameId}/rules/{lang}", h.DeleteRules)
	r.Get("/rules/{ruleId}/index-status", h.IndexStatus)
	r.Post("/rules/{ruleId}/reindex", h.Reindex)
}
