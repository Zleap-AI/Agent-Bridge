// -*- coding: utf-8 -*-
// Go 1.25+
//
// glm.go
// GLM ACP Agent 实现
// 基于智谱 AI（Z.AI）GLM Coding Plan 模型的 ACP Agent
// 安装：npm install -g glm-acp-agent
// 或通过 npx glm-acp-agent@latest 直接运行
// 需要 Z_AI_API_KEY 环境变量，或先运行 glm-acp-agent --setup 完成认证
// 支持的模型：glm-5.1, glm-5-turbo, glm-4.7, glm-4.5-air 等
//
// Lzm 2026-07-11

package agent

import (
	"context"
)

// GlmAgent GLM ACP Agent 实现
type GlmAgent struct {
	*baseAgent
}

// NewGlmAgent 创建 GLM ACP Agent 实例
func NewGlmAgent(meta AgentMeta) *GlmAgent {
	if meta.DisplayName == "" {
		meta.DisplayName = "GLM Agent"
	}
	return &GlmAgent{
		baseAgent: newBaseAgent(meta),
	}
}

// Start 启动 glm-acp-agent 进程并完成 ACP 握手
// glm-acp-agent 在 stdio 上以 JSON-RPC 2.0 格式提供 ACP 服务
// 进程生命周期由 Stop 管理，ctx 只约束启动与握手
// Lzm 2026-07-11
func (a *GlmAgent) Start(ctx context.Context) error {
	return a.start(ctx, nil)
}

// Send, Stream, NewSession, LoadSession 已提升到 baseAgent 实现
// 见 base.go Send / Stream / NewSession / LoadSession 方法
// Lzm 2026-07-21
