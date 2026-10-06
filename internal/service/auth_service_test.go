package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// mockUserRepo implements repository.UserRepository for testing
type mockUserRepo struct {
	mock.Mock
}

func (m *mockUserRepo) Create(ctx context.Context, email, username, passwordHash string) (model.User, error) {
	args := m.Called(ctx, email, username, passwordHash)
	return args.Get(0).(model.User), args.Error(1)
}

func (m *mockUserRepo) GetByEmail(ctx context.Context, email string) (model.User, error) {
	args := m.Called(ctx, email)
	return args.Get(0).(model.User), args.Error(1)
}

func (m *mockUserRepo) GetByID(ctx context.Context, id uuid.UUID) (model.User, error) {
	args := m.Called(ctx, id)
	return args.Get(0).(model.User), args.Error(1)
}

func mustHashPassword(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	return string(hash)
}

func TestRegister_Success(t *testing.T) {
	repo := &mockUserRepo{}
	svc := NewAuthService(repo, "test-secret-32-chars-minimum-ok!")

	expectedUser := model.User{ID: uuid.New(), Email: "test@example.com", Username: "testuser"}
	repo.On("Create", mock.Anything, "test@example.com", "testuser", mock.AnythingOfType("string")).
		Return(expectedUser, nil)

	user, err := svc.Register(context.Background(), "test@example.com", "testuser", "password123")
	require.NoError(t, err)
	assert.Equal(t, "test@example.com", user.Email)
	repo.AssertExpectations(t)
}

func TestRegister_WeakPassword(t *testing.T) {
	repo := &mockUserRepo{}
	svc := NewAuthService(repo, "test-secret")

	_, err := svc.Register(context.Background(), "test@example.com", "testuser", "short")
	assert.ErrorIs(t, err, ErrWeakPassword)
	repo.AssertNotCalled(t, "Create")
}

func TestRegister_InvalidEmail(t *testing.T) {
	repo := &mockUserRepo{}
	svc := NewAuthService(repo, "test-secret")

	_, err := svc.Register(context.Background(), "notanemail", "testuser", "password123")
	assert.ErrorIs(t, err, ErrInvalidEmail)
}

func TestRegister_EmailTaken(t *testing.T) {
	repo := &mockUserRepo{}
	svc := NewAuthService(repo, "test-secret")

	existingUser := model.User{ID: uuid.New(), Email: "test@example.com"}
	repo.On("Create", mock.Anything, "test@example.com", "testuser", mock.AnythingOfType("string")).
		Return(model.User{}, repository.ErrDuplicate)
	repo.On("GetByEmail", mock.Anything, "test@example.com").
		Return(existingUser, nil)

	_, err := svc.Register(context.Background(), "test@example.com", "testuser", "password123")
	assert.ErrorIs(t, err, ErrEmailTaken)
}

func TestLogin_Success(t *testing.T) {
	repo := &mockUserRepo{}
	svc := NewAuthService(repo, "test-secret-32-chars-minimum-ok!")

	userID := uuid.New()
	user := model.User{
		ID:           userID,
		Email:        "test@example.com",
		PasswordHash: mustHashPassword(t, "password123"),
	}
	repo.On("GetByEmail", mock.Anything, "test@example.com").Return(user, nil)

	token, err := svc.Login(context.Background(), "test@example.com", "password123")
	require.NoError(t, err)
	assert.NotEmpty(t, token)
}

func TestLogin_WrongPassword(t *testing.T) {
	repo := &mockUserRepo{}
	svc := NewAuthService(repo, "test-secret")

	user := model.User{
		ID:           uuid.New(),
		Email:        "test@example.com",
		PasswordHash: mustHashPassword(t, "correctpassword"),
	}
	repo.On("GetByEmail", mock.Anything, "test@example.com").Return(user, nil)

	_, err := svc.Login(context.Background(), "test@example.com", "wrongpassword")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestLogin_UserNotFound(t *testing.T) {
	repo := &mockUserRepo{}
	svc := NewAuthService(repo, "test-secret")

	repo.On("GetByEmail", mock.Anything, "noone@example.com").
		Return(model.User{}, repository.ErrNotFound)

	_, err := svc.Login(context.Background(), "noone@example.com", "password123")
	assert.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestValidateToken_RoundTrip(t *testing.T) {
	repo := &mockUserRepo{}
	svc := NewAuthService(repo, "test-secret-32-chars-minimum-ok!")

	userID := uuid.New()
	user := model.User{
		ID:           userID,
		Email:        "test@example.com",
		PasswordHash: mustHashPassword(t, "password123"),
	}
	repo.On("GetByEmail", mock.Anything, "test@example.com").Return(user, nil)

	token, err := svc.Login(context.Background(), "test@example.com", "password123")
	require.NoError(t, err)

	repo.On("GetByID", mock.Anything, userID).Return(user, nil)
	gotID, err := svc.ValidateToken(token)
	require.NoError(t, err)
	assert.Equal(t, userID, gotID)
}

func TestValidateToken_Invalid(t *testing.T) {
	repo := &mockUserRepo{}
	svc := NewAuthService(repo, "test-secret")

	_, err := svc.ValidateToken("not.a.valid.token")
	assert.Error(t, err)
}

func TestValidateToken_WrongSecret(t *testing.T) {
	repo := &mockUserRepo{}
	svc1 := NewAuthService(repo, "secret-one-32-chars-minimum-ok!!")
	svc2 := NewAuthService(repo, "secret-two-32-chars-minimum-ok!!")

	userID := uuid.New()
	user := model.User{
		ID:           userID,
		Email:        "test@example.com",
		PasswordHash: mustHashPassword(t, "password123"),
	}
	repo.On("GetByEmail", mock.Anything, "test@example.com").Return(user, nil)

	token, err := svc1.Login(context.Background(), "test@example.com", "password123")
	require.NoError(t, err)

	_, err = svc2.ValidateToken(token)
	assert.Error(t, err)
}
