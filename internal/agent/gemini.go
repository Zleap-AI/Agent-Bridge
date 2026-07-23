// -*- coding: utf-8 -*-
// Go 1.25+
//
// gemini.go
// Gemini CLI Agent 实现
// Google Gemini CLI 内置 ACP 支持，通过 --experimental-acp 标志启动
// 安装：npm install -g @google/gemini-cli
// 需要 GEMINI_API_KEY 环境变量
//
// Lzm 2026-07-11

package agent

import (
	"context"
)

// GeminiAgent Google Gemini CLI Agent 实现
type GeminiAgent struct {
	*baseAgent
}

// NewGeminiAgent 创建 Gemini CLI Agent 实例
func NewGeminiAgent(meta AgentMeta) *GeminiAgent {
	if meta.DisplayName == "" {
		meta.DisplayName = "Gemini CLI"
	}
	return &GeminiAgent{
		baseAgent: newBaseAgent(meta),
	}
}

// Start 启动 Gemini CLI 进程并完成 ACP 握手
// Gemini CLI 使用 --experimental-acp 标志启动 ACP 协议模式
// 进程生命周期由 Stop 管理，ctx 只约束启动与握手
// Lzm 2026-07-11
func (a *GeminiAgent) Start(ctx context.Context) error {
	return a.start(ctx, nil)
}

// Send, Stream, NewSession, LoadSession 已提升到 baseAgent 实现
// 见 base.go Send / Stream / NewSession / LoadSession 方法
// Lzm 2026-07-21
