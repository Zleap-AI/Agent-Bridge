// -*- coding: utf-8 -*-
// Go 1.26+
//
// message_parts.go
// 标准化消息模型 — 借鉴 OpenViking 的四部件消息模型 (text/reasoning/tool/context)
// 用于统一 ACP session/update 流式块的解析和渲染
//
// Lzm 2026-07-10

package service

import (
	"encoding/json"
	"strings"
	"time"
)

// MessagePartType 消息部件类型
// 对应 OpenViking 的 text/reasoning/tool/context 四部件模型
// Lzm 2026-07-10
type MessagePartType string

const (
	// PartText 文本部件 — 最终生成的消息文本
	PartText MessagePartType = "text"
	// PartReasoning 推理部件 — Agent 思考过程（可折叠）
	PartReasoning MessagePartType = "reasoning"
	// PartTool 工具调用部件 — 工具名称、输入输出
	PartTool MessagePartType = "tool"
	// PartContext 上下文引用部件 — memory/resource/skill URI
	PartContext MessagePartType = "context"
)

// MessagePart 消息部件 — 对应 OpenViking 的 MessagePart
// Lzm 2026-07-10
type MessagePart struct {
	Type       MessagePartType `json:"type"`
	Text       string          `json:"text,omitempty"`       // text/reasoning 部件的内容
	Reasoning  string          `json:"reasoning,omitempty"`  // 推理内容
	IsRunning  bool            `json:"is_running,omitempty"` // 推理是否进行中
	ToolName   string          `json:"tool_name,omitempty"`
	ToolInput  json.RawMessage `json:"tool_input,omitempty"`
	ToolOutput string          `json:"tool_output,omitempty"`
	ToolStatus string          `json:"tool_status,omitempty"` // pending/running/completed/error
	ContextURI string          `json:"context_uri,omitempty"`
}

// NormalizedMessage 标准化消息 — 对应 OpenViking 的 NormalizedMessage
// 与 ACP 的 session/update 结构兼容
// Lzm 2026-07-10
type NormalizedMessage struct {
	Role      string            `json:"role"`       // user | assistant | thought
	Text      string            `json:"text"`       // 纯文本内容（精简版）
	Parts     []MessagePart     `json:"parts"`      // 完整部件列表（详细版）
	CreatedAt int64             `json:"created_at"` // 时间戳（东八区）
	PeerID    string            `json:"peer_id"`    // 发送者身份
	Meta      map[string]string `json:"meta,omitempty"`
}

// StreamChunkToParts 将 ACP 流式块转换为消息部件列表
// 借鉴 OpenViking 的 SSE 流式事件模型（content_delta / reasoning_delta / tool_call / tool_result）
// Lzm 2026-07-10
func StreamChunkToParts(chunkType, chunkText string) []MessagePart {
	switch chunkType {
	case "thought":
		return []MessagePart{{
			Type:      PartReasoning,
			Reasoning: chunkText,
			IsRunning: true,
		}}
	case "response":
		return []MessagePart{{
			Type: PartText,
			Text: chunkText,
		}}
	case "tool_call":
		return []MessagePart{{
			Type:       PartTool,
			ToolName:   "tool",
			ToolInput:  json.RawMessage(chunkText),
			ToolStatus: "running",
		}}
	case "tool_result":
		return []MessagePart{{
			Type:       PartTool,
			ToolName:   "tool",
			ToolOutput: chunkText,
			ToolStatus: "completed",
		}}
	default:
		return []MessagePart{{
			Type: PartText,
			Text: chunkText,
		}}
	}
}

// PartsToText 将部件列表渲染为纯文本（兼容旧版 MessageStore 的 text 字段）
// Lzm 2026-07-10
func PartsToText(parts []MessagePart) string {
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case PartText:
			b.WriteString(p.Text)
		case PartReasoning:
			b.WriteString(p.Reasoning)
		case PartTool:
			if p.ToolOutput != "" {
				b.WriteString(p.ToolOutput)
			}
		}
	}
	return b.String()
}

// CollectChunkParts 收集流式块并聚合为部件列表
// 用于在流式结束后将收集到的块转为结构化部件
// Lzm 2026-07-10
func CollectChunkParts(thoughtParts, responseParts []string, toolCalls map[string]string) []MessagePart {
	var parts []MessagePart

	if len(thoughtParts) > 0 {
		parts = append(parts, MessagePart{
			Type:      PartReasoning,
			Reasoning: strings.Join(thoughtParts, ""),
		})
	}

	if len(responseParts) > 0 {
		parts = append(parts, MessagePart{
			Type: PartText,
			Text: strings.Join(responseParts, ""),
		})
	}

	for name, output := range toolCalls {
		parts = append(parts, MessagePart{
			Type:       PartTool,
			ToolName:   name,
			ToolOutput: output,
			ToolStatus: "completed",
		})
	}

	return parts
}

// ACPUpdateToNormalized 将 ACP session/update 消息转为标准化消息
// 适用于 session/load 回放时的消息重放
// Lzm 2026-07-10
func ACPUpdateToNormalized(updateType string, content map[string]interface{}) *NormalizedMessage {
	msg := &NormalizedMessage{
		CreatedAt: nowUnix(),
	}

	switch updateType {
	case "agent_thought_chunk":
		msg.Role = "thought"
		if text, ok := content["text"].(string); ok {
			msg.Text = text
			msg.Parts = []MessagePart{{Type: PartReasoning, Reasoning: text}}
		}
	case "agent_message_chunk", "agent_response_chunk", "message_chunk":
		msg.Role = "assistant"
		if text, ok := content["text"].(string); ok {
			msg.Text = text
			msg.Parts = []MessagePart{{Type: PartText, Text: text}}
		}
	case "user_message":
		msg.Role = "user"
		if text, ok := content["text"].(string); ok {
			msg.Text = text
			msg.Parts = []MessagePart{{Type: PartText, Text: text}}
		}
	}

	return msg
}

// nowUnix 返回当前东八区时间戳（秒）
func nowUnix() int64 {
	return time.Now().Unix()
}
