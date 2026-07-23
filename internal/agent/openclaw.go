// -*- coding: utf-8 -*-
// Go 1.25+
//
// openclaw.go
// OpenClaw ACP 桥接 Agent 实现
// OpenClaw 是一个 AI 工具编排平台，通过 openclaw acp 在 stdio 上提供 ACP 服务
// 安装：npm install -g openclaw
// 或从 https://openclaw.ai 下载安装
// openclaw acp 将 ACP 请求通过 WebSocket 桥接到 OpenClaw Gateway 网关
//
// Gateway 自动管理：
//   - Start 时自动检测 Gateway（127.0.0.1:18789）是否已运行
//   - 未运行则自动启动 openclaw gateway run --port 18789，等待就绪
//   - Stop 时仅关闭 bridge 自动启动的 Gateway，不影响用户手动启动的
//
// Lzm 2026-07-14

package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/infra"
)

// OpenClawGatewayPort Gateway 默认监听端口
const OpenClawGatewayPort = 18789

// OpenClawGatewayReadyTimeout Gateway 启动等待超时
const OpenClawGatewayReadyTimeout = 30 * time.Second

// OpenClawAgent OpenClaw ACP 桥接 Agent 实现
// openclaw acp 是 ACP 到 OpenClaw Gateway 的桥接器
// 它将 ACP JSON-RPC 2.0 通过 stdio 接收，并通过 WebSocket 转发到 Gateway 会话
type OpenClawAgent struct {
	*baseAgent

	// gatewayPM 当 bridge 自动启动 Gateway 时保存其进程管理器
	gatewayPM *infra.ProcessManager
	// gatewayStartedByBridge 标记 Gateway 是否由 bridge 启动（用于 Stop 时判断是否关闭）
	gatewayStartedByBridge bool
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

// Start 启动 OpenClaw ACP 进程，自动管理 Gateway 生命周期
//
// 自动管理流程：
//  1. 检测 Gateway（127.0.0.1:18789）是否已在监听
//  2. 未监听 → 自动启动 openclaw gateway run --port 18789
//  3. 等待 Gateway 就绪（最多 30 秒）
//  4. 启动 openclaw acp 完成 ACP 握手
//
// Lzm 2026-07-14
func (a *OpenClawAgent) Start(ctx context.Context) error {
	// 1. 检查 Gateway 是否需要自动启动
	if !a.isGatewayListening() {
		slog.Info("OpenClaw Gateway 未运行，自动启动...",
			"port", OpenClawGatewayPort,
		)
		pm, err := a.startGateway(ctx)
		if err != nil {
			return fmt.Errorf("启动 OpenClaw Gateway 失败: %w", err)
		}
		a.gatewayPM = pm
		a.gatewayStartedByBridge = true

		// 2. 等待 Gateway 就绪
		if err := a.waitForGateway(ctx); err != nil {
			// 启动失败，清理 Gateway 进程
			a.stopGateway()
			return fmt.Errorf("等待 OpenClaw Gateway 就绪失败: %w", err)
		}
		slog.Info("OpenClaw Gateway 已就绪",
			"port", OpenClawGatewayPort,
			"pid", pm.PID(),
		)
	} else {
		slog.Info("OpenClaw Gateway 已运行，直接复用",
			"port", OpenClawGatewayPort,
		)
		a.gatewayStartedByBridge = false
	}

	// 3. 启动 openclaw acp（委托 baseAgent）
	return a.start(ctx, nil)
}

// Send, Stream, NewSession, LoadSession 已提升到 baseAgent 实现
// Cancel 也由 baseAgent 直接实现（委托行为一致），此处不再覆盖。
// 见 base.go Send / Stream / NewSession / LoadSession / Cancel 方法
// Lzm 2026-07-21

// Stop 终止 OpenClaw 进程
//
// 若 Gateway 由 bridge 自动启动，一并关闭它；
// 用户手动启动的 Gateway 不受影响。
//
// Lzm 2026-07-14
func (a *OpenClawAgent) Stop(ctx context.Context) error {
	// 1. 停止 openclaw acp（委托 baseAgent）
	err := a.baseAgent.Stop(ctx)

	// 2. 仅关闭 bridge 自动启动的 Gateway
	if a.gatewayStartedByBridge && a.gatewayPM != nil {
		slog.Info("关闭自动启动的 OpenClaw Gateway",
			"pid", a.gatewayPM.PID(),
		)
		a.stopGateway()
	}

	return err
}

// isGatewayListening 检测 Gateway 端口是否已被监听
// Lzm 2026-07-14
func (a *OpenClawAgent) isGatewayListening() bool {
	conn, err := net.DialTimeout("tcp",
		fmt.Sprintf("127.0.0.1:%d", OpenClawGatewayPort),
		1*time.Second,
	)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// startGateway 启动 openclaw gateway run 后台进程
// Lzm 2026-07-14
func (a *OpenClawAgent) startGateway(ctx context.Context) (*infra.ProcessManager, error) {
	pm, err := infra.StartProcess(ctx, infra.StartProcessConfig{
		Command:  a.meta.Cmd,
		Args:     []string{"gateway", "run", "--port", fmt.Sprintf("%d", OpenClawGatewayPort)},
		WorkDir:  a.meta.WorkDir,
		Env:      a.meta.Env,
		PathDirs: a.meta.PathDirs,
	})
	if err != nil {
		return nil, fmt.Errorf("启动 Gateway 进程失败: %w", err)
	}
	return pm, nil
}

// waitForGateway 等待 Gateway 端口可连接（最多 OpenClawGatewayReadyTimeout）
// Lzm 2026-07-14
func (a *OpenClawAgent) waitForGateway(ctx context.Context) error {
	deadline := time.Now().Add(OpenClawGatewayReadyTimeout)
	pollInterval := 200 * time.Millisecond

	for time.Now().Before(deadline) {
		// 检查 Gateway 进程是否已退出
		if a.gatewayPM != nil && !a.gatewayPM.IsRunning() {
			return fmt.Errorf("Gateway 进程已提前退出")
		}

		if a.isGatewayListening() {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
			// 继续轮询
		}
	}

	return fmt.Errorf("Gateway 在 %s 内未就绪", OpenClawGatewayReadyTimeout)
}

// stopGateway 关闭 bridge 自动启动的 Gateway 进程
// Lzm 2026-07-14
func (a *OpenClawAgent) stopGateway() {
	if a.gatewayPM == nil || !a.gatewayPM.IsRunning() {
		return
	}
	if err := a.gatewayPM.Stop(); err != nil {
		slog.Warn("关闭 OpenClaw Gateway 异常",
			"pid", a.gatewayPM.PID(),
			"error", err,
		)
	}
	a.gatewayPM = nil
}


