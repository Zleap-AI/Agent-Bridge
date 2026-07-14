package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPairingClientClaimsCredentialsUsingOrdinaryServerURL(t *testing.T) {
	var request struct {
		Code     string `json:"code"`
		Hostname string `json:"hostname"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/pairings/claim" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("Content-Type = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"device": map[string]string{"id": "bridge-1", "name": "Workstation"},
			"credentials": map[string]string{
				"bridge_id":  "bridge-1",
				"token":      "bridge-token",
				"server_url": strings.Replace(serverURLFromRequest(r), "http://", "ws://", 1) + "/ws",
			},
		})
	}))
	defer server.Close()

	result, err := newPairingClient(server.Client()).Claim(context.Background(), server.URL, "ABC-123", "my-host")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if request.Code != "ABC-123" || request.Hostname != "my-host" {
		t.Fatalf("claim request = %+v", request)
	}
	if result.BridgeID != "bridge-1" || result.Token != "bridge-token" {
		t.Fatalf("credentials = %+v", result)
	}
	if result.ServerURL != strings.Replace(server.URL, "http://", "ws://", 1)+"/ws" {
		t.Fatalf("server_url = %q", result.ServerURL)
	}
	if result.DeviceName != "Workstation" {
		t.Fatalf("device name = %q", result.DeviceName)
	}
}

func TestPairingClientAcceptsTopLevelContractAndDerivesWebSocketURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"bridge_id": "bridge-2",
			"token":     "token-2",
		})
	}))
	defer server.Close()

	result, err := newPairingClient(server.Client()).Claim(context.Background(), server.URL+"/", "code", "host")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	wantURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/ws"
	if result.ServerURL != wantURL {
		t.Fatalf("derived server_url = %q, want %q", result.ServerURL, wantURL)
	}
}

func TestPairingClientPreservesServerErrorCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"code":    "PAIRING_CODE_EXPIRED",
				"message": "配对码已过期",
			},
		})
	}))
	defer server.Close()

	_, err := newPairingClient(server.Client()).Claim(context.Background(), server.URL, "expired", "host")
	pairErr, ok := err.(*pairingError)
	if !ok {
		t.Fatalf("error = %T %v, want *pairingError", err, err)
	}
	if pairErr.Code != "PAIRING_CODE_EXPIRED" || pairErr.Status != http.StatusGone {
		t.Fatalf("pairing error = %+v", pairErr)
	}
}

func TestPairingClientRejectsWebSocketInputURL(t *testing.T) {
	_, err := newPairingClient(http.DefaultClient).Claim(context.Background(), "wss://bridge.example.com/ws", "code", "host")
	if err == nil || !strings.Contains(err.Error(), "HTTP") {
		t.Fatalf("Claim error = %v, want HTTP/HTTPS validation error", err)
	}
}

func TestNormalizeServerURLRejectsUnsupportedPath(t *testing.T) {
	if _, _, err := normalizeHTTPServerURL("https://bridge.example.com/agent-bridge"); err == nil {
		t.Fatal("Server URL with an unsupported path was accepted")
	}
	if normalized, _, err := normalizeHTTPServerURL("https://bridge.example.com/"); err != nil || normalized != "https://bridge.example.com" {
		t.Fatalf("root Server URL = %q, %v", normalized, err)
	}
}

func serverURLFromRequest(r *http.Request) string {
	return "http://" + r.Host
}
