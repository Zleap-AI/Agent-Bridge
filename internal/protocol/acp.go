// -*- coding: utf-8 -*-
// Go 1.25+
//
// acp.go
// ACP (Agent Client Protocol) 消息类型与读写器
// ACP 基于 JSON-RPC 2.0，通过子进程 stdin/stdout 通信
//
// Lzm 2026-07-09

package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Zleap-AI/Agent-Bridge/internal"
)

const maxACPLineSize = 2 * 1024 * 1024

// ACPMessage JSON-RPC 2.0 消息结构（ACP 协议用）
// ID 使用 json.RawMessage 以兼容 Codex ACP 发送的 number 类型 ID（如 "id": 0）
// 标准 Agent（Claude、Kimi）发送 string 类型 ID（如 "id": "1"）
//
// Meta 字段（json:"_meta"）是 ACP V1 规范定义的扩展点，允许在任意消息
// 中携带自定义元数据（如 W3C 追踪上下文 traceparent/tracestate）。
// 该字段在与 Agent 的 JSON 序列化/反序列化中自动传递，不参与核心逻辑。
// Lzm 2026-07-21
type ACPMessage struct {
	JSONRPC string             `json:"jsonrpc"`
	ID      json.RawMessage    `json:"id,omitempty"`
	Method  string             `json:"method,omitempty"`
	Params  json.RawMessage    `json:"params,omitempty"`
	Result  json.RawMessage    `json:"result,omitempty"`
	Error   *internal.ACPError `json:"error,omitempty"`
	Meta    json.RawMessage    `json:"_meta,omitempty"`
}

// IDString 返回消息 ID 的字符串形式，兼容 string 和 number 两种 JSON 类型
// Lzm 2026-07-20
func (m *ACPMessage) IDString() string {
	if len(m.ID) == 0 || string(m.ID) == "null" {
		return ""
	}
	// 尝试解析为 string（标准 Agent 格式）
	var s string
	if err := json.Unmarshal(m.ID, &s); err == nil {
		return s
	}
	// 尝试解析为 number（Codex 通知格式，如 0）
	var n json.Number
	if err := json.Unmarshal(m.ID, &n); err == nil {
		return n.String()
	}
	return string(m.ID)
}

// HasID 判断消息是否包含有效的 ID
func (m *ACPMessage) HasID() bool {
	return len(m.ID) > 0 && string(m.ID) != "null"
}

// IDMatch 判断消息 ID 是否与指定字符串匹配
func (m *ACPMessage) IDMatch(id string) bool {
	return m.IDString() == id
}

// MarshalStringID 将 string 编码为 JSON 字符串格式的 RawMessage
// 用于构造 ACP 请求消息时正确设置 ID 字段
// Lzm 2026-07-20
func MarshalStringID(id string) json.RawMessage {
	data, _ := json.Marshal(id)
	return data
}

// ACPReader 从 io.Reader（子进程 stdout）读取 ACP JSON-RPC 消息
// ACP 协议约定：每行一个完整的 JSON 对象（换行符分隔）
type ACPReader struct {
	scanner *bufio.Scanner
	closed  bool
}

// NewACPReader 创建 ACPReader
func NewACPReader(r io.Reader) *ACPReader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxACPLineSize)
	return &ACPReader{
		scanner: scanner,
	}
}

// ReadMessage 读取一条 ACP 消息（阻塞）
// 返回 nil, nil 表示流已结束（进程退出）
func (r *ACPReader) ReadMessage() (*ACPMessage, error) {
	if r.closed {
		return nil, nil
	}
	for r.scanner.Scan() {
		line := strings.TrimSpace(r.scanner.Text())
		if line == "" {
			continue // 跳过空行
		}
		var msg ACPMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return nil, fmt.Errorf("解析 ACP 消息失败: %w\n原始行: %s", err, line)
		}
		return &msg, nil
	}
	// scanner 结束（进程退出或 io 关闭）
	if err := r.scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取 ACP 流失败: %w", err)
	}
	r.closed = true
	return nil, nil
}

// Close 关闭读取器
func (r *ACPReader) Close() {
	r.closed = true
}

// ACPWriter 向 io.Writer（子进程 stdin）写入 ACP JSON-RPC 消息
type ACPWriter struct {
	w io.Writer
}

// NewACPWriter 创建 ACPWriter
func NewACPWriter(w io.Writer) *ACPWriter {
	return &ACPWriter{w: w}
}

// WriteMessage 写入一条 ACP 消息（追加换行符）
func (w *ACPWriter) WriteMessage(msg *ACPMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("序列化 ACP 消息失败: %w", err)
	}
	data = append(data, '\n')
	_, err = w.w.Write(data)
	if err != nil {
		return fmt.Errorf("写入 ACP 消息失败: %w", err)
	}
	return nil
}

// PromptContentBlock 表示 session/prompt 中的一个内容块
// 支持 text、resource_link、resource 等类型
// Lzm 2026-07-21
type PromptContentBlock struct {
	Type        string          `json:"type"`
	Text        string          `json:"text,omitempty"`
	URI         string          `json:"uri,omitempty"`
	Name        string          `json:"name,omitempty"`
	MimeType    string          `json:"mimeType,omitempty"`
	Size        int             `json:"size,omitempty"`
	Description string          `json:"description,omitempty"`
	Resource    json.RawMessage `json:"resource,omitempty"`
	Meta        json.RawMessage `json:"_meta,omitempty"`
}

// NewInitializeRequest 创建 ACP initialize 请求（ACP 握手）
// 声明 Bridge 作为 Client 的能力：支持文件系统和终端操作
// Lzm 2026-07-20
// NewAuthenticateRequest 创建 authenticate 请求。
// authenticate 在 initialize 后调用，用于需要认证的 Agent。
// methodId 必须是 Agent 在 initialize 响应中 authMethods 数组中的 ID。
// Lzm 2026-07-21
func NewAuthenticateRequest(id, methodID string) *ACPMessage {
	params, _ := json.Marshal(map[string]string{
		"methodId": methodID,
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      MarshalStringID(id),
		Method:  "authenticate",
		Params:  params,
	}
}

// NewLogoutRequest 创建 logout 请求。
// 仅在 Agent 声明了 agentCapabilities.auth.logout 时调用。
// Lzm 2026-07-21
func NewLogoutRequest(id string) *ACPMessage {
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      MarshalStringID(id),
		Method:  "logout",
	}
}

// InitializeParams ACP initialize 请求参数
// Lzm 2026-07-21
type InitializeParams struct {
	ProtocolVersion   int                    `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities    `json:"clientCapabilities"`
	ClientInfo        ClientInfo             `json:"clientInfo"`
}

// ClientCapabilities Client 能力声明
// Lzm 2026-07-21
type ClientCapabilities struct {
	FS       *FSCapabilities              `json:"fs,omitempty"`
	Terminal bool                         `json:"terminal,omitempty"`
	Session  *ClientSessionCapabilities   `json:"session,omitempty"`
}

// FSCapabilities 文件系统操作能力
// Lzm 2026-07-21
type FSCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

// ClientSessionCapabilities 会话能力配置选项
// Lzm 2026-07-21
type ClientSessionCapabilities struct {
	ConfigOptions map[string]interface{} `json:"configOptions,omitempty"`
}

// ClientInfo Client 身份信息
// Lzm 2026-07-21
type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

// NewInitializeRequest 创建 ACP initialize 请求（ACP 握手）
// 使用结构体序列化替代硬编码 JSON，方便动态调整 capabilities
// Lzm 2026-07-21
func NewInitializeRequest(id string) *ACPMessage {
	params, _ := json.Marshal(InitializeParams{
		ProtocolVersion: 1,
		ClientCapabilities: ClientCapabilities{
			FS: &FSCapabilities{
				ReadTextFile:  true,
				WriteTextFile: true,
			},
			Terminal: true,
			Session: &ClientSessionCapabilities{
				ConfigOptions: map[string]interface{}{
					"boolean": struct{}{},
				},
			},
		},
		ClientInfo: ClientInfo{
			Name:    "zleap-bridge",
			Title:   "Zleap Bridge",
			Version: "1.0.0",
		},
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      MarshalStringID(id),
		Method:  "initialize",
		Params:  params,
	}
}

// NewSessionRequest 创建 session/new 请求
func NewSessionRequest(id, cwd string) *ACPMessage {
	params, _ := json.Marshal(map[string]interface{}{
		"cwd":        cwd,
		"mcpServers": []interface{}{},
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      MarshalStringID(id),
		Method:  "session/new",
		Params:  params,
	}
}

// NewSessionLoadRequest 创建 session/load 请求。
// session/load 用于加载已有会话并重放历史消息。
// 与 session/resume 的区别：会触发历史消息重放。
// Lzm 2026-07-21
func NewSessionLoadRequest(id, sessionID, cwd string) *ACPMessage {
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId":  sessionID,
		"cwd":        cwd,
		"mcpServers": []interface{}{},
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      MarshalStringID(id),
		Method:  "session/load",
		Params:  params,
	}
}

// NewResumeRequest 创建 session/resume 请求。
// session/resume 用于恢复已有会话但不重放历史消息。
// 需要 Agent 在 initialize 中声明 sessionCapabilities.resume 能力。
// ACP V1 规范：params 包含 sessionId、cwd、mcpServers。
// Lzm 2026-07-20
func NewResumeRequest(id, sessionID, cwd string) *ACPMessage {
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId":  sessionID,
		"cwd":        cwd,
		"mcpServers": []interface{}{},
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      MarshalStringID(id),
		Method:  "session/resume",
		Params:  params,
	}
}

// NewCloseSessionRequest 创建 session/close 请求。
// session/close 用于关闭活跃会话，释放 Agent 侧资源。
// 需要 Agent 在 initialize 中声明 sessionCapabilities.close 能力。
// Lzm 2026-07-21
func NewCloseSessionRequest(id, sessionID string) *ACPMessage {
	params, _ := json.Marshal(map[string]string{
		"sessionId": sessionID,
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      MarshalStringID(id),
		Method:  "session/close",
		Params:  params,
	}
}

// NewDeleteSessionRequest 创建 session/delete 请求。
// session/delete 用于删除已有会话（含历史消息）。
// 需要 Agent 在 initialize 中声明 sessionCapabilities.delete 能力。
// Lzm 2026-07-21
func NewDeleteSessionRequest(id, sessionID string) *ACPMessage {
	params, _ := json.Marshal(map[string]string{
		"sessionId": sessionID,
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      MarshalStringID(id),
		Method:  "session/delete",
		Params:  params,
	}
}

// NewPromptRequest 创建 session/prompt 请求，支持 ResourceLink 内容类型。
// blocks 参数支持 text、resource_link、resource 等多种内容块类型。
// 当 blocks 为空时，默认使用 text 类型发送 text 内容。
// Lzm 2026-07-21
func NewPromptRequest(id, sessionID string, blocks []PromptContentBlock) *ACPMessage {
	if len(blocks) == 0 {
		blocks = []PromptContentBlock{
			{Type: "text", Text: ""},
		}
	}
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    blocks,
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      MarshalStringID(id),
		Method:  "session/prompt",
		Params:  params,
	}
}

// NewTextPrompt 创建纯文本 session/prompt 请求（简化版，兼容现有调用）
// Lzm 2026-07-21
func NewTextPrompt(id, sessionID, text string) *ACPMessage {
	return NewPromptRequest(id, sessionID, []PromptContentBlock{
		{Type: "text", Text: text},
	})
}

// NewResourceLinkPrompt 创建包含 ResourceLink 的 session/prompt 请求。
// ResourceLink 用于引用 Agent 可以访问的资源（文件、URL 等）。
// Lzm 2026-07-21
func NewResourceLinkPrompt(id, sessionID, uri, name, mimeType string) *ACPMessage {
	return NewPromptRequest(id, sessionID, []PromptContentBlock{
		{Type: "resource_link", URI: uri, Name: name, MimeType: mimeType},
	})
}

// NewSetModeRequest 创建 session/set_mode 请求。
// 用于切换 Agent 的操作模式（如 "ask"、"code"、"architect"）。
// 需要 Agent 在 session/new 响应中声明 availableModes。
// Lzm 2026-07-21
func NewSetModeRequest(id, sessionID, modeID string) *ACPMessage {
	params, _ := json.Marshal(map[string]string{
		"sessionId": sessionID,
		"modeId":    modeID,
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      MarshalStringID(id),
		Method:  "session/set_mode",
		Params:  params,
	}
}

// NewGetConfigRequest 创建 session/get_config 请求。
// 用于读取 Agent 会话的当前配置选项值。
// 需要 Agent 在 session/new 响应中声明 configOptions。
// 返回当前所有配置选项及其值。
// Lzm 2026-07-21
func NewGetConfigRequest(id, sessionID string) *ACPMessage {
	params, _ := json.Marshal(map[string]string{
		"sessionId": sessionID,
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      MarshalStringID(id),
		Method:  "session/get_config",
		Params:  params,
	}
}

// NewSetConfigRequest 创建 session/set_config 请求。
// 用于修改 Agent 会话的配置选项值。
// config 参数为配置选项 ID 到值的映射。
// 需要 Agent 在 session/new 响应中声明 configOptions。
// Lzm 2026-07-21
func NewSetConfigRequest(id, sessionID string, config map[string]interface{}) *ACPMessage {
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"config":    config,
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      MarshalStringID(id),
		Method:  "session/set_config",
		Params:  params,
	}
}

// NewSetTitleRequest 创建 session/set_title 请求。
// 用于设置 Agent 会话的标题。
// 需要 Agent 在 initialize 中声明 sessionCapabilities 支持。
// Lzm 2026-07-21
func NewSetTitleRequest(id, sessionID, title string) *ACPMessage {
	params, _ := json.Marshal(map[string]string{
		"sessionId": sessionID,
		"title":     title,
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      MarshalStringID(id),
		Method:  "session/set_title",
		Params:  params,
	}
}

// NewCancelRequestNotification 创建 $/cancel_request 协议级通知。
// 用于取消特定 JSON-RPC 请求（Agent 侧实现可能需要支持）。
// 通知无需响应，因此不分配 ID。
// Lzm 2026-07-21
func NewCancelRequestNotification(sessionID, requestID string) *ACPMessage {
	params, _ := json.Marshal(map[string]string{
		"sessionId": sessionID,
		"id":        requestID,
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		Method:  "$/cancel_request",
		Params:  params,
	}
}

// NewProgressNotification 创建 $/progress 协议级通知。
// 用于报告长时间操作（如文件扫描、模型推理）的进度。
// 通知无需响应，因此不分配 ID。
// Lzm 2026-07-21
func NewProgressNotification(sessionID, title string, progress, total float64) *ACPMessage {
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"progress": map[string]interface{}{
			"title":    title,
			"progress": progress,
			"total":    total,
		},
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		Method:  "$/progress",
		Params:  params,
	}
}

// NewLogMessageNotification 创建 $/logMessage 协议级通知。
// 用于 Agent 向 Client 发送日志消息。
// 通知无需响应，因此不分配 ID。
// Lzm 2026-07-21
func NewLogMessageNotification(sessionID, level, message string) *ACPMessage {
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"level":     level,
		"message":   message,
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		Method:  "$/logMessage",
		Params:  params,
	}
}

// IsStreamUpdate 判断是否为流式更新消息
func (m *ACPMessage) IsStreamUpdate() bool {
	return m.Method == "session/update"
}

// IsResponse 判断是否为请求响应（有 id 字段）
func (m *ACPMessage) IsResponse() bool {
	return m.HasID() && !m.IsStreamUpdate()
}

// IsSuccess 判断响应是否成功
func (m *ACPMessage) IsSuccess() bool {
	return m.Result != nil && m.Error == nil
}

// --- Elicitation 消息构造器 ---
// 参考：codex-acp CodexElicitationHandler.ts、claude-agent-acp elicitation.ts
// Lzm 2026-07-22

// NewElicitationCreateResponse 创建 session/create_elicitation 的成功响应。
// 用于 Agent 发起 elicitation 请求后，Bridge 将用户响应回传给 Agent。
// Lzm 2026-07-22
func NewElicitationCreateResponse(id json.RawMessage, action string, content json.RawMessage) *ACPMessage {
	result, _ := json.Marshal(map[string]interface{}{
		"action":  action,
		"content": content,
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

// NewPermissionResponse 创建 session/request_permission 的成功响应。
// 用于 Agent 发起权限请求后，Bridge 将用户决策结果回传给 Agent。
// Lzm 2026-07-22
func NewPermissionResponse(id json.RawMessage, outcome, optionID string) *ACPMessage {
	var result json.RawMessage
	if outcome == "cancelled" {
		result, _ = json.Marshal(map[string]interface{}{
			"outcome": map[string]string{
				"outcome": "cancelled",
			},
		})
	} else {
		result, _ = json.Marshal(map[string]interface{}{
			"outcome": map[string]string{
				"outcome":  "selected",
				"optionId": optionID,
			},
		})
	}
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

// NewElicitationCreateError 创建 session/create_elicitation 的错误响应。
// Lzm 2026-07-22
func NewElicitationCreateError(id json.RawMessage, code int, message string) *ACPMessage {
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error: &internal.ACPError{
			Code:    code,
			Message: message,
		},
	}
}
