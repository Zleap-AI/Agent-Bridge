package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	bridgeinternal "github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

func TestWantsStreaming(t *testing.T) {
	tests := []struct {
		name      string
		explicit  bool
		requestID string
		want      bool
	}{
		{name: "explicit flag", explicit: true, requestID: "request-1", want: true},
		{name: "default blocking", requestID: "request-1", want: false},
		{name: "legacy stream suffix", requestID: "request_stream", want: true},
		{name: "legacy bridge suffix", requestID: "request_bridge", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := wantsStreaming(test.explicit, test.requestID); got != test.want {
				t.Fatalf("wantsStreaming(%v, %q) = %v, want %v", test.explicit, test.requestID, got, test.want)
			}
		})
	}
}

func TestSessionNewInvokeNeverReusesCurrentSession(t *testing.T) {
	a := &sessionTestAgent{newSessions: []string{"session-1", "session-2"}}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	router := NewRequestRouter(reg)

	invokeParams, err := json.Marshal(protocol.ANPInvokeParams{
		AgentID: a.ID(),
		Method:  "session/new",
		Params:  json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("marshal invoke params: %v", err)
	}

	create := func(id string) string {
		response := router.Route(context.Background(), &protocol.ANPMessage{
			JSONRPC: "2.0",
			ID:      id,
			Method:  "invoke",
			Params:  invokeParams,
		}, sm)
		if response == nil || response.Error != nil {
			t.Fatalf("session/new response = %+v", response)
		}
		var result struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(response.Result, &result); err != nil {
			t.Fatalf("unmarshal session/new result: %v", err)
		}
		return result.SessionID
	}

	if got := create("request-1"); got != "session-1" {
		t.Fatalf("first session/new = %q, want session-1", got)
	}
	if got := create("request-2"); got != "session-2" {
		t.Fatalf("second session/new = %q, want session-2", got)
	}
}

func TestSessionsListDoesNotDuplicatePersistedActiveSession(t *testing.T) {
	a := &sessionTestAgent{newSessions: []string{"session-1"}}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	if _, err := sm.CreateNewSession(context.Background(), a.ID()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	params, _ := json.Marshal(map[string]string{"agent_id": a.ID()})
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "list-sessions", Method: "sessions/list", Params: params,
	}, sm)
	if response == nil || response.Error != nil {
		t.Fatalf("sessions/list response = %+v", response)
	}
	var sessions []struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(response.Result, &sessions); err != nil {
		t.Fatalf("unmarshal sessions/list: %v", err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "session-1" {
		t.Fatalf("sessions/list = %+v, want one session-1", sessions)
	}
}

type staticSessionScanner struct {
	name     string
	sessions []agent.SessionRef
}

func (s *staticSessionScanner) Name() string { return s.name }
func (s *staticSessionScanner) DiscoverSessions() ([]agent.SessionRef, error) {
	return s.sessions, nil
}
func (s *staticSessionScanner) ReadMessages(agent.SessionRef, int, int) ([]string, int, error) {
	return nil, 0, nil
}

func TestSessionsListIncludesNativeAgentHistory(t *testing.T) {
	const agentID = "native-history-test-agent"
	agent.RegisterScanner(&staticSessionScanner{
		name: agentID,
		sessions: []agent.SessionRef{{
			Harness:   agentID,
			NativeID:  "native-session-1",
			StartedAt: 1234,
		}},
	})

	a := &sessionTestAgent{agentID: agentID}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]string{"agent_id": agentID})
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "list-native-sessions", Method: "sessions/list", Params: params,
	}, sm)
	if response == nil || response.Error != nil {
		t.Fatalf("sessions/list response = %+v", response)
	}

	var sessions []sessionListItem
	if err := json.Unmarshal(response.Result, &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "native-session-1" || sessions[0].UpdatedAt != 1234 {
		t.Fatalf("sessions/list = %+v, want discovered native session", sessions)
	}
}

func TestSessionsListReturnsStructuredErrorWhenResultExceedsFrame(t *testing.T) {
	items := make([]sessionListItem, 17)
	for index := range items {
		items[index] = sessionListItem{AgentID: "agent", SessionID: strings.Repeat(`"`, 512*1024)}
	}
	response := buildSessionListResponse("large-session-list", items)
	if response.Error == nil || response.Error.Code != protocol.ANPErrorResponseTooLarge {
		t.Fatalf("oversized Session list response = %+v", response)
	}
	if !strings.Contains(response.Error.Message, "filter by agent_id") {
		t.Fatalf("oversized Session list error is not actionable: %q", response.Error.Message)
	}
}

func TestSessionLoadPreservesSmallHistory(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	want := []StoredMessage{{Role: "user", Text: "hello"}, {Role: "assistant", Text: "world"}}
	sm.SaveMessages(a.ID(), "session-1", want)
	loadParams, _ := json.Marshal(map[string]string{"sessionId": "session-1"})
	invokeParams, _ := json.Marshal(protocol.ANPInvokeParams{AgentID: a.ID(), Method: "session/load", Params: loadParams})
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "load-small", Method: "invoke", Params: invokeParams,
	}, sm)
	if response == nil || response.Error != nil {
		t.Fatalf("session/load response = %+v", response)
	}
	var result sessionLoadResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || result.SessionID != "session-1" || !reflect.DeepEqual(result.Messages, want) {
		t.Fatalf("session/load result = %+v", result)
	}
}

func TestSessionLoadReturnsStructuredErrorWhenHistoryExceedsFrame(t *testing.T) {
	text := strings.Repeat(`"`, 512*1024)
	messages := make([]StoredMessage, 17)
	for index := range messages {
		messages[index] = StoredMessage{Role: "assistant", Text: text}
	}
	response := buildSessionLoadResponse("load-large", "session-1", messages)
	if response.Error == nil || response.Error.Code != protocol.ANPErrorResponseTooLarge {
		t.Fatalf("oversized session/load response = %+v", response)
	}
	if !strings.Contains(response.Error.Message, "sessions/messages cursor pagination") {
		t.Fatalf("session/load error is not actionable: %q", response.Error.Message)
	}
}

func TestSessionMessagesRejectsNegativeCursor(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	sm.SaveMessages(a.ID(), "session-1", []StoredMessage{{Role: "user", Text: "hello"}})
	params, _ := json.Marshal(map[string]interface{}{
		"agent_id": a.ID(), "session_id": "session-1", "cursor": -1,
	})

	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "negative-cursor", Method: "sessions/messages", Params: params,
	}, sm)
	if response == nil || response.Error == nil {
		t.Fatalf("sessions/messages response = %+v, want invalid params error", response)
	}
	if response.Error.Code != -32602 {
		t.Fatalf("error code = %d, want -32602", response.Error.Code)
	}
}

func TestSessionMessagesReturnsRequestedPage(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	sm.SaveMessages(a.ID(), "session-1", []StoredMessage{
		{Role: "user", Text: "one"},
		{Role: "assistant", Text: "two"},
		{Role: "user", Text: "three"},
	})
	params, _ := json.Marshal(map[string]interface{}{
		"agent_id": a.ID(), "session_id": "session-1", "cursor": 1, "limit": 1,
	})

	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "message-page", Method: "sessions/messages", Params: params,
	}, sm)
	if response == nil || response.Error != nil {
		t.Fatalf("sessions/messages response = %+v", response)
	}
	var result struct {
		Messages []StoredMessage `json:"messages"`
		Total    int             `json:"total"`
		Cursor   int             `json:"cursor"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].Text != "two" || result.Total != 3 || result.Cursor != 2 {
		t.Fatalf("page = %+v", result)
	}
}

func TestSessionMessagesPaginatesByActualEncodedFrameSize(t *testing.T) {
	largeEscapedText := strings.Repeat(`"`, 128*1024)
	messages := make([]StoredMessage, 100)
	for index := range messages {
		messages[index] = StoredMessage{Role: "user", Text: largeEscapedText}
	}
	full := newSessionMessagesResponse("large-history", messages, len(messages), 0, len(messages), len(messages))
	fullWire, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if len(fullWire) <= protocol.MaxANPDeviceMessageBytes {
		t.Fatalf("escaped full history wire size = %d, want above %d", len(fullWire), protocol.MaxANPDeviceMessageBytes)
	}

	cursor := 0
	pages := 0
	for cursor < len(messages) {
		response := buildSessionMessagesResponse("large-history", messages, len(messages), cursor, len(messages))
		if response.Error != nil {
			t.Fatalf("page at cursor %d failed: %+v", cursor, response.Error)
		}
		wire, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if len(wire) > protocol.MaxANPDeviceMessageBytes {
			t.Fatalf("page wire size = %d, exceeds %d", len(wire), protocol.MaxANPDeviceMessageBytes)
		}
		var page sessionMessagesPage
		if err := json.Unmarshal(response.Result, &page); err != nil {
			t.Fatal(err)
		}
		if page.Total != len(messages) || page.Cursor <= cursor || page.Cursor > len(messages) {
			t.Fatalf("page at cursor %d = total:%d next:%d", cursor, page.Total, page.Cursor)
		}
		for _, message := range page.Messages {
			if message.Text != largeEscapedText {
				t.Fatal("page changed escaped Message content")
			}
		}
		cursor = page.Cursor
		pages++
	}
	if pages < 2 {
		t.Fatalf("escaped history pages = %d, want transport pagination", pages)
	}
}

func TestSessionMessagesReturnsStructuredErrorForOversizedLegacyMessage(t *testing.T) {
	// Six-byte JSON escaping for each NUL makes this old on-disk Message larger
	// than one bounded ANP frame even though it is one Go string.
	text := strings.Repeat("\x00", protocol.MaxANPDeviceMessageBytes/6+1)
	response := buildSessionMessagesResponse("legacy-message", []StoredMessage{{Role: "assistant", Text: text}}, 1, 0, 1)
	if response.Error == nil || response.Error.Code != protocol.ANPErrorResponseTooLarge {
		t.Fatalf("oversized legacy Message response = %+v", response)
	}
	if !strings.Contains(response.Error.Message, "cursor 0") || !strings.Contains(response.Error.Message, "Device response limit") {
		t.Fatalf("oversized legacy Message error is not diagnostic: %q", response.Error.Message)
	}
}

func TestThreeMiBLegacyMessageRemainsReadableWhenWireFits(t *testing.T) {
	text := strings.Repeat("x", 3*1024*1024)
	messages := []StoredMessage{{Role: "assistant", Text: text}}

	page := buildSessionMessagesResponse("legacy-page", messages, 1, 0, 1)
	if page.Error != nil {
		t.Fatalf("3 MiB sessions/messages response = %+v", page.Error)
	}
	load := buildSessionLoadResponse("legacy-load", "session-1", messages)
	if load.Error != nil {
		t.Fatalf("3 MiB session/load response = %+v", load.Error)
	}
	for name, response := range map[string]*protocol.ANPMessage{"page": page, "load": load} {
		wire, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if len(wire) <= 3*1024*1024 || len(wire) > protocol.MaxANPDeviceMessageBytes {
			t.Fatalf("%s wire bytes = %d", name, len(wire))
		}
	}
}

func TestSessionMessagesReturnsSessionNotFound(t *testing.T) {
	a := &sessionTestAgent{loadErr: errors.New("unknown session")}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"agent_id": a.ID(), "session_id": "missing-session",
	})

	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "missing-session", Method: "sessions/messages", Params: params,
	}, sm)
	if response == nil || response.Error == nil {
		t.Fatalf("sessions/messages response = %+v, want Session error", response)
	}
	if response.Error.Code != -31005 {
		t.Fatalf("error code = %d, want -31005", response.Error.Code)
	}
}

func TestSessionMessagesStartsDisconnectedAgentBeforeLoadingHistory(t *testing.T) {
	a := &sessionTestAgent{disconnected: true}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"agent_id": a.ID(), "session_id": "remote-history",
	})

	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "disconnected-history", Method: "sessions/messages", Params: params,
	}, sm)
	if response == nil || response.Error != nil {
		t.Fatalf("sessions/messages response = %+v, want empty loaded history", response)
	}
	a.mu.Lock()
	startCalls, loadedSession := a.startCalls, a.loadedSession
	a.mu.Unlock()
	if startCalls != 1 || loadedSession != "remote-history" {
		t.Fatalf("start calls = %d, loaded Session = %q", startCalls, loadedSession)
	}
}

type replaySessionTestAgent struct {
	*sessionTestAgent
	history <-chan bridgeinternal.StreamChunk
}

func (a *replaySessionTestAgent) LoadSessionStream(_ context.Context, sessionID string) (<-chan bridgeinternal.StreamChunk, error) {
	a.mu.Lock()
	a.loadedSession = sessionID
	a.mu.Unlock()
	return a.history, nil
}

func TestSessionMessagesPersistsNativeHistoryReplayedByAgent(t *testing.T) {
	history := make(chan bridgeinternal.StreamChunk, 3)
	history <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkResponse, RawUpdate: "user_message_chunk", Text: "Existing question"}
	history <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkResponse, RawUpdate: "agent_message_chunk", Text: "Existing answer"}
	history <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkFinal}
	close(history)

	a := &replaySessionTestAgent{sessionTestAgent: &sessionTestAgent{}, history: history}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"agent_id": a.ID(), "session_id": "native-history",
	})

	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "native-history", Method: "sessions/messages", Params: params,
	}, sm)
	if response == nil || response.Error != nil {
		t.Fatalf("sessions/messages response = %+v", response)
	}
	var result sessionMessagesPage
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 2 || result.Messages[0].Role != "user" || result.Messages[0].Text != "Existing question" || result.Messages[1].Text != "Existing answer" {
		t.Fatalf("replayed history = %+v", result.Messages)
	}
	persisted := sm.LoadMessages(a.ID(), "native-history")
	if !reflect.DeepEqual(persisted, result.Messages) {
		t.Fatalf("persisted history = %+v, response = %+v", persisted, result.Messages)
	}
}

func TestSessionMessagesReportsAgentUnavailableWhenRestartFails(t *testing.T) {
	a := &sessionTestAgent{disconnected: true, startErr: errors.New("runtime unavailable")}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"agent_id": a.ID(), "session_id": "remote-history",
	})

	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "unavailable-history", Method: "sessions/messages", Params: params,
	}, sm)
	if response == nil || response.Error == nil || response.Error.Code != -31002 {
		t.Fatalf("sessions/messages response = %+v, want Agent unavailable error", response)
	}
	if a.loadedSession != "" {
		t.Fatalf("LoadSession called after failed start: %q", a.loadedSession)
	}
}

func TestSessionMessagesAllowsPersistedEmptySession(t *testing.T) {
	storeDir := t.TempDir()
	creator := &sessionTestAgent{newSessions: []string{"empty-session"}}
	if _, err := newSessionManagerWithStoreDir(newSessionTestRegistry(creator), storeDir).CreateNewSession(context.Background(), creator.ID()); err != nil {
		t.Fatalf("create empty Session: %v", err)
	}
	a := &sessionTestAgent{loadErr: errors.New("must not reload persisted Session")}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, storeDir)
	params, _ := json.Marshal(map[string]interface{}{
		"agent_id": a.ID(), "session_id": "empty-session",
	})

	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "empty-session", Method: "sessions/messages", Params: params,
	}, sm)
	if response == nil || response.Error != nil {
		t.Fatalf("sessions/messages response = %+v, want empty history", response)
	}
	var result struct {
		Messages []StoredMessage `json:"messages"`
		Total    int             `json:"total"`
		Cursor   int             `json:"cursor"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Messages == nil || len(result.Messages) != 0 || result.Total != 0 || result.Cursor != 0 {
		t.Fatalf("empty Session result = %+v", result)
	}
	if a.loadedSession != "" {
		t.Fatalf("persisted empty Session was unnecessarily reloaded: %q", a.loadedSession)
	}
}

func TestHistoricalSessionPromptLoadsAndActivatesBeforeStreaming(t *testing.T) {
	stream := make(chan bridgeinternal.StreamChunk)
	close(stream)
	a := &sessionTestAgent{stream: stream}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	sm.ActivateSession(a.ID(), "current-session")
	router := NewRequestRouter(reg)
	completed := make(chan struct{}, 1)
	router.SetFinalResponseCallback(func(string, json.RawMessage, *protocol.ANPError) { completed <- struct{}{} })
	params, _ := json.Marshal(protocol.ANPInvokeParams{
		AgentID: a.ID(), Method: "session/prompt", Stream: true,
		Params: json.RawMessage(`{"sessionId":"historical-session","prompt":[{"type":"text","text":"hello"}]}`),
	})

	response := router.Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "historical-prompt", Method: "invoke", Params: params,
	}, sm)
	if response != nil {
		t.Fatalf("streaming response = %+v, want asynchronous completion", response)
	}
	if a.loadedSession != "historical-session" || a.streamLoaded != "historical-session" || a.streamCalls != 1 {
		t.Fatalf("load/stream order: loaded=%q observed=%q stream_calls=%d", a.loadedSession, a.streamLoaded, a.streamCalls)
	}
	if active := sm.GetSession(a.ID()); active != "historical-session" {
		t.Fatalf("active Session = %q, want historical-session", active)
	}
	if a.newCalls != 0 {
		t.Fatalf("session/new calls = %d, want none", a.newCalls)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("historical stream did not complete")
	}
}

func TestImplicitSessionPromptRestoresActiveSessionAfterAgentRestart(t *testing.T) {
	stream := make(chan bridgeinternal.StreamChunk)
	close(stream)
	a := &sessionTestAgent{newSessions: []string{"active-before-restart"}, stream: stream}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	if _, err := sm.CreateNewSession(context.Background(), a.ID()); err != nil {
		t.Fatalf("seed active Session: %v", err)
	}
	a.mu.Lock()
	a.disconnected = true
	a.loadedSession = ""
	a.mu.Unlock()
	router := NewRequestRouter(reg)
	completed := make(chan struct{}, 1)
	router.SetFinalResponseCallback(func(string, json.RawMessage, *protocol.ANPError) { completed <- struct{}{} })
	params, _ := json.Marshal(protocol.ANPInvokeParams{
		AgentID: a.ID(), Method: "session/prompt", Stream: true,
		Params: json.RawMessage(`{"prompt":[{"type":"text","text":"hello"}]}`),
	})

	response := router.Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "implicit-after-restart", Method: "invoke", Params: params,
	}, sm)
	if response != nil {
		t.Fatalf("streaming response = %+v, want asynchronous completion", response)
	}
	a.mu.Lock()
	startCalls, loadedSession, streamLoaded, streamCalls, newCalls := a.startCalls, a.loadedSession, a.streamLoaded, a.streamCalls, a.newCalls
	a.mu.Unlock()
	if startCalls != 1 || loadedSession != "active-before-restart" || streamLoaded != loadedSession || streamCalls != 1 {
		t.Fatalf("restart/load/stream order: starts=%d loaded=%q observed=%q streams=%d", startCalls, loadedSession, streamLoaded, streamCalls)
	}
	if newCalls != 1 {
		t.Fatalf("session/new calls = %d, want no new Session after the seeded one", newCalls)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("implicit restarted stream did not complete")
	}
}

func TestImplicitSessionPromptCreatesFreshSessionWhenRestartRestoreFails(t *testing.T) {
	stream := make(chan bridgeinternal.StreamChunk)
	close(stream)
	a := &sessionTestAgent{newSessions: []string{"expired-session", "fresh-session"}, stream: stream}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	if _, err := sm.CreateNewSession(context.Background(), a.ID()); err != nil {
		t.Fatalf("seed expired Session: %v", err)
	}
	a.mu.Lock()
	a.disconnected = true
	a.loadErr = errors.New("session expired")
	a.mu.Unlock()
	router := NewRequestRouter(reg)
	completed := make(chan struct{}, 1)
	router.SetFinalResponseCallback(func(string, json.RawMessage, *protocol.ANPError) { completed <- struct{}{} })
	params, _ := json.Marshal(protocol.ANPInvokeParams{
		AgentID: a.ID(), Method: "session/prompt", Stream: true,
		Params: json.RawMessage(`{"prompt":[{"type":"text","text":"hello"}]}`),
	})

	response := router.Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "implicit-expired-after-restart", Method: "invoke", Params: params,
	}, sm)
	if response != nil {
		t.Fatalf("streaming response = %+v, want asynchronous completion", response)
	}
	a.mu.Lock()
	newCalls, loadedSession := a.newCalls, a.loadedSession
	a.mu.Unlock()
	if loadedSession != "expired-session" || newCalls != 2 {
		t.Fatalf("restore fallback: loaded=%q session/new calls=%d", loadedSession, newCalls)
	}
	if active := sm.GetSession(a.ID()); active != "fresh-session" {
		t.Fatalf("active Session = %q, want fresh-session", active)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("fresh implicit stream did not complete")
	}
}

func TestHistoricalSessionPromptLoadFailurePreservesActiveSession(t *testing.T) {
	a := &sessionTestAgent{loadErr: errors.New("unknown session")}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	sm.ActivateSession(a.ID(), "current-session")
	params, _ := json.Marshal(protocol.ANPInvokeParams{
		AgentID: a.ID(), Method: "session/prompt", Stream: true,
		Params: json.RawMessage(`{"sessionId":"missing-session","prompt":[{"type":"text","text":"hello"}]}`),
	})

	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "missing-prompt", Method: "invoke", Params: params,
	}, sm)
	if response == nil || response.Error == nil || response.Error.Code != -31005 {
		t.Fatalf("load failure response = %+v, want -31005", response)
	}
	if active := sm.GetSession(a.ID()); active != "current-session" {
		t.Fatalf("active Session = %q, want current-session", active)
	}
	if a.streamCalls != 0 || a.newCalls != 0 {
		t.Fatalf("load failure invoked fallback work: stream=%d new=%d", a.streamCalls, a.newCalls)
	}
}

func TestEmptyAgentStreamStillCompletesInvocation(t *testing.T) {
	stream := make(chan bridgeinternal.StreamChunk)
	close(stream)
	a := &sessionTestAgent{stream: stream}
	reg := newSessionTestRegistry(a)
	router := NewRequestRouter(reg)
	router.SetStreamCallback(func(string, string, string) error { return nil })
	completed := make(chan json.RawMessage, 1)
	router.SetFinalResponseCallback(func(_ string, result json.RawMessage, responseError *protocol.ANPError) {
		if responseError != nil {
			t.Errorf("unexpected stream error: %s", responseError.Message)
		}
		completed <- result
	})
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, err := json.Marshal(protocol.ANPInvokeParams{
		AgentID: a.ID(), Method: "session/prompt", Stream: true,
		Params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	response := router.Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "request-empty", Method: "invoke", Params: params,
	}, sm)
	if response != nil {
		t.Fatalf("streaming route response = %+v, want asynchronous completion", response)
	}
	select {
	case result := <-completed:
		if string(result) != "{}" {
			t.Fatalf("final result = %s, want {}", result)
		}
	case <-time.After(time.Second):
		t.Fatal("empty Agent stream never completed")
	}
}

func TestAgentStreamErrorCompletesInvocationAsFailure(t *testing.T) {
	stream := make(chan bridgeinternal.StreamChunk, 2)
	stream <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkResponse, Text: "partial"}
	stream <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkError, Text: "model unavailable"}
	close(stream)
	a := &sessionTestAgent{stream: stream}
	reg := newSessionTestRegistry(a)
	router := NewRequestRouter(reg)
	router.SetStreamCallback(func(string, string, string) error { return nil })
	failed := make(chan string, 1)
	router.SetFinalResponseCallback(func(_ string, _ json.RawMessage, responseError *protocol.ANPError) {
		if responseError == nil {
			failed <- ""
			return
		}
		failed <- responseError.Message
	})
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, err := json.Marshal(protocol.ANPInvokeParams{
		AgentID: a.ID(), Method: "session/prompt", Stream: true,
		Params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response := router.Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "request-error", Method: "invoke", Params: params,
	}, sm); response != nil {
		t.Fatalf("streaming route response = %+v, want asynchronous completion", response)
	}

	select {
	case message := <-failed:
		if message != "model unavailable" {
			t.Fatalf("stream error = %q", message)
		}
	case <-time.After(time.Second):
		t.Fatal("Agent stream error never completed the invocation")
	}
	messages := sm.LoadMessages(a.ID(), "session-1")
	if len(messages) != 2 || messages[0].Text != "hello" || messages[1].Text != "partial" {
		t.Fatalf("partial error history = %+v", messages)
	}
}

func TestFinalOnlyAgentStreamPreservesResponseText(t *testing.T) {
	stream := make(chan bridgeinternal.StreamChunk, 1)
	stream <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkFinal, Text: "final answer"}
	close(stream)
	a := &sessionTestAgent{stream: stream}
	reg := newSessionTestRegistry(a)
	router := NewRequestRouter(reg)
	updates := make(chan bridgeinternal.StreamChunk, 1)
	router.SetStreamCallback(func(_ string, chunkType, text string) error {
		updates <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkFinal, RawUpdate: chunkType, Text: text}
		return nil
	})
	completed := make(chan json.RawMessage, 1)
	router.SetFinalResponseCallback(func(_ string, result json.RawMessage, _ *protocol.ANPError) { completed <- result })
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(protocol.ANPInvokeParams{
		AgentID: a.ID(), Method: "session/prompt", Stream: true,
		Params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}]}`),
	})
	router.Route(context.Background(), &protocol.ANPMessage{JSONRPC: "2.0", ID: "request-final", Method: "invoke", Params: params}, sm)

	select {
	case update := <-updates:
		if update.RawUpdate != "final" || update.Text != "final answer" {
			t.Fatalf("final update = %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("final update not forwarded")
	}
	select {
	case result := <-completed:
		var final struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(result, &final); err != nil || final.Text != "final answer" {
			t.Fatalf("final result = %s, want final answer", result)
		}
	case <-time.After(time.Second):
		t.Fatal("final-only stream did not complete")
	}
	messages := sm.LoadMessages(a.ID(), "session-1")
	if len(messages) != 2 || messages[1].Text != "final answer" {
		t.Fatalf("persisted messages = %+v", messages)
	}
}

func TestStreamOutputLimitReturnsErrorAndDrainsAgent(t *testing.T) {
	stream := make(chan bridgeinternal.StreamChunk)
	producerDone := make(chan struct{})
	first := strings.Repeat("x", MaxStreamOutputBytes-4)
	go func() {
		stream <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkResponse, Text: first}
		stream <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkResponse, Text: "12345"}
		stream <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkResponse, Text: "must be drained, not persisted"}
		close(stream)
		close(producerDone)
	}()
	a := &sessionTestAgent{stream: stream}
	reg := newSessionTestRegistry(a)
	router := NewRequestRouter(reg)
	updates := make(chan bridgeinternal.StreamChunk, 3)
	router.SetStreamCallback(func(_ string, chunkType, text string) error {
		updates <- bridgeinternal.StreamChunk{RawUpdate: chunkType, Text: text}
		return nil
	})
	failed := make(chan string, 1)
	router.SetFinalResponseCallback(func(_ string, result json.RawMessage, responseError *protocol.ANPError) {
		if result != nil {
			t.Errorf("limited stream result = %s, want nil", result)
		}
		if responseError == nil || responseError.Code != protocol.ANPErrorResponseTooLarge {
			t.Errorf("limited stream ANP error = %+v", responseError)
			failed <- ""
			return
		}
		failed <- responseError.Message
	})
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(protocol.ANPInvokeParams{
		AgentID: a.ID(), Method: "session/prompt", Stream: true,
		Params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}]}`),
	})
	if response := router.Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "request-output-limit", Method: "invoke", Params: params,
	}, sm); response != nil {
		t.Fatalf("streaming response = %+v, want asynchronous completion", response)
	}

	wantError := "Agent output exceeded the 2097152-byte limit"
	select {
	case message := <-failed:
		if message != wantError {
			t.Fatalf("output limit error = %q, want %q", message, wantError)
		}
	case <-time.After(time.Second):
		t.Fatal("limited Agent stream did not fail")
	}
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("limited Agent stream was not drained")
	}
	firstUpdate := <-updates
	if firstUpdate.RawUpdate != "response" || firstUpdate.Text != first {
		t.Fatalf("first stream update = type:%q bytes:%d", firstUpdate.RawUpdate, len(firstUpdate.Text))
	}
	select {
	case unexpected := <-updates:
		t.Fatalf("structured output-limit completion also emitted an ambiguous stream update: %+v", unexpected)
	case <-time.After(20 * time.Millisecond):
	}
	messages := sm.LoadMessages(a.ID(), "session-1")
	if len(messages) != 2 || messages[0].Text != "hello" || messages[1].Text != first {
		firstBytes, secondBytes := 0, 0
		if len(messages) > 0 {
			firstBytes = len(messages[0].Text)
		}
		if len(messages) > 1 {
			secondBytes = len(messages[1].Text)
		}
		t.Fatalf("limited stream history = count:%d text_bytes:[%d,%d]", len(messages), firstBytes, secondBytes)
	}
}

func TestLargeStreamPreservesFullTextInBoundedFinalResult(t *testing.T) {
	chunk := strings.Repeat("\x00", MaxStreamOutputBytes/2)
	stream := make(chan bridgeinternal.StreamChunk, 3)
	stream <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkResponse, Text: chunk}
	stream <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkResponse, Text: chunk}
	stream <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkFinal, Text: chunk + chunk}
	close(stream)
	a := &sessionTestAgent{stream: stream}
	reg := newSessionTestRegistry(a)
	router := NewRequestRouter(reg)
	var responseBytes, finalBytes int
	router.SetStreamCallback(func(_ string, chunkType, text string) error {
		switch chunkType {
		case "response":
			responseBytes += len(text)
		case "final":
			finalBytes += len(text)
		}
		return nil
	})
	completed := make(chan json.RawMessage, 1)
	router.SetFinalResponseCallback(func(_ string, result json.RawMessage, responseError *protocol.ANPError) {
		if responseError != nil {
			t.Errorf("large stream error = %q", responseError.Message)
		}
		completed <- result
	})
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(protocol.ANPInvokeParams{
		AgentID: a.ID(), Method: "session/prompt", Stream: true,
		Params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}]}`),
	})
	router.Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "request-large-stream", Method: "invoke", Params: params,
	}, sm)

	select {
	case result := <-completed:
		var final struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(result, &final); err != nil {
			t.Fatal(err)
		}
		if final.Text != chunk+chunk {
			t.Fatalf("large stream final text bytes = %d, want %d", len(final.Text), MaxStreamOutputBytes)
		}
		wire, err := json.Marshal(protocol.NewResultResponse("request-large-stream", result))
		if err != nil {
			t.Fatal(err)
		}
		if len(wire) <= MaxStreamOutputBytes || len(wire) > protocol.MaxANPDeviceMessageBytes {
			t.Fatalf("worst-escape final wire bytes = %d", len(wire))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("large stream did not complete")
	}
	if responseBytes != MaxStreamOutputBytes || finalBytes != 0 {
		t.Fatalf("forwarded stream bytes = response:%d final:%d", responseBytes, finalBytes)
	}
	messages := sm.LoadMessages(a.ID(), "session-1")
	if len(messages) != 2 || len(messages[1].Text) != MaxStreamOutputBytes {
		t.Fatalf("large stream history = count:%d", len(messages))
	}
	pageResponse := buildSessionMessagesResponse("large-stream-history", messages, len(messages), 0, len(messages))
	if pageResponse.Error != nil {
		t.Fatalf("maximum legal stream output could not be read back: %+v", pageResponse.Error)
	}
	wire, err := json.Marshal(pageResponse)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) > protocol.MaxANPDeviceMessageBytes {
		t.Fatalf("maximum legal stream history wire bytes = %d", len(wire))
	}
}

func TestStreamCallbackFailureStillDrainsAndPersistsHistory(t *testing.T) {
	stream := make(chan bridgeinternal.StreamChunk, 3)
	stream <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkResponse, Text: "complete "}
	stream <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkResponse, Text: "answer"}
	stream <- bridgeinternal.StreamChunk{Type: bridgeinternal.StreamChunkFinal}
	close(stream)
	a := &sessionTestAgent{stream: stream}
	reg := newSessionTestRegistry(a)
	router := NewRequestRouter(reg)
	callbackCalls := 0
	router.SetStreamCallback(func(string, string, string) error {
		callbackCalls++
		return errors.New("client disconnected")
	})
	completed := make(chan struct{}, 1)
	router.SetFinalResponseCallback(func(_ string, _ json.RawMessage, responseError *protocol.ANPError) {
		if responseError != nil {
			t.Errorf("unexpected final error: %s", responseError.Message)
		}
		completed <- struct{}{}
	})
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(protocol.ANPInvokeParams{
		AgentID: a.ID(), Method: "session/prompt", Stream: true,
		Params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}]}`),
	})
	response := router.Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "request-disconnect", Method: "invoke", Params: params,
	}, sm)
	if response != nil {
		t.Fatalf("streaming route response = %+v, want asynchronous completion", response)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("stream was not completed after callback failure")
	}

	if callbackCalls != 1 {
		t.Fatalf("stream callback calls = %d, want forwarding disabled after first failure", callbackCalls)
	}
	messages := sm.LoadMessages(a.ID(), "session-1")
	if len(messages) != 2 || messages[0].Text != "hello" || messages[1].Text != "complete answer" {
		t.Fatalf("persisted messages = %+v", messages)
	}
}

func TestFirstNonSessionStreamErrorIsNotRetried(t *testing.T) {
	stream := make(chan bridgeinternal.StreamChunk, 1)
	stream <- bridgeinternal.StreamChunk{
		Type:  bridgeinternal.StreamChunkError,
		Error: &bridgeinternal.ACPError{Code: 429, Message: "model rate limited"},
	}
	close(stream)
	a := &sessionTestAgent{stream: stream}
	reg := newSessionTestRegistry(a)
	router := NewRequestRouter(reg)
	router.SetStreamCallback(func(string, string, string) error { return nil })
	failed := make(chan string, 1)
	router.SetFinalResponseCallback(func(_ string, _ json.RawMessage, responseError *protocol.ANPError) {
		if responseError == nil {
			failed <- ""
			return
		}
		failed <- responseError.Message
	})
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(protocol.ANPInvokeParams{
		AgentID: a.ID(), Method: "session/prompt", Stream: true,
		Params: json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"text","text":"hello"}]}`),
	})
	router.Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "request-rate-limit", Method: "invoke", Params: params,
	}, sm)

	select {
	case message := <-failed:
		if message != "model rate limited" {
			t.Fatalf("stream error = %q", message)
		}
	case <-time.After(time.Second):
		t.Fatal("non-session error did not complete")
	}
	if a.newCalls != 0 {
		t.Fatalf("session/new calls = %d, want no retry for model error", a.newCalls)
	}
	messages := sm.LoadMessages(a.ID(), "session-1")
	if len(messages) != 1 || messages[0].Text != "hello" {
		t.Fatalf("failed prompt history = %+v", messages)
	}
}

func TestInvalidSessionStreamErrorClassification(t *testing.T) {
	tests := []struct {
		message string
		want    bool
	}{
		{message: "session not found", want: true},
		{message: "Unknown session abc", want: true},
		{message: "model unavailable", want: false},
		{message: "rate limited", want: false},
	}
	for _, test := range tests {
		t.Run(test.message, func(t *testing.T) {
			chunk := bridgeinternal.StreamChunk{
				Type:  bridgeinternal.StreamChunkError,
				Error: &bridgeinternal.ACPError{Message: test.message},
			}
			if got := isInvalidSessionStreamError(chunk); got != test.want {
				t.Fatalf("isInvalidSessionStreamError(%q) = %v, want %v", test.message, got, test.want)
			}
		})
	}
}
