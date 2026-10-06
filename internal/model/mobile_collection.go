package model

import (
	"github.com/google/uuid"
	"time"
)

type GameSummaryDTO struct {
	ID            uuid.UUID `json:"id"`
	BGGID         int       `json:"bgg_id"`
	Name          string    `json:"name"`
	ThumbnailURL  string    `json:"thumbnail_url"`
	YearPublished int       `json:"year_published"`
	MinPlayers    int       `json:"min_players"`
	MaxPlayers    int       `json:"max_players"`
	PlayingTime   int       `json:"playing_time"`
	AverageRating float64   `json:"average_rating"`
}
type GameDetailDTO struct {
	GameSummaryDTO
	Description string    `json:"description"`
	ImageURL    string    `json:"image_url"`
	FetchedAt   time.Time `json:"fetched_at"`
}
type CollectionEntryDTO struct {
	ID        uuid.UUID        `json:"id"`
	GameID    uuid.UUID        `json:"game_id"`
	Status    CollectionStatus `json:"status"`
	Rating    *int             `json:"rating"`
	Notes     string           `json:"notes"`
	AddedAt   time.Time        `json:"added_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	Version   int64            `json:"version"`
	Game      GameSummaryDTO   `json:"game"`
}

func GameSummary(g Game) GameSummaryDTO {
	return GameSummaryDTO{g.ID, g.BGGID, g.Name, g.ThumbnailURL, g.YearPublished, g.MinPlayers, g.MaxPlayers, g.PlayingTime, g.AverageRating}
}
func CollectionEntry(c Collection, g Game) CollectionEntryDTO {
	return CollectionEntryDTO{c.ID, c.GameID, c.Status, c.Rating, c.Notes, c.AddedAt, c.UpdatedAt, c.Version, GameSummary(g)}
}

type CollectionFilter struct {
	Status      CollectionStatus
	Query, Sort string
}
type CollectionPatch struct {
	Status    *CollectionStatus
	Rating    *int
	HasRating bool
	Notes     *string
}
type CollectionSnapshot struct {
	ID                    uuid.UUID
	UserID                uuid.UUID
	CapturedAt, ExpiresAt time.Time
	Items                 []CollectionEntryDTO
}
