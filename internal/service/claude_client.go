package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const claudeAPIURL = "https://api.anthropic.com/v1/messages"
const claudeModel = "claude-sonnet-4-6"

// claudeMessage is the internal wire type for the Anthropic API.
type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system"`
	Messages  []claudeMessage `json:"messages"`
	Stream    bool            `json:"stream"`
}

// ClaudeClient implements LLMProvider using the Anthropic Messages API.
type ClaudeClient struct {
	apiKey     string
	httpClient *http.Client
}

func NewClaudeClient(apiKey string) *ClaudeClient {
	return &ClaudeClient{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *ClaudeClient) Name() string { return "claude" }

// StreamMessage implements LLMProvider.
func (c *ClaudeClient) StreamMessage(ctx context.Context, systemPrompt string, messages []LLMMessage) (<-chan string, <-chan error) {
	chunks := make(chan string, 64)
	errc := make(chan error, 1)

	go func() {
		defer close(chunks)
		defer close(errc)

		wire := make([]claudeMessage, len(messages))
		for i, m := range messages {
			wire[i] = claudeMessage{Role: m.Role, Content: m.Content}
		}
		if err := c.stream(ctx, systemPrompt, wire, chunks); err != nil {
			errc <- err
		}
	}()

	return chunks, errc
}

func (c *ClaudeClient) stream(ctx context.Context, systemPrompt string, messages []claudeMessage, chunks chan<- string) error {
	payload := claudeRequest{
		Model:     claudeModel,
		MaxTokens: 2048,
		System:    systemPrompt,
		Messages:  messages,
		Stream:    true,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeAPIURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("claude request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("claude API error %d: %s", resp.StatusCode, string(b))
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event sseEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" {
			if event.Delta.Text != "" {
				select {
				case chunks <- event.Delta.Text:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
	return scanner.Err()
}

type sseEvent struct {
	Type  string   `json:"type"`
	Delta sseDelta `json:"delta"`
}

type sseDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
