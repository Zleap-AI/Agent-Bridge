package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bridgeinternal "github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
	"github.com/gorilla/websocket"
)

type receivedRegistration struct {
	BridgeID      string
	AgentIDs      string
	Authorization string
	Message       protocol.ANPMessage
}

type mutableStatusAgent struct {
	*sessionTestAgent
	status atomic.Int32
}

func newMutableStatusAgent(status agent.AgentStatus) *mutableStatusAgent {
	result := &mutableStatusAgent{sessionTestAgent: &sessionTestAgent{}}
	result.status.Store(int32(status))
	return result
}

func (a *mutableStatusAgent) Status() agent.AgentStatus {
	return agent.AgentStatus(a.status.Load())
}

func TestTunnelPreservesHeadersAndBridgeRegistrationContract(t *testing.T) {
	received := make(chan receivedRegistration, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		var message protocol.ANPMessage
		if err := conn.ReadJSON(&message); err != nil {
			t.Errorf("read registration: %v", err)
			return
		}
		received <- receivedRegistration{
			BridgeID: r.Header.Get("X-Bridge-Id"), AgentIDs: r.Header.Get("X-Agent-Ids"),
			Authorization: r.Header.Get("Authorization"), Message: message,
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	localAgent := &sessionTestAgent{}
	registry := newSessionTestRegistry(localAgent)
	config := DefaultTunnelConfig()
	config.ServerURL = strings.Replace(server.URL, "http://", "ws://", 1)
	config.BridgeID = "bridge-1"
	config.Token = "token-1"
	config.ReconnectInterval = time.Hour
	tunnel := NewTunnelService(registry, config)
	if err := tunnel.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tunnel.Stop()

	select {
	case registration := <-received:
		if registration.BridgeID != "bridge-1" || registration.AgentIDs != "test-agent" {
			t.Fatalf("connection headers = %+v", registration)
		}
		if registration.Authorization != "Bearer token-1" {
			t.Fatalf("Authorization = %q", registration.Authorization)
		}
		if registration.Message.Method != "bridge/register" || registration.Message.JSONRPC != "2.0" {
			t.Fatalf("registration message = %+v", registration.Message)
		}
		var params protocol.ANPBridgeRegister
		if err := json.Unmarshal(registration.Message.Params, &params); err != nil {
			t.Fatalf("unmarshal registration: %v", err)
		}
		if params.BridgeID != "bridge-1" || len(params.Agents) != 1 || params.Agents[0].AgentID != "test-agent" {
			t.Fatalf("registration params = %+v", params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bridge/register")
	}
}

func TestTunnelRejectsOversizedDeviceMessageBeforeWebSocketWrite(t *testing.T) {
	received := make(chan protocol.ANPMessage, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var registration protocol.ANPMessage
		if err := conn.ReadJSON(&registration); err != nil {
			return
		}
		var message protocol.ANPMessage
		if err := conn.ReadJSON(&message); err == nil {
			received <- message
		}
	}))
	defer server.Close()

	config := DefaultTunnelConfig()
	config.ServerURL = strings.Replace(server.URL, "http://", "ws://", 1)
	config.BridgeID = "bridge-bounded-write"
	config.ReconnectInterval = time.Hour
	tunnel := NewTunnelService(newSessionTestRegistry(&sessionTestAgent{}), config)
	if err := tunnel.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tunnel.Stop()

	hugeResult := json.RawMessage(`{"text":"` + strings.Repeat("x", protocol.MaxANPDeviceMessageBytes) + `"}`)
	err := tunnel.sendJSON(protocol.NewResultResponse("oversized", hugeResult))
	if !errors.Is(err, errANPDeviceMessageTooLarge) {
		t.Fatalf("oversized send error = %v, want transport limit", err)
	}
	if err := tunnel.sendJSON(protocol.NewResultResponse("small", json.RawMessage(`{}`))); err != nil {
		t.Fatalf("small send after rejection: %v", err)
	}
	select {
	case message := <-received:
		if message.ID != "small" {
			t.Fatalf("WebSocket received rejected Message %q", message.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("small Message was not delivered after local size rejection")
	}
}

func TestTunnelReconnectsOnceAfterOrdinaryConnectionLoss(t *testing.T) {
	var connections atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connectionNumber := connections.Add(1)
		var message protocol.ANPMessage
		if err := conn.ReadJSON(&message); err != nil {
			return
		}
		if connectionNumber == 1 {
			return
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	registry := newSessionTestRegistry(&sessionTestAgent{})
	config := DefaultTunnelConfig()
	config.ServerURL = strings.Replace(server.URL, "http://", "ws://", 1)
	config.BridgeID = "bridge-1"
	config.ReconnectInterval = 10 * time.Millisecond
	tunnel := NewTunnelService(registry, config)
	if err := tunnel.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tunnel.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for connections.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := connections.Load(); got < 2 {
		t.Fatalf("connections = %d, want reconnect", got)
	}
	time.Sleep(100 * time.Millisecond)
	if got := connections.Load(); got != 2 {
		t.Fatalf("connections = %d, want exactly one replacement connection", got)
	}
}

func TestTunnelDoesNotReconnectAfterConnectionIsReplaced(t *testing.T) {
	var connections atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connections.Add(1)
		var message protocol.ANPMessage
		if err := conn.ReadJSON(&message); err != nil {
			return
		}
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(connectionReplacedCode, connectionReplacedReason),
			time.Now().Add(time.Second),
		)
	}))
	defer server.Close()

	disconnected := make(chan error, 1)
	registry := newSessionTestRegistry(&sessionTestAgent{})
	config := DefaultTunnelConfig()
	config.ServerURL = strings.Replace(server.URL, "http://", "ws://", 1)
	config.BridgeID = "bridge-1"
	config.ReconnectInterval = 10 * time.Millisecond
	config.OnConnectionChange = func(connected bool, err error) {
		if !connected {
			select {
			case disconnected <- err:
			default:
			}
		}
	}
	tunnel := NewTunnelService(registry, config)
	if err := tunnel.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tunnel.Stop()

	select {
	case err := <-disconnected:
		if !errors.Is(err, ErrConnectionReplaced) {
			t.Fatalf("disconnect error = %v, want ErrConnectionReplaced", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for replacement close")
	}

	time.Sleep(100 * time.Millisecond)
	if got := connections.Load(); got != 1 {
		t.Fatalf("connections = %d, want no reconnect after replacement", got)
	}
}

func TestTunnelDoesNotReconnectAfterDeviceCredentialsAreRevoked(t *testing.T) {
	var connections atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connections.Add(1)
		var message protocol.ANPMessage
		if err := conn.ReadJSON(&message); err != nil {
			return
		}
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(connectionReplacedCode, deviceDeletedReason),
			time.Now().Add(time.Second),
		)
	}))
	defer server.Close()

	disconnected := make(chan error, 1)
	config := DefaultTunnelConfig()
	config.ServerURL = strings.Replace(server.URL, "http://", "ws://", 1)
	config.BridgeID = "bridge-deleted"
	config.ReconnectInterval = 10 * time.Millisecond
	config.OnConnectionChange = func(connected bool, err error) {
		if !connected {
			select {
			case disconnected <- err:
			default:
			}
		}
	}
	tunnel := NewTunnelService(newSessionTestRegistry(&sessionTestAgent{}), config)
	if err := tunnel.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tunnel.Stop()

	select {
	case err := <-disconnected:
		if !errors.Is(err, ErrCredentialsRevoked) {
			t.Fatalf("disconnect error = %v, want ErrCredentialsRevoked", err)
		}
		if !strings.Contains(err.Error(), "pair this Device again") {
			t.Fatalf("disconnect error is not actionable: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for revoked credentials close")
	}

	time.Sleep(100 * time.Millisecond)
	if got := connections.Load(); got != 1 {
		t.Fatalf("connections = %d, want no reconnect after Device deletion", got)
	}
}

func TestTunnelStopsReconnectLoopWhenHandshakeRejectsCredentials(t *testing.T) {
	var connections atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectionNumber := connections.Add(1)
		if connectionNumber > 1 {
			http.Error(w, "invalid device credentials", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var message protocol.ANPMessage
		if err := conn.ReadJSON(&message); err != nil {
			return
		}
	}))
	defer server.Close()

	revoked := make(chan error, 1)
	config := DefaultTunnelConfig()
	config.ServerURL = strings.Replace(server.URL, "http://", "ws://", 1)
	config.BridgeID = "bridge-revoked"
	config.ReconnectInterval = 10 * time.Millisecond
	config.OnConnectionChange = func(connected bool, err error) {
		if !connected && errors.Is(err, ErrCredentialsRevoked) {
			select {
			case revoked <- err:
			default:
			}
		}
	}
	tunnel := NewTunnelService(newSessionTestRegistry(&sessionTestAgent{}), config)
	if err := tunnel.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tunnel.Stop()

	select {
	case err := <-revoked:
		if !strings.Contains(err.Error(), "HTTP 401") {
			t.Fatalf("revocation error lost handshake status: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for rejected reconnect handshake")
	}

	time.Sleep(100 * time.Millisecond)
	if got := connections.Load(); got != 2 {
		t.Fatalf("connections = %d, want one initial connection and one rejected reconnect", got)
	}
}

func TestTunnelStartReportsRejectedCredentialsAsTerminal(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "device access forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	disconnected := make(chan error, 1)
	config := DefaultTunnelConfig()
	config.ServerURL = strings.Replace(server.URL, "http://", "ws://", 1)
	config.BridgeID = "bridge-forbidden"
	config.ReconnectInterval = 10 * time.Millisecond
	config.OnConnectionChange = func(connected bool, err error) {
		if !connected {
			disconnected <- err
		}
	}
	tunnel := NewTunnelService(newSessionTestRegistry(&sessionTestAgent{}), config)
	defer tunnel.Stop()

	err := tunnel.Start()
	if !errors.Is(err, ErrCredentialsRevoked) {
		t.Fatalf("Start error = %v, want ErrCredentialsRevoked", err)
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("Start error lost handshake status: %v", err)
	}
	select {
	case callbackError := <-disconnected:
		if !errors.Is(callbackError, ErrCredentialsRevoked) {
			t.Fatalf("connection callback error = %v, want ErrCredentialsRevoked", callbackError)
		}
	default:
		t.Fatal("rejected credentials were not reported to the connection callback")
	}

	time.Sleep(50 * time.Millisecond)
	if got := requests.Load(); got != 1 {
		t.Fatalf("handshake requests = %d, want no reconnect after HTTP 403", got)
	}
}

func TestTunnelCanShareLocalSessionManager(t *testing.T) {
	registry := newSessionTestRegistry(&sessionTestAgent{})
	sessions := newSessionManagerWithStoreDir(registry, t.TempDir())
	tunnel := NewTunnelServiceWithSessionManager(registry, DefaultTunnelConfig(), sessions)
	if tunnel.sessions != sessions {
		t.Fatal("TunnelService did not retain the shared SessionManager")
	}
}

func TestTunnelRefreshesAgentStatusWithoutReconnect(t *testing.T) {
	statuses := make(chan string, 16)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var message protocol.ANPMessage
			if err := conn.ReadJSON(&message); err != nil {
				return
			}
			if message.Method != "bridge/register" {
				continue
			}
			var registration protocol.ANPBridgeRegister
			if err := json.Unmarshal(message.Params, &registration); err != nil || len(registration.Agents) != 1 {
				continue
			}
			statuses <- registration.Agents[0].Status
		}
	}))
	defer server.Close()

	localAgent := newMutableStatusAgent(agent.AgentIdle)
	registry := newSessionTestRegistry(localAgent)
	config := DefaultTunnelConfig()
	config.ServerURL = strings.Replace(server.URL, "http://", "ws://", 1)
	config.BridgeID = "bridge-status"
	config.StatusRefreshInterval = 20 * time.Millisecond
	config.ReconnectInterval = time.Hour
	tunnel := NewTunnelService(registry, config)
	if err := tunnel.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tunnel.Stop()

	select {
	case status := <-statuses:
		if status != agent.AgentIdle.String() {
			t.Fatalf("initial status = %q", status)
		}
	case <-time.After(time.Second):
		t.Fatal("initial registration not received")
	}
	localAgent.status.Store(int32(agent.AgentDisconnected))

	deadline := time.After(time.Second)
	for {
		select {
		case status := <-statuses:
			if status == agent.AgentDisconnected.String() {
				return
			}
		case <-deadline:
			t.Fatal("refreshed disconnected status not received")
		}
	}
}

func TestTunnelReadLoopContinuesWhileAgentCallBlocks(t *testing.T) {
	streamStarted := make(chan struct{}, 1)
	streamRelease := make(chan struct{})
	stream := make(chan bridgeinternal.StreamChunk)
	close(stream)
	localAgent := &sessionTestAgent{
		stream: stream, streamStarted: streamStarted, streamRelease: streamRelease,
	}
	result := make(chan error, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		var registration protocol.ANPMessage
		if err := conn.ReadJSON(&registration); err != nil {
			result <- err
			return
		}
		invokeParams, _ := json.Marshal(protocol.ANPInvokeParams{
			AgentID: localAgent.ID(), Method: "session/prompt", Stream: true,
			Params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"wait"}]}`),
		})
		if err := conn.WriteJSON(protocol.ANPMessage{
			JSONRPC: "2.0", ID: "blocked-invoke", Method: "invoke", Params: invokeParams,
		}); err != nil {
			result <- err
			return
		}
		select {
		case <-streamStarted:
		case <-time.After(time.Second):
			result <- errors.New("Agent stream was not entered")
			return
		}
		if err := conn.WriteJSON(protocol.ANPMessage{JSONRPC: "2.0", ID: "ping-while-blocked", Method: "ping"}); err != nil {
			result <- err
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		for {
			var response protocol.ANPMessage
			if err := conn.ReadJSON(&response); err != nil {
				result <- err
				return
			}
			if response.ID == "ping-while-blocked" {
				if string(response.Result) != `"pong"` {
					result <- errors.New("unexpected ping response: " + string(response.Result))
					return
				}
				result <- nil
				return
			}
		}
	}))
	defer server.Close()

	registry := newSessionTestRegistry(localAgent)
	config := DefaultTunnelConfig()
	config.ServerURL = strings.Replace(server.URL, "http://", "ws://", 1)
	config.BridgeID = "bridge-nonblocking"
	config.ReconnectInterval = time.Hour
	tunnel := NewTunnelService(registry, config)
	if err := tunnel.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tunnel.Stop()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ping while Agent blocked: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WebSocket read loop was blocked by Agent call")
	}
	close(streamRelease)
}
