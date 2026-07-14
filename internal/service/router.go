// -*- coding: utf-8 -*-
// Go 1.25+
//
// router.go
// 消息路由器 — 将 ANP 消息路由到对应 Agent 并处理响应
// 支持：invoke、ping、sessions/list 等方法
//
// Lzm 2026-07-09

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// RequestRouter ANP 消息路由器
type RequestRouter struct {
	registry        *agent.AgentRegistry
	streamCB        StreamCallback      // 流式推送回调（由 TunnelService 设置）
	finalResponseCB InvokeFinalCallback // 流式最终响应回调（由 TunnelService 设置）
}

// NewRequestRouter 创建消息路由器
func NewRequestRouter(registry *agent.AgentRegistry) *RequestRouter {
	return &RequestRouter{
		registry: registry,
	}
}

// SetStreamCallback 设置流式推送回调
// Lzm 2026-07-09
func (r *RequestRouter) SetStreamCallback(cb StreamCallback) {
	r.streamCB = cb
}

// SetFinalResponseCallback 设置流式调用最终响应回调
// Lzm 2026-07-13
func (r *RequestRouter) SetFinalResponseCallback(cb InvokeFinalCallback) {
	r.finalResponseCB = cb
}

// Route 将 ANP 消息路由到对应处理器，返回响应消息
// 返回 nil 表示无需响应（如流式结果已通过 WebSocket 推送）
// Lzm 2026-07-09
func (r *RequestRouter) Route(ctx context.Context, msg *protocol.ANPMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	switch {
	case msg.Method == "ping":
		return r.handlePing(msg)
	case msg.Method == "sessions/list":
		return r.handleSessionsList(msg, sessionMgr)
	case msg.Method == "sessions/messages":
		return r.HandleSessionMessages(msg, sessionMgr)
	case msg.Method == "invoke":
		return r.handleInvoke(ctx, msg, sessionMgr)
	default:
		return protocol.NewErrorResponse(msg.ID, -32601, fmt.Sprintf("未知方法: %s", msg.Method))
	}
}

// handlePing 处理 ping 请求
func (r *RequestRouter) handlePing(msg *protocol.ANPMessage) *protocol.ANPMessage {
	return protocol.NewResultResponse(msg.ID, json.RawMessage(`"pong"`))
}

// handleSessionsList 处理会话列表查询
// 支持 agent_id 过滤，返回历史会话列表（含消息数）
// Lzm 2026-07-10
func (r *RequestRouter) handleSessionsList(msg *protocol.ANPMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var filter struct {
		AgentID string `json:"agent_id"`
	}
	json.Unmarshal(msg.Params, &filter)

	type responseItem struct {
		AgentID      string `json:"agent_id"`
		SessionID    string `json:"session_id"`
		MessageCount int    `json:"message_count,omitempty"`
		UpdatedAt    int64  `json:"updated_at,omitempty"`
	}

	var items []responseItem

	if filter.AgentID != "" {
		// 查询指定 Agent 的历史会话
		sessions := sessionMgr.ListSessions(filter.AgentID, 50)
		for _, s := range sessions {
			items = append(items, responseItem{
				AgentID:      filter.AgentID,
				SessionID:    s.SessionID,
				MessageCount: s.MessageCount,
				UpdatedAt:    s.UpdatedAt,
			})
		}
		// 同时包含当前活跃会话
		if sid := sessionMgr.GetSession(filter.AgentID); sid != "" {
			items = append(items, responseItem{
				AgentID:   filter.AgentID,
				SessionID: sid,
			})
		}
	} else {
		// 查询所有 Agent 的会话
		all := sessionMgr.ListAllSessions()
		for agentID, sessions := range all {
			for _, s := range sessions {
				items = append(items, responseItem{
					AgentID:      agentID,
					SessionID:    s.SessionID,
					MessageCount: s.MessageCount,
					UpdatedAt:    s.UpdatedAt,
				})
			}
		}
		// 也包含活跃会话
		for _, agentID := range r.registry.IDs() {
			if sid := sessionMgr.GetSession(agentID); sid != "" {
				items = append(items, responseItem{
					AgentID:   agentID,
					SessionID: sid,
				})
			}
		}
	}

	// 确保 nil → [] 避免 JSON 输出 null
	if items == nil {
		items = make([]responseItem, 0)
	}

	result, _ := json.Marshal(items)
	return protocol.NewResultResponse(msg.ID, result)
}

// handleInvoke 处理 invoke 请求 — 将请求转发到指定 Agent
// Lzm 2026-07-09
func (r *RequestRouter) handleInvoke(ctx context.Context, msg *protocol.ANPMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	// 解析 invoke 参数
	var params protocol.ANPInvokeParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析 invoke 参数失败: %v", err))
	}

	// 查找 Agent
	a := r.registry.Get(params.AgentID)
	if a == nil {
		return protocol.NewErrorResponse(msg.ID, -31001,
			fmt.Sprintf("未知 Agent: %s", params.AgentID))
	}

	// 确保 Agent 已启动
	if a.Status() == agent.AgentDisconnected {
		if err := a.Start(ctx); err != nil {
			return protocol.NewErrorResponse(msg.ID, -31002,
				fmt.Sprintf("启动 Agent %s 失败: %v", params.AgentID, err))
		}
	}

	// 根据请求的方法分发
	switch params.Method {
	case "session/new":
		return r.handleInvokeSessionNew(ctx, msg, a, sessionMgr)
	case "session/load":
		return r.handleInvokeSessionLoad(ctx, msg, a, params.Params, sessionMgr)
	case "session/prompt":
		return r.handleInvokeSessionPrompt(ctx, msg, a, params.Params, params.Stream, sessionMgr)
	default:
		return protocol.NewErrorResponse(msg.ID, -31003,
			fmt.Sprintf("Agent %s 不支持方法: %s", params.AgentID, params.Method))
	}
}

// handleInvokeSessionNew 处理创建会话的 invoke 请求
func (r *RequestRouter) handleInvokeSessionNew(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, sessionMgr *SessionManager) *protocol.ANPMessage {
	sid, err := sessionMgr.GetOrCreateSession(ctx, a.ID())
	if err != nil {
		return protocol.NewErrorResponse(msg.ID, -31004,
			fmt.Sprintf("创建会话失败: %v", err))
	}

	result, _ := json.Marshal(map[string]string{
		"sessionId": sid,
	})
	return protocol.NewResultResponse(msg.ID, result)
}

// handleInvokeSessionLoad 处理加载会话的 invoke 请求
// 加载 ACP 会话 + 返回本地持久化的历史消息
// Lzm 2026-07-10
func (r *RequestRouter) handleInvokeSessionLoad(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, params json.RawMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var loadParams struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &loadParams); err != nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析 session/load 参数失败: %v", err))
	}

	if err := a.LoadSession(ctx, loadParams.SessionID); err != nil {
		return protocol.NewErrorResponse(msg.ID, -31005,
			fmt.Sprintf("加载会话 %s 失败: %v", loadParams.SessionID, err))
	}

	// 记录到会话管理器
	sessionMgr.GetOrCreateSession(ctx, a.ID()) // 会跳过已记录的

	// 加载本地持久化的历史消息
	messages := sessionMgr.LoadMessages(a.ID(), loadParams.SessionID)
	if messages == nil {
		messages = []StoredMessage{}
	}

	result, _ := json.Marshal(map[string]interface{}{
		"status":    "ok",
		"sessionId": loadParams.SessionID,
		"messages":  messages,
	})
	return protocol.NewResultResponse(msg.ID, result)
}

// handleInvokeSessionPrompt 处理 prompt 请求 — 最核心的交互
// 远程服务发来 prompt，Bridge 转发到 Agent，流式回传结果
// 支持：消息自动持久化、流式推送、EPERM/Session失效重试
// Lzm 2026-07-10
func (r *RequestRouter) handleInvokeSessionPrompt(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, params json.RawMessage, stream bool, sessionMgr *SessionManager) *protocol.ANPMessage {
	// 解析 prompt 参数
	var promptParams struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	if err := json.Unmarshal(params, &promptParams); err != nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析 prompt 参数失败: %v", err))
	}

	// 提取用户文本
	var userText string
	for _, p := range promptParams.Prompt {
		if p.Type == "text" {
			userText += p.Text
		}
	}

	// 获取或创建会话
	sessionID := promptParams.SessionID
	agentID := a.ID()
	if sessionID == "" {
		var err error
		sessionID, err = sessionMgr.GetOrCreateSession(ctx, agentID)
		if err != nil {
			return protocol.NewErrorResponse(msg.ID, -31006,
				fmt.Sprintf("获取会话失败: %v", err))
		}
	}

	isStream := wantsStreaming(stream, msg.ID)

	if isStream {
		return r.streamPrompt(ctx, msg, a, agentID, sessionID, userText, sessionMgr)
	}

	// 非流式模式：等待完整响应（带重试）
	return r.blockingPrompt(ctx, msg, a, agentID, sessionID, userText, sessionMgr)
}

func wantsStreaming(explicit bool, requestID string) bool {
	if explicit {
		return true
	}
	// 请求 ID 后缀只为旧客户端保留；新客户端应显式传 stream。
	return strings.Contains(requestID, "_stream") || strings.Contains(requestID, "_bridge")
}

// streamPrompt 流式 prompt 处理 — 推送流式块 + 自动保存消息
// Lzm 2026-07-10
func (r *RequestRouter) streamPrompt(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, agentID, sessionID, userText string, sessionMgr *SessionManager) *protocol.ANPMessage {
	// 构建 ACP prompt 请求
	acpReq := r.buildACPPromptReq(msg.ID, sessionID, userText)

	chunkCh, err := a.Stream(ctx, acpReq)
	if err != nil {
		return protocol.NewErrorResponse(msg.ID, -31007,
			fmt.Sprintf("发送 prompt 到 Agent 失败: %v", err))
	}

	if r.streamCB == nil {
		slog.Warn("流式回调未设置，无法推送流式结果")
		return nil
	}

	// 在后台读取流式块，收集消息用于持久化
	go func() {
		var thoughtParts, responseParts []string
		retrySession := false

		// 检查第一个块，如果是 session 错误则自动重试
		select {
		case firstChunk, ok := <-chunkCh:
			if !ok {
				return
			}
			// 如果第一个块是 error 类型，说明 session 失效，自动重试
			if firstChunk.Type == internal.StreamChunkError || firstChunk.Type.String() == "error" {
				slog.Warn("Session 失效，自动创建新会话重试",
					"agent", agentID, "old_session", sessionID,
				)
				retrySession = true
				// 通知前端 session 已失效
				r.streamCB(msg.ID, "session_invalid", sessionID)

				// 创建新会话并重试
				if newSID, err := sessionMgr.GetOrCreateSession(ctx, agentID); err == nil {
					sessionID = newSID
					// 通知前端新 session ID
					r.streamCB(msg.ID, "session_refreshed", newSID)

					acpReq = r.buildACPPromptReq(msg.ID, sessionID, userText)
					var retryErr error
					chunkCh, retryErr = a.Stream(ctx, acpReq)
					if retryErr != nil {
						r.streamCB(msg.ID, "error", "重试失败: "+retryErr.Error())
						if r.finalResponseCB != nil {
							r.finalResponseCB(msg.ID, nil, "重试失败: "+retryErr.Error())
						}
						return
					}
				} else {
					r.streamCB(msg.ID, "error", "创建新会话失败: "+err.Error())
					if r.finalResponseCB != nil {
						r.finalResponseCB(msg.ID, nil, "创建新会话失败: "+err.Error())
					}
					return
				}
			} else {
				// 第一个块正常，处理它
				chunkType := firstChunk.Type.String()
				chunkText := firstChunk.Text
				switch firstChunk.Type {
				case internal.StreamChunkThought:
					thoughtParts = append(thoughtParts, chunkText)
				case internal.StreamChunkResponse:
					responseParts = append(responseParts, chunkText)
				}
				r.streamCB(msg.ID, chunkType, chunkText)
			}
		case <-ctx.Done():
			slog.Warn("流式上下文取消", "request_id", msg.ID)
			return
		}

		if !retrySession {
			// 继续消费剩余块
			for chunk := range chunkCh {
				chunkType := chunk.Type.String()
				chunkText := chunk.Text

				switch chunk.Type {
				case internal.StreamChunkThought:
					thoughtParts = append(thoughtParts, chunkText)
				case internal.StreamChunkResponse:
					responseParts = append(responseParts, chunkText)
				}

				if err := r.streamCB(msg.ID, chunkType, chunkText); err != nil {
					slog.Error("流式推送失败", "request_id", msg.ID, "error", err)
					return
				}
			}
		} else {
			// 消费重试的块
			for chunk := range chunkCh {
				chunkType := chunk.Type.String()
				chunkText := chunk.Text

				switch chunk.Type {
				case internal.StreamChunkThought:
					thoughtParts = append(thoughtParts, chunkText)
				case internal.StreamChunkResponse:
					responseParts = append(responseParts, chunkText)
				}

				if err := r.streamCB(msg.ID, chunkType, chunkText); err != nil {
					slog.Error("流式推送失败", "request_id", msg.ID, "error", err)
					return
				}
			}
		}

		// 流式完成后，自动保存消息
		r.persistPromptMessages(sessionMgr, agentID, sessionID, userText, thoughtParts, responseParts)

		// 发送 invoke 最终响应（告知远程服务调用完成）
		if r.finalResponseCB != nil {
			fullText := strings.Join(responseParts, "")
			if fullText != "" {
				resultBytes, _ := json.Marshal(map[string]string{"text": fullText})
				r.finalResponseCB(msg.ID, resultBytes, "")
			} else {
				r.finalResponseCB(msg.ID, nil, "")
			}
		}
	}()

	// 流式模式返回 nil，结果通过回调推送
	return nil
}

// blockingPrompt 非流式 prompt 处理 — 等待完整响应 + 自动保存消息
// Lzm 2026-07-10
func (r *RequestRouter) blockingPrompt(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, agentID, sessionID, userText string, sessionMgr *SessionManager) *protocol.ANPMessage {
	acpReq := r.buildACPPromptReq(msg.ID, sessionID, userText)

	resp, err := a.Send(ctx, acpReq)
	if err != nil {
		return protocol.NewErrorResponse(msg.ID, -31008,
			fmt.Sprintf("Agent 响应错误: %v", err))
	}

	// 自动保存消息（非流式模式尝试从响应中提取文本）
	if resp.IsSuccess() {
		r.persistResponseMessage(sessionMgr, agentID, sessionID, userText, resp)
		return protocol.NewResultResponse(msg.ID, resp.Result)
	}

	if resp.Error != nil {
		return protocol.NewErrorResponse(msg.ID, resp.Error.Code,
			resp.Error.Message)
	}

	return protocol.NewErrorResponse(msg.ID, -31009, "Agent 返回未知响应")
}

// buildACPPromptReq 构建 ACP session/prompt 请求
// Lzm 2026-07-10
func (r *RequestRouter) buildACPPromptReq(requestID, sessionID, userText string) *protocol.ACPMessage {
	return &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      requestID,
		Method:  "session/prompt",
		Params: func() json.RawMessage {
			data, _ := json.Marshal(map[string]interface{}{
				"sessionId": sessionID,
				"prompt": []map[string]string{
					{"type": "text", "text": userText},
				},
			})
			return data
		}(),
	}
}

// persistPromptMessages 保存流式 prompt 的消息到持久化存储
// Lzm 2026-07-10
func (r *RequestRouter) persistPromptMessages(sessionMgr *SessionManager, agentID, sessionID, userText string, thoughtParts, responseParts []string) {
	messages := make([]StoredMessage, 0, 3)

	if userText != "" {
		messages = append(messages, StoredMessage{Role: "user", Text: userText})
	}

	thoughtText := strings.Join(thoughtParts, "")
	if thoughtText != "" {
		messages = append(messages, StoredMessage{Role: "thought", Text: thoughtText})
	}

	responseText := strings.Join(responseParts, "")
	if responseText != "" {
		messages = append(messages, StoredMessage{Role: "assistant", Text: responseText})
	}

	if len(messages) > 0 {
		sessionMgr.SaveMessages(agentID, sessionID, messages)
		slog.Debug("流式消息已自动保存",
			"agent", agentID,
			"session", sessionID[:16]+"...",
			"messages", len(messages),
		)
	}
}

// persistResponseMessage 保存非流式响应到持久化存储
// Lzm 2026-07-10
func (r *RequestRouter) persistResponseMessage(sessionMgr *SessionManager, agentID, sessionID, userText string, resp *protocol.ACPMessage) {
	messages := make([]StoredMessage, 0, 2)

	if userText != "" {
		messages = append(messages, StoredMessage{Role: "user", Text: userText})
	}

	// 尝试从 result 中提取响应文本
	if resp.Result != nil {
		var resultMap map[string]interface{}
		if data, err := json.Marshal(resp.Result); err == nil {
			if err := json.Unmarshal(data, &resultMap); err == nil {
				if text, ok := resultMap["text"].(string); ok && text != "" {
					messages = append(messages, StoredMessage{Role: "assistant", Text: text})
				}
			}
		}
	}

	if len(messages) > 0 {
		sessionMgr.SaveMessages(agentID, sessionID, messages)
	}
}

// HandleSessionMessages 处理会话消息查询（WebSocket API）
// 请求：{id, method:"sessions/messages", params:{agent_id, session_id, cursor?, limit?}}
// 响应：{id, result:{messages: [{role, text}, ...], total, cursor}}
// Lzm 2026-07-10
func (r *RequestRouter) HandleSessionMessages(msg *protocol.ANPMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var params struct {
		AgentID   string `json:"agent_id"`
		SessionID string `json:"session_id"`
		Cursor    int    `json:"cursor"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析参数失败: %v", err))
	}

	if params.AgentID == "" || params.SessionID == "" {
		return protocol.NewErrorResponse(msg.ID, -32602, "缺少 agent_id 或 session_id")
	}

	// 加载消息
	messages := sessionMgr.LoadMessages(params.AgentID, params.SessionID)
	if messages == nil {
		// 尝试通过 ACP 加载
		a := r.registry.Get(params.AgentID)
		if a != nil {
			ctx := context.Background()
			if err := a.LoadSession(ctx, params.SessionID); err == nil {
				// 重新读取（ACP 可能已将消息写入文件）
				messages = sessionMgr.LoadMessages(params.AgentID, params.SessionID)
			}
		}
	}

	if messages == nil {
		messages = []StoredMessage{}
	}

	// 分页
	limit := params.Limit
	if limit <= 0 {
		limit = 100
	}
	total := len(messages)
	start := params.Cursor
	if start >= total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}

	result, _ := json.Marshal(map[string]interface{}{
		"messages": messages[start:end],
		"total":    total,
		"cursor":   end,
	})

	return protocol.NewResultResponse(msg.ID, result)
}

// StreamCallback 流式推送回调（由 TunnelService 设置）
type StreamCallback func(requestID string, chunkType string, text string) error

// InvokeFinalCallback 流式调用最终响应回调（由 TunnelService 设置）
// 在流式输出全部完成后，发送 invoke 的最终 JSON-RPC 结果
type InvokeFinalCallback func(requestID string, result json.RawMessage, errMsg string)
