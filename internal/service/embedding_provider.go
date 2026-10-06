package service

import "context"

// EmbeddingProvider generates vector embeddings from text.
type EmbeddingProvider interface {
	// Embed returns the embedding vector for the given text.
	Embed(ctx context.Context, text string) ([]float32, error)
	// Dims returns the number of dimensions in the embedding vector.
	Dims() int
}
