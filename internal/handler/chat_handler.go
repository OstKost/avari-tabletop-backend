package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/middleware"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
)

type ChatHandler struct {
	durable  *service.DurableChatService
	chatSvc  *service.ChatService
	gameRepo repository.GameRepository
	renderer *Renderer
}

func NewChatHandler(chatSvc *service.ChatService, gameRepo repository.GameRepository, renderer *Renderer) *ChatHandler {
	return &ChatHandler{chatSvc: chatSvc, gameRepo: gameRepo, renderer: renderer}
}

type chatPageData struct {
	ClientRequestID string
	IdempotencyKey  string
	Game            model.Game
	Messages        []model.ChatMessage
}

func (h *ChatHandler) GetChat(w http.ResponseWriter, r *http.Request) {
	if h.durable != nil {
		h.durableGet(w, r)
		return
	}
	userID, _ := middleware.UserIDFromContext(r.Context())
	gameID, err := uuid.Parse(chi.URLParam(r, "gameID"))
	if err != nil {
		h.renderer.Render(w, "error", errorData{Code: 400, Message: "Invalid game ID"})
		return
	}

	game, err := h.gameRepo.GetByID(r.Context(), gameID)
	if err != nil {
		h.renderer.Render(w, "error", errorData{Code: 404, Message: "Game not found"})
		return
	}

	messages, err := h.chatSvc.GetHistory(r.Context(), userID, gameID)
	if err != nil {
		messages = []model.ChatMessage{}
	}

	h.renderer.Render(w, "chat", chatPageData{Game: game, Messages: messages})
}

func (h *ChatHandler) PostChat(w http.ResponseWriter, r *http.Request) {
	if h.durable != nil {
		h.durablePost(w, r)
		return
	}
	userID, _ := middleware.UserIDFromContext(r.Context())
	gameID, err := uuid.Parse(chi.URLParam(r, "gameID"))
	if err != nil {
		http.Error(w, "invalid game id", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	message := strings.TrimSpace(r.FormValue("message"))
	if message == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Send user message immediately as an OOB swap
	userMsgHTML := renderMessageHTML(model.ChatMessage{
		Role:    model.RoleUser,
		Content: message,
	})
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", escapeSSE(userMsgHTML))
	flusher.Flush()

	// Send "typing" indicator
	typingHTML := `<div id="typing-indicator" class="flex gap-2 p-3"><div class="w-8 h-8 rounded-full bg-indigo-100 flex items-center justify-center text-sm">🤖</div><div class="bg-white rounded-2xl rounded-tl-none px-4 py-3 shadow-sm text-gray-500 italic">Thinking...</div></div>`
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", escapeSSE(typingHTML))
	flusher.Flush()

	chunks, errc := h.chatSvc.SendMessage(r.Context(), userID, gameID, message)

	// Stream assistant response
	var fullResponse strings.Builder
	firstChunk := true

	for chunk := range chunks {
		fullResponse.WriteString(chunk)
		if firstChunk {
			// Replace typing indicator with actual response start
			clearHTML := `<div id="typing-indicator" hx-swap-oob="outerHTML:#typing-indicator"></div>`
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", escapeSSE(clearHTML))

			// Start streaming the assistant message
			startHTML := fmt.Sprintf(`<div id="streaming-message" class="flex gap-2 p-3"><div class="w-8 h-8 rounded-full bg-indigo-100 flex items-center justify-center text-sm flex-shrink-0">🤖</div><div class="bg-white rounded-2xl rounded-tl-none px-4 py-3 shadow-sm max-w-xs sm:max-w-sm md:max-w-md whitespace-pre-wrap">%s`,
				escapeHTML(chunk))
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", escapeSSE(startHTML))
			firstChunk = false
		} else {
			// Append chunk
			chunkHTML := escapeHTML(chunk)
			fmt.Fprintf(w, "event: chunk\ndata: %s\n\n", escapeSSE(chunkHTML))
		}
		flusher.Flush()
	}

	if err := <-errc; err != nil {
		slog.Error("chat stream error", "error", err)
		errHTML := `<div class="text-red-500 text-sm p-3">Error: could not get response. Please try again.</div>`
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", escapeSSE(errHTML))
		flusher.Flush()
	} else if !firstChunk {
		// Close the streaming message div
		fmt.Fprintf(w, "event: message\ndata: </div></div>\n\n")
		flusher.Flush()
	}

	fmt.Fprintf(w, "event: done\ndata: done\n\n")
	flusher.Flush()
}

func (h *ChatHandler) DeleteHistory(w http.ResponseWriter, r *http.Request) {
	if h.durable != nil {
		h.durableDelete(w, r)
		return
	}
	userID, _ := middleware.UserIDFromContext(r.Context())
	gameID, err := uuid.Parse(chi.URLParam(r, "gameID"))
	if err != nil {
		http.Error(w, "invalid game id", http.StatusBadRequest)
		return
	}

	if err := h.chatSvc.ClearHistory(r.Context(), userID, gameID); err != nil {
		http.Error(w, "clear failed", http.StatusInternalServerError)
		return
	}

	if isHTMX(r) {
		w.Write([]byte(`<div id="messages" class="flex-1 overflow-y-auto p-4 space-y-1"><div class="text-center text-gray-500 text-sm py-8">Chat history cleared. Ask me anything about this game!</div></div>`))
		return
	}
	http.Redirect(w, r, r.Header.Get("Referer"), http.StatusSeeOther)
}

func renderMessageHTML(m model.ChatMessage) string {
	if m.Role == model.RoleUser {
		return fmt.Sprintf(
			`<div class="flex gap-2 p-3 justify-end"><div class="bg-indigo-600 text-white rounded-2xl rounded-tr-none px-4 py-3 shadow-sm max-w-xs sm:max-w-sm md:max-w-md whitespace-pre-wrap">%s</div><div class="w-8 h-8 rounded-full bg-indigo-600 flex items-center justify-center text-white text-sm flex-shrink-0">👤</div></div>`,
			escapeHTML(m.Content),
		)
	}
	return fmt.Sprintf(
		`<div class="flex gap-2 p-3"><div class="w-8 h-8 rounded-full bg-indigo-100 flex items-center justify-center text-sm flex-shrink-0">🤖</div><div class="bg-white rounded-2xl rounded-tl-none px-4 py-3 shadow-sm max-w-xs sm:max-w-sm md:max-w-md whitespace-pre-wrap">%s</div></div>`,
		escapeHTML(m.Content),
	)
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

func escapeSSE(s string) string {
	// SSE data cannot contain newlines; replace with space for inline HTML
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}
