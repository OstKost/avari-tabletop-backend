package model

import (
	"github.com/google/uuid"
	"time"
)

type PrivateRule struct {
	ID             uuid.UUID `json:"id"`
	UserID         uuid.UUID `json:"-"`
	GameID         uuid.UUID `json:"game_id"`
	Language       string    `json:"language"`
	Content        string    `json:"content"`
	Source         string    `json:"source"`
	Revision       int64     `json:"revision"`
	ActiveRevision *int64    `json:"active_revision"`
	IndexStatus    string    `json:"index_status"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type RulesIndexStatus struct {
	RuleID         uuid.UUID `json:"rule_id"`
	Revision       int64     `json:"revision"`
	ActiveRevision *int64    `json:"active_revision"`
	IndexStatus    string    `json:"index_status"`
	Retryable      bool      `json:"retryable"`
}

func (r PrivateRule) Status() RulesIndexStatus {
	return RulesIndexStatus{r.ID, r.Revision, r.ActiveRevision, r.IndexStatus, r.IndexStatus == "failed"}
}
