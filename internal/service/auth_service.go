package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrLongPassword       = errors.New("password exceeds 72 UTF-8 bytes")
	ErrInvalidUsername    = errors.New("invalid username")
	ErrWeakPassword       = errors.New("password must be at least 8 characters")
	ErrInvalidEmail       = errors.New("invalid email address")
	ErrUsernameTaken      = errors.New("username already taken")
	ErrEmailTaken         = errors.New("email already registered")
)

type AuthService struct {
	userRepo  repository.UserRepository
	jwtSecret []byte
}

func NewAuthService(userRepo repository.UserRepository, jwtSecret string) *AuthService {
	return &AuthService{
		userRepo:  userRepo,
		jwtSecret: []byte(jwtSecret),
	}
}

func (s *AuthService) Register(ctx context.Context, email, username, password string) (model.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	username = strings.TrimSpace(username)

	if !isValidEmail(email) {
		return model.User{}, ErrInvalidEmail
	}
	if len(password) < 8 {
		return model.User{}, ErrWeakPassword
	}
	if utf8.RuneCountInString(username) < 2 || utf8.RuneCountInString(username) > 50 {
		return model.User{}, ErrInvalidUsername
	}
	if len(password) > 72 {
		return model.User{}, ErrLongPassword
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return model.User{}, fmt.Errorf("hash password: %w", err)
	}

	user, err := s.userRepo.Create(ctx, email, username, string(hash))
	if err != nil {
		if errors.Is(err, repository.ErrDuplicate) {
			// Try to determine which field is duplicate
			_, emailErr := s.userRepo.GetByEmail(ctx, email)
			if emailErr == nil {
				return model.User{}, ErrEmailTaken
			}
			return model.User{}, ErrUsernameTaken
		}
		return model.User{}, fmt.Errorf("create user: %w", err)
	}
	return user, nil
}

func (s *AuthService) Login(ctx context.Context, email, password string) (string, error) {
	user, err := s.AuthenticateUser(ctx, email, password)
	if err != nil {
		return "", err
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":          user.ID.String(),
		"exp":          time.Now().Add(24 * time.Hour).Unix(),
		"iat":          time.Now().Unix(),
		"auth_version": user.AuthVersion,
		"token_use":    "web",
	})

	tokenString, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return tokenString, nil
}

func (s *AuthService) AuthenticateUser(ctx context.Context, email, password string) (model.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	user, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return model.User{}, ErrInvalidCredentials
		}
		return model.User{}, fmt.Errorf("get user: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return model.User{}, ErrInvalidCredentials
	}

	return user, nil
}

func (s *AuthService) ValidateToken(tokenString string) (uuid.UUID, error) {
	return s.ValidateTokenContext(context.Background(), tokenString)
}
func (s *AuthService) ValidateTokenContext(ctx context.Context, tokenString string) (uuid.UUID, error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return uuid.Nil, fmt.Errorf("invalid token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return uuid.Nil, fmt.Errorf("invalid claims")
	}

	sub, ok := claims["sub"].(string)
	if !ok {
		return uuid.Nil, fmt.Errorf("missing sub claim")
	}

	id, err := uuid.Parse(sub)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid user id in token")
	}
	if use, ok := claims["token_use"].(string); ok && use != "web" {
		return uuid.Nil, fmt.Errorf("invalid token")
	}
	if _, ok := claims["sid"]; ok {
		return uuid.Nil, fmt.Errorf("invalid token")
	}
	version := int64(0)
	if raw, ok := claims["auth_version"]; ok {
		n, valid := raw.(float64)
		if !valid || n < 0 || n != float64(int64(n)) {
			return uuid.Nil, fmt.Errorf("invalid token")
		}
		version = int64(n)
	}
	user, err := s.userRepo.GetByID(ctx, id)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return uuid.Nil, ErrAuthUnavailable
	}
	if err != nil || user.AuthVersion != version {
		return uuid.Nil, fmt.Errorf("invalid token")
	}
	return id, nil
}

func isValidEmail(email string) bool {
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return false
	}
	if len(parts[0]) == 0 || len(parts[1]) == 0 {
		return false
	}
	return strings.Contains(parts[1], ".")
}
