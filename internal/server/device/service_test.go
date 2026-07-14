package device_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/server/apierror"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/device"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/model"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/secret"
	serverdb "github.com/Zleap-AI/Agent-Bridge/internal/server/sqlite"
)

type closer struct {
	id     string
	reason string
}

func (c *closer) Disconnect(id, reason string) { c.id, c.reason = id, reason }

func TestPairingIsSingleUseAndNewCodeInvalidatesOldCode(t *testing.T) {
	ctx := context.Background()
	store, err := serverdb.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	connectionCloser := &closer{}
	service := device.New(store, connectionCloser)

	old, err := service.CreatePairingCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current, err := service.CreatePairingCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Claim(ctx, old.Code, "Laptop"); errorCode(err) != apierror.CodePairingCodeInvalid {
		t.Fatalf("replaced code error = %v", err)
	}
	paired, credentials, err := service.Claim(ctx, current.Code, "Laptop")
	if err != nil {
		t.Fatal(err)
	}
	if paired.BridgeID == "" || credentials.BridgeID != paired.BridgeID || credentials.Token == "" {
		t.Fatalf("invalid credentials: device=%#v credentials=%#v", paired, credentials)
	}
	if _, _, err := service.Claim(ctx, current.Code, "Other"); errorCode(err) != apierror.CodePairingCodeConsumed {
		t.Fatalf("reused code error = %v", err)
	}
	if err := service.Delete(ctx, paired.BridgeID); err != nil {
		t.Fatal(err)
	}
	if connectionCloser.id != paired.BridgeID || connectionCloser.reason != "device_deleted" {
		t.Fatalf("active connection was not closed: %#v", connectionCloser)
	}
}

func TestExpiredPairingCodeHasStableError(t *testing.T) {
	ctx := context.Background()
	store, err := serverdb.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	code := "ABCD-EFGH"
	if err := store.ReplacePairingCode(ctx, model.PairingCode{
		ID: "pair_expired", CodeHash: secret.Digest(secret.NormalizePairingCode(code)),
		CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	service := device.New(store, nil)
	if _, _, err := service.Claim(ctx, code, "Laptop"); errorCode(err) != apierror.CodePairingCodeExpired {
		t.Fatalf("expired code error = %v", err)
	}
}

func TestDeviceNameLengthCountsUnicodeCodePoints(t *testing.T) {
	ctx := context.Background()
	store, err := serverdb.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := device.New(store, nil)

	code, err := service.CreatePairingCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	item, _, err := service.Claim(ctx, code.Code, strings.Repeat("设", 120))
	if err != nil {
		t.Fatalf("120-code-point Device name rejected: %v", err)
	}
	if _, err := service.Rename(ctx, item.BridgeID, strings.Repeat("💻", 120)); err != nil {
		t.Fatalf("120-emoji Device name rejected: %v", err)
	}
	if _, err := service.Rename(ctx, item.BridgeID, strings.Repeat("设", 121)); err == nil {
		t.Fatal("121-code-point Device name was accepted")
	}
}

func errorCode(err error) string {
	var apiErr *apierror.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}
