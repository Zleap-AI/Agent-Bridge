// -*- coding: utf-8 -*-
// Go 1.25+
//
// cursor.go
// Cursor CLI Agent 实现
// Cursor CLI 内置 ACP 支持，通过 agent acp 子命令启动
// 安装：curl https://cursor.com/install -fsS | bash（macOS/Linux）
//       irm 'https://cursor.com/install?win32=true' | iex（Windows）
// 需要 CURSOR_API_KEY 环境变量，或先运行 agent login 完成认证
//
// Lzm 2026-07-11

package agent

import (
	"context"
)

// CursorAgent Cursor CLI Agent 实现
type CursorAgent struct {
	*baseAgent
}

// NewCursorAgent 创建 Cursor CLI Agent 实例
func NewCursorAgent(meta AgentMeta) *CursorAgent {
	if meta.DisplayName == "" {
		meta.DisplayName = "Cursor"
	}
	return &CursorAgent{
		baseAgent: newBaseAgent(meta),
	}
}

// Start 启动 agent acp 进程并完成 ACP 握手
// agent acp 在 stdio 上以 JSON-RPC 2.0 格式提供 ACP 服务
// 进程生命周期由 Stop 管理，ctx 只约束启动与握手
// Lzm 2026-07-11
func (a *CursorAgent) Start(ctx context.Context) error {
	return a.start(ctx, nil)
}

// Send, Stream, NewSession, LoadSession 已提升到 baseAgent 实现
// 见 base.go Send / Stream / NewSession / LoadSession 方法
// Lzm 2026-07-21
