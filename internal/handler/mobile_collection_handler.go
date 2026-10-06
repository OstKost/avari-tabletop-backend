package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
)

type MobileCollectionHandler struct {
	service *service.MobileCollectionService
	auth    *MobileAuthHandler
}

func NewMobileCollectionHandler(s *service.MobileCollectionService, auth *MobileAuthHandler) *MobileCollectionHandler {
	return &MobileCollectionHandler{service: s, auth: auth}
}

func (h *MobileCollectionHandler) failure(w http.ResponseWriter, r *http.Request, err error, current *model.CollectionEntryDTO) {
	switch {
	case errors.Is(err, repository.ErrCollectionStale):
		h.conflict(w, r, 412, "version_conflict", current)
	case errors.Is(err, repository.ErrIdempotencyMismatch):
		apiError(w, r, 409, "idempotency_mismatch", "Use a new idempotency key", false)
	case errors.Is(err, repository.ErrNotFound):
		apiError(w, r, 404, "not_found", "Game or collection entry not found", false)
	case errors.Is(err, repository.ErrSnapshotExpired):
		apiError(w, r, 410, "snapshot_expired", "Capture a new snapshot", false)
	case errors.Is(err, service.ErrCollectionInput), errors.Is(err, service.ErrInvalidCollectionStatus):
		apiError(w, r, 400, "invalid_input", "Check the submitted fields", false)
	case errors.Is(err, service.ErrInvalidCollectionRating):
		apiError(w, r, 400, "invalid_rating", "Rating must be 1–10 or null", false)
	case errors.Is(err, service.ErrAuthRateLimit), errors.Is(err, repository.ErrSnapshotQuota):
		w.Header().Set("Retry-After", "60")
		apiError(w, r, 429, "rate_limited", "Try again later", true)
	default:
		var network net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()) {
			apiError(w, r, 504, "provider_timeout", "Provider timed out", true)
		} else if errors.Is(err, service.ErrBGGUnavailable) {
			apiError(w, r, 502, "provider_unavailable", "Game provider is temporarily unavailable", true)
		} else {
			apiError(w, r, 503, "service_unavailable", "Service is temporarily unavailable", true)
		}
	}
}
func collectionETag(e model.CollectionEntryDTO) string {
	return `"v` + strconv.FormatInt(e.Version, 10) + `"`
}
func (h *MobileCollectionHandler) conflict(w http.ResponseWriter, r *http.Request, status int, code string, current *model.CollectionEntryDTO) {
	if current != nil {
		w.Header().Set("ETag", collectionETag(*current))
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": "Collection entry changed", "request_id": w.Header().Get("X-Request-ID"), "retryable": false, "current": current}})
}
func mobileLimit(r *http.Request) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 50, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 100 {
		return 0, service.ErrCollectionInput
	}
	return n, nil
}
func mobileGameID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "gameId"))
	if err != nil || id == uuid.Nil {
		return id, service.ErrCollectionInput
	}
	return id, nil
}
func (h *MobileCollectionHandler) Search(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	out, err := h.service.Search(r.Context(), p.User.ID, r.URL.Query().Get("q"))
	if err != nil {
		h.failure(w, r, err, nil)
		return
	}
	writeJSON(w, 200, out)
}
func (h *MobileCollectionHandler) Detail(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	id, err := strconv.Atoi(chi.URLParam(r, "bggId"))
	if err != nil {
		h.failure(w, r, service.ErrCollectionInput, nil)
		return
	}
	out, err := h.service.Detail(r.Context(), p.User.ID, id)
	if err != nil {
		h.failure(w, r, err, nil)
		return
	}
	writeJSON(w, 200, out)
}
func (h *MobileCollectionHandler) List(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	limit, err := mobileLimit(r)
	if err != nil {
		h.failure(w, r, err, nil)
		return
	}
	f := model.CollectionFilter{Status: model.CollectionStatus(r.URL.Query().Get("status")), Query: r.URL.Query().Get("q"), Sort: r.URL.Query().Get("sort")}
	out, err := h.service.List(r.Context(), p.User.ID, f, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		h.failure(w, r, err, nil)
		return
	}
	writeJSON(w, 200, out)
}
func (h *MobileCollectionHandler) Snapshot(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	limit, err := mobileLimit(r)
	if err != nil {
		h.failure(w, r, err, nil)
		return
	}
	// Snapshot is always unfiltered, reject accidental filter reuse.
	for key := range r.URL.Query() {
		if key != "limit" && key != "cursor" {
			h.failure(w, r, service.ErrCollectionInput, nil)
			return
		}
	}
	out, err := h.service.Snapshot(r.Context(), p.User.ID, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		h.failure(w, r, err, nil)
		return
	}
	writeJSON(w, 200, out)
}
func (h *MobileCollectionHandler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	game, err := mobileGameID(r)
	if err != nil {
		h.failure(w, r, err, nil)
		return
	}
	out, err := h.service.Get(r.Context(), p.User.ID, game)
	if err != nil {
		h.failure(w, r, err, nil)
		return
	}
	w.Header().Set("ETag", collectionETag(out))
	writeJSON(w, 200, out)
}
func (h *MobileCollectionHandler) Add(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	key, err := uuid.Parse(r.Header.Get("Idempotency-Key"))
	if err != nil || key == uuid.Nil {
		h.failure(w, r, service.ErrCollectionInput, nil)
		return
	}
	var input struct {
		BGGID  int                     `json:"bgg_id"`
		Status *model.CollectionStatus `json:"status"`
	}
	// Decode raw fields too, to distinguish an omitted status from forbidden null.
	var raw map[string]json.RawMessage
	if !decodeJSON(w, r, &raw) {
		return
	}
	for k := range raw {
		if k != "bgg_id" && k != "status" {
			h.failure(w, r, service.ErrCollectionInput, nil)
			return
		}
	}
	body, _ := json.Marshal(raw)
	if json.Unmarshal(body, &input) != nil || raw == nil {
		h.failure(w, r, service.ErrCollectionInput, nil)
		return
	}
	status := model.StatusOwned
	if value, present := raw["status"]; present {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || input.Status == nil {
			h.failure(w, r, service.ErrCollectionInput, nil)
			return
		}
		status = *input.Status
	}
	out, err := h.service.Add(r.Context(), p.User.ID, key, input.BGGID, status)
	if err != nil {
		h.failure(w, r, err, nil)
		return
	}
	w.Header().Set("X-Request-ID", out.RequestID.String())
	w.Header().Set("ETag", collectionETag(out.Entry))
	if out.Status == 409 {
		h.conflict(w, r, 409, "already_in_collection", &out.Entry)
		return
	}
	writeJSON(w, out.Status, out.Entry)
}
func parseCollectionPatch(w http.ResponseWriter, r *http.Request) (*model.CollectionPatch, bool) {
	var fields map[string]json.RawMessage
	if !decodeJSONLimit(w, r, &fields, 65536) {
		return nil, false
	}
	p := &model.CollectionPatch{}
	if len(fields) == 0 {
		apiError(w, r, 400, "invalid_input", "Empty patch", false)
		return nil, false
	}
	for key, raw := range fields {
		null := bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
		valid := true
		switch key {
		case "status":
			var v model.CollectionStatus
			valid = !null && json.Unmarshal(raw, &v) == nil
			p.Status = &v
		case "notes":
			var v string
			valid = !null && json.Unmarshal(raw, &v) == nil
			p.Notes = &v
		case "rating":
			p.HasRating = true
			if !null {
				var v int
				valid = json.Unmarshal(raw, &v) == nil
				p.Rating = &v
			}
		default:
			valid = false
		}
		if !valid {
			apiError(w, r, 400, "invalid_input", "Invalid patch", false)
			return nil, false
		}
	}
	return p, true
}
func (h *MobileCollectionHandler) Mutate(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	game, err := mobileGameID(r)
	if err != nil {
		h.failure(w, r, err, nil)
		return
	}
	tag := r.Header.Get("If-Match")
	if tag == "" {
		apiError(w, r, 428, "precondition_required", "Supply the record version", false)
		return
	}
	if !strings.HasPrefix(tag, `"v`) || !strings.HasSuffix(tag, `"`) {
		h.failure(w, r, service.ErrCollectionInput, nil)
		return
	}
	version, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(tag, `"v`), `"`), 10, 64)
	if err != nil || version < 1 || tag != `"v`+strconv.FormatInt(version, 10)+`"` {
		h.failure(w, r, service.ErrCollectionInput, nil)
		return
	}
	var patch *model.CollectionPatch
	if r.Method == http.MethodPatch {
		patch, ok = parseCollectionPatch(w, r)
		if !ok {
			return
		}
	}
	out, err := h.service.Mutate(r.Context(), p.User.ID, game, version, patch)
	if err != nil {
		h.failure(w, r, err, &out)
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(204)
		return
	}
	w.Header().Set("ETag", collectionETag(out))
	writeJSON(w, 200, out)
}
func (h *MobileCollectionHandler) routes(r chi.Router) {
	r.Get("/games/search", h.Search)
	r.Get("/games/bgg/{bggId}", h.Detail)
	r.Get("/collection", h.List)
	r.Post("/collection", h.Add)
	r.Get("/collection/snapshot", h.Snapshot)
	r.Get("/collection/{gameId}", h.Get)
	r.Patch("/collection/{gameId}", h.Mutate)
	r.Delete("/collection/{gameId}", h.Mutate)
}
