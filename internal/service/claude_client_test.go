package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeClient_Stream(t *testing.T) {
	sseResponse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-6","stop_reason":null}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: ping
data: {"type":"ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}

event: message_stop
data: {"type":"message_stop"}

`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/messages", r.URL.Path)
		assert.NotEmpty(t, r.Header.Get("x-api-key"))

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(sseResponse))
	}))
	defer srv.Close()

	// Override the API URL for testing by creating client with modified URL
	client := &ClaudeClient{
		apiKey:     "test-key",
		httpClient: http.DefaultClient,
	}

	// Use a wrapper that redirects to our test server
	origURL := claudeAPIURL
	// We can't change the const, so test via the stream method indirectly
	// by pointing to a test server that mimics the Claude API
	_ = origURL

	// Instead, test the SSE parsing logic directly
	chunks, errc := streamFromTestServer(t, srv.URL+"/v1/messages", client)

	var received []string
	for chunk := range chunks {
		received = append(received, chunk)
	}
	require.NoError(t, <-errc)
	assert.Equal(t, []string{"Hello", " world"}, received)
}

// streamFromTestServer sends a request to the given URL and streams chunks.
func streamFromTestServer(t *testing.T, url string, client *ClaudeClient) (<-chan string, <-chan error) {
	t.Helper()
	chunks := make(chan string, 64)
	errc := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errc)
		msgs := []claudeMessage{{Role: "user", Content: "test"}}
		// Temporarily override URL
		origURL := claudeAPIURL
		err := streamToURL(client, context.Background(), url, "system prompt", msgs, chunks)
		_ = origURL
		if err != nil {
			errc <- err
		}
	}()
	return chunks, errc
}

func TestClaudeClient_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"type":"authentication_error","message":"Invalid API key"}}`))
	}))
	defer srv.Close()

	client := &ClaudeClient{
		apiKey:     "bad-key",
		httpClient: http.DefaultClient,
	}

	chunks := make(chan string, 64)
	err := streamToURL(client, context.Background(), srv.URL+"/v1/messages", "system", []claudeMessage{{Role: "user", Content: "hi"}}, chunks)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}
