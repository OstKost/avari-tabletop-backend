package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/ostkost/avari-tabletop-backend/internal/middleware"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
)

func NewPrivateRulesHandler(svc *service.PrivateRulesService, collection repository.CollectionRepository, renderer *Renderer) *RulesHandler {
	return &RulesHandler{private: svc, collectionRepo: collection, renderer: renderer}
}
func webRulesLanguage(lang string) (string, bool) {
	if lang == "" {
		return "ru", true
	}
	return lang, lang == "ru" || lang == "en"
}
func (h *RulesHandler) privateGet(w http.ResponseWriter, r *http.Request) {
	game, ok := h.ownsGame(w, r)
	if !ok {
		return
	}
	user, _ := middleware.UserIDFromContext(r.Context())
	lang, ok := webRulesLanguage(r.URL.Query().Get("lang"))
	if !ok {
		http.Error(w, "Invalid language", 400)
		return
	}
	data := rulesPageData{GameID: game, Lang: lang}
	rule, err := h.private.Get(r.Context(), user, game, lang)
	if err == nil {
		data.Content = rule.Content
		data.Source = rule.Source
		data.Revision = rule.Revision
		data.IndexStatus = rule.IndexStatus
		w.Header().Set("ETag", rulesETag(rule.Revision))
	} else if !errors.Is(err, repository.ErrNotFound) {
		http.Error(w, "Private rules unavailable", 500)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.renderer.Render(w, "rules_upload", data)
}
func (h *RulesHandler) privatePost(w http.ResponseWriter, r *http.Request) {
	game, ok := h.ownsGame(w, r)
	if !ok {
		return
	}
	user, _ := middleware.UserIDFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Request too large or malformed", 413)
		return
	}
	lang, ok := webRulesLanguage(r.FormValue("lang"))
	if !ok || r.FormValue("document_lang") != lang {
		http.Error(w, "Reload the selected language", 400)
		return
	}
	revision, err := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	if err != nil || revision < 0 {
		http.Error(w, "Reload the rules revision", 428)
		return
	}
	rule, err := h.private.Save(r.Context(), user, game, lang, r.FormValue("content"), r.FormValue("source"), revision)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrRulesStale):
			http.Error(w, "Rules changed; reload before replacing", 412)
		case errors.Is(err, service.ErrPrivateRulesInput):
			http.Error(w, "Invalid rules text", 400)
		case errors.Is(err, service.ErrPrivateRulesSize):
			http.Error(w, "Rules exceed 1 MiB", 413)
		case errors.Is(err, repository.ErrNotFound):
			http.Error(w, "Game not in collection", 404)
		default:
			http.Error(w, "Private rules could not be saved", 500)
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", rulesETag(rule.Revision))
	h.renderer.Render(w, "rules_upload", rulesPageData{GameID: game, Lang: lang, Content: rule.Content, Source: rule.Source, Revision: rule.Revision, IndexStatus: rule.IndexStatus, Success: "Правила сохранены. Индексация выполняется в фоне; обновите страницу для проверки."})
}
func (h *RulesHandler) privateDelete(w http.ResponseWriter, r *http.Request) {
	game, ok := h.ownsGame(w, r)
	if !ok {
		return
	}
	user, _ := middleware.UserIDFromContext(r.Context())
	lang, ok := webRulesLanguage(r.URL.Query().Get("lang"))
	if !ok {
		http.Error(w, "Invalid language", 400)
		return
	}
	revision, ok := rulesPrecondition(w, r, false)
	if !ok {
		return
	}
	err := h.private.Delete(r.Context(), user, game, lang, revision)
	if err != nil {
		if errors.Is(err, repository.ErrRulesStale) {
			http.Error(w, "Rules changed; reload", 412)
		} else if errors.Is(err, repository.ErrNotFound) {
			http.Error(w, "Rules not found", 404)
		} else {
			http.Error(w, "Private rules deletion failed", 500)
		}
		return
	}
	location := "/games/" + game.String() + "/rules?lang=" + lang
	w.Header().Set("HX-Redirect", location)
	http.Redirect(w, r, location, http.StatusSeeOther)
}
