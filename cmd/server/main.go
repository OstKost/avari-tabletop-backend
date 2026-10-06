package main

import (
	"context"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tabletopai "github.com/ostkost/avari-tabletop-backend"
	"github.com/ostkost/avari-tabletop-backend/internal/bgg"
	"github.com/ostkost/avari-tabletop-backend/internal/config"
	"github.com/ostkost/avari-tabletop-backend/internal/handler"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
)

func main() {
	cfg := config.Load()

	// Ensure data directories exist before anything else tries to use them
	if err := os.MkdirAll(filepath.Clean(cfg.RulesPersistPath), 0750); err != nil {
		slog.Error("create rules persist dir", "path", cfg.RulesPersistPath, "error", err)
		os.Exit(1)
	}

	slog.Info("starting tabletop-ai-collection",
		"port", cfg.Port,
		"env", cfg.Env,
		"llm", cfg.LLMProvider,
		"embed", cfg.EmbedProvider,
		"vector", cfg.VectorStore,
	)

	ctx := context.Background()

	// Database
	pool, err := repository.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	slog.Info("connected to database")

	// Migrations from embedded FS
	migsFS, err := fs.Sub(tabletopai.Migrations, "migrations")
	if err != nil {
		slog.Error("prepare migrations fs", "error", err)
		os.Exit(1)
	}
	// Skip pgvector migration unless explicitly configured (requires pg extension)
	var skipMigrations []string
	if cfg.VectorStore != "pgvector" {
		skipMigrations = append(skipMigrations, "003_pgvector", "008_private_pgvector")
	}
	if err := repository.RunMigrations(ctx, pool, migsFS, skipMigrations...); err != nil {
		slog.Error("run migrations", "error", err)
		os.Exit(1)
	}
	slog.Info("migrations applied")

	// Repositories
	userRepo := repository.NewUserRepository(pool)
	gameRepo := repository.NewGameRepository(pool)
	collectionRepo := repository.NewCollectionRepository(pool)

	// LLM Provider
	var llm service.LLMProvider
	switch cfg.LLMProvider {
	case "claude":
		llm = service.NewClaudeClient(cfg.LLMAPIKey)
		slog.Info("LLM provider: Claude", "model", cfg.LLMModel)
	case "openai_compat":
		llm = service.NewOpenAICompatProvider(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel)
		slog.Info("LLM provider: OpenAI-compat", "base_url", cfg.LLMBaseURL, "model", cfg.LLMModel)
	default: // ollama
		llm = service.NewOllamaProvider(cfg.LLMBaseURL, cfg.LLMModel)
		slog.Info("LLM provider: Ollama", "base_url", cfg.LLMBaseURL, "model", cfg.LLMModel)
	}

	// Embedding Provider
	var embedder service.EmbeddingProvider
	switch cfg.EmbedProvider {
	case "openai_compat":
		// Reuse OpenAICompatProvider for embeddings — currently not supported via this path;
		// fall through to Ollama as default.
		embedder = service.NewOllamaEmbedder(cfg.EmbedBaseURL, cfg.EmbedModel)
	default: // ollama
		embedder = service.NewOllamaEmbedder(cfg.EmbedBaseURL, cfg.EmbedModel)
	}
	slog.Info("embedding provider", "provider", cfg.EmbedProvider, "model", cfg.EmbedModel)

	// Private R2 indexes never use legacy game-only namespaces. Account
	// deletion covers both adapters even after switching VECTOR_STORE.
	privateSQL := service.NewPrivatePgvectorStore(pool, embedder)
	privateFiles, err := service.NewPrivateChromemStore(cfg.RulesPersistPath, embedder)
	if err != nil {
		slog.Error("initialize private vector store failed")
		os.Exit(1)
	}
	var privateVectors service.PrivateVectorStore = privateFiles
	var historicalVectors service.PrivateVectorStore = privateSQL
	if cfg.VectorStore == "pgvector" {
		privateVectors = privateSQL
		historicalVectors = privateFiles
	}
	privateRulesSvc := service.NewPrivateRulesService(repository.NewPrivateRulesRepository(pool), privateVectors, historicalVectors)

	// Durable chat uses only scoped rules, independently of SSE subscribers.
	chatOptions := service.DefaultChatOptions()
	chatOptions.DailyLimit = cfg.ChatDailyLimit
	chatOptions.Timeout = time.Duration(cfg.ChatTimeoutSeconds) * time.Second
	chatOptions.MaxOutputBytes = cfg.ChatMaxOutputBytes
	durableChatSvc := service.NewDurableChatService(repository.NewDurableChatRepository(pool), gameRepo, llm, privateRulesSvc, cfg.JWTSecret, chatOptions)

	// Services
	authSvc := service.NewAuthService(userRepo, cfg.JWTSecret)
	var resetMailer service.PasswordResetMailer = service.DisabledResetMailer{}
	if cfg.Env == "development" {
		resetMailer = &service.FileResetMailer{Directory: cfg.DevMailboxDir}
	}
	mobileAuthSvc, err := service.NewMobileAuthService(authSvc, userRepo, repository.NewMobileAuthRepository(pool), cfg.JWTSecret, resetMailer)
	if err != nil {
		slog.Error("initialize mobile auth failed")
		os.Exit(1)
	}
	deletionSvc := service.NewAccountDeletionService(repository.NewAccountDeletionRepository(pool), cfg.JWTSecret, privateRulesSvc.CleanupOwner, durableChatSvc.CleanupOwner)
	bggClient := bgg.NewClientWithToken(cfg.BGGAPIToken)
	gameSvc := service.NewGameService(gameRepo, bggClient)
	mobileCollectionSvc := service.NewMobileCollectionService(repository.NewMobileCollectionRepository(pool), gameSvc, cfg.JWTSecret)
	cleanupCtx, cancelAuthCleanup := context.WithCancel(ctx)
	defer cancelAuthCleanup()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-cleanupCtx.Done():
				return
			case <-ticker.C:
				if err := deletionSvc.Work(cleanupCtx); err != nil {
					slog.Error("account deletion cleanup failed")
				}
				if err := durableChatSvc.Cleanup(cleanupCtx); err != nil {
					slog.Error("durable chat cleanup failed")
				}
				if err := mobileAuthSvc.Cleanup(cleanupCtx); err != nil {
					slog.Error("mobile auth cleanup failed")
				}
				if err := mobileCollectionSvc.Cleanup(cleanupCtx); err != nil {
					slog.Error("mobile collection cleanup failed")
				}
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-cleanupCtx.Done():
				return
			case <-ticker.C:
				workCtx, cancel := context.WithTimeout(cleanupCtx, 2*time.Minute)
				if err := privateRulesSvc.Work(workCtx); err != nil {
					slog.Error("private rules worker failed")
				}
				cancel()
			}
		}
	}()
	for worker := 0; worker < 2; worker++ {
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-cleanupCtx.Done():
					return
				case <-ticker.C:
					if err := durableChatSvc.Work(cleanupCtx); err != nil {
						slog.Error("durable chat worker failed")
					}
				}
			}
		}()
	}
	collectionSvc := service.NewCollectionService(collectionRepo)

	// Templates + static files from embedded FS
	webSubFS, err := fs.Sub(tabletopai.Web, "web")
	if err != nil {
		slog.Error("prepare web fs", "error", err)
		os.Exit(1)
	}
	staticFS, err := fs.Sub(tabletopai.Web, "web/static")
	if err != nil {
		slog.Error("prepare static fs", "error", err)
		os.Exit(1)
	}

	renderer, err := handler.NewRenderer(webSubFS)
	if err != nil {
		slog.Error("load templates", "error", err)
		os.Exit(1)
	}

	mobileCollHandler := &handler.MobileCollectionHandler{}
	_ = mobileCollHandler // populated by NewMobileAuthHandler or below

	mobileChatH := handler.NewMobileChatHandler(durableChatSvc, nil)
	mobileRulesH := handler.NewMobileRulesHandler(privateRulesSvc, collectionRepo, nil)
	mobileCollH := &handler.MobileCollectionHandler{} // constructed in NewMobileAuthHandler
	_ = mobileCollH

	mobileAuthHandler := handler.NewMobileAuthHandler(
		mobileAuthSvc,
		handler.NewMobileCollectionHandler(mobileCollectionSvc, nil),
		mobileChatH,
		mobileRulesH,
	)

	mobileAuthHandler.SetDeletion(deletionSvc)

	// Handlers
	handlers := &handler.Handlers{
		MobileAuth: mobileAuthHandler,
		Auth:       handler.NewAuthHandler(authSvc, renderer, cfg.IsProduction()),
		Game:       handler.NewGameHandler(gameSvc, collectionSvc, renderer),
		Collection: handler.NewCollectionHandler(collectionSvc, gameSvc, renderer),
		Chat:       handler.NewDurableChatHandler(durableChatSvc, gameRepo, renderer),
		Rules:      handler.NewPrivateRulesHandler(privateRulesSvc, collectionRepo, renderer),
		Renderer:   renderer,
	}

	router := handler.NewRouter(handlers, authSvc, staticFS)

	server := &http.Server{
		Addr:         net.JoinHostPort(cfg.ListenHost, cfg.Port),
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 120 * time.Second, // longer for SSE streams
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("server listening", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-quit
	slog.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
	slog.Info("server stopped")
}
