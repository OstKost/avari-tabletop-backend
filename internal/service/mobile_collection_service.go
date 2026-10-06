package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
)

var ErrCollectionInput = errors.New("invalid collection input")
var ErrBGGUnavailable = errors.New("BGG unavailable")

type MobileCollectionService struct {
	repo      repository.MobileCollectionRepository
	games     *GameService
	cursorKey []byte
	limiter   *AuthLimiter
}

func NewMobileCollectionService(repo repository.MobileCollectionRepository, games *GameService, secret string) *MobileCollectionService {
	key := sha256.Sum256([]byte("avari/collection/cursor/v1:" + secret))
	return &MobileCollectionService{repo, games, key[:], NewAuthLimiter()}
}

type SearchItemDTO struct {
	BGGID         int        `json:"bgg_id"`
	Name          string     `json:"name"`
	YearPublished int        `json:"year_published"`
	InCollection  bool       `json:"in_collection"`
	GameID        *uuid.UUID `json:"game_id"`
}
type SearchPageDTO struct {
	Items     []SearchItemDTO `json:"items"`
	Truncated bool            `json:"truncated"`
}
type GameDetailResponseDTO struct {
	Game       model.GameDetailDTO       `json:"game"`
	Collection *model.CollectionEntryDTO `json:"collection"`
	Stale      bool                      `json:"stale"`
}
type CollectionPageDTO struct {
	Items      []model.CollectionEntryDTO `json:"items"`
	NextCursor *string                    `json:"next_cursor"`
}
type CollectionSnapshotDTO struct {
	SnapshotID uuid.UUID                  `json:"snapshot_id"`
	CapturedAt time.Time                  `json:"captured_at"`
	ExpiresAt  time.Time                  `json:"expires_at"`
	Items      []model.CollectionEntryDTO `json:"items"`
	NextCursor *string                    `json:"next_cursor"`
	Complete   bool                       `json:"complete"`
}

func (s *MobileCollectionService) Search(ctx context.Context, user uuid.UUID, q string) (SearchPageDTO, error) {
	q = strings.TrimSpace(q)
	if !utf8.ValidString(q) || utf8.RuneCountInString(q) < 2 || utf8.RuneCountInString(q) > 120 {
		return SearchPageDTO{}, ErrCollectionInput
	}
	if !s.limiter.Allow("bgg:"+user.String(), 30, time.Minute, time.Now()) {
		return SearchPageDTO{}, ErrAuthRateLimit
	}
	results, err := s.games.SearchBGG(ctx, q)
	if err != nil {
		return SearchPageDTO{}, err
	}
	out := SearchPageDTO{Items: make([]SearchItemDTO, 0), Truncated: len(results) > 100}
	if len(results) > 100 {
		results = results[:100]
	}
	ids := make([]int, 0, len(results))
	for _, r := range results {
		if r.BGGID < 1 || r.BGGID > 2147483647 || r.YearPublished < 0 {
			return out, ErrBGGUnavailable
		}
		ids = append(ids, r.BGGID)
	}
	members, err := s.repo.Membership(ctx, user, ids)
	if err != nil {
		return out, err
	}
	for _, r := range results {
		item := SearchItemDTO{BGGID: r.BGGID, Name: r.Name, YearPublished: r.YearPublished}
		if id, ok := members[r.BGGID]; ok {
			item.InCollection = true
			item.GameID = &id
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}
func (s *MobileCollectionService) Detail(ctx context.Context, user uuid.UUID, id int) (GameDetailResponseDTO, error) {
	if id < 1 || id > 2147483647 {
		return GameDetailResponseDTO{}, ErrCollectionInput
	}
	if !s.limiter.Allow("bgg:"+user.String(), 30, time.Minute, time.Now()) {
		return GameDetailResponseDTO{}, ErrAuthRateLimit
	}
	game, err := s.games.GetOrFetch(ctx, id)
	if err != nil {
		return GameDetailResponseDTO{}, err
	}
	out := GameDetailResponseDTO{Game: model.GameDetailDTO{GameSummaryDTO: model.GameSummary(game), Description: game.Description, ImageURL: game.ImageURL, FetchedAt: game.FetchedAt}, Stale: time.Since(game.FetchedAt) >= cacheTTL}
	entry, err := s.repo.Get(ctx, user, game.ID)
	if err == nil {
		out.Collection = &entry
	} else if !errors.Is(err, repository.ErrNotFound) {
		return out, err
	}
	return out, nil
}
func (s *MobileCollectionService) Get(ctx context.Context, user, game uuid.UUID) (model.CollectionEntryDTO, error) {
	return s.repo.Get(ctx, user, game)
}
func (s *MobileCollectionService) Mutate(ctx context.Context, user, game uuid.UUID, version int64, p *model.CollectionPatch) (model.CollectionEntryDTO, error) {
	if p != nil {
		if p.Status == nil && !p.HasRating && p.Notes == nil {
			return model.CollectionEntryDTO{}, ErrCollectionInput
		}
		if p.Status != nil && !isValidStatus(*p.Status) {
			return model.CollectionEntryDTO{}, ErrInvalidCollectionStatus
		}
		if p.HasRating && p.Rating != nil && (*p.Rating < 1 || *p.Rating > 10) {
			return model.CollectionEntryDTO{}, ErrInvalidCollectionRating
		}
		if p.Notes != nil && (!utf8.ValidString(*p.Notes) || utf8.RuneCountInString(*p.Notes) > 10000) {
			return model.CollectionEntryDTO{}, ErrCollectionInput
		}
	}
	return s.repo.Mutate(ctx, user, game, version, p)
}
func (s *MobileCollectionService) Add(ctx context.Context, user, key uuid.UUID, bggID int, status model.CollectionStatus) (repository.CollectionAddResult, error) {
	if bggID < 1 || bggID > 2147483647 || key == uuid.Nil || !isValidStatus(status) {
		return repository.CollectionAddResult{}, ErrCollectionInput
	}
	body, _ := json.Marshal(struct {
		ID     int
		Status model.CollectionStatus
	}{bggID, status})
	hash := sha256.Sum256(body)
	return s.repo.Add(ctx, user, key, hex.EncodeToString(hash[:]), status, func(ctx context.Context, gameRepo repository.GameRepository) (model.Game, error) {
		if !s.limiter.Allow("bgg:"+user.String(), 30, time.Minute, time.Now()) {
			return model.Game{}, ErrAuthRateLimit
		}
		return s.games.getOrFetch(ctx, bggID, gameRepo)
	})
}

type collectionCursor struct {
	Kind     string                 `json:"k"`
	User     uuid.UUID              `json:"u"`
	Filter   model.CollectionFilter `json:"f"`
	Offset   int                    `json:"o"`
	Limit    int                    `json:"l"`
	Snapshot uuid.UUID              `json:"s"`
	Expires  int64                  `json:"e"`
}

func (s *MobileCollectionService) encodeCursor(c collectionCursor) *string {
	raw, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write(raw)
	token := base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return &token
}
func (s *MobileCollectionService) decodeCursor(token string) (collectionCursor, error) {
	var c collectionCursor
	if len(token) > 2048 {
		return c, ErrCollectionInput
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return c, ErrCollectionInput
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return c, ErrCollectionInput
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, ErrCollectionInput
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return c, ErrCollectionInput
	}
	if json.Unmarshal(raw, &c) != nil || c.Offset < 0 || c.Offset > 1000000 || c.Limit < 1 || c.Limit > 100 {
		return c, ErrCollectionInput
	}
	return c, nil
}
func (s *MobileCollectionService) List(ctx context.Context, user uuid.UUID, f model.CollectionFilter, limit int, token string) (CollectionPageDTO, error) {
	f.Query = strings.ToLower(strings.TrimSpace(f.Query))
	if f.Sort == "" {
		f.Sort = "newest"
	}
	if !utf8.ValidString(f.Query) || utf8.RuneCountInString(f.Query) > 120 || (f.Status != "" && !isValidStatus(f.Status)) || (f.Sort != "newest" && f.Sort != "name" && f.Sort != "rating") || limit < 1 || limit > 100 {
		return CollectionPageDTO{}, ErrCollectionInput
	}
	c := collectionCursor{Kind: "list", User: user, Filter: f, Limit: limit, Expires: time.Now().Add(24 * time.Hour).Unix()}
	if token != "" {
		var err error
		c, err = s.decodeCursor(token)
		if err != nil || c.Kind != "list" || c.User != user || c.Filter != f || c.Limit != limit || c.Expires <= time.Now().Unix() {
			return CollectionPageDTO{}, ErrCollectionInput
		}
	}
	items, err := s.repo.List(ctx, user, f, limit+1, c.Offset)
	if err != nil {
		return CollectionPageDTO{}, err
	}
	out := CollectionPageDTO{Items: items}
	if len(items) > limit {
		out.Items = items[:limit]
		c.Offset += limit
		out.NextCursor = s.encodeCursor(c)
	}
	return out, nil
}
func (s *MobileCollectionService) Snapshot(ctx context.Context, user uuid.UUID, limit int, token string) (CollectionSnapshotDTO, error) {
	if limit < 1 || limit > 100 {
		return CollectionSnapshotDTO{}, ErrCollectionInput
	}
	c := collectionCursor{Kind: "snapshot", User: user, Limit: limit}
	if token != "" {
		var err error
		c, err = s.decodeCursor(token)
		if err != nil || c.Kind != "snapshot" || c.User != user || c.Limit != limit || c.Snapshot == uuid.Nil {
			return CollectionSnapshotDTO{}, ErrCollectionInput
		}
		if c.Expires <= time.Now().Unix() {
			return CollectionSnapshotDTO{}, repository.ErrSnapshotExpired
		}
	}
	snap, err := s.repo.Snapshot(ctx, user, c.Snapshot)
	if err != nil {
		return CollectionSnapshotDTO{}, err
	}
	if c.Offset > len(snap.Items) {
		return CollectionSnapshotDTO{}, ErrCollectionInput
	}
	end := min(c.Offset+limit, len(snap.Items))
	out := CollectionSnapshotDTO{SnapshotID: snap.ID, CapturedAt: snap.CapturedAt, ExpiresAt: snap.ExpiresAt, Items: snap.Items[c.Offset:end], Complete: end == len(snap.Items)}
	if !out.Complete {
		c.Snapshot = snap.ID
		c.Offset = end
		c.Expires = snap.ExpiresAt.Unix()
		out.NextCursor = s.encodeCursor(c)
	}
	return out, nil
}
func (s *MobileCollectionService) Cleanup(ctx context.Context) error { return s.repo.Cleanup(ctx) }
