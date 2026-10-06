package service

import (
	"context"

	"github.com/google/uuid"
)

// Document is a chunk of text with metadata, stored and retrieved from a VectorStore.
type Document struct {
	ID      string
	Content string
	GameID  uuid.UUID
	Lang    string
}

// VectorStore abstracts over chromem-go (start) and pgvector (scale).
type VectorStore interface {
	// AddDocuments embeds and stores text chunks for a game.
	AddDocuments(ctx context.Context, gameID uuid.UUID, lang string, docs []Document) error
	// Search retrieves the topK most relevant chunks for the given query.
	Search(ctx context.Context, gameID uuid.UUID, lang string, query string, topK int) ([]Document, error)
	// DeleteGame removes all stored chunks for a game.
	DeleteGame(ctx context.Context, gameID uuid.UUID) error
}
