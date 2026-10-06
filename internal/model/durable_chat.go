package model

import (
	"encoding/json"
	"github.com/google/uuid"
	"time"
)

type Citation struct {
	RuleID   uuid.UUID `json:"rule_id"`
	Revision int64     `json:"revision"`
	Language string    `json:"language"`
	ChunkID  string    `json:"chunk_id"`
	Excerpt  string    `json:"excerpt"`
}
type ChatResult struct {
	Text      string     `json:"text"`
	Mode      string     `json:"mode"`
	Citations []Citation `json:"citations"`
}
type ChatRequest struct {
	ID              uuid.UUID   `json:"id"`
	UserID          uuid.UUID   `json:"-"`
	GameID          uuid.UUID   `json:"game_id"`
	ClientRequestID uuid.UUID   `json:"client_request_id"`
	Status          string      `json:"status"`
	CreatedAt       time.Time   `json:"created_at"`
	Result          *ChatResult `json:"result"`
	ErrorCode       *string     `json:"error_code"`
	Input           string      `json:"-"`
	LastSequence    int64       `json:"-"`
	EventBytes      int         `json:"-"`
	CompletedAt     *time.Time  `json:"-"`
	DeadlineAt      *time.Time  `json:"-"`
}

func (r ChatRequest) Terminal() bool {
	return r.Status == "completed" || r.Status == "cancelled" || r.Status == "failed"
}

type ChatEvent struct {
	Sequence int64
	Kind     string
	Payload  json.RawMessage
}
type DurableChatMessage struct {
	ID        uuid.UUID `json:"id"`
	Role      ChatRole  `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	RequestID uuid.UUID `json:"request_id"`
	Sequence  int64     `json:"-"`
}
type ChatPage struct {
	Items      []DurableChatMessage `json:"items"`
	NextCursor *string              `json:"next_cursor"`
}
