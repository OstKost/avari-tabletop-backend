package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
)

type MobileChatHandler struct {
	service *service.DurableChatService
	auth    *MobileAuthHandler
	mu      sync.Mutex
	streams map[uuid.UUID]int
	total   int
}

func NewMobileChatHandler(svc *service.DurableChatService, auth *MobileAuthHandler) *MobileChatHandler {
	return &MobileChatHandler{service: svc, auth: auth, streams: make(map[uuid.UUID]int)}
}
func (h *MobileChatHandler) failure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		apiError(w, r, 404, "not_found", "Chat request or collection game not found", false)
	case errors.Is(err, repository.ErrChatActive):
		apiError(w, r, 409, "generation_active", "Finish or cancel the active request", false)
	case errors.Is(err, repository.ErrIdempotencyMismatch):
		apiError(w, r, 409, "idempotency_conflict", "Use the original chat request", false)
	case errors.Is(err, repository.ErrChatQuota):
		w.Header().Set("Retry-After", "3600")
		apiError(w, r, 429, "ai_quota", "AI request allowance exceeded", true)
	case errors.Is(err, repository.ErrChatReplayExpired):
		apiError(w, r, 410, "replay_expired", "Read request status to recover the answer", false)
	case errors.Is(err, service.ErrChatInput), errors.Is(err, repository.ErrRulesStale):
		apiError(w, r, 400, "invalid_input", "Check the message, identifiers and event cursor", false)
	default:
		apiError(w, r, 503, "chat_unavailable", "Chat is temporarily unavailable", true)
	}
}
func chatPathID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil || id == uuid.Nil {
		apiError(w, r, 400, "invalid_input", "Invalid chat identifier", false)
		return uuid.Nil, false
	}
	return id, true
}
func (h *MobileChatHandler) History(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	game, ok := chatPathID(w, r, "gameId")
	if !ok {
		return
	}
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil {
			h.failure(w, r, service.ErrChatInput)
			return
		}
	}
	page, err := h.service.History(r.Context(), p.User.ID, game, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, 200, page)
}
func (h *MobileChatHandler) ClearHistory(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	game, ok := chatPathID(w, r, "gameId")
	if !ok {
		return
	}
	if err := h.service.ClearHistory(r.Context(), p.User.ID, game); err != nil {
		h.failure(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *MobileChatHandler) Submit(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	game, ok := chatPathID(w, r, "gameId")
	if !ok {
		return
	}
	key, err := uuid.Parse(r.Header.Get("Idempotency-Key"))
	if err != nil || key == uuid.Nil {
		h.failure(w, r, service.ErrChatInput)
		return
	}
	var input struct {
		Message  string    `json:"message"`
		ClientID uuid.UUID `json:"client_request_id"`
	}
	if !decodeJSONLimit(w, r, &input, 64<<10) {
		return
	}
	request, err := h.service.Submit(r.Context(), p.User.ID, game, input.ClientID, key, input.Message)
	if errors.Is(err, repository.ErrChatActive) && request.ID != uuid.Nil {
		w.Header().Set("Location", "/api/v1/chat/requests/"+request.ID.String())
	}
	if err != nil {
		h.failure(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/chat/requests/"+request.ID.String())
	writeJSON(w, 201, request)
}
func (h *MobileChatHandler) Status(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	id, ok := chatPathID(w, r, "requestId")
	if !ok {
		return
	}
	request, err := h.service.Get(r.Context(), p.User.ID, id)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, 200, request)
}
func (h *MobileChatHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	id, ok := chatPathID(w, r, "requestId")
	if !ok {
		return
	}
	request, err := h.service.Cancel(r.Context(), p.User.ID, id)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, 200, request)
}
func (h *MobileChatHandler) reserve(user uuid.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.total >= 64 || h.streams[user] >= 4 {
		return false
	}
	h.streams[user]++
	h.total++
	return true
}
func (h *MobileChatHandler) release(user uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.streams[user]--
	h.total--
	if h.streams[user] == 0 {
		delete(h.streams, user)
	}
}
func (h *MobileChatHandler) Events(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	id, ok := chatPathID(w, r, "requestId")
	if !ok {
		return
	}
	after := int64(0)
	if value := r.Header.Get("Last-Event-ID"); value != "" {
		var err error
		after, err = strconv.ParseInt(value, 10, 64)
		if err != nil || after < 0 || strconv.FormatInt(after, 10) != value {
			h.failure(w, r, service.ErrChatInput)
			return
		}
	}
	events, request, err := h.service.Events(r.Context(), p.User.ID, id, after)
	if err != nil {
		h.failure(w, r, err)
		return
	}
	if !h.reserve(p.User.ID) {
		w.Header().Set("Retry-After", "15")
		apiError(w, r, 429, "stream_limit", "Too many active streams", true)
		return
	}
	defer h.release(p.User.ID)
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.failure(w, r, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	// Flush headers even for pending/no-event requests; GET never invokes Work.
	flusher.Flush()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		for _, event := range events {
			if _, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Kind, event.Payload); err != nil {
				return
			}
			after = event.Sequence
			flusher.Flush()
		}
		if request.Terminal() && after >= request.LastSequence {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err = h.auth.service.ValidateAccess(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); err != nil {
				return
			}
			if _, err = fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			events, request, err = h.service.Events(r.Context(), p.User.ID, id, after)
			if err != nil {
				return
			}
		}
	}
}
func (h *MobileChatHandler) Report(w http.ResponseWriter, r *http.Request) {
	p, ok := h.auth.principal(w, r)
	if !ok {
		return
	}
	var input struct {
		RequestID uuid.UUID `json:"request_id"`
		Reason    string    `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := h.service.Report(r.Context(), p.User.ID, input.RequestID, input.Reason); err != nil {
		h.failure(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]string{"status": "accepted"})
}
func (h *MobileChatHandler) routes(r chi.Router) {
	r.Get("/games/{gameId}/chat/messages", h.History)
	r.Delete("/games/{gameId}/chat/messages", h.ClearHistory)
	r.Post("/games/{gameId}/chat/requests", h.Submit)
	r.Get("/chat/requests/{requestId}", h.Status)
	r.Delete("/chat/requests/{requestId}", h.Cancel)
	r.Get("/chat/requests/{requestId}/events", h.Events)
	r.Post("/ai/reports", h.Report)
}
