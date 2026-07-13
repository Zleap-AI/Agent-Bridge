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

// ACPMessage JSON-RPC 2.0 消息结构（ACP 协议用）
type ACPMessage struct {
	JSONRPC string             `json:"jsonrpc"`
	ID      string             `json:"id,omitempty"`
	Method  string             `json:"method,omitempty"`
	Params  json.RawMessage    `json:"params,omitempty"`
	Result  json.RawMessage    `json:"result,omitempty"`
	Error   *internal.ACPError `json:"error,omitempty"`
}

// ACPReader 从 io.Reader（子进程 stdout）读取 ACP JSON-RPC 消息
// ACP 协议约定：每行一个完整的 JSON 对象（换行符分隔）
type ACPReader struct {
	scanner *bufio.Scanner
	closed  bool
}

// NewACPReader 创建 ACPReader
func NewACPReader(r io.Reader) *ACPReader {
	return &ACPReader{
		scanner: bufio.NewScanner(r),
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

// NewInitializeRequest 创建 ACP initialize 请求（ACP 握手）
func NewInitializeRequest(id string) *ACPMessage {
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":1,"clientCapabilities":{}}`),
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
		ID:      id,
		Method:  "session/new",
		Params:  params,
	}
}

// NewPromptRequest 创建 session/prompt 请求
func NewPromptRequest(id, sessionID, text string) *ACPMessage {
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"prompt": []map[string]string{
			{"type": "text", "text": text},
		},
	})
	return &ACPMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "session/prompt",
		Params:  params,
	}
}

// IsStreamUpdate 判断是否为流式更新消息
func (m *ACPMessage) IsStreamUpdate() bool {
	return m.Method == "session/update"
}

// IsResponse 判断是否为请求响应（有 id 字段）
func (m *ACPMessage) IsResponse() bool {
	return m.ID != "" && !m.IsStreamUpdate()
}

// IsSuccess 判断响应是否成功
func (m *ACPMessage) IsSuccess() bool {
	return m.Result != nil && m.Error == nil
}
