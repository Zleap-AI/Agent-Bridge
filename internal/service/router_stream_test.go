// -*- coding: utf-8 -*-
// Go 1.25+
//
// router_stream_test.go
// 消息路由器 — 流式推送/提示/大输出测试
//
// Lzm 2026-07-22

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	bridgeinternal "github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// ─── 历史会话提示测试 ─────────────────────────────────────

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
	if _, err := sm.CreateNewSession(context.Background(), a.ID(), "", ""); err != nil {
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
	if _, err := sm.CreateNewSession(context.Background(), a.ID(), "", ""); err != nil {
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

// ─── 流式推送测试 ─────────────────────────────────────────

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
