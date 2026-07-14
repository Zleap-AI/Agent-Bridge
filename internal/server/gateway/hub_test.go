package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/apierror"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/caller"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/gateway"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/model"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/secret"
	"github.com/gorilla/websocket"
)

type repository struct {
	mu                  sync.Mutex
	deviceID            string
	tokenHash           string
	agents              []model.Agent
	authenticateDevice  func(string, string) (bool, error)
	replaceDeviceAgents func([]model.Agent) error
}

func TestSessionANPErrorMapsToPublicNotFound(t *testing.T) {
	err := gateway.ResultError(gateway.Result{Error: &protocol.ANPError{Code: -31005, Message: "missing"}})
	apiErr := apierror.As(err)
	if apiErr.Code != apierror.CodeSessionNotFound || apiErr.Status != http.StatusNotFound {
		t.Fatalf("mapped error = %#v", apiErr)
	}
}

func TestOversizedDeviceResponseMapsToDiagnosticPublicError(t *testing.T) {
	err := gateway.ResultError(gateway.Result{Error: &protocol.ANPError{
		Code: protocol.ANPErrorResponseTooLarge, Message: "Message at cursor 4 exceeds the Device response limit",
	}})
	apiErr := apierror.As(err)
	if apiErr.Code != apierror.CodePayloadTooLarge || apiErr.Status != http.StatusBadGateway {
		t.Fatalf("mapped error = %#v", apiErr)
	}
	if apiErr.Message != "Message at cursor 4 exceeds the Device response limit" {
		t.Fatalf("mapped error message = %q", apiErr.Message)
	}
}

func (r *repository) AuthenticateDevice(_ context.Context, id, hash string) (bool, error) {
	if r.authenticateDevice != nil {
		return r.authenticateDevice(id, hash)
	}
	return id == r.deviceID && hash == r.tokenHash, nil
}
func (r *repository) TouchDevice(context.Context, string, time.Time) error { return nil }
func (r *repository) ReplaceDeviceAgents(_ context.Context, _ string, agents []model.Agent, _ time.Time) error {
	if r.replaceDeviceAgents != nil {
		return r.replaceDeviceAgents(agents)
	}
	r.storeAgents(agents)
	return nil
}

func (r *repository) storeAgents(agents []model.Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents = append([]model.Agent(nil), agents...)
}

func TestLatestConnectionWinsAndUnconsumedStreamDoesNotBlock(t *testing.T) {
	repo := &repository{deviceID: "dev_test", tokenHash: secret.Digest("bridge-token")}
	hub := gateway.New(repo)
	server := httptest.NewServer(hub)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	header := http.Header{"X-Bridge-Id": []string{"dev_test"}, "Authorization": []string{"Bearer bridge-token"}}

	old, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	registerDevice(t, old, "dev_test")
	waitOnline(t, hub, "dev_test")
	newConn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatal(err)
	}
	defer newConn.Close()
	registerDevice(t, newConn, "dev_test")

	_ = old.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = old.ReadMessage()
	closeError, ok := err.(*websocket.CloseError)
	if !ok || closeError.Code != 4001 || closeError.Text != "connection_replaced" {
		t.Fatalf("old connection close = %#v", err)
	}
	if !hub.IsOnline("dev_test") {
		t.Fatal("replacement connection is not online")
	}

	operation, err := hub.Start("dev_test", protocol.ANPMessage{Method: "invoke", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var request protocol.ANPMessage
	if err := newConn.ReadJSON(&request); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if err := newConn.WriteJSON(protocol.NewStreamUpdate(request.ID, "response", "x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := newConn.WriteJSON(protocol.NewResultResponse(request.ID, json.RawMessage(`{"ok":true}`))); err != nil {
		t.Fatal(err)
	}
	select {
	case <-operation.Done():
		if err := gateway.ResultError(operation.Result()); err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("operation blocked because stream events were not consumed")
	}
	eventCount := 0
	for range operation.Events() {
		eventCount++
	}
	if eventCount != 1000 {
		t.Fatalf("stream event count = %d, want 1000", eventCount)
	}

	requestResult := make(chan error, 1)
	go func() {
		_, err := hub.Request(context.Background(), "dev_test", protocol.ANPMessage{Method: "sessions/list", Params: json.RawMessage(`{}`)})
		requestResult <- err
	}()
	if err := newConn.ReadJSON(&request); err != nil {
		t.Fatal(err)
	}
	if err := newConn.WriteJSON(protocol.NewStreamUpdate(request.ID, "response", "unexpected update")); err != nil {
		t.Fatal(err)
	}
	if err := newConn.WriteJSON(protocol.NewResultResponse(request.ID, json.RawMessage(`[]`))); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-requestResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("non-stream request with an update did not complete")
	}

	detached, err := hub.Start("dev_test", protocol.ANPMessage{Method: "invoke", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	detached.Detach()
	if err := newConn.ReadJSON(&request); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if err := newConn.WriteJSON(protocol.NewStreamUpdate(request.ID, "response", "x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := newConn.WriteJSON(protocol.NewResultResponse(request.ID, json.RawMessage(`{"ok":true}`))); err != nil {
		t.Fatal(err)
	}
	select {
	case <-detached.Done():
		if err := gateway.ResultError(detached.Result()); err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("detached stream stopped the Device operation")
	}

	hub.Disconnect("dev_test", "device_deleted")
	if hub.IsOnline("dev_test") {
		t.Fatal("disconnected Device remains online")
	}
}

func TestLatestConnectionCatalogWinsInFlightOldRefresh(t *testing.T) {
	const (
		oldInitialAgent = "old-initial"
		oldStaleAgent   = "old-stale"
		newCurrentAgent = "new-current"
	)
	staleStarted := make(chan struct{})
	releaseStale := make(chan struct{})
	staleStored := make(chan struct{})
	newStored := make(chan struct{})
	newReauthenticated := make(chan struct{})
	var releaseStaleOnce sync.Once
	releaseStaleCatalog := func() { releaseStaleOnce.Do(func() { close(releaseStale) }) }

	repo := &repository{deviceID: "dev_test", tokenHash: secret.Digest("bridge-token")}
	authCalls := 0
	repo.authenticateDevice = func(id, hash string) (bool, error) {
		repo.mu.Lock()
		authCalls++
		call := authCalls
		repo.mu.Unlock()
		if call == 4 {
			close(newReauthenticated)
		}
		return id == repo.deviceID && hash == repo.tokenHash, nil
	}
	repo.replaceDeviceAgents = func(agents []model.Agent) error {
		agentID := ""
		if len(agents) != 0 {
			agentID = agents[0].AgentID
		}
		if agentID == oldStaleAgent {
			close(staleStarted)
			<-releaseStale
		}
		repo.storeAgents(agents)
		switch agentID {
		case oldStaleAgent:
			close(staleStored)
		case newCurrentAgent:
			close(newStored)
		}
		return nil
	}

	hub := gateway.New(repo)
	server := httptest.NewServer(hub)
	defer server.Close()
	defer releaseStaleCatalog()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	header := http.Header{"X-Bridge-Id": []string{"dev_test"}, "Authorization": []string{"Bearer bridge-token"}}

	old, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	registerAgents(t, old, "dev_test", []protocol.ANPAgent{{AgentID: oldInitialAgent}})
	waitOnline(t, hub, "dev_test")
	waitForAgentCatalog(t, repo, oldInitialAgent)

	registerAgents(t, old, "dev_test", []protocol.ANPAgent{{AgentID: oldStaleAgent}})
	waitSignal(t, staleStarted, "old catalog refresh did not reach persistence")

	newConn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatal(err)
	}
	defer newConn.Close()
	registerAgents(t, newConn, "dev_test", []protocol.ANPAgent{{AgentID: newCurrentAgent}})
	// Before the fix, the new handshake persisted here before attach while the
	// old refresh remained in flight. After the fix, it can only persist while
	// becoming the Hub's current connection.
	waitSignal(t, newReauthenticated, "replacement connection was not reauthenticated")
	releaseStaleCatalog()
	waitSignal(t, staleStored, "old catalog refresh did not finish")
	waitSignal(t, newStored, "replacement catalog was not stored")

	_ = old.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = old.ReadMessage()
	closeError, ok := err.(*websocket.CloseError)
	if !ok || closeError.Code != 4001 || closeError.Text != "connection_replaced" {
		t.Fatalf("old connection close = %#v", err)
	}
	waitForAgentCatalog(t, repo, newCurrentAgent)
}

func TestConnectionIsPublishedOnlyAfterRegistrationAndRevocationWinsHandshakeRace(t *testing.T) {
	repo := &repository{deviceID: "dev_test", tokenHash: secret.Digest("bridge-token")}
	hub := gateway.New(repo)
	server := httptest.NewServer(hub)
	defer server.Close()
	header := http.Header{"X-Bridge-Id": []string{"dev_test"}, "Authorization": []string{"Bearer bridge-token"}}

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if hub.IsOnline("dev_test") {
		t.Fatal("device became online before bridge/register")
	}

	// This models deletion after the initial credential check but before the
	// handshake publishes the connection.
	hub.Disconnect("dev_test", "device_deleted")
	registerDevice(t, conn, "dev_test")
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	closeError, ok := err.(*websocket.CloseError)
	if !ok || closeError.Text != "device_revoked" {
		t.Fatalf("revoked handshake close = %#v", err)
	}
	if hub.IsOnline("dev_test") {
		t.Fatal("revoked device became online")
	}
	repo.mu.Lock()
	storedAgents := len(repo.agents)
	repo.mu.Unlock()
	if storedAgents != 0 {
		t.Fatalf("revoked handshake persisted %d Agent records", storedAgents)
	}
}

func TestFailedRegistrationReauthenticationDoesNotPublishOrPersist(t *testing.T) {
	repo := &repository{deviceID: "dev_test", tokenHash: secret.Digest("bridge-token")}
	authCalls := 0
	repo.authenticateDevice = func(id, hash string) (bool, error) {
		repo.mu.Lock()
		authCalls++
		call := authCalls
		repo.mu.Unlock()
		return call == 1 && id == repo.deviceID && hash == repo.tokenHash, nil
	}
	hub := gateway.New(repo)
	server := httptest.NewServer(hub)
	defer server.Close()
	header := http.Header{"X-Bridge-Id": []string{"dev_test"}, "Authorization": []string{"Bearer bridge-token"}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	registerDevice(t, conn, "dev_test")

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	closeError, ok := err.(*websocket.CloseError)
	if !ok || closeError.Code != 4001 || closeError.Text != "device_revoked" {
		t.Fatalf("failed reauthentication close = %#v", err)
	}
	if hub.IsOnline("dev_test") {
		t.Fatal("failed reauthentication published the Device")
	}
	repo.mu.Lock()
	storedAgents := len(repo.agents)
	repo.mu.Unlock()
	if storedAgents != 0 {
		t.Fatalf("failed reauthentication persisted %d Agent records", storedAgents)
	}
}

func TestRegistrationAcceptsNormalAgentCatalog(t *testing.T) {
	repo := &repository{deviceID: "dev_test", tokenHash: secret.Digest("bridge-token")}
	hub := gateway.New(repo)
	server := httptest.NewServer(hub)
	defer server.Close()
	header := http.Header{"X-Bridge-Id": []string{"dev_test"}, "Authorization": []string{"Bearer bridge-token"}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	agents := make([]protocol.ANPAgent, 11)
	for index := range agents {
		agents[index] = protocol.ANPAgent{
			AgentID: fmt.Sprintf("agent-%d", index), DisplayName: fmt.Sprintf("Agent %d", index), Status: "ready",
		}
	}
	registerAgents(t, conn, "dev_test", agents)
	waitOnline(t, hub, "dev_test")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		repo.mu.Lock()
		count := len(repo.agents)
		repo.mu.Unlock()
		if count == len(agents) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("normal 11-Agent registration was not stored")
}

func TestInvalidRegistrationRefreshClosesConnection(t *testing.T) {
	tests := []struct {
		name   string
		agents func() []protocol.ANPAgent
	}{
		{name: "too many agents", agents: func() []protocol.ANPAgent {
			agents := make([]protocol.ANPAgent, gateway.MaxRegisteredAgents+1)
			for index := range agents {
				agents[index].AgentID = fmt.Sprintf("agent-%d", index)
			}
			return agents
		}},
		{name: "long id", agents: func() []protocol.ANPAgent {
			return []protocol.ANPAgent{{AgentID: strings.Repeat("a", gateway.MaxAgentIDRunes+1)}}
		}},
		{name: "long display name", agents: func() []protocol.ANPAgent {
			return []protocol.ANPAgent{{AgentID: "agent", DisplayName: strings.Repeat("名", gateway.MaxAgentDisplayNameRunes+1)}}
		}},
		{name: "long status", agents: func() []protocol.ANPAgent {
			return []protocol.ANPAgent{{AgentID: "agent", Status: strings.Repeat("s", gateway.MaxAgentStatusRunes+1)}}
		}},
		{name: "duplicate id", agents: func() []protocol.ANPAgent {
			return []protocol.ANPAgent{{AgentID: "agent"}, {AgentID: "agent"}}
		}},
		{name: "whitespace id", agents: func() []protocol.ANPAgent {
			return []protocol.ANPAgent{{AgentID: " agent "}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hub, conn := connectedDevice(t)
			registerAgents(t, conn, "dev_test", test.agents())
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _, err := conn.ReadMessage()
			closeError, ok := err.(*websocket.CloseError)
			if !ok || closeError.Code != 4001 || closeError.Text != "invalid_registration" {
				t.Fatalf("invalid registration close = %#v", err)
			}
			deadline := time.Now().Add(time.Second)
			for hub.IsOnline("dev_test") && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if hub.IsOnline("dev_test") {
				t.Fatal("invalid registration left Device online")
			}
		})
	}
}

func TestSlowSubscriberIsBoundedWithoutStoppingDeviceOperation(t *testing.T) {
	repo := &repository{deviceID: "dev_test", tokenHash: secret.Digest("bridge-token")}
	hub := gateway.New(repo)
	server := httptest.NewServer(hub)
	defer server.Close()
	header := http.Header{"X-Bridge-Id": []string{"dev_test"}, "Authorization": []string{"Bearer bridge-token"}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	registerDevice(t, conn, "dev_test")
	waitOnline(t, hub, "dev_test")

	operation, err := hub.Start("dev_test", protocol.ANPMessage{Method: "invoke", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var request protocol.ANPMessage
	if err := conn.ReadJSON(&request); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5000; i++ {
		if err := conn.WriteJSON(protocol.NewStreamUpdate(request.ID, "response", strings.Repeat("x", 512))); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.WriteJSON(protocol.NewResultResponse(request.ID, json.RawMessage(`{"ok":true}`))); err != nil {
		t.Fatal(err)
	}
	select {
	case <-operation.Done():
		if err := gateway.ResultError(operation.Result()); err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow subscriber blocked the Device read loop")
	}
	for range operation.Events() {
	}
	if operation.SubscriptionError() == nil {
		t.Fatal("slow subscriber was not detached at the bounded queue limit")
	}
}

func TestPerDevicePendingLimitRejectsExcessWithoutDroppingConnection(t *testing.T) {
	repo := &repository{deviceID: "dev_test", tokenHash: secret.Digest("bridge-token")}
	hub := gateway.New(repo)
	server := httptest.NewServer(hub)
	defer server.Close()
	header := http.Header{"X-Bridge-Id": []string{"dev_test"}, "Authorization": []string{"Bearer bridge-token"}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	registerDevice(t, conn, "dev_test")
	waitOnline(t, hub, "dev_test")

	for i := 0; i < 64; i++ {
		if _, err := hub.Start("dev_test", protocol.ANPMessage{Method: "invoke", Params: json.RawMessage(`{}`)}); err != nil {
			t.Fatalf("request %d unexpectedly rejected: %v", i+1, err)
		}
	}
	if _, err := hub.Start("dev_test", protocol.ANPMessage{Method: "invoke", Params: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("65th active request was not rejected")
	} else if got := apierror.As(err); got.Code != apierror.CodeAgentUnavailable || got.Status != http.StatusTooManyRequests {
		t.Fatalf("limit error = %#v", got)
	}
	if !hub.IsOnline("dev_test") {
		t.Fatal("pending limit disconnected the healthy Device")
	}
	hub.Disconnect("dev_test", "device_deleted")
}

func TestCanceledRequestsReleasePendingCapacity(t *testing.T) {
	repo := &repository{deviceID: "dev_test", tokenHash: secret.Digest("bridge-token")}
	hub := gateway.New(repo)
	server := httptest.NewServer(hub)
	defer server.Close()
	header := http.Header{"X-Bridge-Id": []string{"dev_test"}, "Authorization": []string{"Bearer bridge-token"}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	registerDevice(t, conn, "dev_test")
	waitOnline(t, hub, "dev_test")

	var request protocol.ANPMessage
	for i := 0; i < 1024; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := hub.Request(ctx, "dev_test", protocol.ANPMessage{Method: "sessions/list", Params: json.RawMessage(`{}`)})
			result <- err
		}()
		if err := conn.ReadJSON(&request); err != nil {
			t.Fatalf("read canceled request %d: %v", i+1, err)
		}
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled request %d error = %v, want context.Canceled", i+1, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("canceled request %d did not return", i+1)
		}
	}

	operation, err := hub.Start("dev_test", protocol.ANPMessage{Method: "sessions/list", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("request after canceled requests was rejected: %v", err)
	}
	if err := conn.ReadJSON(&request); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(protocol.NewResultResponse(request.ID, json.RawMessage(`[]`))); err != nil {
		t.Fatal(err)
	}
	select {
	case <-operation.Done():
		if err := gateway.ResultError(operation.Result()); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request after canceled requests did not complete")
	}
}

func TestOversizedOutgoingRequestIsRejectedBeforeWebSocketWrite(t *testing.T) {
	repo := &repository{deviceID: "dev_test", tokenHash: secret.Digest("bridge-token")}
	hub := gateway.New(repo)
	server := httptest.NewServer(hub)
	defer server.Close()
	header := http.Header{"X-Bridge-Id": []string{"dev_test"}, "Authorization": []string{"Bearer bridge-token"}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	registerDevice(t, conn, "dev_test")
	waitOnline(t, hub, "dev_test")

	params := json.RawMessage(`{"text":"` + strings.Repeat("x", gateway.MaxRequestMessageSize) + `"}`)
	if _, err := hub.Start("dev_test", protocol.ANPMessage{Method: "invoke", Params: params}); err == nil {
		t.Fatal("oversized Device request was accepted")
	} else if got := apierror.As(err); got.Code != apierror.CodePayloadTooLarge || got.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request error = %#v", got)
	}

	operation, err := hub.Start("dev_test", protocol.ANPMessage{Method: "invoke", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("healthy request after rejection: %v", err)
	}
	var request protocol.ANPMessage
	if err := conn.ReadJSON(&request); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(protocol.NewResultResponse(request.ID, json.RawMessage(`{"ok":true}`))); err != nil {
		t.Fatal(err)
	}
	select {
	case <-operation.Done():
		if err := gateway.ResultError(operation.Result()); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request after oversized rejection did not complete")
	}
}

func TestDeviceWebSocketAcceptsFullLargeHistoryPage(t *testing.T) {
	hub, conn := connectedDevice(t)

	operation, err := hub.Start("dev_test", protocol.ANPMessage{Method: "sessions/messages", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var request protocol.ANPMessage
	if err := conn.ReadJSON(&request); err != nil {
		t.Fatal(err)
	}

	largeText := strings.Repeat("x", caller.MaxMessageTextBytes)
	messages := make([]map[string]string, caller.MaxMessagesPageSize)
	for index := range messages {
		messages[index] = map[string]string{"role": "user", "text": largeText}
	}
	result, err := json.Marshal(map[string]any{
		"messages": messages,
		"total":    len(messages),
		"cursor":   len(messages),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) <= gateway.MaxRequestMessageSize || len(result) >= gateway.MaxDeviceMessageSize {
		t.Fatalf("history payload size = %d, want between request and Device limits", len(result))
	}
	if err := conn.WriteJSON(protocol.NewResultResponse(request.ID, result)); err != nil {
		t.Fatal(err)
	}

	waitOperation(t, operation)
	var page struct {
		Messages []map[string]string `json:"messages"`
		Total    int                 `json:"total"`
		Cursor   int                 `json:"cursor"`
	}
	if err := json.Unmarshal(operation.Result().Value, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != caller.MaxMessagesPageSize || page.Total != caller.MaxMessagesPageSize || page.Cursor != caller.MaxMessagesPageSize {
		t.Fatalf("large history page = messages:%d total:%d cursor:%d", len(page.Messages), page.Total, page.Cursor)
	}
	if !hub.IsOnline("dev_test") {
		t.Fatal("large but valid history response disconnected Device")
	}
}

func TestDeviceWebSocketAcceptsLargeStreamingFinalResult(t *testing.T) {
	hub, conn := connectedDevice(t)

	operation, err := hub.Start("dev_test", protocol.ANPMessage{Method: "invoke", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var request protocol.ANPMessage
	if err := conn.ReadJSON(&request); err != nil {
		t.Fatal(err)
	}

	const chunkCount = 16
	chunkText := strings.Repeat("x", 128*1024)
	drainDone := make(chan struct{})
	go func() {
		for range operation.Events() {
		}
		close(drainDone)
	}()
	for range chunkCount {
		if err := conn.WriteJSON(protocol.NewStreamUpdate(request.ID, "response", chunkText)); err != nil {
			t.Fatal(err)
		}
	}
	fullText := strings.Repeat(chunkText, chunkCount)
	result, err := json.Marshal(map[string]string{"text": fullText})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(protocol.NewResultResponse(request.ID, result)); err != nil {
		t.Fatal(err)
	}

	waitOperation(t, operation)
	select {
	case <-drainDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stream event drain did not finish")
	}
	var final struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(operation.Result().Value, &final); err != nil || final.Text != fullText {
		t.Fatalf("stream final text bytes = %d, want %d", len(final.Text), len(fullText))
	}
	if !hub.IsOnline("dev_test") {
		t.Fatal("large valid stream disconnected Device")
	}
}

func TestDeviceWebSocketRejectsFrameBeyondDeviceLimit(t *testing.T) {
	hub, conn := connectedDevice(t)

	payload := make([]byte, gateway.MaxDeviceMessageSize+1)
	for index := range payload {
		payload[index] = 'x'
	}
	// Depending on socket buffering the write may either complete before the
	// Server closes the connection or observe that close directly.
	_ = conn.WriteMessage(websocket.TextMessage, payload)

	deadline := time.Now().Add(3 * time.Second)
	for hub.IsOnline("dev_test") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if hub.IsOnline("dev_test") {
		t.Fatalf("Device remained online after %d-byte frame exceeded %d-byte limit", len(payload), gateway.MaxDeviceMessageSize)
	}
	if _, err := hub.Start("dev_test", protocol.ANPMessage{Method: "ping"}); err == nil {
		t.Fatal("request unexpectedly started after oversized Device frame")
	} else if got := apierror.As(err); got.Code != apierror.CodeDeviceOffline {
		t.Fatalf("request after oversized frame error = %#v", got)
	}
}

func connectedDevice(t *testing.T) (*gateway.Hub, *websocket.Conn) {
	t.Helper()
	repo := &repository{deviceID: "dev_test", tokenHash: secret.Digest("bridge-token")}
	hub := gateway.New(repo)
	server := httptest.NewServer(hub)
	t.Cleanup(server.Close)
	header := http.Header{"X-Bridge-Id": []string{"dev_test"}, "Authorization": []string{"Bearer bridge-token"}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	registerDevice(t, conn, "dev_test")
	waitOnline(t, hub, "dev_test")
	return hub, conn
}

func waitOperation(t *testing.T, operation *gateway.Operation) {
	t.Helper()
	select {
	case <-operation.Done():
		if err := gateway.ResultError(operation.Result()); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Device operation did not complete")
	}
}

func registerDevice(t *testing.T, conn *websocket.Conn, deviceID string) {
	t.Helper()
	registerAgents(t, conn, deviceID, []protocol.ANPAgent{{AgentID: "codex", DisplayName: "Codex", Status: "ready"}})
}

func registerAgents(t *testing.T, conn *websocket.Conn, deviceID string, agents []protocol.ANPAgent) {
	t.Helper()
	params, err := json.Marshal(protocol.ANPBridgeRegister{BridgeID: deviceID, Agents: agents})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(protocol.ANPMessage{JSONRPC: "2.0", Method: "bridge/register", Params: params}); err != nil {
		t.Fatal(err)
	}
}

func waitOnline(t *testing.T, hub *gateway.Hub, deviceID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.IsOnline(deviceID) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("device did not become online after registration")
}

func waitForAgentCatalog(t *testing.T, repo *repository, agentID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		repo.mu.Lock()
		matches := len(repo.agents) == 1 && repo.agents[0].AgentID == agentID
		repo.mu.Unlock()
		if matches {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Agent catalog did not settle on %q", agentID)
}

func waitSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func TestDeviceWebSocketRejectsInvalidCredentials(t *testing.T) {
	repo := &repository{deviceID: "dev_test", tokenHash: secret.Digest("correct")}
	server := httptest.NewServer(gateway.New(repo))
	defer server.Close()
	header := http.Header{"X-Bridge-Id": []string{"dev_test"}, "Authorization": []string{"Bearer wrong"}}
	_, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), header)
	if err == nil {
		t.Fatal("invalid credentials connected")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %#v, err = %v", response, err)
	}
	response.Body.Close()
}
