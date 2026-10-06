package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/middleware"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
)

type GameHandler struct {
	gameSvc       *service.GameService
	collectionSvc *service.CollectionService
	renderer      *Renderer
}

func NewGameHandler(gameSvc *service.GameService, collectionSvc *service.CollectionService, renderer *Renderer) *GameHandler {
	return &GameHandler{gameSvc: gameSvc, collectionSvc: collectionSvc, renderer: renderer}
}

type searchPageData struct {
	Query   string
	Results []searchResultItem
	Error   string
}

type searchResultItem struct {
	BGGID         int
	Name          string
	YearPublished int
	InCollection  bool
	CollectionID  uuid.UUID
}

type gameDetailData struct {
	Game         model.Game
	InCollection bool
	Collection   model.Collection
}

func (h *GameHandler) GetSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	data := searchPageData{Query: query}

	if query != "" {
		results, err := h.gameSvc.SearchBGG(r.Context(), query)
		if err != nil {
			data.Error = "Search failed, please try again"
		} else {
			userID, _ := middleware.UserIDFromContext(r.Context())
			collectionGames := make(map[int]uuid.UUID)
			if userID != uuid.Nil && len(results) > 0 {
				// Collection markers are best-effort; search never fetches game details.
				entries, err := h.collectionSvc.List(r.Context(), userID, "")
				if err == nil {
					for _, entry := range entries {
						collectionGames[entry.Game.BGGID] = entry.Game.ID
					}
				}
			}
			for _, res := range results {
				item := searchResultItem{
					BGGID:         res.BGGID,
					Name:          res.Name,
					YearPublished: res.YearPublished,
				}

				if gameID, ok := collectionGames[res.BGGID]; ok {
					item.InCollection = true
					item.CollectionID = gameID
				}
				data.Results = append(data.Results, item)
			}
		}
	}

	if isHTMX(r) {
		h.renderer.RenderPartial(w, "search_results", data)
		return
	}
	h.renderer.Render(w, "search", data)
}

func (h *GameHandler) GetGameDetail(w http.ResponseWriter, r *http.Request) {
	bggIDStr := chi.URLParam(r, "bggID")
	bggID, err := strconv.Atoi(bggIDStr)
	if err != nil {
		h.renderer.Render(w, "error", errorData{Code: 400, Message: "Invalid game ID"})
		return
	}

	game, err := h.gameSvc.GetOrFetch(r.Context(), bggID)
	if err != nil {
		h.renderer.Render(w, "error", errorData{Code: 404, Message: "Game not found"})
		return
	}

	data := gameDetailData{Game: game}

	userID, ok := middleware.UserIDFromContext(r.Context())
	if ok {
		col, err := h.collectionSvc.GetEntry(r.Context(), userID, game.ID)
		if err == nil {
			data.InCollection = true
			data.Collection = col
		}
	}

	h.renderer.Render(w, "game_detail", data)
}

type errorData struct {
	Code    int
	Message string
}
