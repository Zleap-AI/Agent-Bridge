// -*- coding: utf-8 -*-
// Go 1.25+
//
// copilot.go
// GitHub Copilot CLI Agent 实现
// GitHub Copilot CLI 内置 ACP 支持，通过 --acp 标志启动
// 安装：npm install -g @github/copilot-cli
// 无需 API Key，使用 GitHub 账号登录认证
//
// Lzm 2026-07-11

package agent

import (
	"context"
	"fmt"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// CopilotAgent GitHub Copilot CLI Agent 实现
type CopilotAgent struct {
	*baseAgent
}

// NewCopilotAgent 创建 GitHub Copilot CLI Agent 实例
func NewCopilotAgent(meta AgentMeta) *CopilotAgent {
	if meta.DisplayName == "" {
		meta.DisplayName = "GitHub Copilot"
	}
	return &CopilotAgent{
		baseAgent: newBaseAgent(meta),
	}
}

// Start 启动 Copilot CLI 进程并完成 ACP 握手
// Copilot CLI 使用 --acp 标志启动 ACP 协议模式
// 进程生命周期由 Stop 管理，ctx 只约束启动与握手
// Lzm 2026-07-11
func (a *CopilotAgent) Start(ctx context.Context) error {
	return a.start(ctx, nil)
}

// Send 发送请求并等待完整响应
func (a *CopilotAgent) Send(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doSend(ctx, req)
}

// Stream 发送请求并返回流式块通道
func (a *CopilotAgent) Stream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doStream(ctx, req)
}

// NewSession 创建新 ACP 会话
func (a *CopilotAgent) NewSession(ctx context.Context) (string, error) {
	return a.doNewSession(ctx)
}

// LoadSession 加载已有会话
func (a *CopilotAgent) LoadSession(ctx context.Context, sessionID string) error {
	return a.doLoadSession(ctx, sessionID)
}
