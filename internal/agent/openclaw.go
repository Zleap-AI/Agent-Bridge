// -*- coding: utf-8 -*-
// Go 1.25+
//
// openclaw.go
// OpenClaw ACP 桥接 Agent 实现
// OpenClaw 是一个 AI 工具编排平台，通过 openclaw acp 在 stdio 上提供 ACP 服务
// 安装：npm install -g openclaw
// 或从 https://openclaw.ai 下载安装
// openclaw acp 将 ACP 请求通过 WebSocket 桥接到 OpenClaw Gateway 网关
// 前置条件：OpenClaw Gateway 必须运行中（本地或远程），且已配置认证
//
// Lzm 2026-07-11

package agent

import (
	"context"
	"fmt"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// OpenClawAgent OpenClaw ACP 桥接 Agent 实现
// openclaw acp 是 ACP 到 OpenClaw Gateway 的桥接器
// 它将 ACP JSON-RPC 2.0 通过 stdio 接收，并通过 WebSocket 转发到 Gateway 会话
type OpenClawAgent struct {
	*baseAgent
}

// NewOpenClawAgent 创建 OpenClaw ACP 桥接 Agent 实例
func NewOpenClawAgent(meta AgentMeta) *OpenClawAgent {
	if meta.DisplayName == "" {
		meta.DisplayName = "OpenClaw"
	}
	return &OpenClawAgent{
		baseAgent: newBaseAgent(meta),
	}
}

// Start 启动 openclaw acp 进程并完成 ACP 握手
// openclaw acp 在 stdio 上以 JSON-RPC 2.0 格式提供 ACP 服务
// 注意：必须使用父 ctx 启动进程（非 timeout ctx），否则 Start 返回后进程被杀死
// Lzm 2026-07-11
func (a *OpenClawAgent) Start(ctx context.Context) error {
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
func (a *OpenClawAgent) Send(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doSend(ctx, req)
}

// Stream 发送请求并返回流式块通道
func (a *OpenClawAgent) Stream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doStream(ctx, req)
}

// NewSession 创建新 ACP 会话
func (a *OpenClawAgent) NewSession(ctx context.Context) (string, error) {
	return a.doNewSession(ctx)
}

// LoadSession 加载已有会话
func (a *OpenClawAgent) LoadSession(ctx context.Context, sessionID string) error {
	return a.doLoadSession(ctx, sessionID)
}
