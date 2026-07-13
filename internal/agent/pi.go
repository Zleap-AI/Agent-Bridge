// -*- coding: utf-8 -*-
// Go 1.26+
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
	"fmt"

	"github.com/zleap/bridge/internal"
	"github.com/zleap/bridge/internal/protocol"
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
// 注意：必须使用父 ctx 启动进程（非 timeout ctx），否则 Start 返回后进程被杀死
// Lzm 2026-07-11
func (a *PiAgent) Start(ctx context.Context) error {
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
func (a *PiAgent) Send(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doSend(ctx, req)
}

// Stream 发送请求并返回流式块通道
func (a *PiAgent) Stream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doStream(ctx, req)
}

// NewSession 创建新 ACP 会话
func (a *PiAgent) NewSession(ctx context.Context) (string, error) {
	return a.doNewSession(ctx)
}

// LoadSession 加载已有会话
func (a *PiAgent) LoadSession(ctx context.Context, sessionID string) error {
	return a.doLoadSession(ctx, sessionID)
}
