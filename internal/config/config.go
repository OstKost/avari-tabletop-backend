package config

import (
	"os"
	"strconv"
)

type Config struct {
	ChatDailyLimit     int
	ChatTimeoutSeconds int
	ChatMaxOutputBytes int
	ListenHost         string
	Port               string
	DatabaseURL        string
	JWTSecret          string
	Env                string
	DevMailboxDir      string
	BGGAPIToken        string

	// LLM Provider: "ollama" | "claude" | "openai_compat"
	LLMProvider string
	LLMModel    string
	LLMBaseURL  string // used for ollama and openai_compat
	LLMAPIKey   string // used for claude and openai_compat (Groq/OpenRouter)

	// Legacy — kept for backward compatibility; maps to LLM* fields when provider=claude
	ClaudeAPIKey string

	// Embeddings
	EmbedProvider string // "ollama" | "openai_compat"
	EmbedModel    string
	EmbedBaseURL  string

	// Vector store: "chromem" | "pgvector"
	VectorStore      string
	RulesPersistPath string // directory for chromem gob files
}

func Load() *Config {
	claudeKey := getEnv("CLAUDE_API_KEY", "")
	llmProvider := getEnv("LLM_PROVIDER", "")
	llmAPIKey := getEnv("LLM_API_KEY", "")

	// If LLM_PROVIDER not set, fall back to "claude" when CLAUDE_API_KEY is present
	if llmProvider == "" {
		if claudeKey != "" {
			llmProvider = "claude"
		} else {
			llmProvider = "ollama"
		}
	}

	// LLM_API_KEY falls back to CLAUDE_API_KEY when provider is claude
	if llmAPIKey == "" && llmProvider == "claude" {
		llmAPIKey = claudeKey
	}

	return &Config{
		ChatDailyLimit:     boundedInt("CHAT_DAILY_LIMIT", 50, 1, 1000),
		ChatTimeoutSeconds: boundedInt("CHAT_TIMEOUT_SECONDS", 120, 1, 120),
		ChatMaxOutputBytes: boundedInt("CHAT_MAX_OUTPUT_BYTES", 131072, 1024, 131072),
		ListenHost:         getEnv("LISTEN_HOST", ""),
		Port:               getEnv("PORT", "8080"),
		DatabaseURL:        getEnv("DATABASE_URL", "postgres://tabletop:tabletop@localhost:5432/tabletop?sslmode=disable"),
		JWTSecret:          getEnv("JWT_SECRET", "dev-secret-change-in-production"),
		Env:                getEnv("ENV", "development"),
		BGGAPIToken:        getEnv("BGG_API_TOKEN", ""),
		DevMailboxDir:      getEnv("DEV_MAILBOX_DIR", ".cache/dev-mailbox"),

		ClaudeAPIKey: claudeKey,

		LLMProvider: llmProvider,
		LLMModel:    getEnv("LLM_MODEL", defaultModel(llmProvider)),
		LLMBaseURL:  getEnv("LLM_BASE_URL", "http://localhost:11434"),
		LLMAPIKey:   llmAPIKey,

		EmbedProvider: getEnv("EMBED_PROVIDER", "ollama"),
		EmbedModel:    getEnv("EMBED_MODEL", "nomic-embed-text"),
		EmbedBaseURL:  getEnv("EMBED_BASE_URL", "http://localhost:11434"),

		VectorStore:      getEnv("VECTOR_STORE", "chromem"),
		RulesPersistPath: getEnv("RULES_PERSIST_PATH", "./data/rules"),
	}
}

func (c *Config) IsProduction() bool {
	return c.Env == "production"
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func defaultModel(provider string) string {
	switch provider {
	case "claude":
		return "claude-sonnet-4-6"
	case "ollama":
		return "qwen3.5"
	default:
		return "llama-3.1-8b-instant"
	}
}

func boundedInt(key string, fallback, min, max int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < min || parsed > max {
		panic("invalid configuration: " + key)
	}
	return parsed
}
