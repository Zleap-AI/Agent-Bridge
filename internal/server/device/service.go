package device

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Zleap-AI/Agent-Bridge/internal/server/apierror"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/model"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/secret"
)

const pairingLifetime = 10 * time.Minute

var (
	ErrPairingInvalid  = errors.New("pairing code invalid")
	ErrPairingExpired  = errors.New("pairing code expired")
	ErrPairingConsumed = errors.New("pairing code consumed")
)

type Repository interface {
	ReplacePairingCode(context.Context, model.PairingCode) error
	ClaimPairingCode(context.Context, string, time.Time, model.Device) error
	ListDevices(context.Context) ([]model.Device, error)
	Device(context.Context, string) (model.Device, bool, error)
	RenameDevice(context.Context, string, string) (bool, error)
	DeleteDevice(context.Context, string) (bool, error)
	ListDeviceAgents(context.Context, string) ([]model.Agent, error)
}

type ConnectionCloser interface {
	Disconnect(string, string)
}

type Service struct {
	repo   Repository
	closer ConnectionCloser
	now    func() time.Time
}

type IssuedPairingCode struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
	ExpiresIn int64     `json:"expires_in"`
}

type Credentials struct {
	BridgeID string `json:"bridge_id"`
	Token    string `json:"token"`
}

func New(repo Repository, closer ConnectionCloser) *Service {
	return &Service{repo: repo, closer: closer, now: time.Now}
}

func (s *Service) CreatePairingCode(ctx context.Context) (IssuedPairingCode, error) {
	code, err := secret.PairingCode()
	if err != nil {
		return IssuedPairingCode{}, err
	}
	now := s.now().UTC()
	expiresAt := now.Add(pairingLifetime)
	id, err := secret.Token("pair_", 12)
	if err != nil {
		return IssuedPairingCode{}, err
	}
	item := model.PairingCode{
		ID:        id,
		CodeHash:  secret.Digest(secret.NormalizePairingCode(code)),
		ExpiresAt: expiresAt,
		CreatedAt: now,
	}
	if err := s.repo.ReplacePairingCode(ctx, item); err != nil {
		return IssuedPairingCode{}, err
	}
	return IssuedPairingCode{Code: code, ExpiresAt: expiresAt, ExpiresIn: int64(pairingLifetime.Seconds())}, nil
}

func (s *Service) Claim(ctx context.Context, code, name string) (model.Device, Credentials, error) {
	code = secret.NormalizePairingCode(code)
	if code == "" {
		return model.Device{}, Credentials{}, pairingError(ErrPairingInvalid)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "New Device"
	}
	if utf8.RuneCountInString(name) > 120 {
		return model.Device{}, Credentials{}, apierror.New(apierror.CodeInvalidRequest, "Device name must not exceed 120 characters", http.StatusBadRequest)
	}
	bridgeID, err := secret.Token("dev_", 16)
	if err != nil {
		return model.Device{}, Credentials{}, err
	}
	token, err := secret.Token("abt_", 32)
	if err != nil {
		return model.Device{}, Credentials{}, err
	}
	now := s.now().UTC()
	item := model.Device{
		BridgeID:  bridgeID,
		Name:      name,
		TokenHash: secret.Digest(token),
		CreatedAt: now,
	}
	if err := s.repo.ClaimPairingCode(ctx, secret.Digest(code), now, item); err != nil {
		return model.Device{}, Credentials{}, pairingError(err)
	}
	return item, Credentials{BridgeID: bridgeID, Token: token}, nil
}

func (s *Service) List(ctx context.Context) ([]model.Device, error) {
	return s.repo.ListDevices(ctx)
}

func (s *Service) Get(ctx context.Context, id string) (model.Device, error) {
	item, found, err := s.repo.Device(ctx, id)
	if err != nil {
		return model.Device{}, err
	}
	if !found {
		return model.Device{}, apierror.New(apierror.CodeDeviceNotFound, "Device was not found", http.StatusNotFound)
	}
	return item, nil
}

func (s *Service) Rename(ctx context.Context, id, name string) (model.Device, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 120 {
		return model.Device{}, apierror.New(apierror.CodeInvalidRequest, "Device name must be between 1 and 120 characters", http.StatusBadRequest)
	}
	updated, err := s.repo.RenameDevice(ctx, id, name)
	if err != nil {
		return model.Device{}, err
	}
	if !updated {
		return model.Device{}, apierror.New(apierror.CodeDeviceNotFound, "Device was not found", http.StatusNotFound)
	}
	return s.Get(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	deleted, err := s.repo.DeleteDevice(ctx, id)
	if err != nil {
		return err
	}
	if !deleted {
		return apierror.New(apierror.CodeDeviceNotFound, "Device was not found", http.StatusNotFound)
	}
	if s.closer != nil {
		s.closer.Disconnect(id, "device_deleted")
	}
	return nil
}

func (s *Service) Agents(ctx context.Context, id string) ([]model.Agent, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	return s.repo.ListDeviceAgents(ctx, id)
}

func pairingError(err error) error {
	switch {
	case errors.Is(err, ErrPairingExpired):
		return apierror.New(apierror.CodePairingCodeExpired, "Pairing code has expired", http.StatusGone)
	case errors.Is(err, ErrPairingConsumed):
		return apierror.New(apierror.CodePairingCodeConsumed, "Pairing code has already been used", http.StatusConflict)
	case errors.Is(err, ErrPairingInvalid):
		return apierror.New(apierror.CodePairingCodeInvalid, "Pairing code is invalid", http.StatusBadRequest)
	default:
		return err
	}
}
