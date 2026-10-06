package service

import "context"

// LLMMessage is a unified message type across all LLM providers.
type LLMMessage struct {
	Role    string // "user" | "assistant"
	Content string
}

// LLMProvider is the interface all LLM backends must satisfy.
// It mirrors the streaming approach already used by ClaudeClient.
type LLMProvider interface {
	// StreamMessage streams a response token by token.
	// Returns a channel of text chunks and an error channel (closed when done).
	StreamMessage(ctx context.Context, systemPrompt string, messages []LLMMessage) (<-chan string, <-chan error)
	// Name returns a human-readable provider name for logging.
	Name() string
}
