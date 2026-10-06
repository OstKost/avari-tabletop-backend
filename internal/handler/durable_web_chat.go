package handler

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/middleware"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
)

func NewDurableChatHandler(svc *service.DurableChatService, games repository.GameRepository, renderer *Renderer) *ChatHandler {
	return &ChatHandler{durable: svc, gameRepo: games, renderer: renderer}
}
func (h *ChatHandler) durableFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		http.Error(w, "Chat or collection game not found", 404)
	case errors.Is(err, repository.ErrChatActive), errors.Is(err, repository.ErrIdempotencyMismatch):
		http.Error(w, "Chat request conflict", 409)
	case errors.Is(err, repository.ErrChatQuota):
		http.Error(w, "AI allowance exceeded", 429)
	case errors.Is(err, service.ErrChatInput):
		http.Error(w, "Invalid chat input", 400)
	default:
		http.Error(w, "Chat unavailable", 503)
	}
}
func (h *ChatHandler) durableGet(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserIDFromContext(r.Context())
	game, err := uuid.Parse(chi.URLParam(r, "gameID"))
	if err != nil {
		h.durableFailure(w, service.ErrChatInput)
		return
	}
	page, err := h.durable.History(r.Context(), user, game, 50, "")
	if err != nil {
		h.durableFailure(w, err)
		return
	}
	gameInfo, err := h.gameRepo.GetByID(r.Context(), game)
	if err != nil {
		h.durableFailure(w, err)
		return
	}
	messages := make([]model.ChatMessage, 0, len(page.Items))
	for _, m := range page.Items {
		messages = append(messages, model.ChatMessage{ID: m.ID, UserID: user, GameID: game, Role: m.Role, Content: m.Content, CreatedAt: m.CreatedAt})
	}
	w.Header().Set("Cache-Control", "no-store")
	h.renderer.Render(w, "chat", chatPageData{Game: gameInfo, Messages: messages, ClientRequestID: uuid.NewString(), IdempotencyKey: uuid.NewString()})
}
func (h *ChatHandler) durablePost(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserIDFromContext(r.Context())
	game, err := uuid.Parse(chi.URLParam(r, "gameID"))
	if err != nil {
		h.durableFailure(w, service.ErrChatInput)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err = r.ParseForm(); err != nil {
		h.durableFailure(w, service.ErrChatInput)
		return
	}
	client, key := uuid.New(), uuid.New()
	if value := r.FormValue("client_request_id"); value != "" {
		client, err = uuid.Parse(value)
		if err != nil {
			h.durableFailure(w, service.ErrChatInput)
			return
		}
	}
	if value := r.FormValue("idempotency_key"); value != "" {
		key, err = uuid.Parse(value)
		if err != nil {
			h.durableFailure(w, service.ErrChatInput)
			return
		}
	}
	request, err := h.durable.Submit(r.Context(), user, game, client, key, r.FormValue("message"))
	if err != nil {
		h.durableFailure(w, err)
		return
	}
	events, current, err := h.durable.Events(r.Context(), user, request.ID, 0)
	if err != nil {
		h.durableFailure(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.durableFailure(w, errors.New("stream unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()
	after := int64(0)
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
		if current.Terminal() && after >= current.LastSequence {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err = fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			events, current, err = h.durable.Events(r.Context(), user, request.ID, after)
			if err != nil {
				return
			}
		}
	}
}
func (h *ChatHandler) durableDelete(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserIDFromContext(r.Context())
	game, err := uuid.Parse(chi.URLParam(r, "gameID"))
	if err != nil {
		h.durableFailure(w, service.ErrChatInput)
		return
	}
	if err = h.durable.ClearHistory(r.Context(), user, game); err != nil {
		h.durableFailure(w, err)
		return
	}
	if isHTMX(r) {
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, `<div id="messages" class="flex-1 overflow-y-auto p-4"><p>История очищена.</p></div>`)
		return
	}
	http.Redirect(w, r, "/chat/"+game.String(), http.StatusSeeOther)
}
