package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAICompatProvider implements LLMProvider for any OpenAI-compatible API:
// Groq (api.groq.com/openai), OpenRouter (openrouter.ai/api), LM Studio, etc.
type OpenAICompatProvider struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

func NewOpenAICompatProvider(baseURL, apiKey, model string) *OpenAICompatProvider {
	return &OpenAICompatProvider{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *OpenAICompatProvider) Name() string { return "openai_compat:" + p.model }

// StreamMessage implements LLMProvider.
func (p *OpenAICompatProvider) StreamMessage(ctx context.Context, systemPrompt string, messages []LLMMessage) (<-chan string, <-chan error) {
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

func (p *OpenAICompatProvider) stream(ctx context.Context, systemPrompt string, messages []LLMMessage, chunks chan<- string) error {
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

	url := p.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("openai_compat request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(b))
	}

	return parseOpenAIStream(ctx, resp.Body, chunks)
}
