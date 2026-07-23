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
// 进程生命周期由 Stop 管理，ctx 只约束启动与握手
// Lzm 2026-07-10
func (a *KimiAgent) Start(ctx context.Context) error {
	return a.start(ctx, func() error {
		if err := a.prepareDirs(); err != nil {
			return fmt.Errorf("准备 Kimi 目录失败: %w", err)
		}
		return nil
	})
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

// Send, Stream, NewSession, LoadSession 已提升到 baseAgent 实现
// 见 base.go Send / Stream / NewSession / LoadSession 方法
// Lzm 2026-07-21
