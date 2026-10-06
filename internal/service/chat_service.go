package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
)

type ChatService struct {
	chatRepo     repository.ChatRepository
	gameRepo     repository.GameRepository
	llm          LLMProvider
	rag          *RAGService // legacy only; disabled when a private rules service is set
	privateRules *PrivateRulesService
}

func NewChatService(chatRepo repository.ChatRepository, gameRepo repository.GameRepository, llm LLMProvider, rag *RAGService) *ChatService {
	return &ChatService{chatRepo: chatRepo, gameRepo: gameRepo, llm: llm, rag: rag}
}

func (s *ChatService) SetPrivateRules(rules *PrivateRulesService) { s.privateRules = rules }

// SendMessage sends a user message and returns channels for streaming response and errors.
// After the stream completes, both messages are saved to the DB.
func (s *ChatService) SendMessage(ctx context.Context, userID, gameID uuid.UUID, userMessage string) (<-chan string, <-chan error) {
	chunks := make(chan string, 64)
	errc := make(chan error, 1)

	go func() {
		defer close(chunks)
		defer close(errc)

		if err := s.processMessage(ctx, userID, gameID, userMessage, chunks); err != nil {
			errc <- err
		}
	}()

	return chunks, errc
}

func (s *ChatService) processMessage(ctx context.Context, userID, gameID uuid.UUID, userMessage string, chunks chan<- string) error {
	game, err := s.gameRepo.GetByID(ctx, gameID)
	if err != nil {
		return fmt.Errorf("load game: %w", err)
	}

	history, err := s.chatRepo.GetHistory(ctx, userID, gameID, 20)
	if err != nil {
		return fmt.Errorf("load history: %w", err)
	}

	// Build RAG-enriched system prompt
	var ragChunks []Document
	if s.privateRules != nil {
		docs, err := s.privateRules.Retrieve(ctx, userID, gameID, detectLang(userMessage), userMessage, 3)
		if err != nil {
			return fmt.Errorf("private rules retrieval failed")
		}
		for _, doc := range docs {
			ragChunks = append(ragChunks, Document{ID: doc.ID, Content: doc.Content, GameID: doc.Scope.GameID, Lang: doc.Scope.Language})
		}
	} else if s.rag != nil {
		// Detect language: Cyrillic → ru, otherwise → en
		lang := detectLang(userMessage)
		var ragErr error
		ragChunks, ragErr = s.rag.Retrieve(ctx, gameID, lang, userMessage, 3)
		if ragErr != nil {
			slog.Warn("rag retrieve failed", "game_id", gameID, "lang", lang, "error", ragErr)
		}
		// Fallback: if nothing found in detected lang, try the other language
		if len(ragChunks) == 0 {
			fallback := "en"
			if lang == "en" {
				fallback = "ru"
			}
			ragChunks, ragErr = s.rag.Retrieve(ctx, gameID, fallback, userMessage, 3)
			if ragErr != nil {
				slog.Warn("rag retrieve fallback failed", "game_id", gameID, "lang", fallback, "error", ragErr)
			}
		}
	}

	systemPrompt := BuildSystemPrompt(game, ragChunks)
	if s.privateRules != nil {
		systemPrompt = strings.Replace(systemPrompt, "ПРАВИЛА ИГРЫ (из официального источника):", "ЗАГРУЖЕННЫЕ ПОЛЬЗОВАТЕЛЕМ ПРАВИЛА (источник не подтверждён):", 1)
	}

	messages := make([]LLMMessage, 0, len(history)+1)
	for _, m := range history {
		messages = append(messages, LLMMessage{
			Role:    string(m.Role),
			Content: m.Content,
		})
	}
	messages = append(messages, LLMMessage{
		Role:    "user",
		Content: userMessage,
	})

	streamChunks, streamErr := s.llm.StreamMessage(ctx, systemPrompt, messages)

	var fullResponse strings.Builder
	for chunk := range streamChunks {
		fullResponse.WriteString(chunk)
		select {
		case chunks <- chunk:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if err := <-streamErr; err != nil {
		return fmt.Errorf("stream response: %w", err)
	}

	// Save both messages after successful stream
	if _, err := s.chatRepo.SaveMessage(ctx, userID, gameID, model.RoleUser, userMessage); err != nil {
		return fmt.Errorf("save user message: %w", err)
	}
	if _, err := s.chatRepo.SaveMessage(ctx, userID, gameID, model.RoleAssistant, fullResponse.String()); err != nil {
		return fmt.Errorf("save assistant message: %w", err)
	}

	return nil
}

func (s *ChatService) GetHistory(ctx context.Context, userID, gameID uuid.UUID) ([]model.ChatMessage, error) {
	return s.chatRepo.GetHistory(ctx, userID, gameID, 50)
}

func (s *ChatService) ClearHistory(ctx context.Context, userID, gameID uuid.UUID) error {
	return s.chatRepo.ClearHistory(ctx, userID, gameID)
}

// detectLang returns "ru" if the text contains Cyrillic characters, otherwise "en".
func detectLang(text string) string {
	for _, r := range text {
		if r >= 0x0400 && r <= 0x04FF {
			return "ru"
		}
	}
	return "en"
}
