package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/infra"
	"github.com/Zleap-AI/Agent-Bridge/internal/service"
)

type fakePairingClaimer struct {
	serverURL string
	code      string
	hostname  string
	result    pairingResult
	err       error
}

func (f *fakePairingClaimer) Claim(_ context.Context, serverURL, code, hostname string) (pairingResult, error) {
	f.serverURL, f.code, f.hostname = serverURL, code, hostname
	return f.result, f.err
}

type fakeTunnelController struct {
	config      service.TunnelConfig
	status      tunnelStatus
	stopped     bool
	switchCount int
	stopCount   int
}

func (f *fakeTunnelController) Switch(config service.TunnelConfig) {
	f.config = config
	f.status = tunnelStatus{State: "connected", Connected: true}
	f.switchCount++
}
func (f *fakeTunnelController) Stop() {
	f.stopped = true
	f.status = tunnelStatus{State: "unpaired"}
	f.stopCount++
}
func (f *fakeTunnelController) Status() tunnelStatus { return f.status }

type delayedPairingPlan struct {
	started chan struct{}
	release chan struct{}
	result  pairingResult
	err     error
	once    sync.Once
}

type delayedPairingClaimer struct {
	plans map[string]*delayedPairingPlan
}

func (f *delayedPairingClaimer) Claim(_ context.Context, _ string, code, _ string) (pairingResult, error) {
	plan := f.plans[code]
	if plan == nil {
		return pairingResult{}, errors.New("unexpected pairing code")
	}
	plan.once.Do(func() { close(plan.started) })
	<-plan.release
	return plan.result, plan.err
}

func TestLocalPairingAPIClaimsSavesAndStartsTunnel(t *testing.T) {
	var saved infra.Config
	state := newConfigState(infra.DefaultConfig(), func(cfg *infra.Config) error {
		saved = *cfg
		return nil
	})
	pairer := &fakePairingClaimer{result: pairingResult{
		BridgeID:   "bridge-1",
		Token:      "token-1",
		ServerURL:  "wss://bridge.example.com/ws",
		DeviceName: "My PC",
	}}
	tunnels := &fakeTunnelController{}
	handler := newTestLocalHandler(state, pairer, tunnels)

	response := requestJSON(t, handler, http.MethodPost, "/api/v1/local/pairings", map[string]any{
		"server_url":   "https://bridge.example.com",
		"pairing_code": "PAIR-123",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if pairer.serverURL != "https://bridge.example.com" || pairer.code != "PAIR-123" || pairer.hostname == "" {
		t.Fatalf("claim args = server:%q code:%q hostname:%q", pairer.serverURL, pairer.code, pairer.hostname)
	}
	if saved.ServerURL != pairer.result.ServerURL || saved.BridgeID != "bridge-1" || saved.Token != "token-1" {
		t.Fatalf("saved config = %+v", saved)
	}
	if tunnels.config.ServerURL != saved.ServerURL || tunnels.config.BridgeID != saved.BridgeID || tunnels.config.Token != saved.Token {
		t.Fatalf("tunnel config = %+v, saved = %+v", tunnels.config, saved)
	}

	var body struct {
		Remote localRemoteStatus `json:"remote"`
	}
	decodeResponse(t, response, &body)
	if !body.Remote.Paired || !body.Remote.Connected || body.Remote.DeviceID != "bridge-1" {
		t.Fatalf("remote status = %+v", body.Remote)
	}
}

func TestLocalPairingAPIRequiresConfirmationBeforeSwitchingServer(t *testing.T) {
	state := newConfigState(&infra.Config{
		BridgeID: "old-bridge", Token: "old-token", ServerURL: "wss://old.example.com/ws", AdminPort: 9202,
	}, func(*infra.Config) error { return nil })
	pairer := &fakePairingClaimer{}
	handler := newTestLocalHandler(state, pairer, &fakeTunnelController{})

	response := requestJSON(t, handler, http.MethodPost, "/api/v1/local/pairings", map[string]any{
		"server_url": "https://new.example.com", "pairing_code": "PAIR-123",
	})
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var body apiErrorResponse
	decodeResponse(t, response, &body)
	if body.Error.Code != "PAIRING_REPLACE_CONFIRMATION_REQUIRED" {
		t.Fatalf("error = %+v", body.Error)
	}
	if pairer.code != "" {
		t.Fatal("pairing server was called before replacement confirmation")
	}
}

func TestLocalUnpairingClearsRemoteConfigAndStopsTunnel(t *testing.T) {
	var saved infra.Config
	state := newConfigState(&infra.Config{
		BridgeID: "bridge-1", Token: "token-1", ServerURL: "wss://bridge.example.com/ws", AdminPort: 9202,
	}, func(cfg *infra.Config) error { saved = *cfg; return nil })
	tunnels := &fakeTunnelController{status: tunnelStatus{State: "connected", Connected: true}}
	handler := newTestLocalHandler(state, &fakePairingClaimer{}, tunnels)

	response := requestJSON(t, handler, http.MethodDelete, "/api/v1/local/pairing", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if saved.ServerURL != "" || saved.BridgeID != "" || saved.Token != "" {
		t.Fatalf("saved config after unpair = %+v", saved)
	}
	if !tunnels.stopped {
		t.Fatal("tunnel was not stopped")
	}
}

func TestDelayedPairingCannotReapplyAfterUnpair(t *testing.T) {
	state := newConfigState(&infra.Config{
		BridgeID: "old-bridge", Token: "old-token", ServerURL: "wss://old.example.com/ws", AdminPort: 9202,
	}, func(*infra.Config) error { return nil })
	plan := &delayedPairingPlan{
		started: make(chan struct{}),
		release: make(chan struct{}),
		result: pairingResult{
			BridgeID: "new-bridge", Token: "new-token", ServerURL: "wss://new.example.com/ws", DeviceName: "New Device",
		},
	}
	tunnels := &fakeTunnelController{status: tunnelStatus{State: "connected", Connected: true}}
	handler := newTestLocalHandler(state, &delayedPairingClaimer{plans: map[string]*delayedPairingPlan{"PAIR-NEW": plan}}, tunnels)

	pairingDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		pairingDone <- requestJSON(t, handler, http.MethodPost, "/api/v1/local/pairings", map[string]any{
			"server_url": "https://new.example.com", "pairing_code": "PAIR-NEW", "replace": true,
		})
	}()
	<-plan.started

	unpair := requestJSON(t, handler, http.MethodDelete, "/api/v1/local/pairing", nil)
	if unpair.Code != http.StatusOK {
		t.Fatalf("unpair status = %d body=%s", unpair.Code, unpair.Body.String())
	}
	close(plan.release)
	pairing := <-pairingDone
	if pairing.Code != http.StatusConflict {
		t.Fatalf("stale pairing status = %d body=%s", pairing.Code, pairing.Body.String())
	}
	var pairingError apiErrorResponse
	decodeResponse(t, pairing, &pairingError)
	if pairingError.Error.Code != "PAIRING_OPERATION_SUPERSEDED" {
		t.Fatalf("stale pairing error = %+v", pairingError.Error)
	}
	config := state.Snapshot()
	if config.HasRemoteConnection() || config.Token != "" {
		t.Fatalf("stale pairing restored config = %+v", config)
	}
	if tunnels.switchCount != 0 || tunnels.stopCount != 1 || !tunnels.stopped {
		t.Fatalf("tunnel operations = switches:%d stops:%d stopped:%v", tunnels.switchCount, tunnels.stopCount, tunnels.stopped)
	}
}

func TestLatestStartedPairingWinsWhenClaimsReturnInReverseOrder(t *testing.T) {
	state := newConfigState(infra.DefaultConfig(), func(*infra.Config) error { return nil })
	first := &delayedPairingPlan{
		started: make(chan struct{}), release: make(chan struct{}),
		result: pairingResult{BridgeID: "first-bridge", Token: "first-token", ServerURL: "wss://first.example.com/ws"},
	}
	second := &delayedPairingPlan{
		started: make(chan struct{}), release: make(chan struct{}),
		result: pairingResult{BridgeID: "second-bridge", Token: "second-token", ServerURL: "wss://second.example.com/ws"},
	}
	pairer := &delayedPairingClaimer{plans: map[string]*delayedPairingPlan{"PAIR-FIRST": first, "PAIR-SECOND": second}}
	tunnels := &fakeTunnelController{}
	handler := newTestLocalHandler(state, pairer, tunnels)

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- requestJSON(t, handler, http.MethodPost, "/api/v1/local/pairings", map[string]any{
			"server_url": "https://first.example.com", "pairing_code": "PAIR-FIRST",
		})
	}()
	<-first.started

	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		secondDone <- requestJSON(t, handler, http.MethodPost, "/api/v1/local/pairings", map[string]any{
			"server_url": "https://second.example.com", "pairing_code": "PAIR-SECOND",
		})
	}()
	<-second.started

	close(second.release)
	secondResponse := <-secondDone
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("latest pairing status = %d body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	close(first.release)
	firstResponse := <-firstDone
	if firstResponse.Code != http.StatusConflict {
		t.Fatalf("older pairing status = %d body=%s", firstResponse.Code, firstResponse.Body.String())
	}

	config := state.Snapshot()
	if config.ServerURL != second.result.ServerURL || config.BridgeID != second.result.BridgeID || config.Token != second.result.Token {
		t.Fatalf("winning config = %+v, want second pairing", config)
	}
	if tunnels.switchCount != 1 || tunnels.config.BridgeID != second.result.BridgeID {
		t.Fatalf("tunnel operations = switches:%d config:%+v", tunnels.switchCount, tunnels.config)
	}
}

func TestPairingSaveFailureDoesNotSwitchTunnel(t *testing.T) {
	state := newConfigState(infra.DefaultConfig(), func(*infra.Config) error { return errors.New("disk unavailable") })
	pairer := &fakePairingClaimer{result: pairingResult{
		BridgeID: "bridge-1", Token: "token-1", ServerURL: "wss://bridge.example.com/ws",
	}}
	tunnels := &fakeTunnelController{}
	handler := newTestLocalHandler(state, pairer, tunnels)

	response := requestJSON(t, handler, http.MethodPost, "/api/v1/local/pairings", map[string]any{
		"server_url": "https://bridge.example.com", "pairing_code": "PAIR-123",
	})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if state.Snapshot().HasRemoteConnection() || tunnels.switchCount != 0 {
		t.Fatalf("save failure changed config or tunnel: config=%+v switches=%d", state.Snapshot(), tunnels.switchCount)
	}
}

func TestUnpairSaveFailureKeepsConfigAndTunnel(t *testing.T) {
	initial := &infra.Config{
		BridgeID: "bridge-1", Token: "token-1", ServerURL: "wss://bridge.example.com/ws", AdminPort: 9202,
	}
	state := newConfigState(initial, func(*infra.Config) error { return errors.New("disk unavailable") })
	tunnels := &fakeTunnelController{status: tunnelStatus{State: "connected", Connected: true}}
	handler := newTestLocalHandler(state, &fakePairingClaimer{}, tunnels)

	response := requestJSON(t, handler, http.MethodDelete, "/api/v1/local/pairing", nil)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	config := state.Snapshot()
	if config.ServerURL != initial.ServerURL || config.BridgeID != initial.BridgeID || config.Token != initial.Token {
		t.Fatalf("failed unpair changed config = %+v", config)
	}
	if tunnels.stopped || tunnels.stopCount != 0 {
		t.Fatalf("failed unpair stopped tunnel: %+v", tunnels)
	}
}

func TestLocalStatusUsesLoopbackAddressByDefault(t *testing.T) {
	state := newConfigState(infra.DefaultConfig(), func(*infra.Config) error { return nil })
	handler := newTestLocalHandler(state, &fakePairingClaimer{}, &fakeTunnelController{})

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9202/api/v1/local/status", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var body localStatusResponse
	decodeResponse(t, response, &body)
	if body.Local.Address != "127.0.0.1:9202" {
		t.Fatalf("local address = %q, want 127.0.0.1:9202", body.Local.Address)
	}
	if body.Local.Status != "ok" {
		t.Fatalf("local status = %q, want ok", body.Local.Status)
	}
	if body.Remote.Paired || body.Remote.Connected {
		t.Fatalf("default remote status = %+v, want unpaired", body.Remote)
	}
}

func TestLocalMutationRejectsForeignBrowserOrigin(t *testing.T) {
	state := newConfigState(infra.DefaultConfig(), func(*infra.Config) error { return nil })
	handler := newTestLocalHandler(state, &fakePairingClaimer{}, &fakeTunnelController{})
	body, _ := json.Marshal(map[string]string{"server_url": "https://example.com", "pairing_code": "code"})
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9202/api/v1/local/pairings", bytes.NewReader(body))
	request.Host = "127.0.0.1:9202"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://evil.example")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestLocalMutationRejectsBodyBeyondLimitEvenAfterValidJSON(t *testing.T) {
	state := newConfigState(infra.DefaultConfig(), func(*infra.Config) error { return nil })
	handler := newTestLocalHandler(state, &fakePairingClaimer{}, &fakeTunnelController{})
	body := `{"debug":false}` + strings.Repeat(" ", maxLocalBodySize)
	request := httptest.NewRequest(http.MethodPatch, "http://127.0.0.1:9202/api/v1/local/settings", strings.NewReader(body))
	request.Host = "127.0.0.1:9202"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://127.0.0.1:9202")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	var result apiErrorResponse
	decodeResponse(t, response, &result)
	if result.Error.Code != "INVALID_REQUEST" || !strings.Contains(result.Error.Message, "不能超过") {
		t.Fatalf("error = %+v", result.Error)
	}
}

func TestLocalHostPolicyRejectsDNSRebinding(t *testing.T) {
	tests := []struct {
		name          string
		listenAddress string
		requestHost   string
		want          bool
	}{
		{name: "same loopback", listenAddress: "127.0.0.1:9202", requestHost: "127.0.0.1:9202", want: true},
		{name: "equivalent loopback name", listenAddress: "127.0.0.1:9202", requestHost: "localhost:9202", want: true},
		{name: "IPv6 loopback", listenAddress: "[::1]:9202", requestHost: "[::1]:9202", want: true},
		{name: "different port", listenAddress: "127.0.0.1:9202", requestHost: "localhost:9203", want: false},
		{name: "rebound public hostname", listenAddress: "127.0.0.1:9202", requestHost: "attacker.example:9202", want: false},
		{name: "wildcard accepts concrete IP", listenAddress: "0.0.0.0:9202", requestHost: "192.0.2.10:9202", want: true},
		{name: "wildcard rejects public hostname", listenAddress: "0.0.0.0:9202", requestHost: "console.example:9202", want: false},
		{name: "named listener", listenAddress: "studio.local:9202", requestHost: "studio.local:9202", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isAllowedLocalHost(test.listenAddress, test.requestHost); got != test.want {
				t.Fatalf("isAllowedLocalHost(%q, %q) = %v, want %v", test.listenAddress, test.requestHost, got, test.want)
			}
		})
	}
}

func newTestLocalHandler(state *configState, pairer pairingClaimer, tunnels tunnelController) http.Handler {
	reg := agent.NewAgentRegistry(agent.DefaultAgentRegistryConfig())
	app := &localApplication{
		version:       "0.4.0-test",
		listenAddress: localListenAddress("", 9202),
		registry:      reg,
		sessions:      service.NewSessionManager(reg),
		config:        state,
		pairer:        pairer,
		tunnels:       tunnels,
		hostname:      func() (string, error) { return "test-host", nil },
	}
	return newLocalHandler(app, http.NotFoundHandler())
}

func requestJSON(t *testing.T, handler http.Handler, method, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if value != nil {
		if err := json.NewEncoder(&body).Encode(value); err != nil {
			t.Fatalf("encode request: %v", err)
		}
	}
	request := httptest.NewRequest(method, "http://127.0.0.1:9202"+path, &body)
	request.Host = "127.0.0.1:9202"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://127.0.0.1:9202")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, value any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(value); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
}
