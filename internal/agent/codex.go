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

// Send, Stream, NewSession, LoadSession 已提升到 baseAgent 实现
// 见 base.go Send / Stream / NewSession / LoadSession 方法
// Lzm 2026-07-21
