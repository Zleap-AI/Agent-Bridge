// -*- coding: utf-8 -*-
// Go 1.25+
//
// router_exec.go
// 消息路由器 — Shell 命令执行处理
// 负责 session/exec 方法的白名单/黑名单安全检查与命令执行
//
// Lzm 2026-07-22

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// Shell 命令白名单 — 只允许执行安全的命令
// 参照 Coze 的安全策略，限制交互式/危险命令
// Lzm 2026-07-22
var shellCommandWhitelist = map[string]bool{
	// 文件操作
	"dir":        true,
	"ls":         true,
	"type":       true,
	"cat":        true,
	"find":       true,
	"where":      true,
	"which":      true,
	"head":       true,
	"tail":       true,
	"more":       true,
	"less":       true,
	// 脚本执行
	"python":     true,
	"python3":    true,
	"py":         true,
	"node":       true,
	"npm":        true,
	"npx":        true,
	"go":         true,
	"rustc":      true,
	"cargo":      true,
	"dotnet":     true,
	// Git 操作
	"git":        true,
	// 系统工具
	"echo":       true,
	"pwd":        true,
	"cd":         true,
	"mkdir":      true,
	"copy":       true,
	"move":       true,
	"del":        true,
	"rm":         true,
	"chmod":      true,
	"curl":       true,
	"wget":       true,
	"powershell": true,
	"pwsh":       true,
	"cmd":        true,
}

// 禁止的命令 — 覆盖白名单中的危险子命令
// Lzm 2026-07-22
var shellCommandBlacklist = []string{
	"rm -rf /", "rm -rf /*", "rm -rf ~",
	"format", "fdisk", "dd if=",
	"shutdown", "reboot", "init",
	"taskkill /f", "kill -9",
}

// handleSessionExec 处理 shell 命令执行请求 (session/exec)。
// 通过白名单 + 黑名单双重安全检查，防止危险命令执行。
// 执行结果通过流式响应返回 stdout/stderr。
// Lzm 2026-07-22
func (r *RequestRouter) handleSessionExec(ctx context.Context, msg *protocol.ANPMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var execParams struct {
		AgentID string `json:"agent_id"`
		Command string `json:"command"`
		Timeout int    `json:"timeout,omitempty"` // 命令超时（秒），默认 30
	}
	if err := json.Unmarshal(msg.Params, &execParams); err != nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析 session/exec 参数失败: %v", err))
	}
	if execParams.AgentID == "" {
		return protocol.NewErrorResponse(msg.ID, -32602, "缺少 agent_id")
	}
	if execParams.Command == "" {
		return protocol.NewErrorResponse(msg.ID, -32602, "缺少 command")
	}

	// 安全检查：黑名单优先
	cmdLower := strings.ToLower(execParams.Command)
	for _, blacklisted := range shellCommandBlacklist {
		if strings.Contains(cmdLower, blacklisted) {
			slog.Warn("禁止执行危险命令",
				"agent", execParams.AgentID,
				"command", truncateString(execParams.Command, 100),
				"matched_rule", blacklisted,
			)
			return protocol.NewErrorResponse(msg.ID, -31010,
				fmt.Sprintf("禁止执行危险命令（匹配规则: %s）", blacklisted))
		}
	}

	// 提取命令名检查白名单
	cmdName := strings.Fields(execParams.Command)[0]
	// 去掉路径前缀
	cmdBase := strings.TrimSuffix(strings.ToLower(filepath.Base(cmdName)), ".exe")
	if !shellCommandWhitelist[cmdBase] {
		slog.Warn("命令不在白名单中",
			"agent", execParams.AgentID,
			"command", truncateString(execParams.Command, 100),
			"cmd_base", cmdBase,
		)
		return protocol.NewErrorResponse(msg.ID, -31010,
			fmt.Sprintf("命令 '%s' 不在允许执行的命令列表中", cmdName))
	}

	// 获取或创建 Agent 会话（用于权限检查）
	a := r.registry.Get(execParams.AgentID)
	if a == nil {
		return protocol.NewErrorResponse(msg.ID, -31001,
			fmt.Sprintf("未知 Agent: %s", execParams.AgentID))
	}

	slog.Info("执行 Shell 命令",
		"agent", execParams.AgentID,
		"command", truncateString(execParams.Command, 200),
	)

	timeout := execParams.Timeout
	if timeout <= 0 {
		timeout = 30
	}
	execCtx, execCancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer execCancel()

	cmd := shellCommandContext(execCtx, execParams.Command)
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return protocol.NewErrorResponse(msg.ID, -31011,
				fmt.Sprintf("命令执行超时（%d秒）", timeout))
		}
		slog.Warn("命令执行返回错误",
			"agent", execParams.AgentID,
			"error", err,
			"output", truncateString(outputStr, 200),
		)
		// 即使有错误，也返回部分输出
		result, _ := json.Marshal(map[string]interface{}{
			"stdout":   outputStr,
			"stderr":   err.Error(),
			"exitCode": 1,
		})
		return protocol.NewResultResponse(msg.ID, result)
	}

	result, _ := json.Marshal(map[string]interface{}{
		"stdout":   outputStr,
		"stderr":   "",
		"exitCode": 0,
	})
	return protocol.NewResultResponse(msg.ID, result)
}
