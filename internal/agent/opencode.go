// -*- coding: utf-8 -*-
// Go 1.26+
//
// opencode.go
// OpenCode Agent 实现
// 内置 ACP 支持，直接使用 opencode.cmd 启动
// 需要 OPENAI_API_KEY 环境变量
//
// Lzm 2026-07-09

package agent

import (
	"context"
	"fmt"

	"github.com/zleap/bridge/internal"
	"github.com/zleap/bridge/internal/protocol"
)

// OpenCodeAgent OpenCode Agent 实现
type OpenCodeAgent struct {
	*baseAgent
}

// NewOpenCodeAgent 创建 OpenCode Agent 实例
func NewOpenCodeAgent(meta AgentMeta) *OpenCodeAgent {
	if meta.DisplayName == "" {
		meta.DisplayName = "OpenCode"
	}
	return &OpenCodeAgent{
		baseAgent: newBaseAgent(meta),
	}
}

// Start 启动 OpenCode 进程并完成 ACP 握手
// 注意：必须使用父 ctx 启动进程（非 timeout ctx），否则 Start 返回后进程被杀死
// Lzm 2026-07-10
func (a *OpenCodeAgent) Start(ctx context.Context) error {
	if a.Status() != AgentDisconnected {
		return fmt.Errorf("agent %s 已启动，当前状态: %s", a.meta.ID, a.Status())
	}

	// 1. 启动子进程（使用父 ctx，进程需要长期运行）
	if err := a.startProcess(ctx); err != nil {
		return err
	}

	// 2. 启动后台读取协程
	a.startReadLoop(ctx)

	// 3. ACP 握手（握手阶段使用 timeout）
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
func (a *OpenCodeAgent) Send(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doSend(ctx, req)
}

// Stream 发送请求并返回流式块通道
func (a *OpenCodeAgent) Stream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doStream(ctx, req)
}

// NewSession 创建新 ACP 会话
func (a *OpenCodeAgent) NewSession(ctx context.Context) (string, error) {
	return a.doNewSession(ctx)
}

// LoadSession 加载已有会话
func (a *OpenCodeAgent) LoadSession(ctx context.Context, sessionID string) error {
	return a.doLoadSession(ctx, sessionID)
}
