// -*- coding: utf-8 -*-
// Go 1.25+
//
// pi.go
// pi coding agent (pi-acp) 实现
// pi 是一个开源的 AI 编码 Agent，pi-acp 是其 ACP 协议适配器
// 安装：npm install -g @earendil-works/pi-coding-agent （pi 本体）
//       npm install -g pi-acp                    （ACP 适配器）
// pi-acp 内部启动 pi --mode rpc 并桥接 ACP JSON-RPC 2.0 over stdio
//
// Lzm 2026-07-11

package agent

import (
	"context"
)

// PiAgent pi coding agent 实现（通过 pi-acp 适配器）
type PiAgent struct {
	*baseAgent
}

// NewPiAgent 创建 pi coding agent 实例
func NewPiAgent(meta AgentMeta) *PiAgent {
	if meta.DisplayName == "" {
		meta.DisplayName = "pi"
	}
	return &PiAgent{
		baseAgent: newBaseAgent(meta),
	}
}

// Start 启动 pi-acp 进程并完成 ACP 握手
// pi-acp 内部自动启动 pi --mode rpc，对外暴露 ACP 协议
// 进程生命周期由 Stop 管理，ctx 只约束启动与握手
// Lzm 2026-07-11
func (a *PiAgent) Start(ctx context.Context) error {
	return a.start(ctx, nil)
}

// Send, Stream, NewSession, LoadSession 已提升到 baseAgent 实现
// 见 base.go Send / Stream / NewSession / LoadSession 方法
// Lzm 2026-07-21
