//go:build rag_integration

package service_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
	"github.com/stretchr/testify/require"
)

// TestCatanRAG_RussianRules tests the full RAG pipeline:
// 1. Load Catan rules from testdata
// 2. Chunk + embed via Ollama (nomic-embed-text)
// 3. Ask a question about the rules in Russian
// 4. Verify retrieved chunks contain the answer
// 5. Call LLM (qwen3.5 via Ollama) with chunks in context
// 6. Verify the answer contains "10"
//
// Requirements: ollama running with nomic-embed-text and qwen3.5 pulled.
// Run: go test -v -tags=rag_integration -timeout=300s ./internal/service/ -run TestCatanRAG
func TestCatanRAG_RussianRules(t *testing.T) {
	ollamaURL := getEnvOr("OLLAMA_BASE_URL", "http://localhost:11434")
	embedModel := getEnvOr("EMBED_MODEL", "nomic-embed-text")
	llmModel := getEnvOr("LLM_MODEL", "qwen3.5")

	// Load testdata
	rulesBytes, err := os.ReadFile("../../testdata/catan_rules_ru.txt")
	require.NoError(t, err, "testdata/catan_rules_ru.txt must exist")
	rulesText := string(rulesBytes)
	require.NotEmpty(t, rulesText)

	// Warm up the LLM — first call loads the model into VRAM (can take 30–60s for 6GB models)
	t.Log("Warming up LLM model (loading into memory)...")
	warmCtx, warmCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer warmCancel()
	warmLLM := service.NewOllamaProvider(ollamaURL, llmModel)
	wChunks, wErr := warmLLM.StreamMessage(warmCtx, "You are helpful.", []service.LLMMessage{
		{Role: "user", Content: "Reply with one word: ready"},
	})
	for range wChunks {
	}
	if err := <-wErr; err != nil {
		t.Logf("Warm-up warning (non-fatal): %v", err)
	} else {
		t.Log("LLM warmed up successfully")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Set up embedder
	embedder := service.NewOllamaEmbedder(ollamaURL, embedModel)

	// Set up in-memory chromem store
	tmpDir := t.TempDir()
	store, err := service.NewChromemStore(tmpDir, embedder)
	require.NoError(t, err)

	// Set up RAG service with a stub loader (we index directly)
	ragSvc := service.NewRAGService(store, nil)

	gameID := uuid.New()
	lang := "ru"

	// Index the rules
	t.Log("Indexing Catan rules...")
	err = ragSvc.IndexRules(ctx, gameID, lang, rulesText)
	require.NoError(t, err, "IndexRules should succeed")

	// Retrieve chunks for the question
	question := "Сколько победных очков нужно для победы в игре?"
	t.Logf("Querying: %s", question)

	chunks, err := ragSvc.Retrieve(ctx, gameID, lang, question, 3)
	require.NoError(t, err)
	require.NotEmpty(t, chunks, "should find relevant chunks for the victory points question")

	// Verify at least one chunk mentions "10" (the answer)
	found10InChunks := false
	for _, c := range chunks {
		if strings.Contains(c.Content, "10") {
			found10InChunks = true
			break
		}
	}
	require.True(t, found10InChunks, "retrieved chunks should contain '10' (victory points answer)")
	t.Logf("Retrieved %d chunks, first chunk preview: %.150s...", len(chunks), chunks[0].Content)

	// Build system prompt with RAG context
	game := model.Game{Name: "Колонизаторы", YearPublished: 1995}
	systemPrompt := service.BuildSystemPrompt(game, chunks)
	require.Contains(t, systemPrompt, "10", "system prompt should contain the victory points rule")

	// Call LLM and verify response mentions "10"
	t.Log("Calling LLM...")
	llm := service.NewOllamaProvider(ollamaURL, llmModel)
	messages := []service.LLMMessage{
		{Role: "user", Content: question},
	}

	streamChunks, streamErr := llm.StreamMessage(ctx, systemPrompt, messages)

	var response strings.Builder
	for chunk := range streamChunks {
		response.WriteString(chunk)
	}
	require.NoError(t, <-streamErr, "LLM stream should complete without error")

	answer := response.String()
	t.Logf("LLM answer: %s", answer)
	require.NotEmpty(t, answer, "LLM should produce a response")
	require.Contains(t, answer, "10",
		"LLM answer should mention '10' as the number of victory points needed to win")
}

func getEnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
