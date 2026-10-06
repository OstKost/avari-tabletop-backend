package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ostkost/avari-tabletop-backend/internal/bgg"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
)

const cacheTTL = 7 * 24 * time.Hour

type GameService struct {
	gameRepo  repository.GameRepository
	bggClient *bgg.Client
}

func NewGameService(gameRepo repository.GameRepository, bggClient *bgg.Client) *GameService {
	return &GameService{
		gameRepo:  gameRepo,
		bggClient: bggClient,
	}
}

func (s *GameService) SearchBGG(ctx context.Context, query string) ([]bgg.SearchResult, error) {
	query = strings.TrimSpace(query)
	if utf8.RuneCountInString(query) < 2 {
		return nil, fmt.Errorf("query too short")
	}
	results, err := s.bggClient.Search(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBGGUnavailable, err)
	}
	return results, nil
}

// GetOrFetch returns a game from DB if cached and fresh, otherwise fetches from BGG.
func (s *GameService) GetOrFetch(ctx context.Context, bggID int) (model.Game, error) {
	return s.getOrFetch(ctx, bggID, s.gameRepo)
}

// The mobile add transaction supplies its connection, avoiding nested pool acquisition.
func (s *GameService) getOrFetch(ctx context.Context, bggID int, repo repository.GameRepository) (model.Game, error) {
	existing, err := repo.GetByBGGID(ctx, bggID)
	if err == nil {
		if time.Since(existing.FetchedAt) < cacheTTL {
			return existing, nil
		}
	} else if !errors.Is(err, repository.ErrNotFound) {
		return model.Game{}, fmt.Errorf("check cached game: %w", err)
	}

	detail, err := s.bggClient.GetGame(ctx, bggID)
	if err != nil {
		if errors.Is(err, bgg.ErrGameNotFound) {
			return model.Game{}, fmt.Errorf("game %d: %w", bggID, repository.ErrNotFound)
		}
		if !errors.Is(err, repository.ErrNotFound) && existing.BGGID != 0 {
			// BGG is down but we have stale data - return it
			return existing, nil
		}
		return model.Game{}, fmt.Errorf("%w: %w", ErrBGGUnavailable, err)
	}

	g := model.Game{
		BGGID:         detail.BGGID,
		Name:          detail.Name,
		Description:   detail.Description,
		ImageURL:      detail.ImageURL,
		ThumbnailURL:  detail.ThumbnailURL,
		MinPlayers:    detail.MinPlayers,
		MaxPlayers:    detail.MaxPlayers,
		PlayingTime:   detail.PlayingTime,
		YearPublished: detail.YearPublished,
		AverageRating: detail.AverageRating,
	}

	saved, err := repo.Upsert(ctx, g)
	if err != nil {
		return model.Game{}, fmt.Errorf("save game: %w", err)
	}
	return saved, nil
}
