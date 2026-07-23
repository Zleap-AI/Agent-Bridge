// -*- coding: utf-8 -*-
// Go 1.25+
//
// codec.go
// ACP 流式消息类型标准化编解码器
// 不同 Agent 可能使用不同的 sessionUpdate 类型名，统一归一化处理
//
// Lzm 2026-07-09

package protocol

import (
	"encoding/json"

	"github.com/Zleap-AI/Agent-Bridge/internal"
)

// sessionUpdate 名称常量（ACP 流式通知中的 sessionUpdate 字段值）
const (
	// 思考过程（各种 Agent 的变体命名）
	SessionUpdateThought     = "agent_thought_chunk"
	SessionUpdateThoughtAlt1 = "thought_chunk"
	SessionUpdateThoughtAlt2 = "thinking_chunk"

	// 响应文本（各种 Agent 的变体命名）
	SessionUpdateResponse     = "agent_message_chunk"
	SessionUpdateResponseAlt1 = "agent_response_chunk"
	SessionUpdateResponseAlt2 = "message_chunk"
	SessionUpdateResponseAlt3 = "content_chunk"

	// 工具调用通知类型
	SessionUpdateToolCall     = "tool_call"
	SessionUpdateToolCallUpd  = "tool_call_update"

	// 计划步骤更新
	SessionUpdatePlan = "plan"

	// 模式切换通知
	SessionUpdateModeChange   = "mode_change"
	SessionUpdateCurrentMode  = "current_mode_update"

	// 元数据更新
	SessionUpdateAvailableCommands = "available_commands_update"
	SessionUpdateSessionInfo       = "session_info_update"

	// token 使用量更新（Codex 特有）
	SessionUpdateUsage = "usage_update"

	// 配置选项更新（Codex 特有）
	SessionUpdateConfigOption = "config_option_update"

	// 用户消息块（Claude Agent 特有）
	SessionUpdateUserMessage = "user_message_chunk"

	// 文本思考过程（Codex 推理用）
	SessionUpdateTextThought = "agent_text_thought_chunk"
)

// ACPStreamUpdate 流式更新消息参数结构
type ACPStreamUpdate struct {
	Update struct {
		SessionUpdate      string          `json:"sessionUpdate"`
		Content            json.RawMessage `json:"content"`
		ToolCallID         string          `json:"toolCallId,omitempty"`
		Title              string          `json:"title,omitempty"`
		Kind               string          `json:"kind,omitempty"`
		Status             string          `json:"status,omitempty"`
		ToolCall           json.RawMessage `json:"toolCall,omitempty"`
		Options            json.RawMessage `json:"options,omitempty"`
		Plan               json.RawMessage `json:"plan,omitempty"`
		AvailableCommands  json.RawMessage `json:"availableCommands,omitempty"`
		ModeID             string          `json:"modeId,omitempty"`
		// rawInput 工具调用的输入参数（Codex 特有）
		RawInput  json.RawMessage `json:"rawInput,omitempty"`
		// rawOutput 工具调用的输出结果（Codex 特有）
		RawOutput json.RawMessage `json:"rawOutput,omitempty"`
		// Locations 工具操作涉及的文件路径（Codex 特有）
		Locations []string `json:"locations,omitempty"`
		// Used token 使用量（usage_update 事件专用）
		Used *int `json:"used,omitempty"`
		// Size 模型上下文窗口大小（usage_update 事件专用）
		Size *int `json:"size,omitempty"`
		// Meta 扩展元数据（_meta），包含 terminal_info/terminal_output/terminal_exit 等
		Meta json.RawMessage `json:"_meta,omitempty"`
	} `json:"update"`
}

// CodexStreamUpdate Codex codex-acp wrapper 使用的流式更新格式
// Codex 将数据放在 params 下，而非标准的 result.update
// 格式：{"params": {"request_id": "...", "type": "response|final", "content": {"text": "..."}}}
type CodexStreamUpdate struct {
	RequestID string `json:"request_id"`
	Type      string `json:"type"` // "response" | "final"
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

	case SessionUpdateToolCall,
		SessionUpdateToolCallUpd:
		return internal.StreamChunkToolCall

	case SessionUpdatePlan:
		return internal.StreamChunkPlan

	case SessionUpdateModeChange,
		SessionUpdateCurrentMode:
		return internal.StreamChunkModeChange

	case SessionUpdateAvailableCommands,
		SessionUpdateSessionInfo,
		SessionUpdateConfigOption:
		return internal.StreamChunkMetadata

	case SessionUpdateUsage:
		return internal.StreamChunkUsageUpdate

	case SessionUpdateUserMessage:
		return internal.StreamChunkResponse

	case SessionUpdateTextThought:
		return internal.StreamChunkThought

	default:
		// 未知类型，按响应处理（保守策略）
		return internal.StreamChunkResponse
	}
}

// ACPStreamUpdateCodexLike Codex codex-acp 使用的 session/update 格式。
// 与标准 ACPStreamUpdate 的区别：update 对象中使用 "type" 字段替代 "sessionUpdate"。
// 格式：{"sessionId": "...", "update": {"type": "response|final|error|thought", "content": {"text": "..."}}}
// Lzm 2026-07-21
type ACPStreamUpdateCodexLike struct {
	Update struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	} `json:"update"`
}

// ParseStreamChunk 从 ACP 流式更新消息中解析出统一的 StreamChunk
// 兼容格式：
//  1. Kimi/Claude 标准格式：params = {"sessionId": "...", "update": {"sessionUpdate": "...", "content": {...}, ...}}
//  2. Codex codex-acp session/update 格式：params = {"sessionId": "...", "update": {"type": "response|final|error|thought", "content": {"text": "..."}}}
//  3. Codex codex-acp 非标格式：params = {"type": "response|final|error", "content": {"text": "..."}}
//
// 支持的 sessionUpdate 类型：
//   - agent_thought_chunk / thought_chunk / thinking_chunk → thought
//   - agent_message_chunk / agent_response_chunk / message_chunk / content_chunk → response
//   - tool_call / tool_call_update → tool_call（Data 为 ToolCallInfo）
//   - plan → plan（Data 为 PlanUpdate）
//   - current_mode_update / mode_change → mode_change（Data 为 ModeChangeInfo）
//   - available_commands_update / session_info_update → metadata（Data 为 map）
//
// Lzm 2026-07-21
func ParseStreamChunk(msg *ACPMessage) (*internal.StreamChunk, error) {
	if !msg.IsStreamUpdate() {
		return nil, nil
	}

	// 尝试标准格式（Kimi/Claude 等 Agent 使用）
	var update ACPStreamUpdate
	if err := json.Unmarshal(msg.Params, &update); err == nil && update.Update.SessionUpdate != "" {
		return parseACPStreamUpdate(update), nil
	}

	// 尝试 Codex codex-acp session/update 格式
	// update 中使用 "type" 字段替代 "sessionUpdate"
	// {"sessionId": "...", "update": {"type": "response|final|error|thought", "content": {"text": "..."}}}
	var codexLikeUpdate ACPStreamUpdateCodexLike
	if err := json.Unmarshal(msg.Params, &codexLikeUpdate); err == nil && codexLikeUpdate.Update.Type != "" {
		return parseCodexLikeStreamUpdate(codexLikeUpdate), nil
	}

	// 尝试 Codex 备用格式（codex-acp wrapper 直接传递的 params）
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

// parseACPStreamUpdate 解析标准 ACPStreamUpdate 格式（Kimi/Claude/Codex 等 Agent 使用）
// Lzm 2026-07-22
func parseACPStreamUpdate(update ACPStreamUpdate) *internal.StreamChunk {
	rawType := update.Update.SessionUpdate
	chunkType := NormalizeStreamType(rawType)

	chunk := &internal.StreamChunk{
		Type:      chunkType,
		RawUpdate: rawType,
	}

	// 根据类型提取结构化数据和文本
	switch chunkType {
	case internal.StreamChunkThought, internal.StreamChunkResponse:
		// 从 content 中提取文本
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
		chunk.Text = text

	case internal.StreamChunkToolCall:
		// 工具调用：提取 toolCallId、title、kind、status、content、rawInput、rawOutput、locations、meta
		tc := &internal.ToolCallInfo{
			ToolCallID: update.Update.ToolCallID,
			Title:      update.Update.Title,
			Kind:       update.Update.Kind,
			Status:     update.Update.Status,
		}
		// locations 文件路径
		if len(update.Update.Locations) > 0 {
			tc.Locations = update.Update.Locations
		}
		// rawInput 工具输入参数
		if update.Update.RawInput != nil {
			tc.RawInput = update.Update.RawInput
		}
		// rawOutput 工具输出结果
		if update.Update.RawOutput != nil {
			tc.RawOutput = update.Update.RawOutput
		}
		// content 可能携带文本摘要
		if update.Update.Content != nil {
			var content StreamContent
			if err := json.Unmarshal(update.Update.Content, &content); err == nil {
				tc.RawContent = content
				chunk.Text = content.Text
			} else {
				tc.RawContent = update.Update.Content
			}
		}
		// _meta 扩展元数据：尝试提取 terminal_info/terminal_output/terminal_exit
		// Codex/Claude Code 在 commandExecution 工具执行过程中通过 _meta
		// 传递终端 ID、累积输出和退出状态。
		// Lzm 2026-07-22
		if update.Update.Meta != nil {
			if terminalID := extractTerminalID(update.Update.Meta); terminalID != "" {
				tc.TerminalID = terminalID
			}
			// 构建 TerminalEvent 并附加到 chunk 的 _meta 层
			terminalEvent := extractTerminalMeta(update.Update.Meta)
			if terminalEvent != nil {
				chunk.TerminalEvent = terminalEvent
			}
		}
		chunk.Data = tc

	case internal.StreamChunkUsageUpdate:
		// token 使用量更新：提取 used 和 size 字段
		usage := &internal.UsageUpdate{}
		if update.Update.Used != nil {
			usage.Used = *update.Update.Used
		}
		if update.Update.Size != nil {
			usage.Size = *update.Update.Size
		}
		chunk.Data = usage

	case internal.StreamChunkPlan:
		// 计划步骤更新：提取 plan 字段
		if update.Update.Plan != nil {
			var planUpdate internal.PlanUpdate
			if err := json.Unmarshal(update.Update.Plan, &planUpdate); err == nil {
				chunk.Data = &planUpdate
			}
		}

	case internal.StreamChunkModeChange:
		// 模式切换：提取 modeId
		chunk.Data = &internal.ModeChangeInfo{
			ModeID: update.Update.ModeID,
		}

	case internal.StreamChunkMetadata:
		// 元数据更新：提取 availableCommands 或 session_info 或 config_option 内容
		var metaData map[string]interface{}
		if update.Update.AvailableCommands != nil {
			json.Unmarshal(update.Update.AvailableCommands, &metaData)
			if metaData == nil {
				metaData = make(map[string]interface{})
			}
			metaData["_type"] = "available_commands"
			metaData["_raw"] = update.Update.AvailableCommands
		} else if update.Update.Content != nil {
			json.Unmarshal(update.Update.Content, &metaData)
			if metaData == nil {
				metaData = make(map[string]interface{})
			}
			// 尝试解析为标准 session_info_update 结构
			var infoUpdate internal.SessionInfoUpdate
			if err := json.Unmarshal(update.Update.Content, &infoUpdate); err == nil && infoUpdate.Title != "" {
				metaData["title"] = infoUpdate.Title
				metaData["updatedAt"] = infoUpdate.UpdatedAt
				metaData["_meta"] = infoUpdate.Meta
			}
			metaData["_type"] = "session_info"
		} else {
			metaData = map[string]interface{}{
				"_type": rawType,
			}
		}
		chunk.Data = metaData
	}

	return chunk
}

// extractTerminalID 从 _meta 扩展元数据中提取终端 ID。
// 支持两种格式：
//   - Codex 格式：_meta.terminal_info.terminal_id
//   - Claude Code 格式：_meta.terminal_info.terminal_id
// Lzm 2026-07-22
func extractTerminalID(meta json.RawMessage) string {
	if meta == nil {
		return ""
	}

	var metaObj struct {
		TerminalInfo *struct {
			TerminalID string `json:"terminal_id"`
		} `json:"terminal_info"`
	}
	if err := json.Unmarshal(meta, &metaObj); err == nil && metaObj.TerminalInfo != nil {
		return metaObj.TerminalInfo.TerminalID
	}
	return ""
}

// extractTerminalOutput 从 _meta 扩展元数据中提取终端输出内容。
// Codex/Claude Code 在 commandExecution 工具完成时通过
// _meta.terminal_output 传递终端的标准输出。
// Lzm 2026-07-22
func extractTerminalOutput(meta json.RawMessage) *internal.TerminalOutput {
	if meta == nil {
		return nil
	}

	var metaObj struct {
		TerminalOutput *struct {
			TerminalID string `json:"terminal_id"`
			Data       string `json:"data"`
		} `json:"terminal_output"`
	}
	if err := json.Unmarshal(meta, &metaObj); err == nil && metaObj.TerminalOutput != nil {
		return &internal.TerminalOutput{
			TerminalID: metaObj.TerminalOutput.TerminalID,
			Data:       metaObj.TerminalOutput.Data,
		}
	}
	return nil
}

// extractTerminalExit 从 _meta 扩展元数据中提取终端退出信息。
// Codex/Claude Code 在命令执行完成后通过
// _meta.terminal_exit 传递退出码和信号。
// Lzm 2026-07-22
func extractTerminalExit(meta json.RawMessage) *internal.TerminalExit {
	if meta == nil {
		return nil
	}

	var metaObj struct {
		TerminalExit *struct {
			TerminalID string `json:"terminal_id"`
			ExitCode   *int   `json:"exit_code"`
			Signal     string `json:"signal"`
		} `json:"terminal_exit"`
	}
	if err := json.Unmarshal(meta, &metaObj); err == nil && metaObj.TerminalExit != nil {
		return &internal.TerminalExit{
			TerminalID: metaObj.TerminalExit.TerminalID,
			ExitCode:   metaObj.TerminalExit.ExitCode,
			Signal:     metaObj.TerminalExit.Signal,
		}
	}
	return nil
}

// extractTerminalMeta 从 _meta 扩展元数据中提取终端事件信息。
// 组合提取 terminal_output 和 terminal_exit，统一返回 TerminalEvent。
// Lzm 2026-07-22
func extractTerminalMeta(meta json.RawMessage) *internal.TerminalEvent {
	if meta == nil {
		return nil
	}

	event := &internal.TerminalEvent{}
	hasData := false

	if output := extractTerminalOutput(meta); output != nil {
		event.Output = output
		hasData = true
	}
	if exit := extractTerminalExit(meta); exit != nil {
		event.Exit = exit
		hasData = true
	}

	if !hasData {
		return nil
	}
	return event
}

// parseCodexLikeStreamUpdate 解析 Codex codex-acp 的 session/update 格式
// Codex 使用 "type" 字段替代标准 "sessionUpdate" 字段
// 格式：{"sessionId": "...", "update": {"type": "response|final|error|thought", "content": {"text": "..."}}}
// Lzm 2026-07-21
func parseCodexLikeStreamUpdate(update ACPStreamUpdateCodexLike) *internal.StreamChunk {
	rawType := update.Update.Type
	text := ""
	if update.Update.Content != nil {
		var content StreamContent
		if err := json.Unmarshal(update.Update.Content, &content); err == nil {
			text = content.Text
		} else {
			var rawStr string
			if err := json.Unmarshal(update.Update.Content, &rawStr); err == nil {
				text = rawStr
			}
		}
	}

	isFinal := rawType == "final"
	isError := rawType == "error"
	isThought := rawType == "thought" || rawType == "thinking"

	chunkType := internal.StreamChunkResponse
	if isFinal {
		chunkType = internal.StreamChunkFinal
	} else if isError {
		chunkType = internal.StreamChunkError
	} else if isThought {
		chunkType = internal.StreamChunkThought
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
	return chunk
}
