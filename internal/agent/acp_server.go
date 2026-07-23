// -*- coding: utf-8 -*-
// Go 1.25+
//
// acp_server.go
// ACP 服务端请求处理器 — 处理 Agent→Client 的 ACP 请求
//
// ACP 协议是双向的：Bridge 不仅发送请求给 Agent，Agent 在执行任务时
// 也会向 Bridge 发送请求（如读文件、写文件、执行命令）。
// 本文件实现这些服务端方法的处理逻辑。
//
// 支持的 Agent→Client 方法：
//   - fs/read_text_file       读取文本文件
//   - fs/write_text_file      写入/更新文本文件
//   - terminal/create         创建终端执行命令
//   - terminal/output         获取终端输出
//   - terminal/wait_for_exit  等待命令结束
//   - terminal/write_stdin    向终端写入输入（如发送命令）
//   - terminal/kill           终止命令
//   - terminal/release        释放终端资源
//
// Lzm 2026-07-20

package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// handleACPRequest 分发并处理 Agent→Client 的 ACP 请求。
// 返回 true 表示消息已被处理（已向 Agent 发送响应），调用方应继续等待。
// 返回 false 表示无法识别的方法，调用方需自行处理。
//
// 此方法必须在 doSend/doStream 的读取循环中被调用，拦截那些 ID
// 不匹配但带有 method 的 ACP 消息（即 Agent 向 Client 发起的请求）。
// Lzm 2026-07-20
func (a *baseAgent) handleACPRequest(run *agentRuntime, msg *protocol.ACPMessage) bool {
	if msg.Method == "" || !msg.HasID() {
		return false
	}

	switch msg.Method {
	case "fs/read_text_file":
		return a.handleFSReadTextFile(run, msg)
	case "fs/write_text_file":
		return a.handleFSWriteTextFile(run, msg)
	case "terminal/create":
		return a.handleTerminalCreate(run, msg)
	case "terminal/output":
		return a.handleTerminalOutput(run, msg)
	case "terminal/watch_for_exit":
		return a.handleTerminalWaitForExit(run, msg)
	case "terminal/write_stdin":
		return a.handleTerminalWriteStdin(run, msg)
	case "terminal/kill":
		return a.handleTerminalKill(run, msg)
	case "terminal/release":
		return a.handleTerminalRelease(run, msg)
	case "session/create_elicitation":
		return a.handleElicitationCreate(run, msg)
	default:
		slog.Warn("[ACP_SERVER] 不支持的 ACP 方法",
			"agent", a.meta.ID,
			"method", msg.Method,
		)
		return false
	}
}

// --- 文件系统方法 ---

// fsReadTextFileParams fs/read_text_file 请求参数
// Lzm 2026-07-20
type fsReadTextFileParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Line      *int   `json:"line,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
}

// handleFSReadTextFile 处理 fs/read_text_file 请求。
// 读取指定路径的文本文件内容，支持可选的起始行号和行数限制。
// Lzm 2026-07-20
func (a *baseAgent) handleFSReadTextFile(run *agentRuntime, msg *protocol.ACPMessage) bool {
	var params fsReadTextFileParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("参数解析失败: %v", err))
		return true
	}

	// 路径必须为绝对路径
	if !filepath.IsAbs(params.Path) {
		a.sendACPError(run, msg.ID, -32602, "路径必须为绝对路径")
		return true
	}

	slog.Debug("ACP fs/read_text_file",
		"agent", a.meta.ID,
		"path", params.Path,
		"line", params.Line,
		"limit", params.Limit,
	)

	// 读取文件内容
	data, err := os.ReadFile(params.Path)
	if err != nil {
		if os.IsNotExist(err) {
			a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("文件不存在: %s", params.Path))
		} else {
			a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("读取文件失败: %v", err))
		}
		return true
	}

	content := string(data)

	// 支持行范围截取（使用标准库 strings.Split/Join 替代自定义 splitLines/joinLines）
	if params.Line != nil || params.Limit != nil {
		lines := strings.Split(content, "\n")
		// 去除尾部空行（文件末尾换行符导致的空字符串）
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		startLine := 1
		if params.Line != nil && *params.Line > 0 {
			startLine = *params.Line
		}
		endLine := len(lines)
		if params.Limit != nil && *params.Limit > 0 {
			endLine = startLine + *params.Limit - 1
			if endLine > len(lines) {
				endLine = len(lines)
			}
		}
		if startLine <= len(lines) {
			content = strings.Join(lines[startLine-1:endLine], "\n")
		} else {
			content = ""
		}
	}

	// 构建响应
	result := map[string]string{"content": content}
	response := &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      msg.ID,
	}
	respData, _ := json.Marshal(result)
	response.Result = respData

	if err := run.writer.WriteMessage(response); err != nil {
		slog.Warn("发送 fs/read_text_file 响应失败",
			"agent", a.meta.ID,
			"error", err,
		)
	}
	return true
}

// fsWriteTextFileParams fs/write_text_file 请求参数
// Lzm 2026-07-20
type fsWriteTextFileParams struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

// handleFSWriteTextFile 处理 fs/write_text_file 请求。
// 写入内容到指定路径的文本文件，文件不存在时自动创建。
// Lzm 2026-07-20
func (a *baseAgent) handleFSWriteTextFile(run *agentRuntime, msg *protocol.ACPMessage) bool {
	var params fsWriteTextFileParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("参数解析失败: %v", err))
		return true
	}

	// 路径必须为绝对路径
	if !filepath.IsAbs(params.Path) {
		a.sendACPError(run, msg.ID, -32602, "路径必须为绝对路径")
		return true
	}

	slog.Debug("ACP fs/write_text_file",
		"agent", a.meta.ID,
		"path", params.Path,
		"content_len", len(params.Content),
	)

	// 确保父目录存在
	parentDir := filepath.Dir(params.Path)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("创建目录失败: %v", err))
		return true
	}

	// 写入文件
	if err := os.WriteFile(params.Path, []byte(params.Content), 0644); err != nil {
		a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("写入文件失败: %v", err))
		return true
	}

	// 成功响应（空结果）
	response := &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  json.RawMessage(`null`),
	}
	if err := run.writer.WriteMessage(response); err != nil {
		slog.Warn("发送 fs/write_text_file 响应失败",
			"agent", a.meta.ID,
			"error", err,
		)
	}
	return true
}

// --- 终端方法 ---

// terminalCreateParams terminal/create 请求参数
// ACP V1 规范定义：command 必填，args/env/cwd/outputByteLimit 可选
// 当输出超出 outputByteLimit 时，Client 从开头截断以保持在此限制内
// Lzm 2026-07-20
type terminalCreateParams struct {
	SessionID       string           `json:"sessionId"`
	Command         string           `json:"command"`
	Args            []string         `json:"args,omitempty"`
	Env             []envVar         `json:"env,omitempty"`
	Cwd             string           `json:"cwd,omitempty"`
	OutputByteLimit int              `json:"outputByteLimit,omitempty"`
}

// envVar 环境变量键值对
// Lzm 2026-07-20
type envVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// terminalCreateResult terminal/create 响应
// Lzm 2026-07-20
type terminalCreateResult struct {
	TerminalID string `json:"terminalId"`
}

// handleTerminalCreate 处理 terminal/create 请求。
// 创建新终端并启动指定命令，返回终端 ID。
// Lzm 2026-07-20
func (a *baseAgent) handleTerminalCreate(run *agentRuntime, msg *protocol.ACPMessage) bool {
	var params terminalCreateParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("参数解析失败: %v", err))
		return true
	}

	// 将环境变量从 []envVar 转换为 []string
	var envStrs []string
	for _, e := range params.Env {
		envStrs = append(envStrs, e.Name+"="+e.Value)
	}

	slog.Debug("ACP terminal/create",
		"agent", a.meta.ID,
		"command", params.Command,
		"args", params.Args,
		"cwd", params.Cwd,
	)

	// 通过全局终端管理器创建终端（传入 outputByteLimit 供截断使用）
	termID, err := globalTerminalManager.Create(params.Command, params.Args, params.Cwd, envStrs, params.OutputByteLimit)
	if err != nil {
		a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("创建终端失败: %v", err))
		return true
	}

	// 构建响应
	result := terminalCreateResult{TerminalID: termID}
	respData, _ := json.Marshal(result)
	response := &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  respData,
	}
	if err := run.writer.WriteMessage(response); err != nil {
		slog.Warn("发送 terminal/create 响应失败",
			"agent", a.meta.ID,
			"error", err,
		)
	}
	return true
}

// terminalOutputParams terminal/output 请求参数
// Lzm 2026-07-20
type terminalOutputParams struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

// terminalOutputResult terminal/output 响应
// Lzm 2026-07-20
type terminalOutputResult struct {
	Output     string              `json:"output"`
	Truncated  bool                `json:"truncated"`
	ExitStatus *terminalExitStatus `json:"exitStatus,omitempty"`
}

// terminalExitStatus 终端退出状态
// Lzm 2026-07-20
type terminalExitStatus struct {
	ExitCode *int    `json:"exitCode"`
	Signal   *string `json:"signal"`
}

// handleTerminalOutput 处理 terminal/output 请求。
// 获取终端的当前累积输出和退出状态（若命令已结束）。
// Lzm 2026-07-20
func (a *baseAgent) handleTerminalOutput(run *agentRuntime, msg *protocol.ACPMessage) bool {
	var params terminalOutputParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("参数解析失败: %v", err))
		return true
	}

	output, truncated, exitCode, signal := globalTerminalManager.Output(params.TerminalID)

	result := terminalOutputResult{
		Output:    output,
		Truncated: truncated,
	}
	if exitCode != nil {
		result.ExitStatus = &terminalExitStatus{
			ExitCode: exitCode,
			Signal:   signal,
		}
	}

	respData, _ := json.Marshal(result)
	response := &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  respData,
	}
	if err := run.writer.WriteMessage(response); err != nil {
		slog.Warn("发送 terminal/output 响应失败",
			"agent", a.meta.ID,
			"error", err,
		)
	}
	return true
}

// terminalWaitForExitParams terminal/wait_for_exit 请求参数
// Lzm 2026-07-20
type terminalWaitForExitParams struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

// handleTerminalWaitForExit 处理 terminal/wait_for_exit 请求。
// 阻塞等待终端命令结束，返回退出状态。
// Lzm 2026-07-20
func (a *baseAgent) handleTerminalWaitForExit(run *agentRuntime, msg *protocol.ACPMessage) bool {
	var params terminalWaitForExitParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("参数解析失败: %v", err))
		return true
	}

	// 等待命令结束后再返回，但设置短于 ReadTimeout 的超时，
	// 让 WaitForExit 先返回，防止与 readTimer 同时触发导致进程被杀死。
	waitTimeout := a.meta.ReadTimeout
	if waitTimeout > 10*time.Second {
		waitTimeout = waitTimeout - 10*time.Second
	}
	exitCode, signal := globalTerminalManager.WaitForExit(params.TerminalID, waitTimeout)

	result := terminalExitStatus{
		ExitCode: exitCode,
		Signal:   signal,
	}
	respData, _ := json.Marshal(result)
	response := &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  respData,
	}
	if err := run.writer.WriteMessage(response); err != nil {
		slog.Warn("发送 terminal/wait_for_exit 响应失败",
			"agent", a.meta.ID,
			"error", err,
		)
	}
	return true
}

// terminalWriteStdinParams terminal/write_stdin 请求参数
// Lzm 2026-07-21
type terminalWriteStdinParams struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
	Input      string `json:"input"`
}

// handleTerminalWriteStdin 处理 terminal/write_stdin 请求。
// 向终端进程的标准输入写入数据，Codex 通过此方法将命令发送到终端。
// Lzm 2026-07-21
func (a *baseAgent) handleTerminalWriteStdin(run *agentRuntime, msg *protocol.ACPMessage) bool {
	var params terminalWriteStdinParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("参数解析失败: %v", err))
		return true
	}

	slog.Debug("ACP terminal/write_stdin",
		"agent", a.meta.ID,
		"terminal_id", params.TerminalID,
		"bytes", len(params.Input),
	)

	n, err := globalTerminalManager.WriteStdin(params.TerminalID, params.Input)
	if err != nil {
		a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("写入 stdin 失败: %v", err))
		return true
	}

	// 构建响应
	result := map[string]int{"bytes_written": n}
	respData, _ := json.Marshal(result)
	response := &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  respData,
	}
	if err := run.writer.WriteMessage(response); err != nil {
		slog.Warn("发送 terminal/write_stdin 响应失败",
			"agent", a.meta.ID,
			"error", err,
		)
	}
	return true
}

// terminalKillParams terminal/kill 请求参数
// Lzm 2026-07-20
type terminalKillParams struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

// handleTerminalKill 处理 terminal/kill 请求。
// 终止终端中的命令进程但不释放终端。
// Lzm 2026-07-20
func (a *baseAgent) handleTerminalKill(run *agentRuntime, msg *protocol.ACPMessage) bool {
	var params terminalKillParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("参数解析失败: %v", err))
		return true
	}

	if err := globalTerminalManager.Kill(params.TerminalID); err != nil {
		a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("终止命令失败: %v", err))
		return true
	}

	response := &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  json.RawMessage(`null`),
	}
	if err := run.writer.WriteMessage(response); err != nil {
		slog.Warn("发送 terminal/kill 响应失败",
			"agent", a.meta.ID,
			"error", err,
		)
	}
	return true
}

// terminalReleaseParams terminal/release 请求参数
// Lzm 2026-07-20
type terminalReleaseParams struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

// handleTerminalRelease 处理 terminal/release 请求。
// 释放终端资源，杀死仍在运行的命令。
// Lzm 2026-07-20
func (a *baseAgent) handleTerminalRelease(run *agentRuntime, msg *protocol.ACPMessage) bool {
	var params terminalReleaseParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("参数解析失败: %v", err))
		return true
	}

	if err := globalTerminalManager.Release(params.TerminalID); err != nil {
		slog.Warn("释放终端失败",
			"agent", a.meta.ID,
			"terminal_id", params.TerminalID,
			"error", err,
		)
	}

	response := &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  json.RawMessage(`null`),
	}
	if err := run.writer.WriteMessage(response); err != nil {
		slog.Warn("发送 terminal/release 响应失败",
			"agent", a.meta.ID,
			"error", err,
		)
	}
	return true
}

// --- Elicitation 方法 ---

// handleElicitationCreate 处理 session/create_elicitation 请求。
// 当 Agent 需要用户输入（显示表单或打开 URL）时发送此请求。
// Bridge 通过 elicitationCB 回调将请求转发给 SaaS 平台，等待用户响应。
// 参考：codex-acp CodexElicitationHandler.ts、claude-agent-acp elicitation.ts
// Lzm 2026-07-22
func (a *baseAgent) handleElicitationCreate(run *agentRuntime, msg *protocol.ACPMessage) bool {
	var params internal.CreateElicitationRequest
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		a.sendACPError(run, msg.ID, -32602, fmt.Sprintf("参数解析失败: %v", err))
		return true
	}

	slog.Debug("ACP session/create_elicitation",
		"agent", a.meta.ID,
		"mode", params.Mode,
		"elicitation_id", params.ElicitationID,
		"has_schema", params.RequestedSchema != nil,
		"has_url", params.URL != "",
	)

	// 使用 elicitationCB 回调转发给 SaaS 平台
	if a.elicitationCB != nil {
		resp, err := a.elicitationCB(msg.Params)
		if err != nil {
			slog.Warn("Elicitation 回调处理失败",
				"agent", a.meta.ID,
				"error", err,
			)
			// 默认取消elicitation
			response := protocol.NewElicitationCreateResponse(msg.ID, string(internal.ElicitationActionCancel), nil)
			if writeErr := run.writer.WriteMessage(response); writeErr != nil {
				slog.Warn("发送 elicitation 取消响应失败",
					"agent", a.meta.ID,
					"error", writeErr,
				)
			}
			return true
		}
		if writeErr := run.writer.WriteMessage(resp); writeErr != nil {
			slog.Warn("发送 elicitation 响应失败",
				"agent", a.meta.ID,
				"error", writeErr,
			)
		}
		return true
	}

	// 未设置回调时自动取消
	slog.Info("收到 Agent elicitation 请求（无回调，自动取消）",
		"agent", a.meta.ID,
		"mode", params.Mode,
	)
	response := protocol.NewElicitationCreateResponse(msg.ID, string(internal.ElicitationActionCancel), nil)
	if err := run.writer.WriteMessage(response); err != nil {
		slog.Warn("发送 elicitation 取消响应失败",
			"agent", a.meta.ID,
			"error", err,
		)
	}
	return true
}

// --- 工具方法 ---

// sendACPError 向 Agent 发送 JSON-RPC 错误响应
// Lzm 2026-07-20
func (a *baseAgent) sendACPError(run *agentRuntime, id json.RawMessage, code int, message string) {
	response := &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error: &internal.ACPError{
			Code:    code,
			Message: message,
		},
	}
	if err := run.writer.WriteMessage(response); err != nil {
		slog.Warn("发送 ACP 错误响应失败",
			"agent", a.meta.ID,
			"error", err,
			"original_msg", message,
		)
	}
}


