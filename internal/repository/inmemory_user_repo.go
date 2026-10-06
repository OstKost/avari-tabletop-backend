package repository

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
)

// InMemoryUserRepository is an in-memory implementation of UserRepository for testing.
type InMemoryUserRepository struct {
	mu    sync.RWMutex
	users []model.User
}

func NewInMemoryUserRepository() UserRepository {
	return &InMemoryUserRepository{}
}

func (r *InMemoryUserRepository) Create(_ context.Context, email, username, passwordHash string) (model.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.users {
		if u.Email == email || u.Username == username {
			return model.User{}, ErrDuplicate
		}
	}
	u := model.User{
		ID:           uuid.New(),
		Email:        email,
		Username:     username,
		PasswordHash: passwordHash,
	}
	r.users = append(r.users, u)
	return u, nil
}

func (r *InMemoryUserRepository) GetByEmail(_ context.Context, email string) (model.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, u := range r.users {
		if u.Email == email {
			return u, nil
		}
	}
	return model.User{}, ErrNotFound
}

func (r *InMemoryUserRepository) GetByID(_ context.Context, id uuid.UUID) (model.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, u := range r.users {
		if u.ID == id {
			return u, nil
		}
	}
	return model.User{}, ErrNotFound
}
