package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/middleware"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
)

const maxRulesBodyBytes = 2 << 20 // 2 MB

// RulesHandler manages game rules upload, display, and deletion.
type RulesHandler struct {
	private        *service.PrivateRulesService
	rulesRepo      repository.RulesStore
	collectionRepo repository.CollectionRepository
	rag            *service.RAGService
	renderer       *Renderer
}

func NewRulesHandler(
	rulesRepo repository.RulesStore,
	collectionRepo repository.CollectionRepository,
	rag *service.RAGService,
	renderer *Renderer,
) *RulesHandler {
	return &RulesHandler{
		rulesRepo:      rulesRepo,
		collectionRepo: collectionRepo,
		rag:            rag,
		renderer:       renderer,
	}
}

type rulesPageData struct {
	Revision    int64
	IndexStatus string
	GameID      uuid.UUID
	Lang        string
	Content     string
	Source      string
	Error       string
	Success     string
}

// ownsGame checks that the authenticated user has the game in their collection.
// Returns (gameID, true) on success, or renders a 403/404 and returns (zero, false).
func (h *RulesHandler) ownsGame(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	gameID, err := uuid.Parse(chi.URLParam(r, "gameID"))
	if err != nil {
		h.renderer.Render(w, "error", errorData{Code: 400, Message: "Invalid game ID"})
		return uuid.Nil, false
	}

	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return uuid.Nil, false
	}

	_, err = h.collectionRepo.GetByUserAndGame(r.Context(), userID, gameID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.renderer.Render(w, "error", errorData{Code: 403, Message: "This game is not in your collection"})
		} else {
			slog.Error("check collection ownership", "error", err)
			h.renderer.Render(w, "error", errorData{Code: 500, Message: "Internal error"})
		}
		return uuid.Nil, false
	}

	return gameID, true
}

// GetRules renders the rules upload page for a game.
func (h *RulesHandler) GetRules(w http.ResponseWriter, r *http.Request) {
	if h.private != nil {
		h.privateGet(w, r)
		return
	}
	gameID, ok := h.ownsGame(w, r)
	if !ok {
		return
	}

	lang := sanitiseLang(r.URL.Query().Get("lang"))
	data := rulesPageData{GameID: gameID, Lang: lang}

	existing, err := h.rulesRepo.Get(r.Context(), gameID, lang)
	if err == nil && existing != nil {
		data.Content = existing.Content
		data.Source = existing.Source
	}

	h.renderer.Render(w, "rules_upload", data)
}

// PostRules saves rules text and re-indexes for RAG.
func (h *RulesHandler) PostRules(w http.ResponseWriter, r *http.Request) {
	if h.private != nil {
		h.privatePost(w, r)
		return
	}
	gameID, ok := h.ownsGame(w, r)
	if !ok {
		return
	}

	// Guard against huge uploads
	r.Body = http.MaxBytesReader(w, r.Body, maxRulesBodyBytes)
	if err := r.ParseForm(); err != nil {
		h.renderer.Render(w, "rules_upload", rulesPageData{
			GameID: gameID, Error: "Request too large or malformed (max 2 MB)",
		})
		return
	}

	lang := sanitiseLang(r.FormValue("lang"))
	content := r.FormValue("content")
	source := r.FormValue("source")
	if source == "" {
		source = "manual"
	}

	if content == "" {
		h.renderer.Render(w, "rules_upload", rulesPageData{
			GameID: gameID, Lang: lang, Error: "Rules text cannot be empty",
		})
		return
	}

	if err := h.rulesRepo.Upsert(r.Context(), gameID, lang, content, source); err != nil {
		slog.Error("save rules", "game_id", gameID, "error", err)
		h.renderer.Render(w, "rules_upload", rulesPageData{
			GameID: gameID, Lang: lang, Content: content,
			Error: "Failed to save rules, please try again",
		})
		return
	}

	if h.rag != nil {
		if err := h.rag.IndexRules(r.Context(), gameID, lang, content); err != nil {
			slog.Error("index rules", "game_id", gameID, "error", err)
			h.renderer.Render(w, "rules_upload", rulesPageData{
				GameID: gameID, Lang: lang, Content: content, Source: source,
				Error: "Rules saved but indexing failed: " + err.Error(),
			})
			return
		}
	}

	h.renderer.Render(w, "rules_upload", rulesPageData{
		GameID:  gameID,
		Lang:    lang,
		Content: content,
		Source:  source,
		Success: "Правила сохранены и проиндексированы ✓",
	})
}

// DeleteRules removes rules for a game+language and clears the vector index.
func (h *RulesHandler) DeleteRules(w http.ResponseWriter, r *http.Request) {
	if h.private != nil {
		h.privateDelete(w, r)
		return
	}
	gameID, ok := h.ownsGame(w, r)
	if !ok {
		return
	}

	lang := sanitiseLang(r.URL.Query().Get("lang"))

	if err := h.rulesRepo.Delete(r.Context(), gameID, lang); err != nil {
		slog.Error("delete rules", "game_id", gameID, "lang", lang, "error", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}

	if h.rag != nil {
		if err := h.rag.DeleteGame(r.Context(), gameID); err != nil {
			slog.Warn("delete vector index", "game_id", gameID, "error", err)
		}
	}

	// HTMX DELETE — redirect via header; plain browser fallback via 303
	w.Header().Set("HX-Redirect", "/games/"+gameID.String()+"/rules?lang="+lang)
	http.Redirect(w, r, "/games/"+gameID.String()+"/rules?lang="+lang, http.StatusSeeOther)
}

// sanitiseLang returns "ru" or "en", defaulting to "ru" for anything else.
func sanitiseLang(lang string) string {
	if lang == "en" {
		return "en"
	}
	return "ru"
}
