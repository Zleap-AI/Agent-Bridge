// -*- coding: utf-8 -*-
// Go 1.25+
//
// kimi.go
// Kimi Agent 实现
// 内置 ACP 支持，直接使用 kimi.exe 启动
// 需要 MOONSHOT_API_KEY 环境变量
// Windows 上需要注意 EPERM 问题（mkdir 权限），需预创建会话目录
//
// Lzm 2026-07-09

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// KimiAgent Kimi Agent 实现
type KimiAgent struct {
	*baseAgent
}

// NewKimiAgent 创建 Kimi Agent 实例
func NewKimiAgent(meta AgentMeta) *KimiAgent {
	if meta.DisplayName == "" {
		meta.DisplayName = "Kimi"
	}
	return &KimiAgent{
		baseAgent: newBaseAgent(meta),
	}
}

// Start 启动 Kimi 进程并完成 ACP 握手
// Kimi 使用内置 ACP 支持，直接启动进程即可
// 注意：必须使用父 ctx 启动进程（非 timeout ctx），否则 Start 返回后进程被杀死
// Lzm 2026-07-10
func (a *KimiAgent) Start(ctx context.Context) error {
	if a.Status() != AgentDisconnected {
		return fmt.Errorf("agent %s 已启动，当前状态: %s", a.meta.ID, a.Status())
	}

	// 预创建会话目录，避免 Kimi 的 EPERM 问题
	if err := a.prepareDirs(); err != nil {
		return fmt.Errorf("准备 Kimi 目录失败: %w", err)
	}

	// 1. 启动子进程（使用父 ctx，进程需要长期运行，不可被 Start 的 timeout 控制）
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

// prepareDirs 预创建 Kimi 可能需要的目录并授予权限（规避 Windows EPERM）
// Kimi 在 session/new 时会尝试 mkdir ~/.kimi-code/sessions/wd_xxx/，
// Windows 上可能因权限不足失败，需要 icacls 预授权
// Lzm 2026-07-10
func (a *KimiAgent) prepareDirs() error {
	home, _ := os.UserHomeDir()
	sessionDir := filepath.Join(home, ".kimi-code", "sessions")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return err
	}

	// Windows 上使用 icacls 授予当前用户完全控制权限
	currentUser, err := user.Current()
	if err == nil {
		// 非阻塞执行，失败不影响启动
		cmd := exec.Command("icacls", sessionDir,
			"/grant", currentUser.Username+":(OI)(CI)F",
			"/T", "/Q")
		_ = cmd.Run()
	}

	return nil
}

// Send 发送请求并等待完整响应
func (a *KimiAgent) Send(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doSend(ctx, req)
}

// Stream 发送请求并返回流式块通道
func (a *KimiAgent) Stream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doStream(ctx, req)
}

// NewSession 创建新 ACP 会话
func (a *KimiAgent) NewSession(ctx context.Context) (string, error) {
	return a.doNewSession(ctx)
}

// LoadSession 加载已有会话
func (a *KimiAgent) LoadSession(ctx context.Context, sessionID string) error {
	return a.doLoadSession(ctx, sessionID)
}
