package auth

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/server/apierror"
	serverdb "github.com/Zleap-AI/Agent-Bridge/internal/server/sqlite"
)

func TestSetupValidatesTokenBeforeHashingPassword(t *testing.T) {
	ctx := context.Background()
	store, err := serverdb.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store)
	token, err := service.PrepareSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hashCalls := 0
	service.hashPassword = func(string) (string, error) {
		hashCalls++
		return "test-password-hash", nil
	}

	if err := service.Setup(ctx, "invalid-token", "owner password"); apierror.As(err).Status != http.StatusUnauthorized {
		t.Fatalf("invalid setup token error = %v", err)
	}
	if hashCalls != 0 {
		t.Fatalf("password hash ran %d times for an invalid setup token", hashCalls)
	}
	if err := service.Setup(ctx, token, "owner password"); err != nil {
		t.Fatal(err)
	}
	if hashCalls != 1 {
		t.Fatalf("password hash calls = %d, want 1", hashCalls)
	}
}

func TestPasswordWorkLimitReturnsTooManyRequests(t *testing.T) {
	ctx := context.Background()
	store, err := serverdb.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store)
	token, err := service.PrepareSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for range cap(service.passwordSlots) {
		service.passwordSlots <- struct{}{}
	}
	assertRateLimited := func(err error) {
		t.Helper()
		apiErr := apierror.As(err)
		if apiErr.Status != http.StatusTooManyRequests || apiErr.Code != apierror.CodeRateLimited {
			t.Fatalf("error = %#v, want RATE_LIMITED 429", apiErr)
		}
	}
	assertRateLimited(service.Setup(ctx, token, "owner password"))

	// Invalid tokens are rejected before an Argon2 slot is considered.
	if err := service.Setup(ctx, "invalid-token", "owner password"); apierror.As(err).Status != http.StatusUnauthorized {
		t.Fatalf("invalid setup token while saturated = %v", err)
	}
	for range cap(service.passwordSlots) {
		<-service.passwordSlots
	}
	service.hashPassword = func(string) (string, error) { return "test-password-hash", nil }
	if err := service.Setup(ctx, token, "owner password"); err != nil {
		t.Fatal(err)
	}

	for range cap(service.passwordSlots) {
		service.passwordSlots <- struct{}{}
	}
	_, err = service.Login(ctx, "owner password")
	assertRateLimited(err)
}

func TestSetupLoginExpiryAndPasswordReset(t *testing.T) {
	ctx := context.Background()
	store, err := serverdb.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	service := New(store)
	service.now = func() time.Time { return now }

	first, err := service.PrepareSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.PrepareSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("setup token was not rotated")
	}
	if err := service.Setup(ctx, first, "correct horse battery staple"); err == nil {
		t.Fatal("rotated setup token unexpectedly succeeded")
	}
	if err := service.Setup(ctx, second, "correct horse battery staple"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := service.PrepareSetupToken(ctx); err == nil {
		t.Fatal("initialized server issued another setup token")
	}
	if _, err := service.Login(ctx, "wrong password"); err == nil {
		t.Fatal("wrong password logged in")
	}
	session, err := service.Login(ctx, "correct horse battery staple")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := session.ExpiresAt.Sub(now); got != 30*24*time.Hour {
		t.Fatalf("session lifetime = %s", got)
	}
	valid, err := service.AuthenticateOwner(ctx, session.Token)
	if err != nil || !valid {
		t.Fatalf("valid session rejected: valid=%v err=%v", valid, err)
	}
	now = now.Add(30*24*time.Hour + time.Second)
	valid, err = service.AuthenticateOwner(ctx, session.Token)
	if err != nil || valid {
		t.Fatalf("expired session accepted: valid=%v err=%v", valid, err)
	}

	now = now.Add(time.Minute)
	fresh, err := service.Login(ctx, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ResetPassword(ctx, "a completely new password"); err != nil {
		t.Fatal(err)
	}
	valid, err = service.AuthenticateOwner(ctx, fresh.Token)
	if err != nil || valid {
		t.Fatalf("session survived password reset: valid=%v err=%v", valid, err)
	}
	if _, err := service.Login(ctx, "correct horse battery staple"); err == nil {
		t.Fatal("old password remained valid")
	}
	if _, err := service.Login(ctx, "a completely new password"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
}

func TestUnicodePasswordSetupLoginAndReset(t *testing.T) {
	ctx := context.Background()
	store, err := serverdb.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store)
	token, err := service.PrepareSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := service.Setup(ctx, token, strings.Repeat("密", 7)); err == nil {
		t.Fatal("seven Unicode code points were accepted as an Owner Password")
	}
	initial := strings.Repeat("🔐", 8)
	if err := service.Setup(ctx, token, initial); err != nil {
		t.Fatalf("eight emoji Owner Password rejected: %v", err)
	}
	if _, err := service.Login(ctx, initial); err != nil {
		t.Fatalf("emoji Owner Password login failed: %v", err)
	}
	if _, err := service.Login(ctx, ""); apierror.As(err).Status != http.StatusBadRequest {
		t.Fatalf("empty login password error = %v, want 400", err)
	}
	if _, err := service.Login(ctx, "短"); apierror.As(err).Status != http.StatusUnauthorized {
		t.Fatalf("non-empty short login password error = %v, want authentication attempt", err)
	}

	replacement := "中文密码安全测试"
	if err := service.ResetPassword(ctx, replacement); err != nil {
		t.Fatalf("Chinese Owner Password reset failed: %v", err)
	}
	if _, err := service.Login(ctx, initial); err == nil {
		t.Fatal("old emoji Owner Password remained valid after reset")
	}
	if _, err := service.Login(ctx, replacement); err != nil {
		t.Fatalf("Chinese Owner Password login failed: %v", err)
	}
}

func TestAPIKeyShownOnceAndRevokedImmediately(t *testing.T) {
	ctx := context.Background()
	store, err := serverdb.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store)
	created, err := service.CreateAPIKey(ctx, "Automation")
	if err != nil {
		t.Fatal(err)
	}
	if created.Key == "" || created.KeyHash == "" {
		t.Fatal("created key is incomplete")
	}
	items, err := service.ListAPIKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].KeyHash == "" {
		t.Fatalf("stored API key metadata = %#v", items)
	}
	key, valid, err := service.AuthenticateAPIKey(ctx, created.Key)
	if err != nil || !valid || key.ID != created.ID {
		t.Fatalf("API key rejected: key=%#v valid=%v err=%v", key, valid, err)
	}
	if err := service.DeleteAPIKey(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	_, valid, err = service.AuthenticateAPIKey(ctx, created.Key)
	if err != nil || valid {
		t.Fatalf("revoked API key accepted: valid=%v err=%v", valid, err)
	}
}

func TestAPIKeyNameLengthCountsUnicodeCodePoints(t *testing.T) {
	ctx := context.Background()
	store, err := serverdb.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store)

	if _, err := service.CreateAPIKey(ctx, strings.Repeat("集", 100)); err != nil {
		t.Fatalf("100-code-point API key name rejected: %v", err)
	}
	if _, err := service.CreateAPIKey(ctx, strings.Repeat("🔑", 101)); err == nil {
		t.Fatal("101-code-point API key name was accepted")
	}
}

func TestOwnerSessionAndAPIKeyPersistAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := serverdb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	service := New(store)
	token, err := service.PrepareSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Setup(ctx, token, "persistent password"); err != nil {
		t.Fatal(err)
	}
	session, err := service.Login(ctx, "persistent password")
	if err != nil {
		t.Fatal(err)
	}
	key, err := service.CreateAPIKey(ctx, "Persistent integration")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = serverdb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service = New(store)
	valid, err := service.AuthenticateOwner(ctx, session.Token)
	if err != nil || !valid {
		t.Fatalf("persisted Owner session rejected: valid=%v err=%v", valid, err)
	}
	_, valid, err = service.AuthenticateAPIKey(ctx, key.Key)
	if err != nil || !valid {
		t.Fatalf("persisted API key rejected: valid=%v err=%v", valid, err)
	}
}
