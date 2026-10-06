package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	chromem "github.com/philippgille/chromem-go"
)

// A private scope is mandatory; no adapter accepts a game-only lookup or falls
// back to the legacy vector store/loader. Revision is part of the namespace.
type RulesScope struct {
	UserID, GameID, RuleID uuid.UUID
	Language               string
	Revision               int64
}
type PrivateDocument struct {
	ID      string
	Content string
	Scope   RulesScope
}
type PrivateVectorStore interface {
	Replace(context.Context, RulesScope, []PrivateDocument) error
	Search(context.Context, RulesScope, string, int) ([]PrivateDocument, error)
	Prune(context.Context, uuid.UUID, uuid.UUID, int64) error
	DeleteRule(context.Context, uuid.UUID, uuid.UUID) error
	DeleteOwner(context.Context, uuid.UUID) error
}

func (scope RulesScope) validate() error {
	if scope.UserID == uuid.Nil || scope.GameID == uuid.Nil || scope.RuleID == uuid.Nil || scope.Revision < 1 || (scope.Language != "ru" && scope.Language != "en") {
		return errors.New("invalid private rules scope")
	}
	return nil
}
func validateEmbedding(vec []float32, dimension int) error {
	if len(vec) == 0 || len(vec) > 16000 || (dimension > 0 && len(vec) != dimension) {
		return errors.New("embedding dimension mismatch")
	}
	var norm float64
	for _, v := range vec {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return errors.New("invalid embedding")
		}
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		return errors.New("zero embedding")
	}
	return nil
}
func embedPrivate(ctx context.Context, embedder EmbeddingProvider, scope RulesScope, docs []PrivateDocument) ([][]float32, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, errors.New("empty private index")
	}
	vectors := make([][]float32, len(docs))
	dimension := embedder.Dims()
	ids := map[string]bool{}
	for i, doc := range docs {
		if doc.Scope != scope || doc.ID == "" || ids[doc.ID] {
			return nil, errors.New("document scope/ID mismatch")
		}
		ids[doc.ID] = true
		vec, err := embedder.Embed(ctx, doc.Content)
		if err != nil {
			return nil, errors.New("private embedding failed")
		}
		if err = validateEmbedding(vec, dimension); err != nil {
			return nil, err
		}
		dimension = len(vec)
		vectors[i] = vec
	}
	return vectors, nil
}

type PrivateChromemStore struct {
	db       *chromem.DB
	embedder EmbeddingProvider
	mu       sync.RWMutex
}

func NewPrivateChromemStore(root string, embedder EmbeddingProvider) (*PrivateChromemStore, error) {
	dir := filepath.Join(root, "private-v1")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	db, err := chromem.NewPersistentDB(dir, false)
	if err != nil {
		return nil, err
	}
	return &PrivateChromemStore{db: db, embedder: embedder}, nil
}
func privateOwnerPrefix(owner uuid.UUID) string { return "private_" + owner.String() + "_" }
func privateRulePrefix(owner, rule uuid.UUID) string {
	return privateOwnerPrefix(owner) + rule.String() + "_"
}
func privateScopeName(scope RulesScope) string {
	return privateRulePrefix(scope.UserID, scope.RuleID) + scope.GameID.String() + "_" + scope.Language + "_" + strconv.FormatInt(scope.Revision, 10)
}
func (s *PrivateChromemStore) Replace(ctx context.Context, scope RulesScope, docs []PrivateDocument) error {
	vectors, err := embedPrivate(ctx, s.embedder, scope, docs)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name := privateScopeName(scope)
	// Only unpublished generations reach Replace. Rebuild a recovered job
	// even if Count matches: an earlier persistence failure may leave an
	// in-memory document which never reached disk. Last-ready lives separately.
	if err = s.db.DeleteCollection(name); err != nil {
		return err
	}
	col, err := s.db.CreateCollection(name, map[string]string{"dimension": strconv.Itoa(len(vectors[0]))}, nil)
	if err != nil {
		return err
	}
	for i, doc := range docs {
		err = col.AddDocument(ctx, chromem.Document{ID: doc.ID, Content: doc.Content, Embedding: vectors[i], Metadata: map[string]string{"owner": scope.UserID.String(), "game": scope.GameID.String(), "language": scope.Language, "rule": scope.RuleID.String(), "revision": strconv.FormatInt(scope.Revision, 10)}})
		if err != nil {
			return errors.New("private index persistence failed")
		}
	}
	return nil
}
func (s *PrivateChromemStore) Search(ctx context.Context, scope RulesScope, query string, topK int) ([]PrivateDocument, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	col := s.db.GetCollection(privateScopeName(scope), nil)
	if col == nil || col.Count() == 0 {
		s.mu.RUnlock()
		return nil, nil
	}
	s.mu.RUnlock()
	vec, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, errors.New("private query embedding failed")
	}
	if err = validateEmbedding(vec, 0); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	col = s.db.GetCollection(privateScopeName(scope), nil)
	if col == nil {
		return nil, nil
	}
	// QueryEmbedding checks stored dimensionality too; do not silently mix models.
	if topK <= 0 {
		topK = 3
	}
	if topK > 20 {
		topK = 20
	}
	if topK > col.Count() {
		topK = col.Count()
	}
	if topK == 0 {
		return nil, nil
	}
	results, err := col.QueryEmbedding(ctx, vec, topK, nil, nil)
	if err != nil {
		return nil, errors.New("private vector query failed")
	}
	docs := make([]PrivateDocument, len(results))
	for i, result := range results {
		docs[i] = PrivateDocument{result.ID, result.Content, scope}
	}
	return docs, nil
}
func (s *PrivateChromemStore) deletePrefix(ctx context.Context, prefix string, keep int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range s.db.ListCollections() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasPrefix(name, prefix) && (keep < 1 || !strings.HasSuffix(name, "_"+strconv.FormatInt(keep, 10))) {
			if err := s.db.DeleteCollection(name); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *PrivateChromemStore) Prune(ctx context.Context, user, rule uuid.UUID, keep int64) error {
	return s.deletePrefix(ctx, privateRulePrefix(user, rule), keep)
}
func (s *PrivateChromemStore) DeleteRule(ctx context.Context, user, rule uuid.UUID) error {
	return s.Prune(ctx, user, rule, 0)
}
func (s *PrivateChromemStore) DeleteOwner(ctx context.Context, user uuid.UUID) error {
	if user == uuid.Nil {
		return errors.New("invalid private owner")
	}
	return s.deletePrefix(ctx, privateOwnerPrefix(user), 0)
}

type PrivatePgvectorStore struct {
	pool     *pgxpool.Pool
	embedder EmbeddingProvider
}

func NewPrivatePgvectorStore(pool *pgxpool.Pool, embedder EmbeddingProvider) *PrivatePgvectorStore {
	return &PrivatePgvectorStore{pool, embedder}
}
func (s *PrivatePgvectorStore) Replace(ctx context.Context, scope RulesScope, docs []PrivateDocument) error {
	vectors, err := embedPrivate(ctx, s.embedder, scope, docs)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Validate the metadata scope as well as the vector namespace. A tombstone
	// still exists until external cleanup, but cannot be published by the service.
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_game_rules r JOIN users u ON u.id=r.user_id WHERE r.id=$1 AND r.user_id=$2 AND r.game_id=$3 AND r.language=$4 AND r.deleted_at IS NULL AND u.deletion_requested_at IS NULL AND EXISTS(SELECT 1 FROM collections c WHERE c.user_id=r.user_id AND c.game_id=r.game_id))`, scope.RuleID, scope.UserID, scope.GameID, scope.Language).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return errors.New("private metadata scope missing")
	}
	_, err = tx.Exec(ctx, `DELETE FROM private_rules_chunks WHERE user_id=$1 AND rule_id=$2 AND revision=$3`, scope.UserID, scope.RuleID, scope.Revision)
	if err != nil {
		return err
	}
	for i, doc := range docs {
		_, err = tx.Exec(ctx, `INSERT INTO private_rules_chunks(user_id,rule_id,game_id,language,revision,chunk_id,content,dimension,embedding) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::vector)`, scope.UserID, scope.RuleID, scope.GameID, scope.Language, scope.Revision, doc.ID, doc.Content, len(vectors[i]), privateVectorLiteral(vectors[i]))
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (s *PrivatePgvectorStore) Search(ctx context.Context, scope RulesScope, query string, topK int) ([]PrivateDocument, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	var dimension int
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(dimension),0) FROM private_rules_chunks WHERE user_id=$1 AND rule_id=$2 AND game_id=$3 AND language=$4 AND revision=$5`, scope.UserID, scope.RuleID, scope.GameID, scope.Language, scope.Revision).Scan(&dimension)
	if err != nil {
		return nil, err
	}
	if dimension == 0 {
		return nil, nil
	}
	vec, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, errors.New("private query embedding failed")
	}
	if err = validateEmbedding(vec, dimension); err != nil {
		return nil, err
	}
	if topK <= 0 {
		topK = 3
	}
	if topK > 20 {
		topK = 20
	}
	rows, err := s.pool.Query(ctx, `SELECT chunk_id,content FROM private_rules_chunks WHERE user_id=$1 AND rule_id=$2 AND game_id=$3 AND language=$4 AND revision=$5 ORDER BY embedding <=> $6::vector,chunk_id LIMIT $7`, scope.UserID, scope.RuleID, scope.GameID, scope.Language, scope.Revision, privateVectorLiteral(vec), topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	docs := make([]PrivateDocument, 0)
	for rows.Next() {
		var doc PrivateDocument
		doc.Scope = scope
		if err = rows.Scan(&doc.ID, &doc.Content); err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}
func (s *PrivatePgvectorStore) Prune(ctx context.Context, user, rule uuid.UUID, keep int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM private_rules_chunks WHERE user_id=$1 AND rule_id=$2 AND revision<>$3`, user, rule, keep)
	return err
}
func (s *PrivatePgvectorStore) DeleteRule(ctx context.Context, user, rule uuid.UUID) error {
	return s.Prune(ctx, user, rule, 0)
}
func (s *PrivatePgvectorStore) DeleteOwner(ctx context.Context, user uuid.UUID) error {
	if user == uuid.Nil {
		return fmt.Errorf("invalid private owner")
	}
	// A deployment can switch backends. Cleanup still covers historical SQL
	// chunks, but chromem-only databases have never applied migration 008.
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('private_rules_chunks') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM private_rules_chunks WHERE user_id=$1`, user)
	return err
}

func privateVectorLiteral(vec []float32) string {
	parts := make([]string, len(vec))
	for i, v := range vec {
		parts[i] = strconv.FormatFloat(float64(v), 'g', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
