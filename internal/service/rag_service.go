package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
)

const (
	defaultChunkSize    = 500
	defaultChunkOverlap = 50
)

// RAGService handles chunking, embedding, storing, and retrieving game rules.
type RAGService struct {
	store       VectorStore
	rulesLoader RulesLoader
}

// RulesLoader abstracts reading raw rules text (backed by RulesRepository).
type RulesLoader interface {
	GetContent(ctx context.Context, gameID uuid.UUID, lang string) (string, error)
}

// NewRAGService creates a RAGService.
func NewRAGService(store VectorStore, loader RulesLoader) *RAGService {
	return &RAGService{store: store, rulesLoader: loader}
}

// IndexRules chunks the given text and stores embeddings for gameID+lang.
func (s *RAGService) IndexRules(ctx context.Context, gameID uuid.UUID, lang, text string) error {
	chunks := chunkText(text, defaultChunkSize, defaultChunkOverlap)
	if len(chunks) == 0 {
		return nil
	}

	docs := make([]Document, len(chunks))
	for i, chunk := range chunks {
		docs[i] = Document{
			ID:      fmt.Sprintf("%s_%s_%d", gameID.String(), lang, i),
			Content: chunk,
			GameID:  gameID,
			Lang:    lang,
		}
	}
	return s.store.AddDocuments(ctx, gameID, lang, docs)
}

// IndexRulesFromDB loads raw rules from the loader and indexes them.
// Returns nil silently if no loader is configured.
func (s *RAGService) IndexRulesFromDB(ctx context.Context, gameID uuid.UUID, lang string) error {
	if s.rulesLoader == nil {
		return nil
	}
	text, err := s.rulesLoader.GetContent(ctx, gameID, lang)
	if err != nil {
		return fmt.Errorf("load rules: %w", err)
	}
	if text == "" {
		return nil
	}
	return s.IndexRules(ctx, gameID, lang, text)
}

// Retrieve returns the topK most relevant chunks for a query.
func (s *RAGService) Retrieve(ctx context.Context, gameID uuid.UUID, lang, query string, topK int) ([]Document, error) {
	return s.store.Search(ctx, gameID, lang, query, topK)
}

// DeleteGame removes all indexed chunks for a game.
func (s *RAGService) DeleteGame(ctx context.Context, gameID uuid.UUID) error {
	return s.store.DeleteGame(ctx, gameID)
}

// BuildSystemPrompt creates a RAG-enriched system prompt for the LLM.
func BuildSystemPrompt(game model.Game, chunks []Document) string {
	if len(chunks) == 0 {
		return fmt.Sprintf(
			`Ты эксперт по настольным играм. Помогаешь игроку разобраться с правилами игры "%s" (%d).
Отвечай на русском языке, если вопрос задан по-русски.
Отвечай на английском, если вопрос задан по-английски.
Будь точным и конкретным.`,
			game.Name, game.YearPublished,
		)
	}

	var sb strings.Builder
	for _, c := range chunks {
		sb.WriteString(c.Content)
		sb.WriteString("\n\n")
	}

	return fmt.Sprintf(
		`Ты эксперт по настольным играм. Помогаешь игроку разобраться с правилами игры "%s" (%d).

ПРАВИЛА ИГРЫ (из официального источника):
%s
Отвечай на русском языке, если вопрос задан по-русски.
Отвечай на английском, если вопрос задан по-английски.
Будь точным и конкретным. Если правило не найдено в предоставленном тексте — скажи об этом честно.
Используй нумерованные списки для пошаговых правил.`,
		game.Name, game.YearPublished, sb.String(),
	)
}

// chunkText splits text into overlapping chunks of roughly chunkSize runes.
func chunkText(text string, chunkSize, overlap int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	runes := []rune(text)
	total := len(runes)
	if total <= chunkSize {
		return []string{string(runes)}
	}

	var chunks []string
	start := 0
	for start < total {
		end := start + chunkSize
		if end > total {
			end = total
		}

		// Try to break at sentence boundary (. ! ?) within last 100 chars
		if end < total {
			breakAt := findSentenceBreak(runes, end, 100)
			if breakAt > start {
				end = breakAt
			}
		}

		chunk := strings.TrimSpace(string(runes[start:end]))
		if utf8.RuneCountInString(chunk) > 0 {
			chunks = append(chunks, chunk)
		}

		next := end - overlap
		if next <= start {
			next = start + 1
		}
		start = next
	}
	return chunks
}

// findSentenceBreak looks backward from pos up to window runes for a sentence end.
func findSentenceBreak(runes []rune, pos, window int) int {
	start := pos - window
	if start < 0 {
		start = 0
	}
	best := -1
	for i := pos; i >= start; i-- {
		r := runes[i]
		if r == '.' || r == '!' || r == '?' || r == '\n' {
			best = i + 1
			break
		}
	}
	return best
}
