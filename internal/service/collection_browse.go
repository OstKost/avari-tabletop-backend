package service

import (
	"context"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
)

type CollectionBrowseOptions struct {
	Status model.CollectionStatus
	Query  string
	Sort   string
}

// Browse searches and sorts the current user's collection without external calls.
func (s *CollectionService) Browse(ctx context.Context, userID uuid.UUID, options CollectionBrowseOptions) ([]model.CollectionWithGame, error) {
	if options.Sort != "" && options.Sort != "newest" && options.Sort != "name" && options.Sort != "rating" {
		return nil, ErrInvalidCollectionSort
	}
	items, err := s.List(ctx, userID, options.Status)
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(strings.TrimSpace(options.Query))
	results := make([]model.CollectionWithGame, 0, len(items))
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.Game.Name), query) {
			results = append(results, item)
		}
	}
	slices.SortFunc(results, func(a, b model.CollectionWithGame) int {
		switch options.Sort {
		case "name":
			if order := strings.Compare(strings.ToLower(a.Game.Name), strings.ToLower(b.Game.Name)); order != 0 {
				return order
			}
		case "rating":
			ar, br := 0, 0
			if a.Rating != nil {
				ar = *a.Rating
			}
			if b.Rating != nil {
				br = *b.Rating
			}
			if ar > br {
				return -1
			}
			if ar < br {
				return 1
			}
		}
		if order := b.AddedAt.Compare(a.AddedAt); order != 0 {
			return order
		}
		return strings.Compare(a.GameID.String(), b.GameID.String())
	})
	return results, nil
}
