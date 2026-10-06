package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type collectionTestRepository struct {
	repository.CollectionRepository
	items  []model.CollectionWithGame
	err    error
	writes int
	reads  int
	userID uuid.UUID
	status model.CollectionStatus
	rating *int
}

func (r *collectionTestRepository) Add(_ context.Context, userID, gameID uuid.UUID, status model.CollectionStatus) (model.Collection, error) {
	r.writes++
	return model.Collection{UserID: userID, GameID: gameID, Status: status}, r.err
}

func (r *collectionTestRepository) Update(_ context.Context, userID, gameID uuid.UUID, status model.CollectionStatus, rating *int, notes string) (model.Collection, error) {
	r.writes++
	r.rating = rating
	return model.Collection{UserID: userID, GameID: gameID, Status: status, Rating: rating, Notes: notes}, r.err
}

func (r *collectionTestRepository) ListByUser(_ context.Context, userID uuid.UUID, status model.CollectionStatus) ([]model.CollectionWithGame, error) {
	r.reads++
	r.userID, r.status = userID, status
	return r.items, r.err
}

func TestCollectionRejectsInvalidWrites(t *testing.T) {
	for _, status := range []model.CollectionStatus{"", "unknown"} {
		t.Run(string(status), func(t *testing.T) {
			repo := &collectionTestRepository{}
			svc := NewCollectionService(repo)
			_, err := svc.Add(context.Background(), uuid.New(), uuid.New(), status)
			require.ErrorIs(t, err, ErrInvalidCollectionStatus)
			_, err = svc.Update(context.Background(), uuid.New(), uuid.New(), status, nil, "")
			require.ErrorIs(t, err, ErrInvalidCollectionStatus)
			assert.Zero(t, repo.writes)
		})
	}
	for _, rating := range []int{-1, 0, 11} {
		repo := &collectionTestRepository{}
		_, err := NewCollectionService(repo).Update(context.Background(), uuid.New(), uuid.New(), model.StatusOwned, &rating, "")
		require.ErrorIs(t, err, ErrInvalidCollectionRating)
		assert.Zero(t, repo.writes)
	}
}

func TestCollectionAcceptsValidWritesAndClearsRating(t *testing.T) {
	userID, gameID := uuid.New(), uuid.New()
	for _, status := range []model.CollectionStatus{model.StatusOwned, model.StatusWishlist, model.StatusPlayed} {
		repo := &collectionTestRepository{}
		svc := NewCollectionService(repo)
		added, err := svc.Add(context.Background(), userID, gameID, status)
		require.NoError(t, err)
		assert.Equal(t, userID, added.UserID)
		assert.Equal(t, gameID, added.GameID)
		assert.Equal(t, status, added.Status)
		for _, value := range []int{1, 10} {
			updated, err := svc.Update(context.Background(), userID, gameID, status, &value, "My notes")
			require.NoError(t, err)
			assert.Equal(t, &value, updated.Rating)
			assert.Equal(t, "My notes", updated.Notes)
		}
		_, err = svc.Update(context.Background(), userID, gameID, status, nil, "")
		require.NoError(t, err)
		assert.Nil(t, repo.rating)
	}
}

func TestCollectionPreservesRepositoryErrors(t *testing.T) {
	for _, repoErr := range []error{repository.ErrDuplicate, repository.ErrNotFound, errors.New("storage unavailable")} {
		repo := &collectionTestRepository{err: repoErr}
		svc := NewCollectionService(repo)
		_, err := svc.Add(context.Background(), uuid.New(), uuid.New(), model.StatusOwned)
		require.ErrorIs(t, err, repoErr)
		_, err = svc.Update(context.Background(), uuid.New(), uuid.New(), model.StatusOwned, nil, "")
		require.ErrorIs(t, err, repoErr)
	}
}

func TestCollectionBrowse(t *testing.T) {
	old, recent := time.Unix(1, 0), time.Unix(2, 0)
	rating := 8
	alpha := model.CollectionWithGame{Collection: model.Collection{GameID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), AddedAt: old}, Game: model.Game{Name: "Alpha"}}
	beta := model.CollectionWithGame{Collection: model.Collection{GameID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), AddedAt: recent, Rating: &rating}, Game: model.Game{Name: "beta"}}
	catan := model.CollectionWithGame{Collection: model.Collection{GameID: uuid.MustParse("00000000-0000-0000-0000-000000000003"), AddedAt: recent, Rating: &rating}, Game: model.Game{Name: "Колонизаторы"}}
	items := []model.CollectionWithGame{catan, alpha, beta}
	for _, tc := range []struct {
		name  string
		query string
		sort  string
		want  []string
	}{
		{"default", "", "", []string{"beta", "Колонизаторы", "Alpha"}},
		{"newest", "", "newest", []string{"beta", "Колонизаторы", "Alpha"}},
		{"name", "", "name", []string{"Alpha", "beta", "Колонизаторы"}},
		{"rating", "", "rating", []string{"beta", "Колонизаторы", "Alpha"}},
		{"unicode search", "  ОНИЗА  ", "name", []string{"Колонизаторы"}},
		{"latin search", " ALP ", "name", []string{"Alpha"}},
		{"no matches", "missing", "", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &collectionTestRepository{items: items}
			userID := uuid.New()
			got, err := NewCollectionService(repo).Browse(context.Background(), userID, CollectionBrowseOptions{Status: model.StatusWishlist, Query: tc.query, Sort: tc.sort})
			require.NoError(t, err)
			names := make([]string, 0, len(got))
			for _, item := range got {
				names = append(names, item.Game.Name)
			}
			assert.Equal(t, tc.want, names)
			assert.Equal(t, userID, repo.userID)
			assert.Equal(t, model.StatusWishlist, repo.status)
			assert.Equal(t, 1, repo.reads)
			assert.Equal(t, "Колонизаторы", repo.items[0].Game.Name, "sorting must not mutate repository-owned slices")
		})
	}
}

func TestCollectionBrowseFailures(t *testing.T) {
	for _, tc := range []struct {
		options CollectionBrowseOptions
		want    error
	}{
		{CollectionBrowseOptions{Sort: "random"}, ErrInvalidCollectionSort},
		{CollectionBrowseOptions{Status: "unknown"}, ErrInvalidCollectionStatus},
	} {
		repo := &collectionTestRepository{}
		_, err := NewCollectionService(repo).Browse(context.Background(), uuid.New(), tc.options)
		require.ErrorIs(t, err, tc.want)
		assert.Zero(t, repo.reads)
	}
	repoErr := errors.New("storage unavailable")
	_, err := NewCollectionService(&collectionTestRepository{err: repoErr}).Browse(context.Background(), uuid.New(), CollectionBrowseOptions{})
	require.ErrorIs(t, err, repoErr)
}

func TestCollectionBrowseRatingOrder(t *testing.T) {
	low, high := 1, 10
	items := []model.CollectionWithGame{
		{Collection: model.Collection{Rating: nil, AddedAt: time.Unix(100, 0)}, Game: model.Game{Name: "Unrated"}},
		{Collection: model.Collection{Rating: &low, AddedAt: time.Unix(90, 0)}, Game: model.Game{Name: "Low"}},
		{Collection: model.Collection{Rating: &high, AddedAt: time.Unix(1, 0)}, Game: model.Game{Name: "High"}},
	}
	got, err := NewCollectionService(&collectionTestRepository{items: items}).Browse(context.Background(), uuid.New(), CollectionBrowseOptions{Sort: "rating"})
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "High", got[0].Game.Name)
	assert.Equal(t, "Low", got[1].Game.Name)
	assert.Equal(t, "Unrated", got[2].Game.Name)
}
