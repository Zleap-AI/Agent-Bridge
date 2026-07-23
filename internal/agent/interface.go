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
	"encoding/json"
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
	// Priority 返回 Agent 优先级（值越小优先级越高），用于 SaaS 侧路由决策
	Priority() int
	// Capabilities 返回 Agent 的能力声明（从 ACP 握手结果中读取）
	Capabilities() CapabilityInfo

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
	// NewSession 创建新 ACP 会话，cwd 为可选的工作目录（空值使用默认值）
	NewSession(ctx context.Context, cwd string) (string, error)
	// LoadSession 加载已有会话（重放历史消息）
	LoadSession(ctx context.Context, sessionID string) error
	// ResumeSession 恢复已有会话但不重放历史消息。
	// 需要 Agent 在 initialize 中声明 sessionCapabilities.resume 能力。
	// 与 LoadSession 的区别：不触发历史消息重放，仅恢复上下文。
	// Lzm 2026-07-20
	ResumeSession(ctx context.Context, sessionID string) error

	// --- 会话生命周期 ---
	// CloseSession 关闭指定活跃会话（释放 Agent 侧资源）。
	// 需要 Agent 在 initialize 中声明 sessionCapabilities.close 能力。
	// Lzm 2026-07-21
	CloseSession(ctx context.Context, sessionID string) error
	// DeleteSession 删除指定会话（含历史消息）。
	// 需要 Agent 在 initialize 中声明 sessionCapabilities.delete 能力。
	// Lzm 2026-07-21
	DeleteSession(ctx context.Context, sessionID string) error

	// --- 操作控制 ---
	// Cancel 取消指定会话的当前操作（session/cancel 通知）
	// ACP 协议中 session/cancel 是通知，无需等待响应
	// Lzm 2026-07-20
	Cancel(ctx context.Context, sessionID string) error

	// Logout 调用 Agent 的 logout 方法，结束当前认证状态。
	// 仅在 Agent 声明了 agentCapabilities.auth.logout 能力时可用。
	// Lzm 2026-07-21
	Logout(ctx context.Context) error

	// SetMode 切换 Agent 的操作模式（session/set_mode）。
	// modeID 必须是 Agent 在 session/new 响应中 availableModes 列表中的 ID。
	// 如 "ask"、"code"、"architect" 等。
	// 需要 Agent 在 initialize 中声明 modes 支持。
	// Lzm 2026-07-21
	SetMode(ctx context.Context, sessionID, modeID string) error

	// GetConfig 读取 Agent 会话的当前配置选项值（session/get_config）。
	// 返回当前所有配置选项及其值。需要 Agent 声明 configOptions 支持。
	// Lzm 2026-07-21
	GetConfig(ctx context.Context, sessionID string) (interface{}, error)

	// SetConfig 修改 Agent 会话的配置选项值（session/set_config）。
	// config 参数为配置选项 ID 到值的映射。
	// 需要 Agent 声明 configOptions 支持。
	// Lzm 2026-07-21
	SetConfig(ctx context.Context, sessionID string, config map[string]interface{}) error

	// SetTitle 设置 Agent 会话的标题（session/set_title）。
	// 用于给会话设置人类可读的标题。
	// Lzm 2026-07-21
	SetTitle(ctx context.Context, sessionID, title string) error
}

// SessionHistoryLoader is implemented by Agents that expose the ACP
// session/load replay stream. This lets callers persist native history while
// the Agent continues to own protocol details such as cwd and MCP parameters.
type SessionHistoryLoader interface {
	LoadSessionStream(ctx context.Context, sessionID string) (<-chan internal.StreamChunk, error)
}

// AgentCapabilitiesV1 ACP V1 规范 agentCapabilities 的结构化表示。
// Lzm 2026-07-21
type AgentCapabilitiesV1 struct {
	LoadSession     bool                  `json:"loadSession"`
	Streaming       bool                  `json:"streaming"`
	Sessions        bool                  `json:"sessions"`
	PromptCaps      *PromptCapabilities   `json:"promptCapabilities,omitempty"`
	MCPCaps         *MCPCapabilities      `json:"mcpCapabilities,omitempty"`
	AuthCaps        *AuthCapabilities     `json:"auth,omitempty"`
	SessionCaps     *SessionCapabilities  `json:"sessionCapabilities,omitempty"`
}

// SessionMode 表示 Agent 可用的操作模式
// Lzm 2026-07-21
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModeState 会话模式状态
// Lzm 2026-07-21
type SessionModeState struct {
	CurrentModeID  string         `json:"currentModeId"`
	AvailableModes []SessionMode  `json:"availableModes,omitempty"`
}

// PromptCapabilities prompt 内容类型能力
type PromptCapabilities struct {
	Image            bool `json:"image"`
	Audio            bool `json:"audio"`
	EmbeddedContext  bool `json:"embeddedContext"`
}

// MCPCapabilities MCP 服务器传输方式能力
type MCPCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

// AuthCapabilities 认证相关能力
// ACP V1 规范中 auth.logout 值为空对象 {}（表示支持），非 bool
// Lzm 2026-07-21
type AuthCapabilities struct {
	Logout json.RawMessage `json:"logout,omitempty"`
}

// SupportsLogout 检查是否支持 logout
func (a *AuthCapabilities) SupportsLogout() bool {
	return a != nil && a.Logout != nil
}

// SessionCapabilities 会话相关能力
// ACP V1 规范中 resum/delete/close/additionalDirectories 值为空对象 {}，非 bool
// Lzm 2026-07-21
type SessionCapabilities struct {
	Resume               json.RawMessage `json:"resume,omitempty"`
	Delete               json.RawMessage `json:"delete,omitempty"`
	Close                json.RawMessage `json:"close,omitempty"`
	AdditionalDirectories json.RawMessage `json:"additionalDirectories,omitempty"`
}

// SupportsResume 检查是否支持会话恢复
func (s *SessionCapabilities) SupportsResume() bool {
	return s != nil && s.Resume != nil
}

// SupportsDelete 检查是否支持会话删除
func (s *SessionCapabilities) SupportsDelete() bool {
	return s != nil && s.Delete != nil
}

// SupportsClose 检查是否支持会话关闭
func (s *SessionCapabilities) SupportsClose() bool {
	return s != nil && s.Close != nil
}

// SupportsAdditionalDirectories 检查是否支持额外工作目录
func (s *SessionCapabilities) SupportsAdditionalDirectories() bool {
	return s != nil && s.AdditionalDirectories != nil
}

// ConfigOption 会话配置选项（Session Config Options）
// ACP V1 规范：Agent 在 session/new 响应中返回 configOptions 数组
// Lzm 2026-07-21
type ConfigOption struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	Category     string                 `json:"category,omitempty"`
	Type         string                 `json:"type"` // "select", "boolean" 等
	CurrentValue interface{}            `json:"currentValue,omitempty"`
	Options      []ConfigOptionValue    `json:"options,omitempty"`
}

// ConfigOptionValue 配置选项的可选值
// Lzm 2026-07-21
type ConfigOptionValue struct {
	Value       interface{} `json:"value"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
}

// CapabilityInfo Agent 能力声明。
// 在 ACP initialize 握手完成后填充，用于 SaaS 侧能力感知路由。
// Lzm 2026-07-20
type CapabilityInfo struct {
	// Name Agent 名称（如 "claude-code"）
	Name string `json:"name"`
	// Version Agent 版本
	Version string `json:"version"`
	// Title Agent 标题（人类可读，如 "Claude Code"）
	Title string `json:"title,omitempty"`
	// ProtocolVersion ACP 协议版本
	ProtocolVersion int `json:"protocol_version"`
	// SupportsStreaming 是否支持流式响应
	SupportsStreaming bool `json:"supports_streaming"`
	// SupportsSessions 是否支持会话管理
	SupportsSessions bool `json:"supports_sessions"`
	// SupportsLoadSession 是否支持加载已有会话
	SupportsLoadSession bool `json:"supports_load_session"`
	// SupportsResume 是否支持恢复会话（不重放历史）
	SupportsResume bool `json:"supports_resume"`
	// SupportsSessionDelete 是否支持删除会话
	SupportsSessionDelete bool `json:"supports_session_delete"`
	// SupportsSessionClose 是否支持关闭会话
	SupportsSessionClose bool `json:"supports_session_close"`
	// AgentCapabilities ACP V1 规范 agentCapabilities 的结构化解析结果
	AgentCapabilities *AgentCapabilitiesV1 `json:"agent_capabilities,omitempty"`
	// RawCapabilities ACP initialize 返回的原始 agentCapabilities 字典（兜底用）
	RawCapabilities map[string]interface{} `json:"raw_capabilities,omitempty"`
}

// AgentMeta Agent 元数据（启动前静态配置）
type AgentMeta struct {
	// ID Agent 唯一标识
	ID string
	// DisplayName 显示名称
	DisplayName string
	// Priority Agent 优先级（值越小优先级越高），用于 SaaS 侧路由决策
	// Lzm 2026-07-20
	Priority int
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
	// Capabilities Agent 的静态能力声明（握手未完成时作为默认值）
	Capabilities CapabilityInfo
	// StartupTimeout 启动超时时间（含 ACP 握手）
	StartupTimeout time.Duration
	// ReadTimeout ACP 响应的最大静默时间；流式消息每到一块都会重新计时
	ReadTimeout time.Duration
}

// DefaultStartupTimeout 默认启动超时
const DefaultStartupTimeout = 15 * time.Second

// DefaultReadTimeout 默认读取超时
// 注意：Codex 执行长命令（PowerShell 创建目录等）时可能超过 1 分钟无 ACP 消息，
// 因此需要足够大的超时值。5 分钟可覆盖大多数命令执行场景。
// Lzm 2026-07-20
const DefaultReadTimeout = 5 * time.Minute

// ACPResponseCh 读取 ACP 响应的通道结果
type ACPResponseCh struct {
	Msg *protocol.ACPMessage
	Err error
}
