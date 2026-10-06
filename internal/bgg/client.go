package bgg

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBaseURL = "https://boardgamegeek.com/xmlapi2"

var ErrGameNotFound = errors.New("game not found on BGG")

type Client struct {
	httpClient  *http.Client
	baseURL     string
	token       string
	rateLimiter <-chan time.Time
}

func NewClient() *Client { return NewClientWithToken("") }

// The application token belongs to the server, never to mobile DTOs or URLs.
func NewClientWithToken(token string) *Client {
	return &Client{
		httpClient:  &http.Client{Timeout: 15 * time.Second, CheckRedirect: noRedirect},
		baseURL:     defaultBaseURL,
		token:       strings.TrimSpace(token),
		rateLimiter: time.Tick(600 * time.Millisecond), // ~1.6 req/sec, under 2/sec limit
	}
}

// NewClientWithBaseURL is used in tests to override the base URL.
func NewClientWithBaseURL(baseURL string) *Client {
	return &Client{
		httpClient:  &http.Client{Timeout: 15 * time.Second, CheckRedirect: noRedirect},
		baseURL:     baseURL,
		rateLimiter: time.Tick(1 * time.Millisecond),
	}
}

// Do not forward the server application token to a redirect destination.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

type SearchResult struct {
	BGGID         int
	Name          string
	YearPublished int
}

type GameDetail struct {
	BGGID         int
	Name          string
	Description   string
	ImageURL      string
	ThumbnailURL  string
	MinPlayers    int
	MaxPlayers    int
	PlayingTime   int
	YearPublished int
	AverageRating float64
}

// Search searches BGG for board games by name.
func (c *Client) Search(ctx context.Context, query string) ([]SearchResult, error) {
	if err := c.waitForRateLimit(ctx); err != nil {
		return nil, fmt.Errorf("wait for search rate limit: %w", err)
	}

	endpoint := fmt.Sprintf("%s/search?query=%s&type=boardgame",
		c.baseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build search request: %w", err)
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("BGG search returned %d", resp.StatusCode)
	}

	var result xmlSearchResult
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}

	out := make([]SearchResult, 0, len(result.Items))
	for _, item := range result.Items {
		name := ""
		for _, n := range item.Names {
			if n.Type == "primary" {
				name = n.Value
				break
			}
		}
		if name == "" && len(item.Names) > 0 {
			name = item.Names[0].Value
		}
		out = append(out, SearchResult{
			BGGID:         item.ID,
			Name:          name,
			YearPublished: item.YearPublished.Value,
		})
	}
	return out, nil
}

// GetGame fetches full game details by BGG ID.
func (c *Client) GetGame(ctx context.Context, bggID int) (GameDetail, error) {
	if err := c.waitForRateLimit(ctx); err != nil {
		return GameDetail{}, fmt.Errorf("wait for game rate limit: %w", err)
	}

	endpoint := fmt.Sprintf("%s/thing?id=%d&stats=1", c.baseURL, bggID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return GameDetail{}, fmt.Errorf("build game request: %w", err)
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return GameDetail{}, fmt.Errorf("game request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return GameDetail{}, fmt.Errorf("BGG thing returned %d", resp.StatusCode)
	}

	var result xmlThingResult
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return GameDetail{}, fmt.Errorf("decode game response: %w", err)
	}

	if len(result.Items) == 0 {
		return GameDetail{}, fmt.Errorf("game %d: %w", bggID, ErrGameNotFound)
	}

	item := result.Items[0]
	name := ""
	for _, n := range item.Names {
		if n.Type == "primary" {
			name = n.Value
			break
		}
	}

	var avgRating float64
	if item.Statistics.Ratings.Average.Value != 0 {
		avgRating = item.Statistics.Ratings.Average.Value
	}

	return GameDetail{
		BGGID:         item.ID,
		Name:          name,
		Description:   html.UnescapeString(item.Description),
		ImageURL:      item.Image,
		ThumbnailURL:  item.Thumbnail,
		MinPlayers:    item.MinPlayers.Value,
		MaxPlayers:    item.MaxPlayers.Value,
		PlayingTime:   item.PlayingTime.Value,
		YearPublished: item.YearPublished.Value,
		AverageRating: avgRating,
	}, nil
}

func (c *Client) waitForRateLimit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.rateLimiter:
		return ctx.Err()
	}
}

// --- XML structs ---

type xmlSearchResult struct {
	XMLName xml.Name        `xml:"items"`
	Items   []xmlSearchItem `xml:"item"`
}

type xmlSearchItem struct {
	ID            int       `xml:"id,attr"`
	Names         []xmlName `xml:"name"`
	YearPublished xmlIntVal `xml:"yearpublished"`
}

type xmlName struct {
	Type  string `xml:"type,attr"`
	Value string `xml:"value,attr"`
}

type xmlIntVal struct {
	Value int `xml:"value,attr"`
}

type xmlThingResult struct {
	XMLName xml.Name       `xml:"items"`
	Items   []xmlThingItem `xml:"item"`
}

type xmlThingItem struct {
	ID            int           `xml:"id,attr"`
	Names         []xmlName     `xml:"name"`
	Description   string        `xml:"description"`
	Image         string        `xml:"image"`
	Thumbnail     string        `xml:"thumbnail"`
	MinPlayers    xmlIntVal     `xml:"minplayers"`
	MaxPlayers    xmlIntVal     `xml:"maxplayers"`
	PlayingTime   xmlIntVal     `xml:"playingtime"`
	YearPublished xmlIntVal     `xml:"yearpublished"`
	Statistics    xmlStatistics `xml:"statistics"`
}

type xmlStatistics struct {
	Ratings xmlRatings `xml:"ratings"`
}

type xmlRatings struct {
	Average xmlFloatVal `xml:"average"`
}

type xmlFloatVal struct {
	Value float64 `xml:"value,attr"`
}
