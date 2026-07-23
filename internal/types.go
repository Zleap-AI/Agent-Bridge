// -*- coding: utf-8 -*-
// Go 1.25+
//
// types.go
// Agent-Bridge 全局公共类型定义
//
// Lzm 2026-07-09

package internal

import (
	"encoding/json"
	"fmt"
)

// StreamChunkType 表示 ACP 流式消息的块类型
type StreamChunkType int

const (
	// StreamChunkThought 思考过程
	StreamChunkThought StreamChunkType = iota + 1
	// StreamChunkResponse 响应文本
	StreamChunkResponse
	// StreamChunkFinal 最终响应（含完整 JSON-RPC 结果）
	StreamChunkFinal
	// StreamChunkError 错误
	StreamChunkError
	// StreamChunkToolCall 工具调用通知（创建/更新）
	StreamChunkToolCall
	// StreamChunkPlan 计划步骤更新
	StreamChunkPlan
	// StreamChunkModeChange 模式切换通知
	StreamChunkModeChange
	// StreamChunkMetadata 元数据更新（session_info / available_commands 等）
	StreamChunkMetadata
	// StreamChunkPermissionRequest 权限请求（需要用户手动授权）
	// Codex 请求工作区外操作等需要用户显式同意时触发
	// Lzm 2026-07-21
	StreamChunkPermissionRequest
	// StreamChunkUsageUpdate token 使用量更新（Codex 特有的 usage_update 事件）
	// Codex 在每次 prompt 后发送 usage_update 通知，携带 token 使用量和
	// 模型上下文窗口大小，用于前端展示和 quota 管理
	// Lzm 2026-07-22
	StreamChunkUsageUpdate
)

// String 返回 StreamChunkType 的可读名称
func (t StreamChunkType) String() string {
	switch t {
	case StreamChunkThought:
		return "thought"
	case StreamChunkResponse:
		return "response"
	case StreamChunkFinal:
		return "final"
	case StreamChunkError:
		return "error"
	case StreamChunkToolCall:
		return "tool_call"
	case StreamChunkPlan:
		return "plan"
	case StreamChunkModeChange:
		return "mode_change"
	case StreamChunkMetadata:
		return "metadata"
	case StreamChunkPermissionRequest:
		return "permission_request"
	case StreamChunkUsageUpdate:
		return "usage_update"
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
}

// StreamChunk 表示一个 ACP 流式消息块
// Lzm 2026-07-22
type StreamChunk struct {
	Type      StreamChunkType
	Text      string        // 块文本内容
	Result    interface{}   // final 块时携带完整 JSON-RPC result
	Error     *ACPError     // error 块时携带错误信息
	RawUpdate string        // 原始 sessionUpdate/type 类型名（如 "agent_thought_chunk" 或 "final"），用于 session/load 回放
	IsFinal   bool          // Codex 等 Agent 通过 session/update type=final 通知流结束，非 JSON-RPC 响应
	Data      interface{}   // 结构化数据：tool_call 时携带 ToolCallInfo，plan 时携带 PlanUpdate，mode_change 时携带 ModeChangeInfo，metadata 时携带 map
	// TerminalEvent 终端事件元数据（仅 tool_call/tool_call_update 块有效）
	// Codex/Claude Code 在 commandExecution 工具执行过程中通过 _meta
	// 携带 terminal_info/terminal_output/terminal_exit 等终端事件信息。
	// Lzm 2026-07-22
	TerminalEvent *TerminalEvent `json:"terminalEvent,omitempty"`
}

// ToolCallInfo 工具调用信息（从 session/update tool_call/tool_call_update 解析）
// 支持以下工具类型：edit（文件编辑）、execute（命令执行）、read（读取）、
// search（搜索）、think（思考）、fetch（网络请求）、other（其他）
// Lzm 2026-07-22
type ToolCallInfo struct {
	ToolCallID string      `json:"toolCallId"`
	Title      string      `json:"title,omitempty"`
	Kind       string      `json:"kind,omitempty"`
	Status     string      `json:"status,omitempty"`
	RawContent interface{} `json:"content,omitempty"`
	RawInput   interface{} `json:"rawInput,omitempty"`
	RawOutput  interface{} `json:"rawOutput,omitempty"`
	// TerminalID 终端 ID（当工具调用涉及终端操作时设置）
	// Codex 的 commandExecution 工具使用 _meta.terminal_info 传递终端 ID
	// Lzm 2026-07-22
	TerminalID string `json:"terminalId,omitempty"`
	// Locations 工具操作涉及的文件路径列表
	// 例如文件读取、编辑工具等
	// Lzm 2026-07-22
	Locations []string `json:"locations,omitempty"`
}

// UsageUpdate token 使用量更新（从 session/update usage_update 解析）
// Codex 在每个 prompt 完成后发送 usage_update 通知，用于追踪 token 消耗
// Lzm 2026-07-22
type UsageUpdate struct {
	Used      int                    `json:"used"`                // 已使用的 token 数
	Size      int                    `json:"size"`               // 模型上下文窗口大小
	Model     string                 `json:"model,omitempty"`     // 模型名称
	ExtraMeta map[string]interface{} `json:"_meta,omitempty"`    // 额外元数据
}

// TerminalEvent 终端事件元数据（从 tool_call_update 的 _meta 中提取）
// Codex/Claude Code 的 commandExecution 工具使用此结构描述终端事件
// Lzm 2026-07-22
type TerminalEvent struct {
	// TerminalEventOutput 终端输出（terminal_output）
	// 当工具的 _meta 中包含 terminal_output 时设置
	// Lzm 2026-07-22
	Output *TerminalOutput `json:"output,omitempty"`
	// TerminalEventExit 终端退出（terminal_exit）
	// 当工具的 _meta 中包含 terminal_exit 时设置
	// Lzm 2026-07-22
	Exit *TerminalExit `json:"exit,omitempty"`
}

// TerminalOutput 终端输出信息
// Lzm 2026-07-22
type TerminalOutput struct {
	TerminalID string `json:"terminalId,omitempty"`
	Data       string `json:"data,omitempty"`
	// IsFinal 是否为最终输出块
	IsFinal bool `json:"isFinal,omitempty"`
}

// TerminalExit 终端退出信息
// Lzm 2026-07-22
type TerminalExit struct {
	TerminalID string `json:"terminalId,omitempty"`
	ExitCode   *int   `json:"exitCode,omitempty"`
	Signal     string `json:"signal,omitempty"`
}

// PlanUpdate 计划更新信息（从 session/update plan 解析）
// Lzm 2026-07-21
type PlanUpdate struct {
	Steps []PlanStep `json:"steps,omitempty"`
}

// PlanStep 计划中的单个步骤
// Lzm 2026-07-21
type PlanStep struct {
	ID          string `json:"id,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"` // pending, in_progress, completed, failed
}

// ModeChangeInfo 模式切换信息（从 session/update current_mode_update 解析）
// Lzm 2026-07-21
type ModeChangeInfo struct {
	ModeID string `json:"modeId"`
}

// SessionInfoUpdate 会话元数据更新（从 session/update session_info_update 解析）
// Lzm 2026-07-21
type SessionInfoUpdate struct {
	Title     string      `json:"title,omitempty"`
	UpdatedAt string      `json:"updatedAt,omitempty"`
	Meta      interface{} `json:"_meta,omitempty"`
}

// PermissionMode 授权模式
// 控制 Agent 发送 session/request_permission 时的处理方式。
// Lzm 2026-07-21
type PermissionMode string

const (
	// PermissionModeRequestApproval 请求批准：权限请求转发给前端用户手动授权（默认）
	PermissionModeRequestApproval PermissionMode = "request_approval"
	// PermissionModeAutoApprove 替我审批：自动批准权限请求，但通知前端
	PermissionModeAutoApprove PermissionMode = "auto_approve"
	// PermissionModeFullAccess 完全访问权限：自动批准且不通知前端
	PermissionModeFullAccess PermissionMode = "full_access"
	// PermissionModeSessionApproval 本次会话授权：首次请求时通知前端用户手动授权，
	// 授权后在同一会话中的后续权限请求自动批准。
	// 对应 Coze 的"本对话允许"模式。
	// Lzm 2026-07-22
	PermissionModeSessionApproval PermissionMode = "session_approval"
)

// DefaultPermissionMode 默认授权模式
const DefaultPermissionMode = PermissionModeRequestApproval

// PermissionRequestInfo Agent 权限请求的结构化信息。
// 当 Codex 请求工作区外写入等需要用户授权的操作时，
// Bridge 将此信息转发给前端供用户决策。
// Lzm 2026-07-21
type PermissionRequestInfo struct {
	SessionID      string          `json:"session_id"`
	AgentID        string          `json:"agent_id"`
	Message        string          `json:"message,omitempty"`         // 人类可读的权限说明
	ToolCall       json.RawMessage `json:"tool_call,omitempty"`       // 触发权限的工具调用详情
	Params         json.RawMessage `json:"params,omitempty"`          // request_permission 原始参数
	SessionCWD     string          `json:"session_cwd,omitempty"`     // 会话关联的工作目录
	PermissionMode string          `json:"permission_mode,omitempty"` // 当前会话的授权模式
}

// PermissionDecision 用户对权限请求的决策结果。
// 由前端通过 session/permission_response ANP 方法返回。
// Lzm 2026-07-21
type PermissionDecision struct {
	SessionID      string `json:"session_id"`
	AgentID        string `json:"agent_id,omitempty"`        // 对应 Agent ID，用于更新会话授权模式
	Allowed        bool   `json:"allowed"`
	Reason         string `json:"reason,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"` // 前端可以更新会话的授权模式
}

// PermissionResult 权限回调的结果，包含是否允许和授权模式。
// 用于在 pendingPermissions 通道中传递权限决策结果，
// 使 base_send.go 能够根据授权模式选择正确的 optionId。
// Lzm 2026-07-22
type PermissionResult struct {
	Allowed bool   `json:"allowed"`
	Mode    string `json:"mode,omitempty"` // 授权模式：request_approval（允许一次）/ auto_approve（始终允许）
}

// --- Elicitation 相关类型 ---
// 参考：codex-acp CodexElicitationHandler.ts、claude-agent-acp elicitation.ts
// Elicitation 用于 Agent 向 Client 请求用户输入，支持 form（表单）和 url（URL 弹窗）两种模式。
// Lzm 2026-07-22

// ElicitationFormMode elicitation form 模式常量
const ElicitationFormMode = "form"

// ElicitationURLMode elicitation url 模式常量
const ElicitationURLMode = "url"

// ElicitationAction 用户对 elicitation 的响应动作
type ElicitationAction string

const (
	// ElicitationActionAccept 接受：用户填写了表单或确认了 URL
	ElicitationActionAccept ElicitationAction = "accept"
	// ElicitationActionDecline 拒绝：用户跳过了此次请求
	ElicitationActionDecline ElicitationAction = "decline"
	// ElicitationActionCancel 取消：用户取消了整个操作
	ElicitationActionCancel ElicitationAction = "cancel"
)

// CreateElicitationRequest Agent→Client 的 elicitation 创建请求
// 对应 ACP 方法：session/create_elicitation（Agent→Bridge 方向）
// 当 Agent 需要向用户显示表单或打开 URL 时发送此请求。
// 参考：codex-acp CodexElicitationHandler.ts、claude-agent-acp elicitation.ts
// Lzm 2026-07-22
type CreateElicitationRequest struct {
	SessionID       string          `json:"sessionId"`
	Message         string          `json:"message,omitempty"`         // 显示给用户的消息
	Mode            string          `json:"mode"`                      // "form" 或 "url"
	URL             string          `json:"url,omitempty"`             // url 模式的 URL
	ElicitationID   string          `json:"elicitationId,omitempty"`   // 全局唯一 ID
	ToolCallID      string          `json:"toolCallId,omitempty"`      // 关联的工具调用 ID
	RequestedSchema json.RawMessage `json:"requestedSchema,omitempty"` // form 模式的 JSON Schema
	Meta            json.RawMessage `json:"_meta,omitempty"`           // 扩展元数据
}

// CreateElicitationResponse Client→Agent 的 elicitation 响应
// Lzm 2026-07-22
type CreateElicitationResponse struct {
	Action  ElicitationAction       `json:"action"`            // accept / decline / cancel
	Content json.RawMessage         `json:"content,omitempty"` // accept 时携带表单填写的数据
	Meta    json.RawMessage         `json:"_meta,omitempty"`   // 扩展元数据
}

// ElicitationSchemaProperty form 模式下的 JSON Schema property
// Lzm 2026-07-22
type ElicitationSchemaProperty struct {
	Type        string                        `json:"type"`                  // "string" / "array"
	Title       string                        `json:"title,omitempty"`      // 表单项标题
	Description string                        `json:"description,omitempty"`
	OneOf       []ElicitationSchemaOption      `json:"oneOf,omitempty"`      // 单选选项
	Items       *ElicitationSchemaItems        `json:"items,omitempty"`      // 多选选项
}

// ElicitationSchemaOption 单选/多选项
// Lzm 2026-07-22
type ElicitationSchemaOption struct {
	Const       string          `json:"const"`                // 选项值
	Title       string          `json:"title,omitempty"`      // 选项标签
	Description string          `json:"description,omitempty"`
	Meta        json.RawMessage `json:"_meta,omitempty"`      // 扩展元数据
}

// ElicitationSchemaItems 多选模式下的 items 定义
// Lzm 2026-07-22
type ElicitationSchemaItems struct {
	AnyOf []ElicitationSchemaOption `json:"anyOf,omitempty"`
}

// ElicitationSchema form 模式的 JSON Schema
// Lzm 2026-07-22
type ElicitationSchema struct {
	Type       string                              `json:"type"`       // "object"
	Properties map[string]*ElicitationSchemaProperty `json:"properties"`
}

// --- Permission 相关类型（增强） ---

// PermissionOption 权限请求中的单个选项
// 参考：codex-acp CodexApprovalHandler.ts、claude-agent-acp acp-agent.ts
// Lzm 2026-07-22
type PermissionOption struct {
	OptionID string          `json:"optionId"`
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`        // allow_once / allow_always / reject_once / deny
	Meta     json.RawMessage `json:"_meta,omitempty"`
}

// RequestPermissionRequest Agent→Client 权限请求
// 对应 ACP 方法：session/request_permission（Agent→Bridge 方向）
// 当 Agent 需要用户授权（如执行命令、编辑文件、网络访问）时发送此请求。
// Lzm 2026-07-22
type RequestPermissionRequest struct {
	SessionID string            `json:"sessionId"`
	ToolCall  *ToolCallInfo     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
	Meta      json.RawMessage   `json:"_meta,omitempty"`
}

// PermissionResponseOutcome 权限响应结果
// Lzm 2026-07-22
type PermissionResponseOutcome struct {
	Outcome  string `json:"outcome"`            // "selected" / "cancelled"
	OptionID string `json:"optionId,omitempty"` // 用户选中的 optionId
}

// RequestPermissionResponse Client→Agent 权限响应
// Lzm 2026-07-22
type RequestPermissionResponse struct {
	Outcome *PermissionResponseOutcome `json:"outcome,omitempty"`
	Meta    json.RawMessage            `json:"_meta,omitempty"`
}

// ACPError JSON-RPC 2.0 错误结构
type ACPError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Error 实现 error 接口
func (e *ACPError) Error() string {
	return fmt.Sprintf("ACP error %d: %s", e.Code, e.Message)
}
