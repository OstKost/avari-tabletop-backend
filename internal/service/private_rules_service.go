package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"strings"
	"unicode/utf8"
)

var ErrPrivateRulesInput = errors.New("invalid private rules input")
var ErrPrivateRulesSize = errors.New("private rules content exceeds 1 MiB")

type PrivateRulesService struct {
	repo          repository.PrivateRulesRepository
	store         PrivateVectorStore
	cleanupStores []PrivateVectorStore
}

func NewPrivateRulesService(repo repository.PrivateRulesRepository, store PrivateVectorStore, historical ...PrivateVectorStore) *PrivateRulesService {
	return &PrivateRulesService{repo: repo, store: store, cleanupStores: append([]PrivateVectorStore{store}, historical...)}
}
func validRulesLanguage(lang string) bool { return lang == "ru" || lang == "en" }
func (s *PrivateRulesService) Get(ctx context.Context, user, game uuid.UUID, lang string) (model.PrivateRule, error) {
	if !validRulesLanguage(lang) {
		return model.PrivateRule{}, ErrPrivateRulesInput
	}
	return s.repo.Get(ctx, user, game, lang)
}
func (s *PrivateRulesService) Status(ctx context.Context, user, id uuid.UUID) (model.RulesIndexStatus, error) {
	rule, err := s.repo.GetByID(ctx, user, id)
	return rule.Status(), err
}
func (s *PrivateRulesService) Save(ctx context.Context, user, game uuid.UUID, lang, content, source string, expected int64) (model.PrivateRule, error) {
	if len(content) > 1<<20 {
		return model.PrivateRule{}, ErrPrivateRulesSize
	}
	if !validRulesLanguage(lang) || expected < 0 || !utf8.ValidString(content) || !utf8.ValidString(source) || strings.TrimSpace(content) == "" || utf8.RuneCountInString(source) > 500 {
		return model.PrivateRule{}, ErrPrivateRulesInput
	}
	if strings.TrimSpace(source) == "" {
		source = "manual"
	}
	return s.repo.Save(ctx, user, game, lang, content, source, expected)
}
func (s *PrivateRulesService) Delete(ctx context.Context, user, game uuid.UUID, lang string, expected int64) error {
	if !validRulesLanguage(lang) || expected < 1 {
		return ErrPrivateRulesInput
	}
	return s.repo.Delete(ctx, user, game, lang, expected)
}
func (s *PrivateRulesService) Reindex(ctx context.Context, user, id uuid.UUID, expected int64, key uuid.UUID) (model.RulesIndexStatus, error) {
	if expected < 1 || id == uuid.Nil || key == uuid.Nil {
		return model.RulesIndexStatus{}, ErrPrivateRulesInput
	}
	return s.repo.Reindex(ctx, user, id, expected, key)
}
func rulesScope(rule model.PrivateRule, revision int64) RulesScope {
	return RulesScope{rule.UserID, rule.GameID, rule.ID, rule.Language, revision}
}

// Retrieve consults authoritative metadata before AND after the embedding call.
// A concurrent delete/replacement/freeze cannot leak stale chunks or provenance.
func (s *PrivateRulesService) Retrieve(ctx context.Context, user, game uuid.UUID, lang, query string, topK int) ([]PrivateDocument, error) {
	rule, err := s.Get(ctx, user, game, lang)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rule.ActiveRevision == nil {
		return nil, nil
	}
	docs, err := s.store.Search(ctx, rulesScope(rule, *rule.ActiveRevision), query, topK)
	if err != nil {
		return nil, err
	}
	current, err := s.repo.GetByID(ctx, user, rule.ID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if current.ActiveRevision == nil || *current.ActiveRevision != *rule.ActiveRevision {
		return nil, nil
	}
	return docs, nil
}
func (s *PrivateRulesService) Work(ctx context.Context) error {
	owner, err := s.repo.NextOwner(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		return s.repo.CleanReplays(ctx)
	}
	if err != nil {
		return err
	}
	return s.repo.WithOwnerLock(ctx, owner, false, func() error {
		deleted, err := s.repo.Deleted(ctx, owner)
		if err != nil {
			return err
		}
		for _, id := range deleted {
			if err = s.store.DeleteRule(ctx, owner, id); err != nil {
				return err
			}
			if err = s.repo.PurgeDeleted(ctx, owner, id); err != nil {
				return err
			}
		}
		rule, err := s.repo.Claim(ctx, owner)
		if errors.Is(err, repository.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		scope := rulesScope(rule, rule.Revision)
		chunks := chunkText(rule.Content, defaultChunkSize, defaultChunkOverlap)
		docs := make([]PrivateDocument, len(chunks))
		for i, chunk := range chunks {
			docs[i] = PrivateDocument{fmt.Sprintf("%s:%d:%d", rule.ID, rule.Revision, i), chunk, scope}
		}
		indexErr := s.store.Replace(ctx, scope, docs)
		current, err := s.repo.Finish(ctx, rule, indexErr == nil)
		if err != nil {
			return err
		}
		if !current {
			if err = s.store.Prune(ctx, owner, rule.ID, activeOrZero(rule)); err != nil {
				return err
			}
		} else {
			live, err := s.repo.GetByID(ctx, owner, rule.ID)
			if errors.Is(err, repository.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if err = s.store.Prune(ctx, owner, rule.ID, activeOrZero(live)); err != nil {
				return err
			}
		}
		if !current {
			return nil
		}
		return indexErr
	})
}
func activeOrZero(rule model.PrivateRule) int64 {
	if rule.ActiveRevision == nil {
		return 0
	}
	return *rule.ActiveRevision
}
func (s *PrivateRulesService) CleanupOwner(ctx context.Context, user uuid.UUID) error {
	return s.repo.WithOwnerLock(ctx, user, true, func() error {
		for _, store := range s.cleanupStores {
			if err := store.DeleteOwner(ctx, user); err != nil {
				return err
			}
		}
		return s.repo.DeleteOwner(ctx, user)
	})
}
