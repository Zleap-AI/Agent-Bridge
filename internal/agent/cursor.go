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
	"fmt"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
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
// 注意：必须使用父 ctx 启动进程（非 timeout ctx），否则 Start 返回后进程被杀死
// Lzm 2026-07-11
func (a *CursorAgent) Start(ctx context.Context) error {
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
func (a *CursorAgent) Send(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doSend(ctx, req)
}

// Stream 发送请求并返回流式块通道
func (a *CursorAgent) Stream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doStream(ctx, req)
}

// NewSession 创建新 ACP 会话
func (a *CursorAgent) NewSession(ctx context.Context) (string, error) {
	return a.doNewSession(ctx)
}

// LoadSession 加载已有会话
func (a *CursorAgent) LoadSession(ctx context.Context, sessionID string) error {
	return a.doLoadSession(ctx, sessionID)
}
