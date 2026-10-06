package model

import (
	"time"

	"github.com/google/uuid"
)

type User struct {
	AuthVersion  int64     `db:"auth_version"`
	ID           uuid.UUID `db:"id"`
	Email        string    `db:"email"`
	Username     string    `db:"username"`
	PasswordHash string    `db:"password_hash"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

type Game struct {
	ID            uuid.UUID `db:"id"`
	BGGID         int       `db:"bgg_id"`
	Name          string    `db:"name"`
	Description   string    `db:"description"`
	ImageURL      string    `db:"image_url"`
	ThumbnailURL  string    `db:"thumbnail_url"`
	MinPlayers    int       `db:"min_players"`
	MaxPlayers    int       `db:"max_players"`
	PlayingTime   int       `db:"playing_time"`
	YearPublished int       `db:"year_published"`
	AverageRating float64   `db:"average_rating"`
	FetchedAt     time.Time `db:"fetched_at"`
}

type CollectionStatus string

const (
	StatusOwned    CollectionStatus = "owned"
	StatusWishlist CollectionStatus = "wishlist"
	StatusPlayed   CollectionStatus = "played"
)

type Collection struct {
	Version   int64            `db:"version"`
	UpdatedAt time.Time        `db:"updated_at"`
	ID        uuid.UUID        `db:"id"`
	UserID    uuid.UUID        `db:"user_id"`
	GameID    uuid.UUID        `db:"game_id"`
	Status    CollectionStatus `db:"status"`
	Rating    *int             `db:"rating"`
	Notes     string           `db:"notes"`
	AddedAt   time.Time        `db:"added_at"`
}

type CollectionWithGame struct {
	Collection
	Game Game
}

type ChatRole string

const (
	RoleUser      ChatRole = "user"
	RoleAssistant ChatRole = "assistant"
)

type ChatMessage struct {
	ID        uuid.UUID `db:"id"`
	UserID    uuid.UUID `db:"user_id"`
	GameID    uuid.UUID `db:"game_id"`
	Role      ChatRole  `db:"role"`
	Content   string    `db:"content"`
	CreatedAt time.Time `db:"created_at"`
}
