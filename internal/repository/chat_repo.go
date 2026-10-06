package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
)

type ChatRepository interface {
	SaveMessage(ctx context.Context, userID, gameID uuid.UUID, role model.ChatRole, content string) (model.ChatMessage, error)
	GetHistory(ctx context.Context, userID, gameID uuid.UUID, limit int) ([]model.ChatMessage, error)
	ClearHistory(ctx context.Context, userID, gameID uuid.UUID) error
}

type pgChatRepo struct {
	pool *pgxpool.Pool
}

func NewChatRepository(pool *pgxpool.Pool) ChatRepository {
	return &pgChatRepo{pool: pool}
}

func (r *pgChatRepo) SaveMessage(ctx context.Context, userID, gameID uuid.UUID, role model.ChatRole, content string) (model.ChatMessage, error) {
	var m model.ChatMessage
	err := r.pool.QueryRow(ctx, `
		INSERT INTO chat_messages (user_id, game_id, role, content)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, game_id, role, content, created_at
	`, userID, gameID, role, content).Scan(
		&m.ID, &m.UserID, &m.GameID, &m.Role, &m.Content, &m.CreatedAt,
	)
	if err != nil {
		return model.ChatMessage{}, fmt.Errorf("save message: %w", err)
	}
	return m, nil
}

func (r *pgChatRepo) GetHistory(ctx context.Context, userID, gameID uuid.UUID, limit int) ([]model.ChatMessage, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, user_id, game_id, role, content, created_at
        FROM (
          SELECT id,user_id,game_id,role,content,created_at,sequence
          FROM chat_messages WHERE user_id=$1 AND game_id=$2
          ORDER BY created_at DESC,sequence DESC LIMIT $3
        ) latest
        ORDER BY created_at ASC,sequence ASC
	`, userID, gameID, limit)
	if err != nil {
		return nil, fmt.Errorf("get chat history: %w", err)
	}
	defer rows.Close()

	var messages []model.ChatMessage
	for rows.Next() {
		var m model.ChatMessage
		if err := rows.Scan(&m.ID, &m.UserID, &m.GameID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func (r *pgChatRepo) ClearHistory(ctx context.Context, userID, gameID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM chat_messages WHERE user_id = $1 AND game_id = $2
	`, userID, gameID)
	if err != nil {
		return fmt.Errorf("clear chat history: %w", err)
	}
	return nil
}
