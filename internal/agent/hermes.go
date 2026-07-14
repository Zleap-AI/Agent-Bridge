// -*- coding: utf-8 -*-
// Go 1.25+
//
// hermes.go
// Hermes Agent ACP 适配器实现
// 通过 stdio 运行 hermes acp，提供完整的 ACP 代理能力
// 需要 pip install -e '.[acp]' 安装 ACP 附加组件
// 配置：~/.hermes/.env、~/.hermes/config.yaml
//
// Lzm 2026-07-13

package agent

import (
	"context"
	"fmt"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// HermesAgent Hermes Agent ACP 适配器
type HermesAgent struct {
	*baseAgent
}

// NewHermesAgent 创建 Hermes Agent 实例
func NewHermesAgent(meta AgentMeta) *HermesAgent {
	if meta.DisplayName == "" {
		meta.DisplayName = "Hermes Agent"
	}
	return &HermesAgent{
		baseAgent: newBaseAgent(meta),
	}
}

// Start 启动 Hermes ACP 进程并完成握手
// 注意：必须使用父 ctx 启动进程（非 timeout ctx），否则 Start 返回后进程被杀死
// Lzm 2026-07-13
func (a *HermesAgent) Start(ctx context.Context) error {
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
func (a *HermesAgent) Send(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doSend(ctx, req)
}

// Stream 发送请求并返回流式块通道
func (a *HermesAgent) Stream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doStream(ctx, req)
}

// NewSession 创建新 ACP 会话
func (a *HermesAgent) NewSession(ctx context.Context) (string, error) {
	return a.doNewSession(ctx)
}

// LoadSession 加载已有会话
func (a *HermesAgent) LoadSession(ctx context.Context, sessionID string) error {
	return a.doLoadSession(ctx, sessionID)
}
