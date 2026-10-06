package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgvectorStore implements VectorStore using PostgreSQL with the pgvector extension.
// Scale path: shares the existing Postgres instance, ACID-safe, no extra services.
type PgvectorStore struct {
	pool     *pgxpool.Pool
	embedder EmbeddingProvider
}

func NewPgvectorStore(pool *pgxpool.Pool, embedder EmbeddingProvider) *PgvectorStore {
	return &PgvectorStore{pool: pool, embedder: embedder}
}

func (s *PgvectorStore) AddDocuments(ctx context.Context, gameID uuid.UUID, lang string, docs []Document) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	_, err = tx.Exec(ctx,
		`DELETE FROM rules_chunks WHERE game_id = $1 AND language = $2`,
		gameID, lang,
	)
	if err != nil {
		return fmt.Errorf("delete existing chunks: %w", err)
	}

	for i, doc := range docs {
		vec, err := s.embedder.Embed(ctx, doc.Content)
		if err != nil {
			return fmt.Errorf("embed chunk %d: %w", i, err)
		}

		// Use a fresh UUID as the row ID (doc.ID is a composite string, not a valid UUID)
		rowID := uuid.New()

		_, err = tx.Exec(ctx,
			`INSERT INTO rules_chunks (id, game_id, language, chunk_index, content, embedding)
			 VALUES ($1, $2, $3, $4, $5, $6::vector)`,
			rowID, gameID, lang, i, doc.Content, float32SliceToVector(vec),
		)
		if err != nil {
			return fmt.Errorf("insert chunk %d: %w", i, err)
		}
	}

	return tx.Commit(ctx)
}

func (s *PgvectorStore) Search(ctx context.Context, gameID uuid.UUID, lang string, query string, topK int) ([]Document, error) {
	if topK <= 0 {
		topK = 3
	}

	vec, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id::text, content
		 FROM rules_chunks
		 WHERE game_id = $1 AND language = $2
		 ORDER BY embedding <=> $3::vector
		 LIMIT $4`,
		gameID, lang, float32SliceToVector(vec), topK,
	)
	if err != nil {
		return nil, fmt.Errorf("pgvector search: %w", err)
	}
	defer rows.Close()

	var docs []Document
	for rows.Next() {
		var doc Document
		if err := rows.Scan(&doc.ID, &doc.Content); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		doc.GameID = gameID
		doc.Lang = lang
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}

func (s *PgvectorStore) DeleteGame(ctx context.Context, gameID uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM rules_chunks WHERE game_id = $1`, gameID,
	)
	return err
}

// float32SliceToVector serialises a vector as a pgvector literal: '[0.1,0.2,...]'.
// Uses 'f' format (no scientific notation) so pgvector always accepts the value.
func float32SliceToVector(vec []float32) string {
	parts := make([]string, len(vec))
	for i, v := range vec {
		parts[i] = fmt.Sprintf("%.8f", v) // fixed-point, never "1.2e-05"
	}
	return "[" + strings.Join(parts, ",") + "]"
}
