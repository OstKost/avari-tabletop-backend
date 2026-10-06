package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	chromem "github.com/philippgille/chromem-go"
	"github.com/google/uuid"
)

// ChromemStore implements VectorStore using chromem-go (in-process, gob-persisted).
// Best for single-server deployments. Zero external dependencies beyond Go.
type ChromemStore struct {
	db      *chromem.DB
	embedder EmbeddingProvider
	persistDir string
}

// NewChromemStore creates a persistent chromem-go backed vector store.
// persistDir is the directory where gob files are written.
func NewChromemStore(persistDir string, embedder EmbeddingProvider) (*ChromemStore, error) {
	if err := os.MkdirAll(persistDir, 0755); err != nil {
		return nil, fmt.Errorf("create persist dir: %w", err)
	}

	dbPath := filepath.Join(persistDir, "chromem.db")
	db, err := chromem.NewPersistentDB(dbPath, false)
	if err != nil {
		return nil, fmt.Errorf("open chromem DB: %w", err)
	}

	return &ChromemStore{
		db:         db,
		embedder:   embedder,
		persistDir: persistDir,
	}, nil
}

// collectionName returns a stable collection name for a game+language pair.
func collectionName(gameID uuid.UUID, lang string) string {
	return "game_" + gameID.String() + "_" + lang
}

func (s *ChromemStore) AddDocuments(ctx context.Context, gameID uuid.UUID, lang string, docs []Document) error {
	name := collectionName(gameID, lang)

	// Delete existing collection to allow full re-index
	_ = s.db.DeleteCollection(name)

	// chromem.EmbeddingFunc is just func(ctx, text) ([]float32, error)
	embedFn := chromem.EmbeddingFunc(func(ctx context.Context, text string) ([]float32, error) {
		return s.embedder.Embed(ctx, text)
	})

	col, err := s.db.CreateCollection(name, nil, embedFn)
	if err != nil {
		return fmt.Errorf("create collection: %w", err)
	}

	chromeDocs := make([]chromem.Document, len(docs))
	for i, d := range docs {
		chromeDocs[i] = chromem.Document{
			ID:      d.ID,
			Content: d.Content,
			Metadata: map[string]string{
				"game_id": gameID.String(),
				"lang":    lang,
			},
		}
	}

	if err := col.AddDocuments(ctx, chromeDocs, 1); err != nil {
		return fmt.Errorf("add documents: %w", err)
	}
	return nil
}

func (s *ChromemStore) Search(ctx context.Context, gameID uuid.UUID, lang string, query string, topK int) ([]Document, error) {
	name := collectionName(gameID, lang)

	col := s.db.GetCollection(name, chromem.EmbeddingFunc(func(ctx context.Context, text string) ([]float32, error) {
		return s.embedder.Embed(ctx, text)
	}))
	if col == nil {
		// No rules indexed yet — return empty result (chat still works, just without RAG context)
		return nil, nil
	}

	if topK <= 0 {
		topK = 3
	}

	results, err := col.Query(ctx, query, topK, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("chromem query: %w", err)
	}

	docs := make([]Document, len(results))
	for i, r := range results {
		docs[i] = Document{
			ID:      r.ID,
			Content: r.Content,
			GameID:  gameID,
			Lang:    lang,
		}
	}
	return docs, nil
}

func (s *ChromemStore) DeleteGame(ctx context.Context, gameID uuid.UUID) error {
	for _, lang := range []string{"ru", "en"} {
		_ = s.db.DeleteCollection(collectionName(gameID, lang))
	}
	return nil
}
