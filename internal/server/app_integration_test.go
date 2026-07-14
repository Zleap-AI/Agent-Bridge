package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
	server "github.com/Zleap-AI/Agent-Bridge/internal/server"
	"github.com/gorilla/websocket"
)

func TestRemoteLifecycleFromSetupThroughSSEAndRevocation(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "state.db")
	app, err := server.New(ctx, server.Config{
		ListenAddr: "127.0.0.1:0", DataDir: filepath.Dir(database), DatabasePath: database, Version: server.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()

	ownerClient := newCookieClient(t)
	response := requestJSON(t, ownerClient, http.MethodGet, httpServer.URL+"/api/v1/status", "", nil)
	assertStatus(t, response, http.StatusOK)
	var status struct {
		Initialized bool `json:"initialized"`
	}
	decodeResponse(t, response, &status)
	if status.Initialized {
		t.Fatal("new server is already initialized")
	}

	response = requestJSON(t, ownerClient, http.MethodPost, httpServer.URL+"/api/v1/setup", app.SetupToken(), map[string]any{"password": "owner password"})
	assertStatus(t, response, http.StatusCreated)
	response.Body.Close()
	response = requestJSON(t, ownerClient, http.MethodPost, httpServer.URL+"/api/v1/auth/login", "", map[string]any{"password": "owner password"})
	assertStatus(t, response, http.StatusOK)
	response.Body.Close()

	response = requestJSON(t, ownerClient, http.MethodPost, httpServer.URL+"/api/v1/admin/pairing-codes", "", map[string]any{})
	assertStatus(t, response, http.StatusCreated)
	var pairing struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
		ExpiresIn int64     `json:"expires_in"`
	}
	decodeResponse(t, response, &pairing)
	if pairing.Code == "" || pairing.ExpiresIn != 600 {
		t.Fatalf("invalid pairing response: %#v", pairing)
	}

	response = requestJSON(t, http.DefaultClient, http.MethodPost, httpServer.URL+"/api/v1/pairings/claim", "", map[string]any{
		"code": pairing.Code, "hostname": "Studio Mac",
	})
	assertStatus(t, response, http.StatusCreated)
	var claim struct {
		BridgeID  string `json:"bridge_id"`
		Token     string `json:"token"`
		ServerURL string `json:"server_url"`
		Device    struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"device"`
	}
	decodeResponse(t, response, &claim)
	if claim.BridgeID == "" || claim.Token == "" || claim.Device.ID != claim.BridgeID || claim.Device.Name != "Studio Mac" {
		t.Fatalf("invalid claim: %#v", claim)
	}
	if !strings.HasSuffix(claim.ServerURL, "/ws") {
		t.Fatalf("invalid WebSocket URL: %q", claim.ServerURL)
	}
	response = requestJSON(t, ownerClient, http.MethodPost, fmt.Sprintf("%s/api/v1/devices/%s/agents/codex/sessions", httpServer.URL, claim.BridgeID), "", map[string]any{})
	assertStatus(t, response, http.StatusConflict)
	var initiallyOffline struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeResponse(t, response, &initiallyOffline)
	if initiallyOffline.Error.Code != "DEVICE_OFFLINE" {
		t.Fatalf("unregistered offline Device error = %q", initiallyOffline.Error.Code)
	}

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
	header := http.Header{}
	header.Set("X-Bridge-Id", claim.BridgeID)
	header.Set("X-Agent-Ids", "codex")
	header.Set("Authorization", "Bearer "+claim.Token)
	deviceConn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("connect Device: %v", err)
	}
	defer deviceConn.Close()
	registration, _ := json.Marshal(protocol.ANPBridgeRegister{
		BridgeID: claim.BridgeID,
		Agents:   []protocol.ANPAgent{{AgentID: "codex", DisplayName: "Codex", Status: "connected"}},
	})
	if err := deviceConn.WriteJSON(protocol.ANPMessage{JSONRPC: "2.0", Method: "bridge/register", Params: registration}); err != nil {
		t.Fatal(err)
	}
	deviceErrors := make(chan error, 1)
	go fakeDevice(deviceConn, deviceErrors)

	waitFor(t, 3*time.Second, func() bool {
		response := requestJSON(t, ownerClient, http.MethodGet, httpServer.URL+"/api/v1/admin/devices", "", nil)
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return false
		}
		var result struct {
			Devices []struct {
				ID         string `json:"id"`
				Online     bool   `json:"online"`
				AgentCount int    `json:"agent_count"`
			} `json:"devices"`
		}
		decodeResponse(t, response, &result)
		return len(result.Devices) == 1 && result.Devices[0].Online && result.Devices[0].AgentCount == 1
	})

	response = requestJSON(t, ownerClient, http.MethodPost, httpServer.URL+"/api/v1/admin/api-keys", "", map[string]string{"name": "Test integration"})
	assertStatus(t, response, http.StatusCreated)
	var createdKey struct {
		APIKey struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"api_key"`
	}
	decodeResponse(t, response, &createdKey)
	if createdKey.APIKey.ID == "" || !strings.HasPrefix(createdKey.APIKey.Key, "abk_") {
		t.Fatalf("invalid API key response: %#v", createdKey)
	}

	apiClient := http.DefaultClient
	response = requestJSON(t, apiClient, http.MethodGet, httpServer.URL+"/api/v1/devices", createdKey.APIKey.Key, nil)
	assertStatus(t, response, http.StatusOK)
	response.Body.Close()
	response = requestJSON(t, apiClient, http.MethodGet, httpServer.URL+"/api/v1/admin/devices", createdKey.APIKey.Key, nil)
	assertStatus(t, response, http.StatusForbidden)
	response.Body.Close()

	response = requestJSON(t, apiClient, http.MethodPost, fmt.Sprintf("%s/api/v1/devices/%s/agents/codex/sessions", httpServer.URL, claim.BridgeID), createdKey.APIKey.Key, map[string]any{})
	assertStatus(t, response, http.StatusCreated)
	var sessionResult struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	decodeResponse(t, response, &sessionResult)
	if sessionResult.Session.ID != "sess_remote_1" {
		t.Fatalf("session ID = %q", sessionResult.Session.ID)
	}

	secretMessage := "SERVER_MUST_NOT_PERSIST_THIS_MESSAGE_9f77"
	messageURL := fmt.Sprintf("%s/api/v1/devices/%s/agents/codex/sessions/%s/messages", httpServer.URL, claim.BridgeID, sessionResult.Session.ID)
	response = requestJSON(t, apiClient, http.MethodGet, messageURL+"?cursor=1&limit=1", createdKey.APIKey.Key, nil)
	assertStatus(t, response, http.StatusOK)
	var messagePage struct {
		Messages []struct {
			Text string `json:"text"`
		} `json:"messages"`
		Total  int `json:"total"`
		Cursor int `json:"cursor"`
	}
	decodeResponse(t, response, &messagePage)
	if len(messagePage.Messages) != 1 || messagePage.Messages[0].Text != "two" || messagePage.Total != 3 || messagePage.Cursor != 2 {
		t.Fatalf("Message page = %+v", messagePage)
	}
	missingMessageURL := fmt.Sprintf("%s/api/v1/devices/%s/agents/codex/sessions/missing/messages", httpServer.URL, claim.BridgeID)
	response = requestJSON(t, apiClient, http.MethodGet, missingMessageURL, createdKey.APIKey.Key, nil)
	assertStatus(t, response, http.StatusNotFound)
	var missingSession struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeResponse(t, response, &missingSession)
	if missingSession.Error.Code != "SESSION_NOT_FOUND" {
		t.Fatalf("missing Session error = %q", missingSession.Error.Code)
	}

	response = requestJSON(t, apiClient, http.MethodPost, messageURL, createdKey.APIKey.Key, map[string]any{
		"content": []map[string]string{{"type": "text", "text": secretMessage}},
	})
	assertStatus(t, response, http.StatusOK)
	streamBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	streamText := string(streamBody)
	if !strings.Contains(streamText, "event: reasoning.delta") || !strings.Contains(streamText, "event: message.delta") || !strings.Contains(streamText, "event: done") || !strings.Contains(streamText, "Remote response") {
		t.Fatalf("unexpected SSE stream:\n%s", streamText)
	}
	response = requestJSON(t, apiClient, http.MethodPost, messageURL, createdKey.APIKey.Key, map[string]any{
		"content": []map[string]string{{"type": "text", "text": "TRIGGER_STREAM_ERROR"}},
	})
	assertStatus(t, response, http.StatusOK)
	errorBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(errorBody), "event: error"); got != 1 || strings.Contains(string(errorBody), "event: done") {
		t.Fatalf("terminal SSE error must appear once without done:\n%s", errorBody)
	}
	response = requestJSON(t, apiClient, http.MethodPost, messageURL, createdKey.APIKey.Key, map[string]any{
		"content": []map[string]string{{"type": "text", "text": "TRIGGER_PAYLOAD_ERROR"}},
	})
	assertStatus(t, response, http.StatusOK)
	payloadErrorBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	payloadErrorText := string(payloadErrorBody)
	if got := strings.Count(payloadErrorText, "event: error"); got != 1 || !strings.Contains(payloadErrorText, "PAYLOAD_TOO_LARGE") || strings.Contains(payloadErrorText, "event: done") {
		t.Fatalf("payload limit must return one structured SSE error without done:\n%s", payloadErrorText)
	}

	response = requestJSON(t, apiClient, http.MethodPost, messageURL, createdKey.APIKey.Key, map[string]any{
		"content": []map[string]string{{"type": "image", "text": "ignored"}},
	})
	assertStatus(t, response, http.StatusUnprocessableEntity)
	var unsupported struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeResponse(t, response, &unsupported)
	if unsupported.Error.Code != "UNSUPPORTED_CONTENT_TYPE" {
		t.Fatalf("unsupported content error = %q", unsupported.Error.Code)
	}
	response = requestJSON(t, apiClient, http.MethodPost, messageURL, createdKey.APIKey.Key, map[string]any{
		"content": []map[string]string{{"type": "text", "text": "   "}},
	})
	assertStatus(t, response, http.StatusBadRequest)
	var emptyMessage struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeResponse(t, response, &emptyMessage)
	if emptyMessage.Error.Code != "INVALID_REQUEST" {
		t.Fatalf("empty Message error = %q", emptyMessage.Error.Code)
	}

	waitFor(t, 3*time.Second, func() bool {
		response := requestJSON(t, ownerClient, http.MethodGet, httpServer.URL+"/api/v1/admin/calls", "", nil)
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return false
		}
		var calls struct {
			Calls []any `json:"calls"`
		}
		decodeResponse(t, response, &calls)
		return len(calls.Calls) >= 2
	})

	assertSecretsNotPersisted(t, database, secretMessage, app.SetupToken(), claim.Token, createdKey.APIKey.Key)

	if err := deviceConn.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		response := requestJSON(t, ownerClient, http.MethodGet, httpServer.URL+"/api/v1/admin/devices", "", nil)
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return false
		}
		var result struct {
			Devices []struct {
				Online     bool   `json:"online"`
				LastSeenAt string `json:"last_seen_at"`
			} `json:"devices"`
		}
		decodeResponse(t, response, &result)
		return len(result.Devices) == 1 && !result.Devices[0].Online && result.Devices[0].LastSeenAt != ""
	})
	response = requestJSON(t, apiClient, http.MethodPost, fmt.Sprintf("%s/api/v1/devices/%s/agents/codex/sessions", httpServer.URL, claim.BridgeID), createdKey.APIKey.Key, map[string]any{})
	assertStatus(t, response, http.StatusConflict)
	var offline struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeResponse(t, response, &offline)
	if offline.Error.Code != "DEVICE_OFFLINE" {
		t.Fatalf("offline Device error = %q", offline.Error.Code)
	}

	response = requestJSON(t, ownerClient, http.MethodDelete, httpServer.URL+"/api/v1/admin/api-keys/"+createdKey.APIKey.ID, "", nil)
	assertStatus(t, response, http.StatusNoContent)
	response.Body.Close()
	response = requestJSON(t, apiClient, http.MethodGet, httpServer.URL+"/api/v1/devices", createdKey.APIKey.Key, nil)
	assertStatus(t, response, http.StatusUnauthorized)
	response.Body.Close()

	response = requestJSON(t, ownerClient, http.MethodDelete, httpServer.URL+"/api/v1/admin/devices/"+claim.BridgeID, "", nil)
	assertStatus(t, response, http.StatusNoContent)
	response.Body.Close()
	_, unauthorized, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		t.Fatal("deleted Device reconnected")
	}
	if unauthorized == nil || unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reconnect status = %#v, err = %v", unauthorized, err)
	}
	unauthorized.Body.Close()

	select {
	case err := <-deviceErrors:
		if err != nil && !errors.Is(err, net.ErrClosed) && !websocket.IsCloseError(err, 4001, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
			t.Fatalf("fake Device failed: %v", err)
		}
	default:
	}

	response = requestJSON(t, http.DefaultClient, http.MethodGet, httpServer.URL+"/openapi.json", "", nil)
	assertStatus(t, response, http.StatusOK)
	var spec map[string]any
	decodeResponse(t, response, &spec)
	if spec["openapi"] != "3.1.0" || spec["paths"] == nil {
		t.Fatalf("invalid OpenAPI document: %#v", spec)
	}
	info, ok := spec["info"].(map[string]any)
	if !ok || info["version"] != server.Version {
		t.Fatalf("OpenAPI version = %#v, want %q", info["version"], server.Version)
	}
}

func fakeDevice(conn *websocket.Conn, result chan<- error) {
	for {
		var message protocol.ANPMessage
		if err := conn.ReadJSON(&message); err != nil {
			result <- err
			return
		}
		switch message.Method {
		case "sessions/list":
			_ = conn.WriteJSON(protocol.NewResultResponse(message.ID, json.RawMessage(`[{"session_id":"sess_remote_1","message_count":1}]`)))
		case "sessions/messages":
			var params struct {
				SessionID string `json:"session_id"`
				Cursor    int    `json:"cursor"`
				Limit     int    `json:"limit"`
			}
			if err := json.Unmarshal(message.Params, &params); err != nil {
				result <- err
				return
			}
			if params.SessionID == "missing" {
				_ = conn.WriteJSON(protocol.NewErrorResponse(message.ID, -31005, "Session was not found"))
				continue
			}
			history := []map[string]string{
				{"role": "user", "text": "one"},
				{"role": "assistant", "text": "two"},
				{"role": "user", "text": "three"},
			}
			start := params.Cursor
			if start > len(history) {
				start = len(history)
			}
			end := len(history)
			if params.Limit > 0 && params.Limit < end-start {
				end = start + params.Limit
			}
			page, _ := json.Marshal(map[string]any{"messages": history[start:end], "total": len(history), "cursor": end})
			_ = conn.WriteJSON(protocol.NewResultResponse(message.ID, page))
		case "invoke":
			var invoke protocol.ANPInvokeParams
			if err := json.Unmarshal(message.Params, &invoke); err != nil {
				result <- err
				return
			}
			switch invoke.Method {
			case "session/new":
				_ = conn.WriteJSON(protocol.NewResultResponse(message.ID, json.RawMessage(`{"sessionId":"sess_remote_1"}`)))
			case "session/prompt":
				if strings.Contains(string(invoke.Params), "TRIGGER_PAYLOAD_ERROR") {
					_ = conn.WriteJSON(protocol.NewErrorResponse(message.ID, protocol.ANPErrorResponseTooLarge, "Agent output exceeded the limit"))
				} else if strings.Contains(string(invoke.Params), "TRIGGER_STREAM_ERROR") {
					_ = conn.WriteJSON(protocol.NewStreamUpdate(message.ID, "error", "Agent failed"))
					_ = conn.WriteJSON(protocol.NewErrorResponse(message.ID, -31002, "Agent failed"))
				} else {
					_ = conn.WriteJSON(protocol.NewStreamUpdate(message.ID, "thought", "Thinking"))
					_ = conn.WriteJSON(protocol.NewStreamUpdate(message.ID, "response", "Remote response"))
					_ = conn.WriteJSON(protocol.NewResultResponse(message.ID, json.RawMessage(`{"text":"Remote response"}`)))
				}
			}
		}
	}
}

func newCookieClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

func requestJSON(t *testing.T, client *http.Client, method, target, bearer string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, target, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertStatus(t *testing.T, response *http.Response, want int) {
	t.Helper()
	if response.StatusCode != want {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("HTTP status = %d, want %d, body=%s", response.StatusCode, want, body)
	}
}

func decodeResponse(t *testing.T, response *http.Response, destination any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func assertSecretsNotPersisted(t *testing.T, database string, values ...string) {
	t.Helper()
	files, err := filepath.Glob(database + "*")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			if value != "" && bytes.Contains(data, []byte(value)) {
				t.Fatalf("plaintext secret or Conversation Data was persisted in %s", filepath.Base(file))
			}
		}
	}
}
