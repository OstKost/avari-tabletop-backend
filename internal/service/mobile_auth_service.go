package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/ostkost/avari-tabletop-backend/internal/model"
	"github.com/ostkost/avari-tabletop-backend/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

var ErrAuthRateLimit = errors.New("auth rate limited")
var ErrAuthUnavailable = errors.New("auth unavailable")
var ErrAuthInput = errors.New("invalid auth input")

type UserDTO struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

func publicUser(u model.User) UserDTO { return UserDTO{u.ID, u.Email, u.Username, u.CreatedAt.UTC()} }

type MobileAuthSession struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in"`
	SessionID    uuid.UUID `json:"session_id"`
	User         UserDTO   `json:"user"`
}
type MobilePrincipal struct {
	User      UserDTO
	SessionID uuid.UUID
}

type authBucket struct {
	Count int
	Until time.Time
}
type AuthLimiter struct {
	mu      sync.Mutex
	buckets map[string]authBucket
}

func NewAuthLimiter() *AuthLimiter { return &AuthLimiter{buckets: make(map[string]authBucket)} }
func (l *AuthLimiter) Allow(key string, max int, window time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket, ok := l.buckets[key]
	if !ok || !now.Before(bucket.Until) {
		if len(l.buckets) >= 10000 {
			for k, b := range l.buckets {
				if !now.Before(b.Until) {
					delete(l.buckets, k)
				}
			}
			if len(l.buckets) >= 10000 {
				return false
			}
		}
		bucket = authBucket{Until: now.Add(window)}
	}
	bucket.Count++
	l.buckets[key] = bucket
	return bucket.Count <= max
}

type MobileAuthService struct {
	auth       *AuthService
	users      repository.UserRepository
	sessions   repository.MobileAuthRepository
	signingKey []byte
	replay     cipher.AEAD
	mailer     PasswordResetMailer
	limiter    *AuthLimiter
	now        func() time.Time
}

func NewMobileAuthService(auth *AuthService, users repository.UserRepository, sessions repository.MobileAuthRepository, secret string, mailer PasswordResetMailer) (*MobileAuthService, error) {
	key := sha256.Sum256([]byte("avari/mobile/replay/v1:" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	replay, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &MobileAuthService{auth: auth, users: users, sessions: sessions, signingKey: []byte(secret), replay: replay, mailer: mailer, limiter: NewAuthLimiter(), now: time.Now}, nil
}
func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
func randomToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
func validPassword(password string) bool {
	return utf8.ValidString(password) && utf8.RuneCountInString(password) >= 8 && len(password) <= 72
}
func validMobileEmail(email string) bool {
	return len(email) <= 255 && isValidEmail(email) && !strings.ContainsAny(email, " \t\r\n")
}
func (s *MobileAuthService) AllowPublic(peer string) bool {
	return s.limiter.Allow("public:"+peer, 10, time.Minute, s.now())
}
func (s *MobileAuthService) Login(ctx context.Context, email, password string) (MobileAuthSession, error) {
	if !validMobileEmail(strings.TrimSpace(email)) || len(password) < 1 || len(password) > 72 {
		return MobileAuthSession{}, ErrAuthInput
	}
	user, err := s.auth.AuthenticateUser(ctx, email, password)
	if err != nil {
		return MobileAuthSession{}, err
	}
	return s.create(ctx, user)
}
func (s *MobileAuthService) Register(ctx context.Context, email, username, password string) (MobileAuthSession, error) {
	if !validMobileEmail(strings.TrimSpace(email)) || !validPassword(password) {
		return MobileAuthSession{}, ErrAuthInput
	}
	user, err := s.auth.Register(ctx, email, username, password)
	if err != nil {
		return MobileAuthSession{}, err
	}
	return s.create(ctx, user)
}
func (s *MobileAuthService) create(ctx context.Context, user model.User) (MobileAuthSession, error) {
	sid := uuid.New()
	session, err := s.issue(user, sid)
	if err != nil {
		return MobileAuthSession{}, err
	}
	err = s.sessions.CreateSession(ctx, sid, user, tokenHash(session.RefreshToken), s.now().Add(30*24*time.Hour))
	return session, err
}
func (s *MobileAuthService) issue(user model.User, sid uuid.UUID) (MobileAuthSession, error) {
	refresh, err := randomToken()
	if err != nil {
		return MobileAuthSession{}, err
	}
	now := s.now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": user.ID.String(), "sid": sid.String(), "auth_version": user.AuthVersion,
		"token_use": "mobile_access", "aud": "avari-mobile", "iss": "avari-tabletop",
		"iat": now.Unix(), "exp": now.Add(15 * time.Minute).Unix(),
	})
	access, err := token.SignedString(s.signingKey)
	if err != nil {
		return MobileAuthSession{}, err
	}
	return MobileAuthSession{AccessToken: access, RefreshToken: refresh, ExpiresIn: 900, SessionID: sid, User: publicUser(user)}, nil
}
func (s *MobileAuthService) encrypt(session MobileAuthSession, sid uuid.UUID) ([]byte, error) {
	plain, err := json.Marshal(session)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, s.replay.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := s.replay.Seal(nonce, nonce, plain, []byte(sid.String()))
	return append(append([]byte{}, sid[:]...), sealed...), nil
}
func (s *MobileAuthService) Refresh(ctx context.Context, refresh string, attempt uuid.UUID, peer string) (MobileAuthSession, error) {
	if len(refresh) < 1 || len(refresh) > 128 || attempt == uuid.Nil {
		return MobileAuthSession{}, ErrAuthInput
	}
	// Invalid refresh requests are bounded, while successful replays bypass the
	// issuance limiter inside the rotation callback.
	if !s.limiter.Allow("refresh-lookup:"+peer, 120, time.Minute, s.now()) {
		return MobileAuthSession{}, ErrAuthRateLimit
	}
	var sid uuid.UUID
	encrypted, err := s.sessions.Rotate(ctx, tokenHash(refresh), attempt, s.now(), func(user model.User, id uuid.UUID) (repository.MobileRotation, error) {
		sid = id
		if !s.limiter.Allow("refresh-issue:"+user.ID.String(), 30, time.Minute, s.now()) {
			return repository.MobileRotation{}, ErrAuthRateLimit
		}
		session, err := s.issue(user, id)
		if err != nil {
			return repository.MobileRotation{}, err
		}
		sealed, err := s.encrypt(session, id)
		return repository.MobileRotation{NextHash: tokenHash(session.RefreshToken), Ciphertext: sealed}, err
	})
	if err != nil {
		return MobileAuthSession{}, err
	}
	// AAD includes the family UUID. The ciphertext envelope carries this public
	// UUID so replay decryption does not need a second database lookup.
	if sid == uuid.Nil && len(encrypted) >= 16 {
		copy(sid[:], encrypted[:16])
	}
	if len(encrypted) < 16+s.replay.NonceSize() {
		return MobileAuthSession{}, ErrAuthUnavailable
	}
	sealed := encrypted[16:]
	nonce := sealed[:s.replay.NonceSize()]
	plain, err := s.replay.Open(nil, nonce, sealed[s.replay.NonceSize():], []byte(sid.String()))
	if err != nil {
		return MobileAuthSession{}, ErrAuthUnavailable
	}
	var response MobileAuthSession
	if err = json.Unmarshal(plain, &response); err != nil {
		return MobileAuthSession{}, ErrAuthUnavailable
	}
	return response, nil
}

func (s *MobileAuthService) ValidateAccess(ctx context.Context, access string) (MobilePrincipal, error) {
	uid, sid, version, err := s.deletionIdentity(access)
	if err != nil {
		return MobilePrincipal{}, err
	}
	user, err := s.sessions.SessionUser(ctx, sid, uid, version, s.now())
	if err != nil {
		return MobilePrincipal{}, err
	}
	return MobilePrincipal{User: publicUser(user), SessionID: sid}, nil
}
func (s *MobileAuthService) Logout(ctx context.Context, sid uuid.UUID) error {
	return s.sessions.Revoke(ctx, sid, s.now())
}
func (s *MobileAuthService) RecoveryEnabled() bool { return s.mailer != nil && s.mailer.Enabled() }
func (s *MobileAuthService) RequestReset(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if !validMobileEmail(email) {
		return ErrAuthInput
	}
	if !s.RecoveryEnabled() {
		return ErrAuthUnavailable
	}
	if !s.limiter.Allow("reset-email:"+tokenHash(email), 3, time.Hour, s.now()) {
		return ErrAuthRateLimit
	}
	user, err := s.users.GetByEmail(ctx, email)
	if errors.Is(err, repository.ErrNotFound) {
		return nil
	}
	if err != nil {
		return ErrAuthUnavailable
	}
	token, err := randomToken()
	if err != nil {
		return ErrAuthUnavailable
	}
	expires := s.now().Add(15 * time.Minute)
	if err = s.sessions.CreateReset(ctx, tokenHash(token), user.ID, expires); err != nil {
		return ErrAuthUnavailable
	}
	if err = s.mailer.SendReset(ctx, email, token, expires); err != nil {
		// Same accepted response; do not disclose existence through delivery errors.
		slog.Error("password reset delivery failed")
	}
	return nil
}
func (s *MobileAuthService) ConfirmReset(ctx context.Context, token, password string) error {
	if len(token) < 1 || len(token) > 128 || !validPassword(password) {
		return ErrAuthInput
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return ErrAuthUnavailable
	}
	return s.sessions.ConsumeReset(ctx, tokenHash(token), string(hash), s.now())
}
func (s *MobileAuthService) Cleanup(ctx context.Context) error {
	return s.sessions.Cleanup(ctx, s.now())
}

// Signature-only identity is accepted exclusively for matching a previously
// accepted deletion replay. It never grants access or permits a new job.
func (s *MobileAuthService) DeletionIdentity(access string) (uuid.UUID, error) {
	uid, _, _, err := s.deletionIdentity(access)
	return uid, err
}
func (s *MobileAuthService) deletionIdentity(access string) (uuid.UUID, uuid.UUID, int64, error) {
	token, err := jwt.Parse(access, func(t *jwt.Token) (any, error) { return s.signingKey, nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithAudience("avari-mobile"), jwt.WithIssuer("avari-tabletop"), jwt.WithExpirationRequired(), jwt.WithTimeFunc(s.now))
	if err != nil || !token.Valid {
		return uuid.Nil, uuid.Nil, 0, repository.ErrSessionUnauthorized
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || claims["token_use"] != "mobile_access" {
		return uuid.Nil, uuid.Nil, 0, repository.ErrSessionUnauthorized
	}
	sub, _ := claims["sub"].(string)
	sidText, _ := claims["sid"].(string)
	uid, e1 := uuid.Parse(sub)
	sid, e2 := uuid.Parse(sidText)
	version, ok := claims["auth_version"].(float64)
	if e1 != nil || e2 != nil || !ok || version < 0 || version != float64(int64(version)) {
		return uuid.Nil, uuid.Nil, 0, repository.ErrSessionUnauthorized
	}
	return uid, sid, int64(version), nil
}
