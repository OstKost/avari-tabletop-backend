package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
)

var ErrChatInput = errors.New("invalid chat input")

type ChatOptions struct {
	DailyLimit                               int
	Timeout                                  time.Duration
	MaxOutputBytes, MaxEventBytes, MaxEvents int
}

func DefaultChatOptions() ChatOptions {
	return ChatOptions{50, 120 * time.Second, 128 << 10, 1 << 20, 4096}
}

type DurableChatService struct {
	repo    repository.DurableChatRepository
	games   repository.GameRepository
	llm     LLMProvider
	rules   *PrivateRulesService
	secret  []byte
	options ChatOptions
}

func NewDurableChatService(repo repository.DurableChatRepository, games repository.GameRepository, llm LLMProvider, rules *PrivateRulesService, secret string, options ...ChatOptions) *DurableChatService {
	opts := DefaultChatOptions()
	if len(options) > 0 {
		opts = options[0]
	}
	return &DurableChatService{repo, games, llm, rules, []byte(secret), opts}
}
func (s *DurableChatService) sign(text string) string {
	h := hmac.New(sha256.New, s.secret)
	h.Write([]byte("avari/chat/v1/" + text))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *DurableChatService) Submit(ctx context.Context, user, game, client, key uuid.UUID, message string) (model.ChatRequest, error) {
	if user == uuid.Nil || game == uuid.Nil || client == uuid.Nil || key == uuid.Nil || !utf8.ValidString(message) || strings.TrimSpace(message) == "" || utf8.RuneCountInString(message) > 8000 {
		return model.ChatRequest{}, ErrChatInput
	}
	body, _ := json.Marshal([]string{user.String(), game.String(), client.String(), message})
	return s.repo.Submit(ctx, user, game, client, key, s.sign("body:"+string(body)), message, s.options.DailyLimit)
}
func (s *DurableChatService) Get(ctx context.Context, user, id uuid.UUID) (model.ChatRequest, error) {
	return s.repo.Get(ctx, user, id)
}
func (s *DurableChatService) Cancel(ctx context.Context, user, id uuid.UUID) (model.ChatRequest, error) {
	return s.repo.Cancel(ctx, user, id)
}
func (s *DurableChatService) Events(ctx context.Context, user, id uuid.UUID, after int64) ([]model.ChatEvent, model.ChatRequest, error) {
	return s.repo.Events(ctx, user, id, after)
}

type chatCursor struct {
	User, Game uuid.UUID
	Before     int64
	Expires    int64
}

func (s *DurableChatService) History(ctx context.Context, user, game uuid.UUID, limit int, cursor string) (model.ChatPage, error) {
	result := model.ChatPage{Items: make([]model.DurableChatMessage, 0)}
	if limit < 1 || limit > 100 {
		return result, ErrChatInput
	}
	before := int64(0)
	if cursor != "" {
		parts := strings.Split(cursor, ".")
		if len(parts) != 2 || !hmac.Equal([]byte(parts[1]), []byte(s.sign("cursor:"+parts[0]))) {
			return result, ErrChatInput
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			return result, ErrChatInput
		}
		var value chatCursor
		if json.Unmarshal(payload, &value) != nil || value.User != user || value.Game != game || value.Before < 1 || value.Expires < time.Now().Unix() {
			return result, ErrChatInput
		}
		before = value.Before
	}
	rows, err := s.repo.History(ctx, user, game, limit+1, before)
	if err != nil {
		return result, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		payload, _ := json.Marshal(chatCursor{user, game, rows[len(rows)-1].Sequence, time.Now().Add(24 * time.Hour).Unix()})
		encoded := base64.RawURLEncoding.EncodeToString(payload)
		next := encoded + "." + s.sign("cursor:"+encoded)
		result.NextCursor = &next
	}
	for i := len(rows) - 1; i >= 0; i-- {
		result.Items = append(result.Items, rows[i])
	}
	return result, nil
}
func (s *DurableChatService) ClearHistory(ctx context.Context, user, game uuid.UUID) error {
	return s.repo.ClearHistory(ctx, user, game)
}
func (s *DurableChatService) Report(ctx context.Context, user, id uuid.UUID, reason string) error {
	if id == uuid.Nil || (reason != "incorrect" && reason != "unsafe" && reason != "other") {
		return ErrChatInput
	}
	return s.repo.Report(ctx, user, id, reason)
}
func (s *DurableChatService) Cleanup(ctx context.Context) error { return s.repo.Cleanup(ctx) }
func (s *DurableChatService) CleanupOwner(ctx context.Context, user uuid.UUID) error {
	return s.repo.CleanupOwner(ctx, user)
}

// Work owns generation independently of SSE subscribers. A claimed request is
// never restarted after an uncertain worker death; deadline cleanup fails it.
func (s *DurableChatService) Work(parent context.Context) error {
	request, err := s.repo.Claim(parent, s.options.Timeout)
	if errors.Is(err, repository.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(parent, *request.DeadlineAt)
	defer cancel()
	monitorDone := make(chan struct{})
	defer close(monitorDone)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-monitorDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				running, err := s.repo.Running(ctx, request.ID)
				if err != nil || !running {
					cancel()
					return
				}
			}
		}
	}()
	fail := func(code string) error {
  if errors.Is(ctx.Err(),context.DeadlineExceeded) { code="timeout" } else if errors.Is(ctx.Err(),context.Canceled) { code="worker_interrupted" }
		// Persist the terminal even when the provider budget/client context expired.
		finalCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, err := s.repo.Finish(finalCtx, request, "failed", nil, code)
		if errors.Is(err, repository.ErrNotFound) {
			return nil
		}
		return err
	}
	game, err := s.games.GetByID(ctx, request.GameID)
	if err != nil {
		return fail("game_unavailable")
	}
	result := &model.ChatResult{Mode: "general_knowledge", Citations: make([]model.Citation, 0)}
	var privateDocs []PrivateDocument
	if s.rules != nil {
		privateDocs, err = s.rules.Retrieve(ctx, request.UserID, request.GameID, detectLang(request.Input), request.Input, 3)
		if err != nil {
			return fail("rules_unavailable")
		}
	}
	var promptDocs []Document
	for _, doc := range privateDocs {
		result.Mode = "user_rules"
		excerpt := []rune(doc.Content)
		if len(excerpt) > 500 {
			excerpt = excerpt[:500]
		}
		result.Citations = append(result.Citations, model.Citation{RuleID: doc.Scope.RuleID, Revision: doc.Scope.Revision, Language: doc.Scope.Language, ChunkID: doc.ID, Excerpt: string(excerpt)})
		promptDocs = append(promptDocs, Document{ID: doc.ID, Content: doc.Content, GameID: request.GameID, Lang: doc.Scope.Language})
	}
	if err = s.repo.Start(ctx, request, result.Mode); err != nil {
		return fail("storage_unavailable")
	}
	tail, err := s.repo.History(ctx, request.UserID, request.GameID, 20, 0)
	if err != nil {
		return fail("history_unavailable")
	}
	messages := make([]LLMMessage, 0, len(tail)+1)
	for i := len(tail) - 1; i >= 0; i-- {
		messages = append(messages, LLMMessage{Role: string(tail[i].Role), Content: tail[i].Content})
	}
	messages = append(messages, LLMMessage{Role: "user", Content: request.Input})
	prompt := strings.Replace(BuildSystemPrompt(game, promptDocs), "ПРАВИЛА ИГРЫ (из официального источника):", "ЗАГРУЖЕННЫЕ ПОЛЬЗОВАТЕЛЕМ ПРАВИЛА (источник не подтверждён):", 1)
	chunks, errs := s.llm.StreamMessage(ctx, prompt, messages)
	var text strings.Builder
	for {
		select {
		case <-ctx.Done():
			code := "worker_interrupted"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				code = "timeout"
			}
			return fail(code)
		case chunk, ok := <-chunks:
			if !ok {
				select {
				case providerErr := <-errs:
					if providerErr != nil {
						return fail("provider_error")
					}
				case <-ctx.Done():
					return fail("timeout")
				}
				result.Text = text.String()
				_, err = s.repo.Finish(ctx, request, "completed", result, "")
				if errors.Is(err, repository.ErrNotFound) {
					return nil
				}
				return err
			}
			if !utf8.ValidString(chunk) {
				return fail("provider_error")
			}
			if text.Len()+len(chunk) > s.options.MaxOutputBytes {
				return fail("output_limit")
			}
			if err = s.repo.Delta(ctx, request, chunk, s.options.MaxEventBytes, s.options.MaxEvents); err != nil {
				if errors.Is(err, repository.ErrChatEventLimit) {
					return fail("output_limit")
				}
				return fail("storage_unavailable")
			}
			text.WriteString(chunk)
		}
	}
}
