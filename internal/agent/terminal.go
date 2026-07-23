// -*- coding: utf-8 -*-
// Go 1.25+
//
// terminal.go
// 终端管理器 — 管理 Agent→Client 终端的进程生命周期
// 当 Agent 需要执行 Shell 命令时，通过 terminal/create 创建终端，
// Bridge 负责启动进程、捕获输出、管理生命周期。
//
// 每个终端会话对应一个独立的子进程，绑定 stdout/stderr 缓冲区。
// Agent 可通过 terminal/output 实时获取输出，通过 terminal/wait_for_exit
// 等待命令完成，通过 terminal/kill 终止命令，通过 terminal/release 释放资源。
//
// Lzm 2026-07-20

package agent

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// TerminalSession 表示一个终端会话。
// 每个会话对应一个独立的子进程，绑定 stdout/stderr 缓冲区。
// Lzm 2026-07-20
type TerminalSession struct {
	// ID 终端唯一标识
	ID string
	// Cmd 子进程命令
	Cmd *exec.Cmd
	// Stdin 标准输入写入器
	Stdin io.WriteCloser
	// stdout 输出缓冲区
	stdout bytes.Buffer
	// stderr 错误输出缓冲区
	stderr bytes.Buffer
	// outputMu 保护输出缓冲区
	outputMu sync.Mutex
	// done 命令结束后关闭的通道
	done chan struct{}
	// exitCode 命令退出码（nil 表示尚未退出）
	exitCode *int
	// exitMu 保护 exitCode
	exitMu sync.Mutex
	// outputByteLimit 输出字节数限制（0 表示无限制）。
	// 超出此限制时从开头截断输出以保持在此限制内。
	// 截断在 Output() 调用时计算，确保在字符边界处进行。
	// Lzm 2026-07-20
	outputByteLimit int
	// truncated 标记输出是否曾被截断
	truncated bool
}

// ExitCode 返回退出码（线程安全）
// Lzm 2026-07-20
func (ts *TerminalSession) ExitCode() *int {
	ts.exitMu.Lock()
	defer ts.exitMu.Unlock()
	if ts.exitCode == nil {
		return nil
	}
	c := *ts.exitCode
	return &c
}

// SetExitCode 设置退出码（线程安全）
// Lzm 2026-07-20
func (ts *TerminalSession) SetExitCode(code int) {
	ts.exitMu.Lock()
	defer ts.exitMu.Unlock()
	ts.exitCode = &code
}

// Output 返回累积的 stdout 输出（线程安全）。
// 若设置了 outputByteLimit 且输出超出限制，从开头截断以保持在此限制内。
// 同时返回是否被截断的标志。
// Lzm 2026-07-20
func (ts *TerminalSession) Output() (string, bool) {
	ts.outputMu.Lock()
	defer ts.outputMu.Unlock()

	if ts.outputByteLimit <= 0 || ts.stdout.Len() <= ts.outputByteLimit {
		return ts.stdout.String(), ts.truncated
	}

	// 超出限制，从开头截断
	raw := ts.stdout.String()
	for len(raw) > ts.outputByteLimit {
		// 在字符边界处截断：跳过第一个字符（可能为多字节）
		_, size := runeStart(raw)
		if size <= 0 {
			break
		}
		raw = raw[size:]
	}
	ts.truncated = true
	return raw, true
}

// runeStart 返回字符串中第一个 rune 的字节数，用于在字符边界处截断
func runeStart(s string) (rune, int) {
	if len(s) == 0 {
		return 0, 0
	}
	// UTF-8 编码：起始字节的高位比特决定字符长度
	b := s[0]
	switch {
	case b&0x80 == 0:
		return rune(b), 1
	case b&0xE0 == 0xC0:
		if len(s) < 2 {
			return 0, 0
		}
		return 0, 2
	case b&0xF0 == 0xE0:
		if len(s) < 3 {
			return 0, 0
		}
		return 0, 3
	case b&0xF8 == 0xF0:
		if len(s) < 4 {
			return 0, 0
		}
		return 0, 4
	default:
		return 0, 1
	}
}

// TerminalManager 管理所有终端会话。
// 提供终端的创建、查询、等待、终止和释放能力。
// Lzm 2026-07-20
type TerminalManager struct {
	mu       sync.Mutex
	sessions map[string]*TerminalSession
	nextID   int
}

// NewTerminalManager 创建终端管理器
// Lzm 2026-07-20
func NewTerminalManager() *TerminalManager {
	return &TerminalManager{
		sessions: make(map[string]*TerminalSession),
		nextID:   1,
	}
}

// Create 创建终端会话并启动命令。
// 参数说明：
//   - command: 要执行的命令
//   - args: 命令参数
//   - cwd: 工作目录（为空时使用当前目录）
//   - env: 环境变量，格式为 "KEY=VALUE"
//   - outputByteLimit: 输出字节数限制（0 表示无限制），超出时从开头截断
//
// 返回终端 ID，可用于后续的 Output、WaitForExit、Kill、Release 操作。
// Lzm 2026-07-20
func (tm *TerminalManager) Create(command string, args []string, cwd string, env []string, outputByteLimit int) (string, error) {
	tm.mu.Lock()
	tm.nextID++
	termID := fmt.Sprintf("%d", tm.nextID)
	tm.mu.Unlock()

	// 构造命令
	cmd := exec.Command(command, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if len(env) > 0 {
		cmd.Env = env
	}

	// 获取 stdin 管道
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("创建终端 stdin 管道失败: %w", err)
	}

	// 绑定 stdout/stderr 到缓冲区
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	// 启动命令
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动终端命令失败: %w", err)
	}

	session := &TerminalSession{
		ID:              termID,
		Cmd:             cmd,
		Stdin:           stdin,
		done:            make(chan struct{}),
		outputByteLimit: outputByteLimit,
	}

	// 后台等待命令结束
	go func() {
		defer close(session.done)
		err := cmd.Wait()
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
					exitCode = status.ExitStatus()
				} else {
					exitCode = -1
				}
			} else {
				exitCode = -1
			}
		}
		session.SetExitCode(exitCode)

		// 将缓冲区内容复制到 session 的输出缓冲区
		session.outputMu.Lock()
		session.stdout = stdoutBuf
		session.stderr = stderrBuf
		session.outputMu.Unlock()

		slog.Debug("终端命令已退出",
			"terminal_id", termID,
			"exit_code", exitCode,
		)
	}()

	// 注册到管理器
	tm.mu.Lock()
	tm.sessions[termID] = session
	tm.mu.Unlock()

	slog.Debug("终端已创建",
		"terminal_id", termID,
		"command", command,
		"args", args,
		"cwd", cwd,
	)

	return termID, nil
}

// Get 获取终端会话
// Lzm 2026-07-20
func (tm *TerminalManager) Get(termID string) (*TerminalSession, bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	s, ok := tm.sessions[termID]
	return s, ok
}

// Output 获取终端累积输出。
// 返回输出内容和是否因字节限制而被截断。
// 如果命令已退出，还会附带退出状态。
// Lzm 2026-07-20
func (tm *TerminalManager) Output(termID string) (output string, truncated bool, exitCode *int, signal *string) {
	session, ok := tm.Get(termID)
	if !ok {
		return "", false, nil, nil
	}

	output, truncated = session.Output()
	exitCode = session.ExitCode()

	return output, truncated, exitCode, signal
}

// WaitForExit 等待命令结束并返回退出状态。
// 如果设置了超时时间，超时后返回 nil。
// Lzm 2026-07-20
func (tm *TerminalManager) WaitForExit(termID string, timeout time.Duration) (exitCode *int, signal *string) {
	session, ok := tm.Get(termID)
	if !ok {
		return nil, nil
	}

	if timeout > 0 {
		select {
		case <-session.done:
		case <-time.After(timeout):
			return nil, nil
		}
	} else {
		<-session.done
	}

	signalStr := ""
	if session.Cmd.ProcessState != nil {
		if status, ok := session.Cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
			if status.Signaled() {
				s := status.Signal().String()
				signalStr = s
			}
		}
	}

	return session.ExitCode(), &signalStr
}

// WriteStdin 向终端的标准输入写入数据。
// Agent 通过此方法向正在运行的命令发送输入（如确认提示、交互命令等）。
// 返回实际写入的字节数。
// Lzm 2026-07-21
func (tm *TerminalManager) WriteStdin(termID string, input string) (int, error) {
	session, ok := tm.Get(termID)
	if !ok {
		return 0, fmt.Errorf("终端 %s 不存在", termID)
	}

	if session.Stdin == nil {
		return 0, fmt.Errorf("终端 %s 的 stdin 不可用（命令可能已退出）", termID)
	}

	n, err := session.Stdin.Write([]byte(input))
	if err != nil {
		return n, fmt.Errorf("写入终端 %s 的 stdin 失败: %w", termID, err)
	}

	slog.Debug("已写入终端 stdin",
		"terminal_id", termID,
		"bytes", n,
	)
	return n, nil
}

// Kill 终止终端中的命令进程，但不释放终端资源。
// 终止后仍可通过 Output 获取最终输出，通过 WaitForExit 获取退出状态。
// Lzm 2026-07-20
func (tm *TerminalManager) Kill(termID string) error {
	session, ok := tm.Get(termID)
	if !ok {
		return fmt.Errorf("终端 %s 不存在", termID)
	}

	if session.Cmd.Process == nil {
		return nil
	}

	slog.Debug("终止终端命令", "terminal_id", termID)
	return session.Cmd.Process.Kill()
}

// Release 释放终端资源。
// 杀死仍在运行的命令（若有），并从管理器中移除。
// 释放后该终端 ID 对所有 terminal/* 方法均无效。
// Lzm 2026-07-20
func (tm *TerminalManager) Release(termID string) error {
	tm.mu.Lock()
	session, ok := tm.sessions[termID]
	delete(tm.sessions, termID)
	tm.mu.Unlock()

	if !ok {
		return nil
	}

	// 杀死仍在运行的命令
	if session.Cmd.Process != nil {
		if err := session.Cmd.Process.Kill(); err != nil {
			slog.Warn("释放终端时杀死进程失败",
				"terminal_id", termID,
				"error", err,
			)
		}
	}

	// 等待命令退出（短超时）
	select {
	case <-session.done:
	case <-time.After(3 * time.Second):
	}

	if session.Stdin != nil {
		_ = session.Stdin.Close()
	}

	slog.Debug("终端已释放", "terminal_id", termID)
	return nil
}

// Cleanup 清理所有终端资源（在 Agent 停止时调用）
// Lzm 2026-07-20
func (tm *TerminalManager) Cleanup() {
	tm.mu.Lock()
	ids := make([]string, 0, len(tm.sessions))
	for id := range tm.sessions {
		ids = append(ids, id)
	}
	tm.mu.Unlock()

	for _, id := range ids {
		if err := tm.Release(id); err != nil {
			slog.Warn("清理终端失败",
				"terminal_id", id,
				"error", err,
			)
		}
	}
}

// 终端管理器全局实例（每个 baseAgent 共享一个）
var globalTerminalManager = NewTerminalManager()
