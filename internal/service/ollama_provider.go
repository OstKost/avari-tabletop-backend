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

// OllamaProvider implements LLMProvider using Ollama's OpenAI-compatible API.
// Ollama exposes /v1/chat/completions when running locally.
type OllamaProvider struct {
	baseURL    string // e.g. "http://localhost:11434"
	model      string // e.g. "qwen3.5"
	httpClient *http.Client
}

func NewOllamaProvider(baseURL, model string) *OllamaProvider {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "qwen3.5"
	}
	return &OllamaProvider{
		baseURL:    strings.TrimRight(baseURL, "/"),
		model:      model,
		httpClient: &http.Client{Timeout: 180 * time.Second},
	}
}

func (p *OllamaProvider) Name() string { return "ollama:" + p.model }

// StreamMessage implements LLMProvider using /v1/chat/completions with stream=true.
func (p *OllamaProvider) StreamMessage(ctx context.Context, systemPrompt string, messages []LLMMessage) (<-chan string, <-chan error) {
	chunks := make(chan string, 64)
	errc := make(chan error, 1)

	go func() {
		defer close(chunks)
		defer close(errc)
		if err := p.stream(ctx, systemPrompt, messages, chunks); err != nil {
			errc <- err
		}
	}()

	return chunks, errc
}

func (p *OllamaProvider) stream(ctx context.Context, systemPrompt string, messages []LLMMessage, chunks chan<- string) error {
	// Build OpenAI-compatible message list with system prompt prepended.
	wire := make([]openAIMessage, 0, len(messages)+1)
	if systemPrompt != "" {
		wire = append(wire, openAIMessage{Role: "system", Content: systemPrompt})
	}
	for _, m := range messages {
		wire = append(wire, openAIMessage{Role: m.Role, Content: m.Content})
	}

	payload := openAIChatRequest{
		Model:    p.model,
		Messages: wire,
		Stream:   true,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := p.baseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ollama API error %d: %s", resp.StatusCode, string(b))
	}

	return parseOpenAIStream(ctx, resp.Body, chunks)
}

// openAIMessage is the wire format for /v1/chat/completions.
type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

// parseOpenAIStream reads SSE lines in OpenAI format and sends text chunks to the channel.
func parseOpenAIStream(ctx context.Context, body io.Reader, chunks chan<- string) error {
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event openAIStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		for _, choice := range event.Choices {
			if text := choice.Delta.Content; text != "" {
				select {
				case chunks <- text:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
	return scanner.Err()
}

type openAIStreamEvent struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}
