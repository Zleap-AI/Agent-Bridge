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
	"fmt"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
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
// 注意：必须使用父 ctx 启动进程（非 timeout ctx），否则 Start 返回后进程被杀死
// Lzm 2026-07-11
func (a *GeminiAgent) Start(ctx context.Context) error {
	if a.Status() != AgentDisconnected {
		return fmt.Errorf("agent %s 已启动，当前状态: %s", a.meta.ID, a.Status())
	}

	// 1. 启动子进程（使用父 ctx，进程需要长期运行）
	if err := a.startProcess(ctx); err != nil {
		return err
	}

	// 2. 启动后台读取协程
	a.startReadLoop(ctx)

	// 3. ACP 握手（握手阶段使用 timeout，防止卡死）
	startCtx, cancel := context.WithTimeout(ctx, a.meta.StartupTimeout)
	defer cancel()
	if err := a.doHandshake(startCtx); err != nil {
		a.Stop(ctx)
		return err
	}

	a.setStatus(AgentIdle)
	return nil
}

// Send 发送请求并等待完整响应
func (a *GeminiAgent) Send(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doSend(ctx, req)
}

// Stream 发送请求并返回流式块通道
func (a *GeminiAgent) Stream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doStream(ctx, req)
}

// NewSession 创建新 ACP 会话
func (a *GeminiAgent) NewSession(ctx context.Context) (string, error) {
	return a.doNewSession(ctx)
}

// LoadSession 加载已有会话
func (a *GeminiAgent) LoadSession(ctx context.Context, sessionID string) error {
	return a.doLoadSession(ctx, sessionID)
}
