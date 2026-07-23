// -*- coding: utf-8 -*-
// Go 1.25+
//
// router_exec_test.go
// 消息路由器 — session/exec 命令执行测试
//
// Lzm 2026-07-22

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// ─── session/exec 测试 ─────────────────────────────────────

func TestSessionExecRejectsMissingAgentID(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"command": "echo hello",
	})
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "exec-no-agent", Method: "session/exec", Params: params,
	}, sm)
	if response == nil || response.Error == nil {
		t.Fatalf("session/exec response = %+v, want error for missing agent_id", response)
	}
	if response.Error.Code != -32602 {
		t.Fatalf("error code = %d, want -32602", response.Error.Code)
	}
}

func TestSessionExecRejectsMissingCommand(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"agent_id": a.ID(),
	})
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "exec-no-cmd", Method: "session/exec", Params: params,
	}, sm)
	if response == nil || response.Error == nil {
		t.Fatalf("session/exec response = %+v, want error for missing command", response)
	}
	if response.Error.Code != -32602 {
		t.Fatalf("error code = %d, want -32602", response.Error.Code)
	}
}

func TestSessionExecRejectsBlacklistedCommand(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"agent_id": a.ID(),
		"command":  "rm -rf /",
	})
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "exec-blacklist", Method: "session/exec", Params: params,
	}, sm)
	if response == nil || response.Error == nil {
		t.Fatalf("session/exec response = %+v, want error for blacklisted command", response)
	}
	if response.Error.Code != -31010 {
		t.Fatalf("error code = %d, want -31010", response.Error.Code)
	}
	if !strings.Contains(response.Error.Message, "禁止执行危险命令") {
		t.Fatalf("error message = %q, want blacklist warning", response.Error.Message)
	}
}

func TestSessionExecRejectsUnknownCommand(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"agent_id": a.ID(),
		"command":  "malicious_tool --destroy",
	})
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "exec-unknown", Method: "session/exec", Params: params,
	}, sm)
	if response == nil || response.Error == nil {
		t.Fatalf("session/exec response = %+v, want error for unknown command", response)
	}
	if response.Error.Code != -31010 {
		t.Fatalf("error code = %d, want -31010", response.Error.Code)
	}
	if !strings.Contains(response.Error.Message, "不在允许执行的命令列表中") {
		t.Fatalf("error message = %q, want whitelist warning", response.Error.Message)
	}
}

func TestSessionExecRejectsUnknownAgent(t *testing.T) {
	reg := agent.NewAgentRegistry(agent.DefaultAgentRegistryConfig())
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"agent_id": "nonexistent-agent",
		"command":  "echo hello",
	})
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "exec-unknown-agent", Method: "session/exec", Params: params,
	}, sm)
	if response == nil || response.Error == nil {
		t.Fatalf("session/exec response = %+v, want error for unknown agent", response)
	}
	if response.Error.Code != -31001 {
		t.Fatalf("error code = %d, want -31001", response.Error.Code)
	}
}

func TestSessionExecRunsWhitelistedCommand(t *testing.T) {
	a := &sessionTestAgent{}
	reg := newSessionTestRegistry(a)
	sm := newSessionManagerWithStoreDir(reg, t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"agent_id": a.ID(),
		"command":  "echo hello_world",
	})
	response := NewRequestRouter(reg).Route(context.Background(), &protocol.ANPMessage{
		JSONRPC: "2.0", ID: "exec-echo", Method: "session/exec", Params: params,
	}, sm)
	if response == nil || response.Error != nil {
		t.Fatalf("session/exec response = %+v, want success", response)
	}
	var result struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exitCode"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0. stdout=%q stderr=%q", result.ExitCode, result.Stdout, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "hello_world") {
		t.Fatalf("stdout = %q, want hello_world", result.Stdout)
	}
}
