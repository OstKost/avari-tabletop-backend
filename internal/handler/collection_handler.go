package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/middleware"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
)

type CollectionHandler struct {
	collectionSvc *service.CollectionService
	gameSvc       *service.GameService
	renderer      *Renderer
}

func NewCollectionHandler(collectionSvc *service.CollectionService, gameSvc *service.GameService, renderer *Renderer) *CollectionHandler {
	return &CollectionHandler{collectionSvc: collectionSvc, gameSvc: gameSvc, renderer: renderer}
}

type collectionStatusLink struct {
	Label    string
	URL      string
	Selected bool
}

type collectionPageData struct {
	Items        []model.CollectionWithGame
	FilterStatus model.CollectionStatus
	Query        string
	Sort         string
	CurrentURL   string
	StatusLinks  []collectionStatusLink
	Error        string
	Success      string
}

func (h *CollectionHandler) GetCollection(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())
	status := model.CollectionStatus(r.URL.Query().Get("status"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	sort := r.URL.Query().Get("sort")
	if sort == "" {
		sort = "newest"
	}
	data := collectionPageData{FilterStatus: status, Query: query, Sort: sort}
	values := url.Values{"q": {query}, "sort": {sort}, "status": {string(status)}}
	data.CurrentURL = "/collection?" + values.Encode()
	for _, tab := range []struct {
		status model.CollectionStatus
		label  string
	}{
		{"", "📚 All"}, {model.StatusOwned, "🏠 Owned"}, {model.StatusWishlist, "💭 Wishlist"}, {model.StatusPlayed, "✅ Played"},
	} {
		values.Set("status", string(tab.status))
		data.StatusLinks = append(data.StatusLinks, collectionStatusLink{Label: tab.label, URL: "/collection?" + values.Encode(), Selected: status == tab.status})
	}
	items, err := h.collectionSvc.Browse(r.Context(), userID, service.CollectionBrowseOptions{Status: status, Query: query, Sort: sort})
	code := http.StatusOK
	if err != nil {
		code, data.Error = collectionError(err)
	} else {
		data.Items = items
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if isHTMX(r) && r.Header.Get("HX-Boosted") != "true" {
		h.renderer.RenderPartial(w, "collection_list", data)
		return
	}
	h.renderer.Render(w, "collection", data)
}

func (h *CollectionHandler) PostCollection(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data.", http.StatusBadRequest)
		return
	}
	bggID, err := parseIntHelper(r.PostForm.Get("bgg_id"))
	if err != nil || bggID <= 0 {
		http.Error(w, "Choose a valid game ID.", http.StatusBadRequest)
		return
	}
	status := model.CollectionStatus(r.PostForm.Get("status"))
	if status == "" {
		status = model.StatusOwned
	}
	if !validCollectionStatus(status) {
		http.Error(w, "Choose a valid collection status.", http.StatusBadRequest)
		return
	}
	game, err := h.gameSvc.GetOrFetch(r.Context(), bggID)
	if err != nil {
		code, message := collectionError(err)
		http.Error(w, message, code)
		return
	}
	if _, err := h.collectionSvc.Add(r.Context(), userID, game.ID, status); err != nil {
		code, message := collectionError(err)
		http.Error(w, message, code)
		return
	}
	if isHTMX(r) {
		w.Header().Set("HX-Trigger", "collectionChanged")
		h.renderer.RenderPartial(w, "add_button", addButtonData{BGGID: bggID, InCollection: true, GameID: game.ID})
		return
	}
	http.Redirect(w, r, "/collection", http.StatusSeeOther)
}

func (h *CollectionHandler) PutCollection(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())
	gameID, err := uuid.Parse(chi.URLParam(r, "gameID"))
	if err != nil {
		http.Error(w, "Choose a valid game ID.", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data.", http.StatusBadRequest)
		return
	}
	status := model.CollectionStatus(r.PostForm.Get("status"))
	if !validCollectionStatus(status) {
		http.Error(w, "Choose a valid collection status.", http.StatusBadRequest)
		return
	}
	var rating *int
	if value := r.PostForm.Get("rating"); value != "" {
		n, err := parseIntHelper(value)
		if err != nil || n < 1 || n > 10 {
			http.Error(w, "Rating must be empty or an integer from 1 to 10.", http.StatusBadRequest)
			return
		}
		rating = &n
	}
	if _, err := h.collectionSvc.Update(r.Context(), userID, gameID, status, rating, r.PostForm.Get("notes")); err != nil {
		code, message := collectionError(err)
		http.Error(w, message, code)
		return
	}
	if isHTMX(r) {
		w.Header().Set("HX-Trigger", "collectionChanged")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/collection", http.StatusSeeOther)
}

func (h *CollectionHandler) DeleteCollection(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFromContext(r.Context())
	gameID, err := uuid.Parse(chi.URLParam(r, "gameID"))
	if err != nil {
		http.Error(w, "Choose a valid game ID.", http.StatusBadRequest)
		return
	}
	if err := h.collectionSvc.Remove(r.Context(), userID, gameID); err != nil {
		code, message := collectionError(err)
		http.Error(w, message, code)
		return
	}
	if isHTMX(r) {
		w.Header().Set("HX-Trigger", "collectionChanged")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/collection", http.StatusSeeOther)
}

func validCollectionStatus(status model.CollectionStatus) bool {
	return status == model.StatusOwned || status == model.StatusWishlist || status == model.StatusPlayed
}

func collectionError(err error) (int, string) {
	switch {
	case errors.Is(err, service.ErrInvalidCollectionStatus):
		return http.StatusBadRequest, "Choose a valid collection status."
	case errors.Is(err, service.ErrInvalidCollectionRating):
		return http.StatusBadRequest, "Rating must be empty or an integer from 1 to 10."
	case errors.Is(err, service.ErrInvalidCollectionSort):
		return http.StatusBadRequest, "Choose a valid collection sort order."
	case errors.Is(err, repository.ErrDuplicate):
		return http.StatusConflict, "This game is already in your collection."
	case errors.Is(err, repository.ErrNotFound):
		return http.StatusNotFound, "Game or collection entry not found."
	default:
		return http.StatusInternalServerError, "Unable to load or save your collection. Please try again."
	}
}

type addButtonData struct {
	BGGID        int
	InCollection bool
	GameID       uuid.UUID
}

func parseInt(s string, v *int) (int, error) {
	n, err := parseIntHelper(s)
	if err != nil {
		return 0, err
	}
	if v != nil {
		*v = n
	}
	return n, nil
}

func parseIntHelper(s string) (int, error) {
	// ParseUint rejects signs, non-digits and overflow. Limit to the signed int range.
	n, err := strconv.ParseUint(s, 10, strconv.IntSize-1)
	return int(n), err
}
