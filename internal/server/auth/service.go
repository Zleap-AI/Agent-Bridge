package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Zleap-AI/Agent-Bridge/internal/server/apierror"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/model"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/secret"
	"golang.org/x/crypto/argon2"
)

const (
	OwnerCookieName                 = "agent_bridge_owner"
	sessionLifetime                 = 30 * 24 * time.Hour
	maxConcurrentPasswordOperations = 2
)

type Repository interface {
	Initialized(context.Context) (bool, error)
	ReplaceSetupTokenHash(context.Context, string) error
	ValidateSetupTokenHash(context.Context, string) (bool, error)
	InitializeOwner(context.Context, string, string, time.Time) (bool, error)
	OwnerCredential(context.Context) (model.OwnerCredential, error)
	CreateOwnerSession(context.Context, model.OwnerSession) error
	ValidateOwnerSession(context.Context, string, time.Time) (bool, error)
	DeleteOwnerSession(context.Context, string) error
	ResetOwnerPassword(context.Context, string, time.Time) error
	CreateAPIKey(context.Context, model.APIKey) error
	ListAPIKeys(context.Context) ([]model.APIKey, error)
	DeleteAPIKey(context.Context, string) (bool, error)
	AuthenticateAPIKey(context.Context, string, time.Time) (model.APIKey, bool, error)
}

type Service struct {
	repo           Repository
	now            func() time.Time
	hashPassword   func(string) (string, error)
	verifyPassword func(string, string) (bool, error)
	passwordSlots  chan struct{}
}

type Session struct {
	Token     string
	ExpiresAt time.Time
}

type CreatedAPIKey struct {
	model.APIKey
	Key string `json:"key"`
}

func New(repo Repository) *Service {
	return &Service{
		repo: repo, now: time.Now,
		hashPassword: HashPassword, verifyPassword: VerifyPassword,
		passwordSlots: make(chan struct{}, maxConcurrentPasswordOperations),
	}
}

func (s *Service) IsInitialized(ctx context.Context) (bool, error) {
	return s.repo.Initialized(ctx)
}

// PrepareSetupToken rotates the one-time setup token while the server is uninitialized.
func (s *Service) PrepareSetupToken(ctx context.Context) (string, error) {
	initialized, err := s.repo.Initialized(ctx)
	if err != nil {
		return "", err
	}
	if initialized {
		return "", apierror.New(apierror.CodeConflict, "Server is already initialized", http.StatusConflict)
	}
	token, err := secret.Token("setup_", 32)
	if err != nil {
		return "", err
	}
	if err := s.repo.ReplaceSetupTokenHash(ctx, secret.Digest(token)); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) Setup(ctx context.Context, setupToken, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	setupHash := secret.Digest(setupToken)
	valid, err := s.repo.ValidateSetupTokenHash(ctx, setupHash)
	if err != nil {
		return err
	}
	if !valid {
		return apierror.New(apierror.CodeUnauthorized, "Setup token is invalid or has expired", http.StatusUnauthorized)
	}
	if !s.acquirePasswordSlot() {
		return passwordWorkLimitError()
	}
	defer s.releasePasswordSlot()
	hash, err := s.hashPassword(password)
	if err != nil {
		return fmt.Errorf("hash owner password: %w", err)
	}
	ok, err := s.repo.InitializeOwner(ctx, setupHash, hash, s.now().UTC())
	if err != nil {
		return err
	}
	if !ok {
		return apierror.New(apierror.CodeUnauthorized, "Setup token is invalid or has expired", http.StatusUnauthorized)
	}
	return nil
}

func (s *Service) Login(ctx context.Context, password string) (Session, error) {
	passwordLength := utf8.RuneCountInString(password)
	if passwordLength == 0 || passwordLength > 1024 {
		return Session{}, apierror.New(apierror.CodeInvalidRequest, "Password must be between 1 and 1024 characters", http.StatusBadRequest)
	}
	credential, err := s.repo.OwnerCredential(ctx)
	if err != nil {
		return Session{}, apierror.Wrap(apierror.CodeUnauthorized, "Owner password is incorrect", http.StatusUnauthorized, err)
	}
	if !s.acquirePasswordSlot() {
		return Session{}, passwordWorkLimitError()
	}
	defer s.releasePasswordSlot()
	valid, err := s.verifyPassword(password, credential.PasswordHash)
	if err != nil || !valid {
		return Session{}, apierror.New(apierror.CodeUnauthorized, "Owner password is incorrect", http.StatusUnauthorized)
	}
	token, err := secret.Token("abs_", 32)
	if err != nil {
		return Session{}, err
	}
	now := s.now().UTC()
	expiresAt := now.Add(sessionLifetime)
	sessionID, err := secret.Token("session_", 12)
	if err != nil {
		return Session{}, err
	}
	if err := s.repo.CreateOwnerSession(ctx, model.OwnerSession{
		ID:          sessionID,
		TokenHash:   secret.Digest(token),
		ExpiresAt:   expiresAt,
		AuthVersion: credential.AuthVersion,
		CreatedAt:   now,
	}); err != nil {
		return Session{}, err
	}
	return Session{Token: token, ExpiresAt: expiresAt}, nil
}

func (s *Service) AuthenticateOwner(ctx context.Context, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	return s.repo.ValidateOwnerSession(ctx, secret.Digest(token), s.now().UTC())
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.repo.DeleteOwnerSession(ctx, secret.Digest(token))
}

func (s *Service) ResetPassword(ctx context.Context, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	if !s.acquirePasswordSlot() {
		return passwordWorkLimitError()
	}
	defer s.releasePasswordSlot()
	hash, err := s.hashPassword(password)
	if err != nil {
		return err
	}
	return s.repo.ResetOwnerPassword(ctx, hash, s.now().UTC())
}

func (s *Service) acquirePasswordSlot() bool {
	select {
	case s.passwordSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Service) releasePasswordSlot() {
	<-s.passwordSlots
}

func passwordWorkLimitError() error {
	return apierror.New(apierror.CodeRateLimited, "Too many authentication attempts are in progress", http.StatusTooManyRequests)
}

func (s *Service) CreateAPIKey(ctx context.Context, name string) (CreatedAPIKey, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return CreatedAPIKey{}, apierror.New(apierror.CodeInvalidRequest, "API key name must be between 1 and 100 characters", http.StatusBadRequest)
	}
	raw, err := secret.Token("abk_", 32)
	if err != nil {
		return CreatedAPIKey{}, err
	}
	now := s.now().UTC()
	id, err := secret.Token("key_", 12)
	if err != nil {
		return CreatedAPIKey{}, err
	}
	item := model.APIKey{
		ID:        id,
		Name:      name,
		KeyHash:   secret.Digest(raw),
		Prefix:    raw[:min(12, len(raw))],
		CreatedAt: now,
	}
	if err := s.repo.CreateAPIKey(ctx, item); err != nil {
		return CreatedAPIKey{}, err
	}
	return CreatedAPIKey{APIKey: item, Key: raw}, nil
}

func (s *Service) ListAPIKeys(ctx context.Context) ([]model.APIKey, error) {
	return s.repo.ListAPIKeys(ctx)
}

func (s *Service) DeleteAPIKey(ctx context.Context, id string) error {
	deleted, err := s.repo.DeleteAPIKey(ctx, id)
	if err != nil {
		return err
	}
	if !deleted {
		return apierror.New(apierror.CodeInvalidRequest, "API key was not found", http.StatusNotFound)
	}
	return nil
}

func (s *Service) AuthenticateAPIKey(ctx context.Context, raw string) (model.APIKey, bool, error) {
	if !strings.HasPrefix(raw, "abk_") {
		return model.APIKey{}, false, nil
	}
	return s.repo.AuthenticateAPIKey(ctx, secret.Digest(raw), s.now().UTC())
}

func validatePassword(password string) error {
	passwordLength := utf8.RuneCountInString(password)
	if passwordLength < 8 || passwordLength > 1024 {
		return apierror.New(apierror.CodeInvalidRequest, "Password must be between 8 and 1024 characters", http.StatusBadRequest)
	}
	return nil
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	const memory = 19 * 1024
	const iterations = 2
	const parallelism = 1
	hash := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, fmt.Errorf("invalid password hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, fmt.Errorf("invalid argon2 version")
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false, fmt.Errorf("invalid argon2 parameters")
	}
	memory64, err := strconv.ParseUint(strings.TrimPrefix(params[0], "m="), 10, 32)
	if err != nil {
		return false, err
	}
	iterations64, err := strconv.ParseUint(strings.TrimPrefix(params[1], "t="), 10, 32)
	if err != nil {
		return false, err
	}
	parallelism64, err := strconv.ParseUint(strings.TrimPrefix(params[2], "p="), 10, 8)
	if err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, uint32(iterations64), uint32(memory64), uint8(parallelism64), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
