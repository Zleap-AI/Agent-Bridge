// -*- coding: utf-8 -*-
// Go 1.25+
//
// codex.go
// Codex CLI Agent 实现
// 通过 codex.cmd 启动，支持 ACP 协议
// 特殊处理：CODEX_HOME 重定向、503 重试
//
// Lzm 2026-07-09

package agent

import (
	"context"
	"fmt"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// CodexAgent Codex CLI Agent 实现
type CodexAgent struct {
	*baseAgent
}

// NewCodexAgent 创建 Codex Agent 实例
func NewCodexAgent(meta AgentMeta) *CodexAgent {
	if meta.DisplayName == "" {
		meta.DisplayName = "Codex CLI"
	}
	return &CodexAgent{
		baseAgent: newBaseAgent(meta),
	}
}

// Start 启动 Codex 进程并完成 ACP 握手
// 进程生命周期由 Stop 管理，ctx 只约束启动与握手
// Lzm 2026-07-10
func (a *CodexAgent) Start(ctx context.Context) error {
	return a.start(ctx, nil)
}

// Send 发送请求并等待完整响应
func (a *CodexAgent) Send(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doSend(ctx, req)
}

// Stream 发送请求并返回流式块通道
func (a *CodexAgent) Stream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doStream(ctx, req)
}

// NewSession 创建新 ACP 会话
func (a *CodexAgent) NewSession(ctx context.Context) (string, error) {
	return a.doNewSession(ctx)
}

// LoadSession 加载已有会话
func (a *CodexAgent) LoadSession(ctx context.Context, sessionID string) error {
	return a.doLoadSession(ctx, sessionID)
}
