package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"time"

	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

type DeletionAccepted struct {
	RequestID uuid.UUID `json:"request_id"`
	Status    string    `json:"status"`
	Receipt   string    `json:"deletion_receipt"`
}
type DeletionStatus struct {
	RequestID uuid.UUID `json:"request_id"`
	Status    string    `json:"status"`
	Retryable bool      `json:"retryable"`
}

// R2 registers scoped SQL/vector/files/cache cleanup implementations here.
// Each hook must be idempotent; failure keeps the frozen account and job.
type AccountCleanup func(context.Context, uuid.UUID) error

type AccountDeletionService struct {
	repo    repository.AccountDeletionRepository
	secret  []byte
	limiter *AuthLimiter
	cleanup []AccountCleanup
}

func NewAccountDeletionService(repo repository.AccountDeletionRepository, secret string, cleanup ...AccountCleanup) *AccountDeletionService {
	return &AccountDeletionService{repo: repo, secret: []byte(secret), limiter: NewAuthLimiter(), cleanup: cleanup}
}
func (s *AccountDeletionService) proof(kind, value string) string {
	h := hmac.New(sha256.New, s.secret)
	h.Write([]byte("avari/deletion/v1/" + kind + ":" + value))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *AccountDeletionService) Request(ctx context.Context, uid, key uuid.UUID, password string, allowNew bool) (DeletionAccepted, error) {
	if uid == uuid.Nil || key == uuid.Nil || len(password) < 1 || len(password) > 72 {
		return DeletionAccepted{}, ErrAuthInput
	}
	if !s.limiter.Allow("delete:"+uid.String(), 5, time.Minute, time.Now()) {
		return DeletionAccepted{}, ErrAuthRateLimit
	}
	id := uuid.New()
	receipt := s.proof("receipt", id.String())
	accepted, err := s.repo.Accept(ctx, uid, s.proof("replay", uid.String()+":"+key.String()), s.proof("body", uid.String()+":"+key.String()+":"+password), id, tokenHash(receipt), allowNew, func(hash string) bool { return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil })
	if err != nil {
		return DeletionAccepted{}, err
	}
	return DeletionAccepted{accepted, "pending", s.proof("receipt", accepted.String())}, nil
}
func (s *AccountDeletionService) Status(ctx context.Context, id uuid.UUID, receipt string) (DeletionStatus, error) {
	if id == uuid.Nil || len(receipt) < 1 || len(receipt) > 128 {
		return DeletionStatus{}, repository.ErrNotFound
	}
	job, err := s.repo.Status(ctx, id, tokenHash(receipt))
	if err != nil {
		return DeletionStatus{}, err
	}
	return DeletionStatus{job.ID, job.Status, job.Status == "failed"}, nil
}
func (s *AccountDeletionService) Work(ctx context.Context) error {
	return s.repo.Work(ctx, func(ctx context.Context, uid uuid.UUID) error {
		for _, cleanup := range s.cleanup {
			if err := cleanup(ctx, uid); err != nil {
				return err
			}
		}
		return nil
	})
}
