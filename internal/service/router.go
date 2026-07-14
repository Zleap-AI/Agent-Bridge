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
	"sort"
	"strings"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

const (
	// MaxStreamOutputBytes bounds the combined reasoning and response text kept
	// in memory and persisted for one streamed Agent call. With JSON's maximum
	// string escaping expansion, one forwarded chunk remains below the ANP
	// Device-message limit.
	MaxStreamOutputBytes = 2 * 1024 * 1024
	maxSessionsPerAgent  = 50
	maxMessageRoleBytes  = 32
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
		return r.handleSessionMessages(ctx, msg, sessionMgr)
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

type sessionListItem struct {
	AgentID      string `json:"agent_id"`
	SessionID    string `json:"session_id"`
	MessageCount int    `json:"message_count,omitempty"`
	UpdatedAt    int64  `json:"updated_at,omitempty"`
}

func buildSessionListResponse(requestID string, items []sessionListItem) *protocol.ANPMessage {
	emptyItems := make([]sessionListItem, 0)
	emptyResult, _ := json.Marshal(emptyItems)
	emptyResponse := protocol.NewResultResponse(requestID, emptyResult)
	emptyWire, err := json.Marshal(emptyResponse)
	if err != nil {
		return protocol.NewErrorResponse(requestID, -32603, "Session list cannot be encoded")
	}
	encodedItemsBytes := 0
	for index, item := range items {
		if len(item.AgentID)+len(item.SessionID) > MaxStreamOutputBytes {
			return sessionListTooLarge(requestID)
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return protocol.NewErrorResponse(requestID, -32603, "Session list item cannot be encoded")
		}
		encodedItemsBytes += len(encoded)
		if index > 0 {
			encodedItemsBytes++
		}
		if len(emptyWire)+encodedItemsBytes > protocol.MaxANPDeviceMessageBytes {
			return sessionListTooLarge(requestID)
		}
	}
	result, _ := json.Marshal(items)
	return protocol.NewResultResponse(requestID, result)
}

func sessionListTooLarge(requestID string) *protocol.ANPMessage {
	return protocol.NewErrorResponse(requestID, protocol.ANPErrorResponseTooLarge,
		fmt.Sprintf("Session list exceeds the %d-byte Device response limit; filter by agent_id", protocol.MaxANPDeviceMessageBytes))
}

// handleSessionsList 处理会话列表查询
// 支持 agent_id 过滤，返回历史会话列表（含消息数）
// Lzm 2026-07-10
func (r *RequestRouter) handleSessionsList(msg *protocol.ANPMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var filter struct {
		AgentID string `json:"agent_id"`
	}
	json.Unmarshal(msg.Params, &filter)

	if filter.AgentID != "" {
		return buildSessionListResponse(msg.ID, listAgentSessions(filter.AgentID, sessionMgr))
	}

	agentIDs := make(map[string]struct{})
	for agentID := range sessionMgr.ListAllSessions() {
		agentIDs[agentID] = struct{}{}
	}
	for _, agentID := range r.registry.IDs() {
		agentIDs[agentID] = struct{}{}
	}
	orderedAgentIDs := make([]string, 0, len(agentIDs))
	for agentID := range agentIDs {
		orderedAgentIDs = append(orderedAgentIDs, agentID)
	}
	sort.Strings(orderedAgentIDs)

	items := make([]sessionListItem, 0)
	for _, agentID := range orderedAgentIDs {
		items = append(items, listAgentSessions(agentID, sessionMgr)...)
	}
	return buildSessionListResponse(msg.ID, items)
}

func listAgentSessions(agentID string, sessionMgr *SessionManager) []sessionListItem {
	byID := make(map[string]sessionListItem)
	add := func(item sessionListItem) {
		if item.SessionID == "" {
			return
		}
		if current, exists := byID[item.SessionID]; exists {
			if item.MessageCount > current.MessageCount {
				current.MessageCount = item.MessageCount
			}
			if item.UpdatedAt > current.UpdatedAt {
				current.UpdatedAt = item.UpdatedAt
			}
			byID[item.SessionID] = current
			return
		}
		byID[item.SessionID] = item
	}

	for _, session := range sessionMgr.ListSessions(agentID, maxSessionsPerAgent) {
		add(sessionListItem{
			AgentID:      agentID,
			SessionID:    session.SessionID,
			MessageCount: session.MessageCount,
			UpdatedAt:    session.UpdatedAt,
		})
	}
	if nativeSessions, err := agent.DiscoverHistoricalSessions(agentID, maxSessionsPerAgent); err == nil {
		for _, session := range nativeSessions {
			add(sessionListItem{AgentID: agentID, SessionID: session.NativeID, UpdatedAt: session.StartedAt})
		}
	} else {
		slog.Debug("未扫描到 Agent 原生 Session", "agent", agentID, "error", err)
	}
	activeSessionID := sessionMgr.GetSession(agentID)
	add(sessionListItem{AgentID: agentID, SessionID: activeSessionID})

	items := make([]sessionListItem, 0, len(byID))
	for _, item := range byID {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt == items[j].UpdatedAt {
			return items[i].SessionID < items[j].SessionID
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	if len(items) <= maxSessionsPerAgent {
		return items
	}
	items = items[:maxSessionsPerAgent]
	if activeSessionID == "" {
		return items
	}
	for _, item := range items {
		if item.SessionID == activeSessionID {
			return items
		}
	}
	items[len(items)-1] = byID[activeSessionID]
	return items
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

	// 确保 Agent 已启动。SessionManager 的内存状态可能跨过一次 Agent
	// 子进程重启，因此后续显式 Session 调用需要强制重新加载。
	agentRestarted, err := ensureAgentStarted(ctx, a)
	if err != nil {
		return protocol.NewErrorResponse(msg.ID, -31002,
			fmt.Sprintf("启动 Agent %s 失败: %v", params.AgentID, err))
	}

	// 根据请求的方法分发
	switch params.Method {
	case "session/new":
		return r.handleInvokeSessionNew(ctx, msg, a, sessionMgr)
	case "session/load":
		return r.handleInvokeSessionLoad(ctx, msg, a, params.Params, sessionMgr)
	case "session/prompt":
		return r.handleInvokeSessionPrompt(ctx, msg, a, params.Params, params.Stream, agentRestarted, sessionMgr)
	default:
		return protocol.NewErrorResponse(msg.ID, -31003,
			fmt.Sprintf("Agent %s 不支持方法: %s", params.AgentID, params.Method))
	}
}

// handleInvokeSessionNew 处理创建会话的 invoke 请求
func (r *RequestRouter) handleInvokeSessionNew(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, sessionMgr *SessionManager) *protocol.ANPMessage {
	sid, err := sessionMgr.CreateNewSession(ctx, a.ID())
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

	if _, err := NewSessionLoadReplayer(r.registry, sessionMgr.msgStore).LoadAndSaveSession(ctx, a.ID(), loadParams.SessionID); err != nil {
		return protocol.NewErrorResponse(msg.ID, -31005,
			fmt.Sprintf("加载会话 %s 失败: %v", loadParams.SessionID, err))
	}

	// 记录显式加载的会话，不触发恢复或创建其他会话。
	sessionMgr.ActivateSession(a.ID(), loadParams.SessionID)

	messages := sessionMgr.LoadMessages(a.ID(), loadParams.SessionID)
	if messages == nil {
		messages = []StoredMessage{}
	}
	return buildSessionLoadResponse(msg.ID, loadParams.SessionID, messages)
}

type sessionLoadResult struct {
	Status    string          `json:"status"`
	SessionID string          `json:"sessionId"`
	Messages  []StoredMessage `json:"messages"`
}

func buildSessionLoadResponse(requestID, sessionID string, messages []StoredMessage) *protocol.ANPMessage {
	empty := newSessionLoadResponse(requestID, sessionID, messages[:0])
	emptyWire, err := json.Marshal(empty)
	if err != nil {
		return protocol.NewErrorResponse(requestID, -32603, "session/load response cannot be encoded")
	}
	encodedMessagesBytes := 0
	for index, message := range messages {
		if len(message.Role) > maxMessageRoleBytes || len(message.Text) > protocol.MaxANPDeviceMessageBytes {
			return sessionLoadTooLarge(requestID, index)
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			return protocol.NewErrorResponse(requestID, -32603, "session/load Message cannot be encoded")
		}
		encodedMessagesBytes += len(encoded)
		if index > 0 {
			encodedMessagesBytes++
		}
		if len(emptyWire)+encodedMessagesBytes > protocol.MaxANPDeviceMessageBytes {
			return sessionLoadTooLarge(requestID, index)
		}
	}
	return newSessionLoadResponse(requestID, sessionID, messages)
}

func newSessionLoadResponse(requestID, sessionID string, messages []StoredMessage) *protocol.ANPMessage {
	result, _ := json.Marshal(sessionLoadResult{Status: "ok", SessionID: sessionID, Messages: messages})
	return protocol.NewResultResponse(requestID, result)
}

func sessionLoadTooLarge(requestID string, messageIndex int) *protocol.ANPMessage {
	return protocol.NewErrorResponse(requestID, protocol.ANPErrorResponseTooLarge,
		fmt.Sprintf("session/load history exceeds the %d-byte Device response limit at Message %d; use sessions/messages cursor pagination", protocol.MaxANPDeviceMessageBytes, messageIndex))
}

// handleInvokeSessionPrompt 处理 prompt 请求 — 最核心的交互
// 远程服务发来 prompt，Bridge 转发到 Agent，流式回传结果
// 支持：消息自动持久化、流式推送、EPERM/Session失效重试
// Lzm 2026-07-10
func (r *RequestRouter) handleInvokeSessionPrompt(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, params json.RawMessage, stream, agentRestarted bool, sessionMgr *SessionManager) *protocol.ANPMessage {
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
		// Old/internal callers may omit sessionId. The in-memory Session belongs
		// to the previous process generation after a restart, so invalidate it and
		// let GetOrCreateSession restore the persisted Session (or create a fresh
		// one when the Agent reports that the old Session has expired).
		if agentRestarted {
			sessionMgr.ReleaseSession(agentID)
		}

		var err error
		sessionID, err = sessionMgr.GetOrCreateSession(ctx, agentID)
		if err != nil {
			return protocol.NewErrorResponse(msg.ID, -31006,
				fmt.Sprintf("获取会话失败: %v", err))
		}
	} else if agentRestarted || sessionMgr.GetSession(agentID) != sessionID {
		// A selected historical Session must be restored before its next Message.
		// Keeping this rule in the Router covers both Consoles and every Caller API
		// client, without requiring callers to know ACP's session/load ordering.
		if err := a.LoadSession(ctx, sessionID); err != nil {
			return protocol.NewErrorResponse(msg.ID, -31005,
				fmt.Sprintf("加载会话 %s 失败: %v", sessionID, err))
		}
		sessionMgr.ActivateSession(agentID, sessionID)
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

func isInvalidSessionStreamError(chunk internal.StreamChunk) bool {
	message := chunk.Text
	if message == "" && chunk.Error != nil {
		message = chunk.Error.Message
	}
	message = strings.ToLower(message)
	for _, marker := range []string{
		"invalid session",
		"session invalid",
		"session not found",
		"unknown session",
		"session expired",
		"session does not exist",
		"no such session",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
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
		slog.Warn("流式回调未设置，结果将仅保存到本地会话")
	}

	// 在后台读取流式块，收集消息用于持久化
	go func() {
		var thoughtParts, responseParts []string
		streamOutputBytes := 0
		outputLimitExceeded := false
		forwarding := r.streamCB != nil
		forward := func(chunkType, chunkText string) {
			if !forwarding {
				return
			}
			if err := r.streamCB(msg.ID, chunkType, chunkText); err != nil {
				forwarding = false
				slog.Warn("流式连接已断开，继续在本地完成调用",
					"request_id", msg.ID,
					"error", err,
				)
			}
		}
		handleChunk := func(chunk internal.StreamChunk) bool {
			if outputLimitExceeded {
				// Keep draining the Agent stream so its producer cannot block after
				// the request has already failed at the output boundary.
				return true
			}
			chunkType := chunk.Type.String()
			chunkText := chunk.Text
			if chunk.Type == internal.StreamChunkError || chunkType == "error" {
				if chunkText == "" && chunk.Error != nil {
					chunkText = chunk.Error.Message
				}
				if chunkText == "" {
					chunkText = "Agent 流式调用失败"
				}
				r.persistPromptMessages(sessionMgr, agentID, sessionID, userText, thoughtParts, responseParts)
				forward("error", chunkText)
				if r.finalResponseCB != nil {
					r.finalResponseCB(msg.ID, nil, &protocol.ANPError{Code: -31008, Message: chunkText})
				}
				return false
			}
			if chunk.Type == internal.StreamChunkFinal && len(responseParts) != 0 {
				// Some adapters repeat the complete response in final after already
				// sending deltas. It is neither forwarded nor counted twice.
				chunkText = ""
			}
			if chunk.Type == internal.StreamChunkThought || chunk.Type == internal.StreamChunkResponse || chunk.Type == internal.StreamChunkFinal {
				if len(chunkText) > MaxStreamOutputBytes-streamOutputBytes {
					message := fmt.Sprintf("Agent output exceeded the %d-byte limit", MaxStreamOutputBytes)
					r.persistPromptMessages(sessionMgr, agentID, sessionID, userText, thoughtParts, responseParts)
					if r.finalResponseCB != nil {
						r.finalResponseCB(msg.ID, nil, &protocol.ANPError{Code: protocol.ANPErrorResponseTooLarge, Message: message})
					} else {
						// Legacy stream-only integrations have no structured completion
						// channel, so retain their explicit error update fallback.
						forward("error", message)
					}
					outputLimitExceeded = true
					return true
				}
				streamOutputBytes += len(chunkText)
			}
			switch chunk.Type {
			case internal.StreamChunkThought:
				thoughtParts = append(thoughtParts, chunkText)
			case internal.StreamChunkResponse:
				responseParts = append(responseParts, chunkText)
			case internal.StreamChunkFinal:
				if len(responseParts) == 0 && chunkText != "" {
					responseParts = append(responseParts, chunkText)
				}
			}
			forward(chunkType, chunkText)
			return true
		}

		// 检查第一个块，如果是 session 错误则自动重试
		firstChunk, ok := <-chunkCh
		if !ok {
			r.finalizeStream(sessionMgr, agentID, sessionID, userText, thoughtParts, responseParts, msg.ID)
			return
		}
		// 仅明确的 Session 失效错误可以安全地创建新会话重试。
		// 认证、限流、模型错误等不得重复执行 prompt。
		if (firstChunk.Type == internal.StreamChunkError || firstChunk.Type.String() == "error") && isInvalidSessionStreamError(firstChunk) {
			slog.Warn("Session 失效，自动创建新会话重试",
				"agent", agentID, "old_session", sessionID,
			)
			// 通知前端 session 已失效
			forward("session_invalid", sessionID)

			// 创建新会话并重试
			if newSID, err := sessionMgr.CreateNewSession(ctx, agentID); err == nil {
				sessionID = newSID
				// 通知前端新 session ID
				forward("session_refreshed", newSID)

				acpReq = r.buildACPPromptReq(msg.ID, sessionID, userText)
				var retryErr error
				chunkCh, retryErr = a.Stream(ctx, acpReq)
				if retryErr != nil {
					forward("error", "重试失败: "+retryErr.Error())
					if r.finalResponseCB != nil {
						r.finalResponseCB(msg.ID, nil, &protocol.ANPError{Code: -31008, Message: "重试失败: " + retryErr.Error()})
					}
					return
				}
			} else {
				forward("error", "创建新会话失败: "+err.Error())
				if r.finalResponseCB != nil {
					r.finalResponseCB(msg.ID, nil, &protocol.ANPError{Code: -31008, Message: "创建新会话失败: " + err.Error()})
				}
				return
			}
		} else {
			if !handleChunk(firstChunk) {
				return
			}
		}

		for chunk := range chunkCh {
			if !handleChunk(chunk) {
				return
			}
		}
		if outputLimitExceeded {
			return
		}

		r.finalizeStream(sessionMgr, agentID, sessionID, userText, thoughtParts, responseParts, msg.ID)
	}()

	// 流式模式返回 nil，结果通过回调推送
	return nil
}

func (r *RequestRouter) finalizeStream(sessionMgr *SessionManager, agentID, sessionID, userText string, thoughtParts, responseParts []string, requestID string) {
	r.persistPromptMessages(sessionMgr, agentID, sessionID, userText, thoughtParts, responseParts)
	if r.finalResponseCB == nil {
		return
	}
	result := json.RawMessage(`{}`)
	if fullText := strings.Join(responseParts, ""); fullText != "" {
		result, _ = json.Marshal(map[string]string{"text": fullText})
	}
	r.finalResponseCB(requestID, result, nil)
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
			"session", truncateString(sessionID, 16),
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

// handleSessionMessages 处理会话消息查询（WebSocket API）
// 请求：{id, method:"sessions/messages", params:{agent_id, session_id, cursor?, limit?}}
// 响应：{id, result:{messages: [{role, text}, ...], total, cursor}}
// Lzm 2026-07-10
func (r *RequestRouter) handleSessionMessages(ctx context.Context, msg *protocol.ANPMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
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
	if params.Cursor < 0 {
		return protocol.NewErrorResponse(msg.ID, -32602, "cursor 不能小于 0")
	}

	// 加载消息。已持久化的空 Session 是合法的；只有本地无任何记录时
	// 才向 Agent 验证 Session，避免把“空历史”误报为不存在。
	messages := sessionMgr.LoadMessages(params.AgentID, params.SessionID)
	if messages == nil && !sessionMgr.SessionExists(params.AgentID, params.SessionID) {
		a := r.registry.Get(params.AgentID)
		if a == nil {
			return protocol.NewErrorResponse(msg.ID, -31001,
				fmt.Sprintf("未知 Agent: %s", params.AgentID))
		}
		if _, err := ensureAgentStarted(ctx, a); err != nil {
			return protocol.NewErrorResponse(msg.ID, -31002,
				fmt.Sprintf("启动 Agent %s 失败: %v", params.AgentID, err))
		}
		if _, err := NewSessionLoadReplayer(r.registry, sessionMgr.msgStore).LoadAndSaveSession(ctx, params.AgentID, params.SessionID); err != nil {
			return protocol.NewErrorResponse(msg.ID, -31005,
				fmt.Sprintf("加载会话 %s 失败: %v", params.SessionID, err))
		}
		sessionMgr.ActivateSession(params.AgentID, params.SessionID)
		// ACP 回放可能已将消息持久化；空 Session 仍返回空数组。
		messages = sessionMgr.LoadMessages(params.AgentID, params.SessionID)
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
	end := total
	if limit < total-start {
		end = start + limit
	}

	return buildSessionMessagesResponse(msg.ID, messages, total, start, end)
}

type sessionMessagesPage struct {
	Messages []StoredMessage `json:"messages"`
	Total    int             `json:"total"`
	Cursor   int             `json:"cursor"`
}

// buildSessionMessagesResponse returns the largest cursor page whose actual
// JSON-RPC wire encoding fits the Device-to-Server frame contract. Callers
// continue from the returned cursor, so reducing a page never loses messages.
func buildSessionMessagesResponse(requestID string, messages []StoredMessage, total, start, end int) *protocol.ANPMessage {
	bestEnd := start
	encodedMessagesBytes := 0
	failureMessage := ""
	for index := start; index < end; index++ {
		message := messages[index]
		if len(message.Role) > maxMessageRoleBytes {
			failureMessage = fmt.Sprintf("Message at cursor %d has a role exceeding %d bytes", index, maxMessageRoleBytes)
			break
		}
		if len(message.Text) > protocol.MaxANPDeviceMessageBytes {
			failureMessage = fmt.Sprintf("Message at cursor %d exceeds the %d-byte raw Message limit", index, protocol.MaxANPDeviceMessageBytes)
			break
		}
		encodedMessage, err := json.Marshal(message)
		if err != nil {
			failureMessage = fmt.Sprintf("Message at cursor %d cannot be encoded", index)
			break
		}
		candidateMessagesBytes := encodedMessagesBytes + len(encodedMessage)
		if index > start {
			candidateMessagesBytes++ // comma between array items
		}
		emptyPage := newSessionMessagesResponse(requestID, messages, total, start, start, index+1)
		emptyWire, err := json.Marshal(emptyPage)
		if err != nil || len(emptyWire)+candidateMessagesBytes > protocol.MaxANPDeviceMessageBytes {
			failureMessage = fmt.Sprintf("Message page at cursor %d exceeds the %d-byte Device response limit", index, protocol.MaxANPDeviceMessageBytes)
			break
		}
		bestEnd = index + 1
		encodedMessagesBytes = candidateMessagesBytes
	}
	if start == end {
		return newSessionMessagesResponse(requestID, messages, total, start, end, start)
	}
	if bestEnd == start {
		if failureMessage == "" {
			failureMessage = fmt.Sprintf("Message at cursor %d exceeds the Device response limits", start)
		}
		return protocol.NewErrorResponse(requestID, protocol.ANPErrorResponseTooLarge, failureMessage)
	}
	return newSessionMessagesResponse(requestID, messages, total, start, bestEnd, bestEnd)
}

func newSessionMessagesResponse(requestID string, messages []StoredMessage, total, start, end, cursor int) *protocol.ANPMessage {
	result, _ := json.Marshal(sessionMessagesPage{
		Messages: messages[start:end],
		Total:    total,
		Cursor:   cursor,
	})
	return protocol.NewResultResponse(requestID, result)
}

func ensureAgentStarted(ctx context.Context, a agent.Agent) (bool, error) {
	if a.Status() != agent.AgentDisconnected {
		return false, nil
	}
	if err := a.Start(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// StreamCallback 流式推送回调（由 TunnelService 设置）
type StreamCallback func(requestID string, chunkType string, text string) error

// InvokeFinalCallback 流式调用最终响应回调（由 TunnelService 设置）
// 在流式输出全部完成后，发送 invoke 的最终 JSON-RPC 结果
type InvokeFinalCallback func(requestID string, result json.RawMessage, responseError *protocol.ANPError)
