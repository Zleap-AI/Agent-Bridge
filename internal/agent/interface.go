// -*- coding: utf-8 -*-
// Go 1.25+
//
// interface.go
// Agent 抽象接口定义，所有 Agent 类型差异由此接口封装
//
// Lzm 2026-07-09

package agent

import (
	"context"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// AgentStatus 表示 Agent 进程的当前状态
type AgentStatus int

const (
	// AgentDisconnected 进程未启动或已断开
	AgentDisconnected AgentStatus = iota
	// AgentIdle 进程已启动，空闲中
	AgentIdle
	// AgentBusy 正在处理请求
	AgentBusy
	// AgentError 进程异常
	AgentError
)

// String 返回 AgentStatus 的可读名称
func (s AgentStatus) String() string {
	switch s {
	case AgentDisconnected:
		return "disconnected"
	case AgentIdle:
		return "idle"
	case AgentBusy:
		return "busy"
	case AgentError:
		return "error"
	default:
		return "unknown"
	}
}

// Agent 表示一个本地 AI Agent 进程。
// 所有 Agent 特定的差异都封装在此接口之后。
type Agent interface {
	// --- 身份信息 ---
	// ID 返回 Agent 唯一标识（如 "claude-code"）
	ID() string
	// DisplayName 返回人类可读的名称（如 "Claude Code"）
	DisplayName() string
	// Status 返回当前连接状态
	Status() AgentStatus

	// --- 生命周期 ---
	// Start 启动 Agent 进程并完成 ACP 握手
	Start(ctx context.Context) error
	// Stop 终止 Agent 进程
	Stop(ctx context.Context) error
	// Health 检查 Agent 进程是否健康
	Health(ctx context.Context) error

	// --- ACP 通信 ---
	// Send 发送请求并等待完整响应（非流式）
	Send(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error)
	// Stream 发送请求并返回流式块通道
	Stream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error)

	// --- 会话管理 ---
	// NewSession 创建新 ACP 会话
	NewSession(ctx context.Context) (string, error)
	// LoadSession 加载已有会话
	LoadSession(ctx context.Context, sessionID string) error
}

// SessionHistoryLoader is implemented by Agents that expose the ACP
// session/load replay stream. This lets callers persist native history while
// the Agent continues to own protocol details such as cwd and MCP parameters.
type SessionHistoryLoader interface {
	LoadSessionStream(ctx context.Context, sessionID string) (<-chan internal.StreamChunk, error)
}

// AgentMeta Agent 元数据（启动前静态配置）
type AgentMeta struct {
	// ID Agent 唯一标识
	ID string
	// DisplayName 显示名称
	DisplayName string
	// Cmd 可执行文件路径或命令名
	Cmd string
	// Args 命令行参数
	Args []string
	// WorkDir 工作目录
	WorkDir string
	// Env 环境变量（APY Key 等）
	Env map[string]string
	// PathDirs Agent 子进程可用的命令搜索目录。
	// 后台服务通常只有最小 PATH；这些目录用于让绝对路径的 CLI 找到
	// shebang 中通过 /usr/bin/env 启动的 node 等解释器。
	PathDirs []string
	// StartupTimeout 启动超时时间（含 ACP 握手）
	StartupTimeout time.Duration
	// ReadTimeout ACP 响应的最大静默时间；流式消息每到一块都会重新计时
	ReadTimeout time.Duration
}

// DefaultStartupTimeout 默认启动超时
const DefaultStartupTimeout = 15 * time.Second

// DefaultReadTimeout 默认读取超时
const DefaultReadTimeout = 60 * time.Second

// ACPResponseCh 读取 ACP 响应的通道结果
type ACPResponseCh struct {
	Msg *protocol.ACPMessage
	Err error
}
