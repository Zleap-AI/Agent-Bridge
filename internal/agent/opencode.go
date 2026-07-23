// -*- coding: utf-8 -*-
// Go 1.25+
//
// opencode.go
// OpenCode Agent 实现
// 内置 ACP 支持，直接使用 opencode.cmd 启动
// 需要 OPENAI_API_KEY 环境变量
//
// 跨平台差异：
//   - Windows: opencode 通过 stdin/stdout 实现 ACP（与 bridge 子进程管道通信）
//   - macOS:   opencode acp 启动 WebSocket 服务端，bridge 连接 WebSocket 实现 ACP
//   - Linux:   暂同 Windows（stdin/stdout ACP）
//
// Lzm 2026-07-09

package agent

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/infra"
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
// 进程生命周期由 Stop 管理，ctx 只约束启动与握手
// Lzm 2026-07-10
func (a *OpenCodeAgent) Start(ctx context.Context) error {
	// macOS：opencode acp 启动 WebSocket 服务端，走 WebSocket ACP
	if runtime.GOOS == "darwin" {
		return a.startMacOS(ctx)
	}

	// Windows/Linux：标准 stdin/stdout ACP，委托 baseAgent.start 管理生命周期
	return a.start(ctx, nil)
}

// startMacOS macOS 用 WebSocket ACP 模式
// opencode acp 启动的是 WebSocket 服务端，bridge 通过 WebSocket 连接进行 ACP 通信
// Lzm 2026-07-14
func (a *OpenCodeAgent) startMacOS(ctx context.Context) error {
	// 1. 查找空闲端口
	port, err := findFreePort()
	if err != nil {
		return fmt.Errorf("查找空闲端口失败: %w", err)
	}

	// 2. 启动 opencode acp --port {port} 作为子进程
	pm, err := infra.StartProcess(ctx, infra.StartProcessConfig{
		Command: a.meta.Cmd,
		Args:    []string{"acp", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port)},
		WorkDir: a.meta.WorkDir,
		Env:     a.meta.Env,
	})
	if err != nil {
		return fmt.Errorf("启动 opencode ACP 服务端失败: %w", err)
	}

	// 3. 等待服务端就绪
	select {
	case <-time.After(800 * time.Millisecond):
		// 等待 ACP 服务端启动
	case <-ctx.Done():
		pm.Stop()
		return fmt.Errorf("等待 opencode ACP 服务端启动超时: %w", ctx.Err())
	}

	// 4. 连接 WebSocket ACP 适配器
	adapter, err := newWSACPAdapter(ctx, pm, port)
	if err != nil {
		pm.Stop()
		return fmt.Errorf("连接 opencode WebSocket ACP 失败: %w", err)
	}

	// 5. 设置 WS 适配器，委托 baseAgent.start 完成后续生命周期管理（握手 + 状态切换）
	a.wsAdapter = adapter

	// 6. baseAgent.start 会检测 wsAdapter != nil 并走 WebSocket 模式
	return a.start(ctx, nil)
}

// Send, Stream, NewSession, LoadSession 已提升到 baseAgent 实现
// Cancel 也由 baseAgent 直接实现（委托行为一致），此处不再覆盖。
// 见 base.go Send / Stream / NewSession / LoadSession / Cancel 方法
// Lzm 2026-07-21
