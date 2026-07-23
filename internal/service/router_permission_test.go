// -*- coding: utf-8 -*-
// Go 1.25+
//
// router_permission_test.go
// 消息路由器 — 权限响应/会话取消/未知方法测试
//
// Lzm 2026-07-22

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// ─── session/permission_response 测试 ─────────────────────

func TestPermissionResponseRejectsMissingSessionID(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"allowed": true,
	})
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "perm-no-session", Method: "session/permission_response", Params: params,
	}, sm)
	if response == nil || response.Error == nil {
		t.Fatalf("session/permission_response response = %+v, want error for missing session_id", response)
	}
	if response.Error.Code != -32602 {
		t.Fatalf("error code = %d, want -32602", response.Error.Code)
	}
}

func TestPermissionResponseAcceptsCamelCaseSessionId(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	router := NewRequestRouter(reg)

	// 先注册一个 pending 权限请求
	sessionID := "test-session-1"
	router.permMu.Lock()
	router.pendingPermissions[sessionID] = make(chan internal.PermissionResult, 1)
	router.permMu.Unlock()

	// 使用 camelCase sessionId 字段发送响应
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"allowed":   true,
	})
	response := router.Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "perm-camel", Method: "session/permission_response", Params: params,
	}, sm)
	if response == nil || response.Error != nil {
		t.Fatalf("session/permission_response response = %+v, want ok", response)
	}
}

func TestPermissionResponseRejectsUnknownSession(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"session_id": "unknown-session",
		"allowed":    true,
	})
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "perm-unknown", Method: "session/permission_response", Params: params,
	}, sm)
	if response == nil || response.Error == nil {
		t.Fatalf("session/permission_response response = %+v, want error for unknown session", response)
	}
	if response.Error.Code != -32602 {
		t.Fatalf("error code = %d, want -32602", response.Error.Code)
	}
}

func TestPermissionResponseUpdatesPermissionMode(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	router := NewRequestRouter(reg)

	// 先注册一个 pending 权限请求
	sessionID := "test-session-mode-update"
	router.permMu.Lock()
	router.pendingPermissions[sessionID] = make(chan internal.PermissionResult, 1)
	router.permMu.Unlock()

	// 发送权限响应同时更新授权模式
	params, _ := json.Marshal(map[string]interface{}{
		"session_id":      sessionID,
		"allowed":         true,
		"agent_id":        a.ID(),
		"permission_mode": "auto_approve",
	})
	response := router.Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "perm-mode-update", Method: "session/permission_response", Params: params,
	}, sm)
	if response == nil || response.Error != nil {
		t.Fatalf("session/permission_response response = %+v, want ok", response)
	}

	// 验证授权模式已更新
	mode := sm.GetSessionPermissionMode(a.ID(), sessionID)
	if mode != "auto_approve" {
		t.Fatalf("permission mode = %q, want auto_approve", mode)
	}
}

// ─── session/cancel 测试 ───────────────────────────────────

func TestSessionCancelRejectsMissingAgentID(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{})
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "cancel-no-agent", Method: "session/cancel", Params: params,
	}, sm)
	if response == nil || response.Error == nil {
		t.Fatalf("session/cancel response = %+v, want error for missing agent_id", response)
	}
	if response.Error.Code != -32602 {
		t.Fatalf("error code = %d, want -32602", response.Error.Code)
	}
}

func TestSessionCancelWithValidAgentReturnsOk(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	// 创建会话让 Cancel 知道有活跃会话
	_, err := sm.CreateNewSession(context.Background(), a.ID(), "", "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	params, _ := json.Marshal(map[string]interface{}{
		"agent_id": a.ID(),
	})
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "cancel-valid", Method: "session/cancel", Params: params,
	}, sm)
	if response == nil || response.Error != nil {
		t.Fatalf("session/cancel response = %+v, want ok", response)
	}
	// 验证返回了 ok 状态
	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.Status != "ok" {
		t.Fatalf("cancel status = %q, want ok", result.Status)
	}
}

func TestSessionCancelWithExplicitSessionID(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"agent_id":  a.ID(),
		"sessionId": "explicit-session",
	})
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "cancel-explicit", Method: "session/cancel", Params: params,
	}, sm)
	if response == nil || response.Error != nil {
		t.Fatalf("session/cancel response = %+v, want ok", response)
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.Status != "ok" {
		t.Fatalf("cancel status = %q, want ok", result.Status)
	}
}

// ─── 路由分发测试 ──────────────────────────────────────────

func TestRouteUnknownMethodReturnsError(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "unknown", Method: "nonexistent/method", Params: json.RawMessage(`{}`),
	}, sm)
	if response == nil || response.Error == nil {
		t.Fatalf("unknown method response = %+v, want error", response)
	}
	if response.Error.Code != -32601 {
		t.Fatalf("error code = %d, want -32601", response.Error.Code)
	}
}
