package bgg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearch(t *testing.T) {
	xml := `<?xml version="1.0" encoding="utf-8"?>
<items total="2" termsofuse="https://boardgamegeek.com/xmlapi/termsofuse">
  <item type="boardgame" id="13">
    <name type="primary" sortindex="5" value="Catan"/>
    <yearpublished value="1995"/>
  </item>
  <item type="boardgame" id="42">
    <name type="primary" sortindex="1" value="Twilight Imperium"/>
    <yearpublished value="1997"/>
  </item>
</items>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/xmlapi2/search", r.URL.Path)
		assert.Equal(t, "catan", r.URL.Query().Get("query"))
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(xml))
	}))
	defer srv.Close()

	client := NewClientWithBaseURL(srv.URL + "/xmlapi2")
	results, err := client.Search(context.Background(), "catan")

	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, 13, results[0].BGGID)
	assert.Equal(t, "Catan", results[0].Name)
	assert.Equal(t, 1995, results[0].YearPublished)
	assert.Equal(t, 42, results[1].BGGID)
	assert.Equal(t, "Twilight Imperium", results[1].Name)
}

func TestSearch_Empty(t *testing.T) {
	xml := `<?xml version="1.0" encoding="utf-8"?>
<items total="0" termsofuse="https://boardgamegeek.com/xmlapi/termsofuse">
</items>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(xml))
	}))
	defer srv.Close()

	client := NewClientWithBaseURL(srv.URL + "/xmlapi2")
	results, err := client.Search(context.Background(), "xyznotfound")

	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestSearch_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := NewClientWithBaseURL(srv.URL + "/xmlapi2")
	_, err := client.Search(context.Background(), "catan")
	assert.Error(t, err)
}

func TestGetGame(t *testing.T) {
	xml := `<?xml version="1.0" encoding="utf-8"?>
<items termsofuse="https://boardgamegeek.com/xmlapi/termsofuse">
  <item type="boardgame" id="13">
    <name type="primary" sortindex="5" value="Catan"/>
    <description>A trading game where players build settlements.</description>
    <image>//cf.geekdo-images.com/thumb/img/abc.png</image>
    <thumbnail>//cf.geekdo-images.com/thumb/img/abc_thumb.png</thumbnail>
    <minplayers value="3"/>
    <maxplayers value="4"/>
    <playingtime value="90"/>
    <yearpublished value="1995"/>
    <statistics>
      <ratings>
        <average value="7.1532"/>
      </ratings>
    </statistics>
  </item>
</items>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/xmlapi2/thing", r.URL.Path)
		assert.Equal(t, "13", r.URL.Query().Get("id"))
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(xml))
	}))
	defer srv.Close()

	client := NewClientWithBaseURL(srv.URL + "/xmlapi2")
	game, err := client.GetGame(context.Background(), 13)

	require.NoError(t, err)
	assert.Equal(t, 13, game.BGGID)
	assert.Equal(t, "Catan", game.Name)
	assert.Equal(t, "A trading game where players build settlements.", game.Description)
	assert.Equal(t, 3, game.MinPlayers)
	assert.Equal(t, 4, game.MaxPlayers)
	assert.Equal(t, 90, game.PlayingTime)
	assert.Equal(t, 1995, game.YearPublished)
	assert.InDelta(t, 7.15, game.AverageRating, 0.1)
}

func TestGetGame_NotFound(t *testing.T) {
	xml := `<?xml version="1.0" encoding="utf-8"?>
<items termsofuse="https://boardgamegeek.com/xmlapi/termsofuse">
</items>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(xml))
	}))
	defer srv.Close()

	client := NewClientWithBaseURL(srv.URL + "/xmlapi2")
	_, err := client.GetGame(context.Background(), 99999)
	assert.ErrorIs(t, err, ErrGameNotFound)
}
