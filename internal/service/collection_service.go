package service

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
)

type CollectionService struct {
	collectionRepo repository.CollectionRepository
}

var (
	ErrInvalidCollectionStatus = errors.New("invalid collection status")
	ErrInvalidCollectionRating = errors.New("rating must be 1-10")
	ErrInvalidCollectionSort   = errors.New("invalid collection sort")
)

func NewCollectionService(collectionRepo repository.CollectionRepository) *CollectionService {
	return &CollectionService{collectionRepo: collectionRepo}
}

func (s *CollectionService) Add(ctx context.Context, userID, gameID uuid.UUID, status model.CollectionStatus) (model.Collection, error) {
	if !isValidStatus(status) {
		return model.Collection{}, ErrInvalidCollectionStatus
	}
	c, err := s.collectionRepo.Add(ctx, userID, gameID, status)
	if err != nil {
		if errors.Is(err, repository.ErrDuplicate) {
			return model.Collection{}, fmt.Errorf("game already in collection: %w", err)
		}
		return model.Collection{}, err
	}
	return c, nil
}

func (s *CollectionService) Remove(ctx context.Context, userID, gameID uuid.UUID) error {
	return s.collectionRepo.Remove(ctx, userID, gameID)
}

func (s *CollectionService) Update(ctx context.Context, userID, gameID uuid.UUID, status model.CollectionStatus, rating *int, notes string) (model.Collection, error) {
	if !isValidStatus(status) {
		return model.Collection{}, ErrInvalidCollectionStatus
	}
	if rating != nil && (*rating < 1 || *rating > 10) {
		return model.Collection{}, ErrInvalidCollectionRating
	}
	if !utf8.ValidString(notes) || utf8.RuneCountInString(notes) > 10000 {
		return model.Collection{}, ErrCollectionInput
	}
	return s.collectionRepo.Update(ctx, userID, gameID, status, rating, notes)
}

func (s *CollectionService) List(ctx context.Context, userID uuid.UUID, status model.CollectionStatus) ([]model.CollectionWithGame, error) {
	if status != "" && !isValidStatus(status) {
		return nil, ErrInvalidCollectionStatus
	}
	return s.collectionRepo.ListByUser(ctx, userID, status)
}

func (s *CollectionService) GetEntry(ctx context.Context, userID, gameID uuid.UUID) (model.Collection, error) {
	return s.collectionRepo.GetByUserAndGame(ctx, userID, gameID)
}

func isValidStatus(s model.CollectionStatus) bool {
	return s == model.StatusOwned || s == model.StatusWishlist || s == model.StatusPlayed
}
