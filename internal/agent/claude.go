// -*- coding: utf-8 -*-
// Go 1.25+
//
// claude.go
// Claude Code Agent 实现
// 通过 claude-agent-acp.cmd 启动，支持 ACP 协议
// 从 ~/.claude/settings.json 读取环境变量配置
//
// Lzm 2026-07-09

package agent

import (
	"context"
	"fmt"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// ClaudeCodeAgent Claude Code Agent 实现
type ClaudeCodeAgent struct {
	*baseAgent
}

// NewClaudeCodeAgent 创建 Claude Code Agent 实例
func NewClaudeCodeAgent(meta AgentMeta) *ClaudeCodeAgent {
	if meta.DisplayName == "" {
		meta.DisplayName = "Claude Code"
	}
	return &ClaudeCodeAgent{
		baseAgent: newBaseAgent(meta),
	}
}

// Start 启动 Claude Code 进程并完成 ACP 握手
// 进程生命周期由 Stop 管理，ctx 只约束启动与握手
// Lzm 2026-07-10
func (a *ClaudeCodeAgent) Start(ctx context.Context) error {
	return a.start(ctx, nil)
}

// Send 发送请求并等待完整响应
func (a *ClaudeCodeAgent) Send(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doSend(ctx, req)
}

// Stream 发送请求并返回流式块通道
func (a *ClaudeCodeAgent) Stream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doStream(ctx, req)
}

// NewSession 创建新 ACP 会话
func (a *ClaudeCodeAgent) NewSession(ctx context.Context) (string, error) {
	return a.doNewSession(ctx)
}

// LoadSession 加载已有会话
func (a *ClaudeCodeAgent) LoadSession(ctx context.Context, sessionID string) error {
	return a.doLoadSession(ctx, sessionID)
}
