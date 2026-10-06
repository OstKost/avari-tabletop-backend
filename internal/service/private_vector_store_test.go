package service

import (
	"context"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

type privateTestEmbedder struct {
	vectors [][]float32
	index   int
}

func (e *privateTestEmbedder) Dims() int { return 3 }
func (e *privateTestEmbedder) Embed(context.Context, string) ([]float32, error) {
	v := e.vectors[e.index]
	e.index++
	return v, nil
}
func TestPrivateEmbeddingRejectsInvalidDimensionsAndValues(t *testing.T) {
	scope := RulesScope{uuid.New(), uuid.New(), uuid.New(), "ru", 1}
	doc := PrivateDocument{"one", "synthetic text", scope}
	for _, vec := range [][]float32{nil, {1, 2}, {0, 0, 0}, {1, float32(math.NaN()), 0}, {1, float32(math.Inf(1)), 0}} {
		_, err := embedPrivate(context.Background(), &privateTestEmbedder{vectors: [][]float32{vec}}, scope, []PrivateDocument{doc})
		require.Error(t, err)
	}
	doc.Scope.UserID = uuid.New()
	_, err := embedPrivate(context.Background(), &privateTestEmbedder{}, scope, []PrivateDocument{doc})
	require.Error(t, err)
}
