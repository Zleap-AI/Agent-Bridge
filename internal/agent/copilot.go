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

// Send, Stream, NewSession, LoadSession 已提升到 baseAgent 实现
// 见 base.go Send / Stream / NewSession / LoadSession 方法
// Lzm 2026-07-21
