package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

type PasswordResetMailer interface {
	Enabled() bool
	SendReset(context.Context, string, string, time.Time) error
}
type DisabledResetMailer struct{}

func (DisabledResetMailer) Enabled() bool { return false }
func (DisabledResetMailer) SendReset(context.Context, string, string, time.Time) error {
	return ErrAuthUnavailable
}

// Local development mailbox only. Never logs delivery recipients or tokens.
type FileResetMailer struct {
	Directory string
	mu        sync.Mutex
}

func (*FileResetMailer) Enabled() bool { return true }
func (m *FileResetMailer) SendReset(ctx context.Context, email, token string, expires time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.MkdirAll(m.Directory, 0700); err != nil {
		return err
	}
	if err := os.Chmod(m.Directory, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(m.Directory)
	if err != nil {
		return err
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if time.Since(info.ModTime()) > 15*time.Minute {
			if err = os.Remove(filepath.Join(m.Directory, entry.Name())); err != nil {
				return err
			}
		} else {
			count++
		}
	}
	if count >= 1000 {
		return errors.New("development mailbox full")
	}
	body, err := json.Marshal(struct {
		Email      string    `json:"email"`
		ResetToken string    `json:"reset_token"`
		Expires    time.Time `json:"expires_at"`
	}{email, token, expires.UTC()})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(m.Directory, uuid.NewString()+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(body); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
