package handler

import (
	"io/fs"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	appMiddleware "github.com/ostkost/avari-tabletop-backend/internal/middleware"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
)

type Handlers struct {
	MobileAuth *MobileAuthHandler
	Auth       *AuthHandler
	Game       *GameHandler
	Collection *CollectionHandler
	Chat       *ChatHandler
	Rules      *RulesHandler
	Renderer   *Renderer
}

func NewRouter(h *Handlers, authSvc *service.AuthService, staticFS fs.FS) http.Handler {
	r := chi.NewRouter()

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:3000", "http://127.0.0.1:3000", "http://localhost:8080", "*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token", "X-Request-ID", "If-Match", "If-None-Match", "X-Idempotency-Key", "Idempotency-Key", "X-Deletion-Receipt"},
		ExposedHeaders:   []string{"Link", "ETag", "Retry-After", "X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Use(chiMiddleware.Recoverer)
	r.Use(appMiddleware.Logger)

	if h.MobileAuth != nil {
		r.Mount("/api/v1", h.MobileAuth.Router())
	}

	r.Get("/account/delete", AccountDeletionPage)

	// Static files
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Public routes
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		h.Renderer.Render(w, "home", nil)
	})
	r.Get("/login", h.Auth.GetLogin)
	r.Get("/register", h.Auth.GetRegister)

	// Auth POST routes — rate-limited: max 10 attempts per minute per IP
	r.Group(func(r chi.Router) {
		r.Use(appMiddleware.RateLimit(10, time.Minute))
		r.Post("/login", h.Auth.PostLogin)
		r.Post("/register", h.Auth.PostRegister)
	})

	// Protected routes
	r.Group(func(r chi.Router) {
		r.Use(appMiddleware.Auth(authSvc))

		r.Post("/logout", h.Auth.PostLogout)

		// Collection
		r.Get("/collection", h.Collection.GetCollection)
		r.Post("/collection", h.Collection.PostCollection)
		r.Put("/collection/{gameID}", h.Collection.PutCollection)
		r.Delete("/collection/{gameID}", h.Collection.DeleteCollection)

		// Games
		r.Get("/games/search", h.Game.GetSearch)
		r.Get("/games/{bggID}", h.Game.GetGameDetail)

		// Chat
		r.Get("/chat/{gameID}", h.Chat.GetChat)
		r.Post("/chat/{gameID}", h.Chat.PostChat)
		r.Delete("/chat/{gameID}/history", h.Chat.DeleteHistory)

		// Rules management
		r.Get("/games/{gameID}/rules", h.Rules.GetRules)
		r.Post("/games/{gameID}/rules", h.Rules.PostRules)
		r.Delete("/games/{gameID}/rules", h.Rules.DeleteRules)
	})

	// 404 handler
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		h.Renderer.Render(w, "error", errorData{Code: 404, Message: "Page not found"})
	})

	return r
}
