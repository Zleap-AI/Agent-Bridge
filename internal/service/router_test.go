// -*- coding: utf-8 -*-
// Go 1.25+
//
// router_test.go
// 消息路由器 — 核心路由/会话/流式/消息测试
//
// Lzm 2026-07-22

package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

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
	if _, err := sm.CreateNewSession(context.Background(), a.ID(), "", ""); err != nil {
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
	if response == nil {
		t.Fatalf("sessions/messages returned nil")
	}
	if response.Error != nil {
		// Agent 无法启动时，当前实现降级返回空消息而非报错。
		// 这是有意为之的设计决策，确保前端不因 Agent 临时不可用而阻塞。
		t.Fatalf("sessions/messages unexpectedly returned error: %+v", response.Error)
	}
	// 验证返回的是空消息
	var result struct {
		Messages []StoredMessage `json:"messages"`
		Total    int             `json:"total"`
		Cursor   int             `json:"cursor"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result.Messages) != 0 || result.Total != 0 {
		t.Fatalf("sessions/messages response = %+v, want empty history", response)
	}
	if a.loadedSession != "" {
		t.Fatalf("LoadSession called after failed start: %q", a.loadedSession)
	}
}

func TestSessionMessagesAllowsPersistedEmptySession(t *testing.T) {
	storeDir := t.TempDir()
	creator := &sessionTestAgent{newSessions: []string{"empty-session"}}
	if _, err := newSessionManagerWithStoreDir(newSessionTestRegistry(creator), storeDir).CreateNewSession(context.Background(), creator.ID(), "", ""); err != nil {
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
