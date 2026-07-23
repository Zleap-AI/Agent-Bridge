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

// Send, Stream, NewSession, LoadSession 已提升到 baseAgent 实现
// 见 base.go Send / Stream / NewSession / LoadSession 方法
// Lzm 2026-07-21
