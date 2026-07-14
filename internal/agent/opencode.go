// -*- coding: utf-8 -*-
// Go 1.25+
//
// opencode.go
// OpenCode Agent 实现
// 内置 ACP 支持，直接使用 opencode.cmd 启动
// 需要 OPENAI_API_KEY 环境变量
//
// Lzm 2026-07-09

package agent

import (
	"context"
	"fmt"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// OpenCodeAgent OpenCode Agent 实现
type OpenCodeAgent struct {
	*baseAgent
}

// NewOpenCodeAgent 创建 OpenCode Agent 实例
func NewOpenCodeAgent(meta AgentMeta) *OpenCodeAgent {
	if meta.DisplayName == "" {
		meta.DisplayName = "OpenCode"
	}
	return &OpenCodeAgent{
		baseAgent: newBaseAgent(meta),
	}
}

// Start 启动 OpenCode 进程并完成 ACP 握手
// 进程生命周期由 Stop 管理，ctx 只约束启动与握手
// Lzm 2026-07-10
func (a *OpenCodeAgent) Start(ctx context.Context) error {
	return a.start(ctx, nil)
}

// Send 发送请求并等待完整响应
func (a *OpenCodeAgent) Send(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doSend(ctx, req)
}

// Stream 发送请求并返回流式块通道
func (a *OpenCodeAgent) Stream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doStream(ctx, req)
}

// NewSession 创建新 ACP 会话
func (a *OpenCodeAgent) NewSession(ctx context.Context) (string, error) {
	return a.doNewSession(ctx)
}

// LoadSession 加载已有会话
func (a *OpenCodeAgent) LoadSession(ctx context.Context, sessionID string) error {
	return a.doLoadSession(ctx, sessionID)
}
