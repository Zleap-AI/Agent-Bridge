// -*- coding: utf-8 -*-
// Go 1.26+
//
// codec.go
// ACP 流式消息类型标准化编解码器
// 不同 Agent 可能使用不同的 sessionUpdate 类型名，统一归一化处理
//
// Lzm 2026-07-09

package protocol

import (
	"encoding/json"

	"github.com/zleap/bridge/internal"
)

// sessionUpdate 名称常量（ACP 流式通知中的 sessionUpdate 字段值）
const (
	// 思考过程（各种 Agent 的变体命名）
	SessionUpdateThought       = "agent_thought_chunk"
	SessionUpdateThoughtAlt1   = "thought_chunk"
	SessionUpdateThoughtAlt2   = "thinking_chunk"

	// 响应文本（各种 Agent 的变体命名）
	SessionUpdateResponse      = "agent_message_chunk"
	SessionUpdateResponseAlt1  = "agent_response_chunk"
	SessionUpdateResponseAlt2  = "message_chunk"
	SessionUpdateResponseAlt3  = "content_chunk"
)

// ACPStreamUpdate 流式更新消息参数结构
type ACPStreamUpdate struct {
	Update struct {
		SessionUpdate string          `json:"sessionUpdate"`
		Content       json.RawMessage `json:"content"`
	} `json:"update"`
}

// CodexStreamUpdate Codex codex-acp wrapper 使用的流式更新格式
// Codex 将数据放在 params 下，而非标准的 result.update
// 格式：{"params": {"request_id": "...", "type": "response|final", "content": {"text": "..."}}}
type CodexStreamUpdate struct {
	RequestID string `json:"request_id"`
	Type      string `json:"type"`      // "response" | "final"
	Content   *struct {
		Text string `json:"text"`
	} `json:"content"`
}

// StreamContent 流式块文本内容
type StreamContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// NormalizeStreamType 将 Agent 差异化的 sessionUpdate 类型名归一化为标准类型
func NormalizeStreamType(rawType string) internal.StreamChunkType {
	switch rawType {
	case SessionUpdateThought,
		SessionUpdateThoughtAlt1,
		SessionUpdateThoughtAlt2:
		return internal.StreamChunkThought

	case SessionUpdateResponse,
		SessionUpdateResponseAlt1,
		SessionUpdateResponseAlt2,
		SessionUpdateResponseAlt3:
		return internal.StreamChunkResponse

	default:
		// 未知类型，按响应处理（保守策略）
		return internal.StreamChunkResponse
	}
}

// ParseStreamChunk 从 ACP 流式更新消息中解析出统一的 StreamChunk
// 兼容两种格式：
//   1. Kimi 标准格式：params = {"update": {"sessionUpdate": "...", "content": {...}}}
//   2. Codex codex-acp 格式：params = {"type": "response|final|error", "content": {"text": "..."}}
// Lzm 2026-07-10
func ParseStreamChunk(msg *ACPMessage) (*internal.StreamChunk, error) {
	if !msg.IsStreamUpdate() {
		return nil, nil
	}

	// 尝试标准格式（Kimi 等 Agent 使用）
	// {"update": {"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "..."}}}
	var update ACPStreamUpdate
	if err := json.Unmarshal(msg.Params, &update); err == nil && update.Update.SessionUpdate != "" {
		rawType := update.Update.SessionUpdate
		chunkType := NormalizeStreamType(rawType)

		var text string
		var content StreamContent
		if err := json.Unmarshal(update.Update.Content, &content); err == nil {
			text = content.Text
		} else {
			var rawStr string
			if err := json.Unmarshal(update.Update.Content, &rawStr); err == nil {
				text = rawStr
			}
		}

		return &internal.StreamChunk{
			Type:      chunkType,
			Text:      text,
			RawUpdate: rawType,
		}, nil
	}

	// 尝试 Codex 格式（codex-acp wrapper 使用）
	// {"request_id": "...", "type": "response|final|error", "content": {"text": "..."}}
	var codexUpdate CodexStreamUpdate
	if err := json.Unmarshal(msg.Params, &codexUpdate); err == nil && codexUpdate.Type != "" {
		text := ""
		if codexUpdate.Content != nil {
			text = codexUpdate.Content.Text
		}

		isFinal := codexUpdate.Type == "final"
		isError := codexUpdate.Type == "error"
		chunkType := internal.StreamChunkResponse
		rawType := codexUpdate.Type

		if isFinal {
			chunkType = internal.StreamChunkFinal
		} else if isError {
			chunkType = internal.StreamChunkError
		}

		chunk := &internal.StreamChunk{
			Type:      chunkType,
			Text:      text,
			RawUpdate: rawType,
			IsFinal:   isFinal,
		}
		if isError {
			chunk.Error = &internal.ACPError{
				Code:    -1,
				Message: text,
			}
		}
		return chunk, nil
	}

	return nil, nil
}
