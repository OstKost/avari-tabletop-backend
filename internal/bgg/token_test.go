package bgg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplicationTokenOnSearchAndDetails(t *testing.T) {
	const token = "synthetic-bgg-application-token"
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
		require.NotContains(t, r.URL.String(), token)
		switch r.URL.Path {
		case "/search":
			_, _ = w.Write([]byte(`<items><item id="13"><name type="primary" value="Catan"/></item></items>`))
		case "/thing":
			_, _ = w.Write([]byte(`<items><item id="13"><name type="primary" value="Catan"/></item></items>`))
		default:
			t.Errorf("unexpected provider path")
		}
	}))
	defer provider.Close()
	client := NewClientWithBaseURL(provider.URL)
	client.token = NewClientWithToken("  " + token + "  ").token
	results, err := client.Search(context.Background(), "catan")
	require.NoError(t, err)
	require.Len(t, results, 1)
	game, err := client.GetGame(context.Background(), 13)
	require.NoError(t, err)
	require.Equal(t, "Catan", game.Name)
	require.EqualValues(t, 2, calls.Load())
}
func TestTokenDoesNotFollowRedirectOrEnterError(t *testing.T) {
	const token = "synthetic-bgg-redirect-token"
	var followed atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1); w.WriteHeader(200) }))
	defer destination.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer provider.Close()
	client := NewClientWithBaseURL(provider.URL)
	client.token = token
	_, err := client.Search(context.Background(), "catan")
	require.Error(t, err)
	require.NotContains(t, err.Error(), token)
	_, err = client.GetGame(context.Background(), 13)
	require.Error(t, err)
	require.NotContains(t, err.Error(), token)
	require.Zero(t, followed.Load())
}
func TestMissingTokenSendsNoAuthorization(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer provider.Close()
	client := NewClientWithBaseURL(provider.URL)
	_, err := client.Search(context.Background(), "catan")
	require.ErrorContains(t, err, "401")
	require.Equal(t, defaultBaseURL, NewClientWithToken("synthetic").baseURL)
}
