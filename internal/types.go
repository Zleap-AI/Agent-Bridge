// -*- coding: utf-8 -*-
// Go 1.26+
//
// types.go
// zleap-bridge 全局公共类型定义
//
// Lzm 2026-07-09

package internal

import (
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
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
}

// StreamChunk 表示一个 ACP 流式消息块
// Lzm 2026-07-10
type StreamChunk struct {
	Type   StreamChunkType
	Text   string       // 块文本内容
	Result interface{}  // final 块时携带完整 JSON-RPC result
	Error  *ACPError    // error 块时携带错误信息
	RawUpdate string   // 原始 sessionUpdate/type 类型名（如 "agent_thought_chunk" 或 "final"），用于 session/load 回放
	IsFinal bool       // Codex 等 Agent 通过 session/update type=final 通知流结束，非 JSON-RPC 响应
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
