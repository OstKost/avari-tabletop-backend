package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	tabletop "github.com/ostkost/avari-tabletop-backend"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/ostkost/avari-tabletop-backend/internal/service"
	"github.com/stretchr/testify/require"
)

type durableLLM struct {
	mu           sync.Mutex
	calls        int
	pieces       []string
	fail         bool
	hold         bool
	started      chan struct{}
	released     chan struct{}
	lastPrompt   string
	lastMessages []service.LLMMessage
}

func (l *durableLLM) Name() string { return "synthetic" }
func (l *durableLLM) StreamMessage(ctx context.Context, prompt string, messages []service.LLMMessage) (<-chan string, <-chan error) {
	l.mu.Lock()
	l.calls++
	l.lastPrompt = prompt
	l.lastMessages = append([]service.LLMMessage{}, messages...)
	pieces := append([]string{}, l.pieces...)
	fail, hold := l.fail, l.hold
	started, released := l.started, l.released
	l.mu.Unlock()
	chunks := make(chan string)
	errs := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errs)
		if started != nil {
			close(started)
		}
		if hold {
			select {
			case <-released:
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
		}
		for _, piece := range pieces {
			select {
			case chunks <- piece:
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
		}
		if fail {
			errs <- errors.New("synthetic provider-private-detail")
		}
	}()
	return chunks, errs
}
func (l *durableLLM) block() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hold = true
	l.started = make(chan struct{})
	l.released = make(chan struct{})
}
func (l *durableLLM) count() int { l.mu.Lock(); defer l.mu.Unlock(); return l.calls }

type durableFixture struct {
	*privateRulesFixture
	chat     *service.DurableChatService
	chatRepo repository.DurableChatRepository
	llm      *durableLLM
	handler  *MobileChatHandler
}

func newDurableFixture(t *testing.T, options ...service.ChatOptions) *durableFixture {
	t.Helper()
	f := newPrivateRulesFixture(t, "chromem")
	llm := &durableLLM{pieces: []string{"Ответ 😊\n", "Перенос и <script>literal</script>."}}
	repo := repository.NewDurableChatRepository(f.pool)
	svc := service.NewDurableChatService(repo, repository.NewGameRepository(f.pool), llm, f.svc, mobileTestSecret, options...)
	h := NewMobileChatHandler(svc, nil)
	auth := NewMobileAuthHandler(f.mobile, h, NewMobileRulesHandler(f.svc, repository.NewCollectionRepository(f.pool), nil))
	f.router = auth.Router()
	return &durableFixture{f, svc, repo, llm, h}
}
func (f *durableFixture) callChat(method, path string, payload any, user service.MobileAuthSession, key string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:9988"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+user.AccessToken)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if dir := os.Getenv("B04_EVIDENCE_DIR"); dir != "" {
		data, _ := json.Marshal(map[string]any{"method": method, "path": strings.Split(path, "?")[0], "status": w.Code, "content_type": w.Header().Get("Content-Type"), "body": w.Body.String()})
		requireWrite(dir, data)
	}
	return w
}
func requireWrite(dir string, data []byte) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, uuid.NewString()+".json"), data, 0600); err != nil {
		panic(err)
	}
}
func (f *durableFixture) requestPath() string { return "/games/" + f.game.String() + "/chat/requests" }
func (f *durableFixture) historyPath() string { return "/games/" + f.game.String() + "/chat/messages" }
func (f *durableFixture) submit(t *testing.T, user service.MobileAuthSession, text string) model.ChatRequest {
	t.Helper()
	w := f.callChat("POST", f.requestPath(), map[string]any{"message": text, "client_request_id": uuid.New()}, user, uuid.NewString())
	require.Equal(t, 201, w.Code, w.Body.String())
	var r model.ChatRequest
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &r))
	require.Equal(t, "/api/v1/chat/requests/"+r.ID.String(), w.Header().Get("Location"))
	return r
}
func chatRequestAt(t *testing.T, f *durableFixture, user service.MobileAuthSession, id uuid.UUID) model.ChatRequest {
	t.Helper()
	w := f.callChat("GET", "/chat/requests/"+id.String(), nil, user, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var r model.ChatRequest
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &r))
	return r
}
func TestDurableChatCompletionReplayIsolationPostgres(t *testing.T) {
	f := newDurableFixture(t)
	ctx := context.Background()
	client, key := uuid.New(), uuid.NewString()
	input := map[string]any{"message": "Вопрос 😊\nС переносом", "client_request_id": client}
	var id uuid.UUID
	for i := 0; i < 8; i++ {
		w := f.callChat("POST", f.requestPath(), input, f.a, key)
		require.Equal(t, 201, w.Code, w.Body.String())
		var r model.ChatRequest
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &r))
		if id == uuid.Nil {
			id = r.ID
		}
		require.Equal(t, id, r.ID)
	}
	require.Equal(t, 0, f.llm.count())
	require.NoError(t, f.chat.Work(ctx))
	require.Equal(t, 1, f.llm.count())
	done := chatRequestAt(t, f, f.a, id)
	require.Equal(t, "completed", done.Status)
	require.Equal(t, "Ответ 😊\nПеренос и <script>literal</script>.", done.Result.Text)
	require.Equal(t, "general_knowledge", done.Result.Mode)
	require.Empty(t, done.Result.Citations)
	require.Equal(t, 404, f.callChat("GET", "/chat/requests/"+id.String(), nil, f.b, "").Code)
	stream := f.callChat("GET", "/chat/requests/"+id.String()+"/events", nil, f.a, "")
	require.Equal(t, 200, stream.Code)
	require.Contains(t, stream.Body.String(), "event: started")
	require.Equal(t, 1, strings.Count(stream.Body.String(), "event: completed"))
	require.NotContains(t, stream.Body.String(), "event: chunk")
	require.Equal(t, 1, f.llm.count())
	req := httptest.NewRequest("GET", "/chat/requests/"+id.String()+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+f.a.AccessToken)
	req.Header.Set("Last-Event-ID", "2")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)
	require.NotContains(t, w.Body.String(), "id: 1\n")
	require.NotContains(t, w.Body.String(), "id: 2\n")
	require.Contains(t, w.Body.String(), "event: completed")
	cancelled := f.callChat("DELETE", "/chat/requests/"+id.String(), nil, f.a, "")
	require.Equal(t, 200, cancelled.Code)
	require.Contains(t, cancelled.Body.String(), `"status":"completed"`)
	require.Equal(t, 202, f.callChat("POST", "/ai/reports", map[string]any{"request_id": id, "reason": "incorrect"}, f.a, "").Code)
	require.Equal(t, 404, f.callChat("POST", "/ai/reports", map[string]any{"request_id": id, "reason": "incorrect"}, f.b, "").Code)
	history := f.callChat("GET", f.historyPath(), nil, f.a, "")
	require.Equal(t, 200, history.Code)
	var page model.ChatPage
	require.NoError(t, json.Unmarshal(history.Body.Bytes(), &page))
	require.Len(t, page.Items, 2)
	require.Equal(t, model.RoleUser, page.Items[0].Role)
	require.Equal(t, model.RoleAssistant, page.Items[1].Role)
	require.Equal(t, id, page.Items[0].RequestID)
	require.Equal(t, 204, f.callChat("DELETE", f.historyPath(), nil, f.a, "").Code)
	require.Equal(t, 404, f.callChat("GET", "/chat/requests/"+id.String(), nil, f.a, "").Code)
	require.Equal(t, 404, f.callChat("POST", f.requestPath(), input, f.a, key).Code)
	var count int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chat_events WHERE request_id=$1`, id).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chat_requests WHERE id=$1 AND result IS NULL AND input_message=''`, id).Scan(&count))
	require.Equal(t, 1, count)
}
func TestDurableChatDuplicateConcurrencyAndCancellationPostgres(t *testing.T) {
	f := newDurableFixture(t)
	ctx := context.Background()
	client, key := uuid.New(), uuid.NewString()
	input := map[string]any{"message": "Same question", "client_request_id": client}
	results := make(chan *httptest.ResponseRecorder, 10)
	var workers sync.WaitGroup
	for i := 0; i < 10; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); results <- f.callChat("POST", f.requestPath(), input, f.a, key) }()
	}
	workers.Wait()
	close(results)
	var id uuid.UUID
	for w := range results {
		require.Equal(t, 201, w.Code, w.Body.String())
		var r model.ChatRequest
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &r))
		if id == uuid.Nil {
			id = r.ID
		}
		require.Equal(t, id, r.ID)
	}
	require.Equal(t, 409, f.callChat("POST", f.requestPath(), map[string]any{"message": "Different", "client_request_id": client}, f.a, key).Code)
	require.Equal(t, 409, f.callChat("POST", f.requestPath(), map[string]any{"message": "Another active", "client_request_id": uuid.New()}, f.a, uuid.NewString()).Code)
	require.Equal(t, 409, f.callChat("DELETE", f.historyPath(), nil, f.a, "").Code)
	f.llm.block()
	work := make(chan error, 1)
	go func() { work <- f.chat.Work(ctx) }()
	select {
	case <-f.llm.started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}
	require.NoError(t, f.chat.Work(ctx))
	require.Equal(t, 1, f.llm.count())
	for i := 0; i < 2; i++ {
		w := f.callChat("DELETE", "/chat/requests/"+id.String(), nil, f.a, "")
		require.Equal(t, 200, w.Code)
		require.Contains(t, w.Body.String(), `"status":"cancelled"`)
	}
	waitPrivateWorker(t, work)
	request := chatRequestAt(t, f, f.a, id)
	require.Equal(t, "cancelled", request.Status)
	require.Nil(t, request.Result)
	events, _, err := f.chat.Events(ctx, f.a.User.ID, id, 0)
	require.NoError(t, err)
	terminal := 0
	for _, e := range events {
		if e.Kind == "cancelled" || e.Kind == "completed" || e.Kind == "failed" {
			terminal++
		}
	}
	require.Equal(t, 1, terminal)
	var count int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chat_messages WHERE request_id=$1`, id).Scan(&count))
	require.Zero(t, count)
}
func TestDurableChatDisconnectDoesNotCancelOrInvokeLLMPostgres(t *testing.T) {
	f := newDurableFixture(t)
	request := f.submit(t, f.a, "Network switch question")
	server := httptest.NewServer(f.router)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/chat/requests/"+request.ID.String()+"/events", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+f.a.AccessToken)
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	require.Equal(t, 0, f.llm.count())
	cancel()
	response.Body.Close()
	pending := chatRequestAt(t, f, f.a, request.ID)
	require.Equal(t, "pending", pending.Status)
	require.NoError(t, f.chat.Work(context.Background()))
	require.Equal(t, "completed", chatRequestAt(t, f, f.a, request.ID).Status)
	require.Equal(t, 1, f.llm.count())
}
func TestDurableChatFailureTimeoutOutputAndLeasePostgres(t *testing.T) {
	for _, kind := range []string{"provider", "timeout", "output", "lease"} {
		t.Run(kind, func(t *testing.T) {
			options := service.DefaultChatOptions()
			if kind == "timeout" {
				options.Timeout = 200 * time.Millisecond
			}
			if kind == "output" {
				options.MaxOutputBytes = 3
			}
			f := newDurableFixture(t, options)
			ctx := context.Background()
			request := f.submit(t, f.a, "Failure question")
			switch kind {
			case "provider":
				f.llm.fail = true
			case "timeout":
				f.llm.block()
			case "lease":
				_, err := f.chatRepo.Claim(ctx, time.Second)
				require.NoError(t, err)
				_, err = f.pool.Exec(ctx, `UPDATE chat_requests SET deadline_at=NOW()-INTERVAL '1 second' WHERE id=$1`, request.ID)
				require.NoError(t, err)
			}
			if kind == "lease" {
				require.NoError(t, f.chat.Cleanup(ctx))
				require.Equal(t, 0, f.llm.count())
			} else {
				require.NoError(t, f.chat.Work(ctx))
			}
			done := chatRequestAt(t, f, f.a, request.ID)
			require.Equal(t, "failed", done.Status)
			require.Nil(t, done.Result)
			require.NotNil(t, done.ErrorCode)
			expected := map[string]string{"provider": "provider_error", "timeout": "timeout", "output": "output_limit", "lease": "worker_interrupted"}[kind]
			require.Equal(t, expected, *done.ErrorCode)
			events, _, err := f.chat.Events(ctx, f.a.User.ID, request.ID, 0)
			require.NoError(t, err)
			var terminals int
			for _, event := range events {
				if event.Kind == "failed" {
					terminals++
				}
				require.NotContains(t, string(event.Payload), "provider-private-detail")
			}
			require.Equal(t, 1, terminals)
			var count int
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chat_messages WHERE request_id=$1`, request.ID).Scan(&count))
			require.Zero(t, count)
		})
	}
}
func TestDurableChatProvenanceTailAndBackwardPaginationPostgres(t *testing.T) {
	f := newDurableFixture(t)
	ctx := context.Background()
	rule := f.save(t, f.a, "ru", "Приватные правила A; две карты", 0)
	f.save(t, f.b, "ru", "PRIVATE B NEVER PROMPT", 0)
	require.NoError(t, f.svc.Work(ctx))
	require.NoError(t, f.svc.Work(ctx))
	var ids []uuid.UUID
	for i := 0; i < 12; i++ {
		request := f.submit(t, f.a, "Вопрос "+strconv.Itoa(i))
		require.NoError(t, f.chat.Work(ctx))
		ids = append(ids, request.ID)
	}
	last := chatRequestAt(t, f, f.a, ids[len(ids)-1])
	require.Equal(t, "user_rules", last.Result.Mode)
	require.Len(t, last.Result.Citations, 1)
	require.Equal(t, rule.ID, last.Result.Citations[0].RuleID)
	require.Equal(t, int64(1), last.Result.Citations[0].Revision)
	f.llm.mu.Lock()
	prompt, messages := f.llm.lastPrompt, append([]service.LLMMessage{}, f.llm.lastMessages...)
	f.llm.mu.Unlock()
	require.Contains(t, prompt, rule.Content)
	require.NotContains(t, prompt, "PRIVATE B NEVER PROMPT")
	require.NotContains(t, prompt, "из официального источника")
	require.Len(t, messages, 21)
	require.Equal(t, "Вопрос 1", messages[0].Content)
	require.Equal(t, "Вопрос 11", messages[20].Content)
	page, err := f.chat.History(ctx, f.a.User.ID, f.game, 5, "")
	require.NoError(t, err)
	require.Len(t, page.Items, 5)
	require.NotNil(t, page.NextCursor)
	older, err := f.chat.History(ctx, f.a.User.ID, f.game, 5, *page.NextCursor)
	require.NoError(t, err)
	require.Less(t, older.Items[len(older.Items)-1].Sequence, page.Items[0].Sequence)
	_, err = f.chat.History(ctx, f.b.User.ID, f.game, 5, *page.NextCursor)
	require.ErrorIs(t, err, service.ErrChatInput)
	legacyTail, err := repository.NewChatRepository(f.pool).GetHistory(ctx, f.a.User.ID, f.game, 4)
	require.NoError(t, err)
	require.Len(t, legacyTail, 4)
	require.Equal(t, "Вопрос 10", legacyTail[0].Content)
}
func TestDurableChatQuotaFreezeAndReplayExpiryPostgres(t *testing.T) {
	options := service.DefaultChatOptions()
	options.DailyLimit = 2
	f := newDurableFixture(t, options)
	ctx := context.Background()
	first := f.submit(t, f.a, "First")
	require.NoError(t, f.chat.Work(ctx))
	f.submit(t, f.a, "Second")
	require.NoError(t, f.chat.Work(ctx))
	require.Equal(t, 429, f.callChat("POST", f.requestPath(), map[string]any{"message": "Third", "client_request_id": uuid.New()}, f.a, uuid.NewString()).Code)
	_, err := f.pool.Exec(ctx, `UPDATE chat_requests SET completed_at=NOW()-INTERVAL '11 minutes' WHERE id=$1`, first.ID)
	require.NoError(t, err)
	expired := f.callChat("GET", "/chat/requests/"+first.ID.String()+"/events", nil, f.a, "")
	require.Equal(t, 410, expired.Code)
	require.Contains(t, expired.Header().Get("Content-Type"), "application/json")
	require.Equal(t, "completed", chatRequestAt(t, f, f.a, first.ID).Status)
	// Account cleanup waits for SQL ownership freeze, cannot resurrect the pair.
	b := f.submit(t, f.b, "BLOCK second owner")
	f.llm.block()
	work := make(chan error, 1)
	go func() { work <- f.chat.Work(ctx) }()
	select {
	case <-f.llm.started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}
	deletion := service.NewAccountDeletionService(repository.NewAccountDeletionRepository(f.pool), mobileTestSecret, f.svc.CleanupOwner, f.chat.CleanupOwner)
	accepted, err := deletion.Request(ctx, f.b.User.ID, uuid.New(), "synthetic-password", true)
	require.NoError(t, err)
	require.NoError(t, deletion.Work(ctx))
	waitPrivateWorker(t, work)
	status, err := deletion.Status(ctx, accepted.RequestID, accepted.Receipt)
	require.NoError(t, err)
	require.Equal(t, "completed", status.Status)
	var count int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chat_requests WHERE id=$1`, b.ID).Scan(&count))
	require.Zero(t, count)
	require.Equal(t, "completed", chatRequestAt(t, f, f.a, first.ID).Status)
}
func TestDurableChatAtomicCompletionFailureAndMembershipPostgres(t *testing.T) {
	f := newDurableFixture(t)
	ctx := context.Background()
	request := f.submit(t, f.a, "Atomic pair")
	_, err := f.pool.Exec(ctx, `CREATE FUNCTION reject_synthetic_assistant() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.role='assistant' THEN RAISE EXCEPTION 'synthetic write rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_assistant BEFORE INSERT ON chat_messages FOR EACH ROW EXECUTE FUNCTION reject_synthetic_assistant()`)
	require.NoError(t, err)
	require.Error(t, f.chat.Work(ctx))
	var count int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chat_messages WHERE request_id=$1`, request.ID).Scan(&count))
	require.Zero(t, count)
	current := chatRequestAt(t, f, f.a, request.ID)
	require.Equal(t, "running", current.Status)
	require.Nil(t, current.Result)
	_, err = f.pool.Exec(ctx, `DROP TRIGGER reject_assistant ON chat_messages;DROP FUNCTION reject_synthetic_assistant()`)
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `UPDATE chat_requests SET deadline_at=NOW()-INTERVAL '1 second' WHERE id=$1`, request.ID)
	require.NoError(t, err)
	require.NoError(t, f.chat.Cleanup(ctx))
	require.Equal(t, "failed", chatRequestAt(t, f, f.a, request.ID).Status)
	pending := f.submit(t, f.a, "Removed membership")
	_, err = f.pool.Exec(ctx, `DELETE FROM collections WHERE user_id=$1 AND game_id=$2`, f.a.User.ID, f.game)
	require.NoError(t, err)
	require.NoError(t, f.chat.Cleanup(ctx))
	var status string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT status FROM chat_requests WHERE id=$1`, pending.ID).Scan(&status))
	require.Equal(t, "failed", status)
	require.Equal(t, 404, f.callChat("GET", f.historyPath(), nil, f.a, "").Code)
}
func TestDurableChatWebSSEAndHistoryPostgres(t *testing.T) {
	f := newDurableFixture(t)
	ctx := context.Background()
	webFS, err := fs.Sub(tabletop.Web, "web")
	require.NoError(t, err)
	renderer, err := NewRenderer(webFS)
	require.NoError(t, err)
	h := NewDurableChatHandler(f.chat, repository.NewGameRepository(f.pool), renderer)
	page := rulesRequest("GET", f.game.String(), f.a.User.ID, nil)
	// Use the existing request helper's gameID parameter and user context.
	w := httptest.NewRecorder()
	h.GetChat(w, page)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "client_request_id")
	require.NotContains(t, w.Body.String(), "data === 'done'")
	input := url.Values{"message": {"Веб вопрос <script>literal</script>\nстрока"}, "client_request_id": {uuid.NewString()}, "idempotency_key": {uuid.NewString()}}
	post := rulesRequest("POST", f.game.String(), f.a.User.ID, input)
	w = httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.PostChat(w, post); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for f.llm.count() == 0 && time.Now().Before(deadline) {
		require.NoError(t, f.chat.Work(ctx))
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("web stream did not terminate")
	}
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "event: delta")
	require.Equal(t, 1, strings.Count(w.Body.String(), "event: completed"))
	require.NotContains(t, w.Body.String(), "<div id=\"streaming-message\"")
	for _, htmx := range []bool{false, true} {
		get := rulesRequest("GET", f.game.String(), f.a.User.ID, nil)
		if htmx {
			get.Header.Set("HX-Request", "true")
		}
		view := httptest.NewRecorder()
		h.GetChat(view, get)
		require.Equal(t, 200, view.Code)
		require.Contains(t, view.Body.String(), "&lt;script&gt;literal&lt;/script&gt;")
	}
}

func TestDurableChatCompletionCancelRaceExactlyOneTerminalPostgres(t *testing.T) {
	f := newDurableFixture(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		request := f.submit(t, f.a, "Race question")
		start := make(chan struct{})
		done := make(chan error, 2)
		go func() { <-start; done <- f.chat.Work(ctx) }()
		go func() { <-start; _, err := f.chat.Cancel(ctx, f.a.User.ID, request.ID); done <- err }()
		close(start)
		waitPrivateWorker(t, done)
		waitPrivateWorker(t, done)
		current, err := f.chat.Get(ctx, f.a.User.ID, request.ID)
		require.NoError(t, err)
		require.True(t, current.Status == "completed" || current.Status == "cancelled")
		var terminals, messages int
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chat_events WHERE request_id=$1 AND terminal`, request.ID).Scan(&terminals))
		require.Equal(t, 1, terminals)
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chat_messages WHERE request_id=$1`, request.ID).Scan(&messages))
		if current.Status == "completed" {
			require.Equal(t, 2, messages)
		} else {
			require.Zero(t, messages)
		}
	}
}
func TestDurableChatSubscriberLimitAndReleasePostgres(t *testing.T) {
	f := newDurableFixture(t)
	request := f.submit(t, f.a, "Only subscribe; never generate")
	server := httptest.NewServer(f.router)
	defer server.Close()
	var cancels []context.CancelFunc
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/chat/requests/"+request.ID.String()+"/events", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+f.a.AccessToken)
		response, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, 200, response.StatusCode)
		defer response.Body.Close()
	}
	req, err := http.NewRequest("GET", server.URL+"/chat/requests/"+request.ID.String()+"/events", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+f.a.AccessToken)
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, 429, response.StatusCode)
	response.Body.Close()
	require.Equal(t, 0, f.llm.count())
	for _, cancel := range cancels {
		cancel()
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.handler.mu.Lock()
		remaining := f.handler.total
		f.handler.mu.Unlock()
		if remaining == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("subscriber reservations leaked")
}
func TestDurableChatMigrationRollbackRetainsMessagesPostgres(t *testing.T) {
	f := newDurableFixture(t)
	ctx := context.Background()
	request := f.submit(t, f.a, "Before rollback")
	require.NoError(t, f.chat.Work(ctx))
	migrations, err := fs.Sub(tabletop.Migrations, "migrations")
	require.NoError(t, err)
	down, err := fs.ReadFile(migrations, "009_durable_chat.down.sql")
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, string(down))
	require.NoError(t, err)
	var count int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM chat_messages WHERE user_id=$1 AND game_id=$2`, f.a.User.ID, f.game).Scan(&count))
	require.Equal(t, 2, count)
	_, err = f.pool.Exec(ctx, `DELETE FROM schema_migrations WHERE filename='009_durable_chat.up.sql'`)
	require.NoError(t, err)
	require.NoError(t, repository.RunMigrations(ctx, f.pool, migrations, "003_pgvector", "008_private_pgvector"))
	require.Equal(t, 404, f.callChat("GET", "/chat/requests/"+request.ID.String(), nil, f.a, "").Code)
	f.submit(t, f.a, "After reapply")
	require.NoError(t, f.chat.Work(ctx))
}
