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
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/infra"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
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

	// macOS：opencode acp 启动 WebSocket 服务端，走 WebSocket ACP
	if runtime.GOOS == "darwin" {
		return a.startMacOS(ctx)
	}

	// Windows/Linux：标准 stdin/stdout ACP
	return a.startStdio(ctx)
}

// startStdio 标准 stdin/stdout ACP 模式（Windows / Linux）
// Lzm 2026-07-10
func (a *OpenCodeAgent) startStdio(ctx context.Context) error {
	// 预检：快速验证 ACP 子命令是否存在
	diagCtx, diagCancel := context.WithTimeout(ctx, 3*time.Second)
	var diagOut bytes.Buffer
	diagCmd := exec.CommandContext(diagCtx, a.meta.Cmd, append(a.meta.Args, "--help")...)
	diagCmd.Stdout = &diagOut
	diagCmd.Stderr = &diagOut
	_ = diagCmd.Run()
	diagCancel()
	if diagOut.Len() > 0 {
		slog.Debug("OpenCode ACP 诊断输出",
			"agent", a.meta.ID,
			"output", diagOut.String(),
		)
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

	// 5. 设置 WS 适配器
	a.wsAdapter = adapter

	// 6. 启动进程（将使用 WS 适配器）
	if err := a.startProcess(ctx); err != nil {
		adapter.Close()
		return err
	}

	// 7. 启动后台读取协程
	a.startReadLoop(ctx)

	// 8. ACP 握手（握手阶段使用 timeout）
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
