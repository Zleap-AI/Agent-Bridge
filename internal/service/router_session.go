// -*- coding: utf-8 -*-
// Go 1.25+
//
// router_session.go
// 消息路由器 — 会话列表/消息/取消/通知处理
// 负责会话管理相关 ANP 方法的路由和处理
//
// Lzm 2026-07-22

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

type sessionListItem struct {
	AgentID      string `json:"agent_id"`
	SessionID    string `json:"session_id"`
	Title        string `json:"title,omitempty"` // 会话标题
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

	slog.Debug("sessions/list 请求",
		"agent_id", filter.AgentID,
		"msg_id", msg.ID,
	)

	if filter.AgentID != "" {
		items := listAgentSessions(filter.AgentID, sessionMgr)
		slog.Debug("sessions/list 结果",
			"agent_id", filter.AgentID,
			"count", len(items),
		)
		return buildSessionListResponse(msg.ID, items)
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
			Title:        session.Title,
			MessageCount: session.MessageCount,
			UpdatedAt:    session.UpdatedAt,
		})
	}
	if nativeSessions, err := agent.DiscoverHistoricalSessions(agentID, maxSessionsPerAgent); err == nil {
		for _, session := range nativeSessions {
			msgCount, _ := strconv.Atoi(session.Meta["message_count"])
			add(sessionListItem{
				AgentID:      agentID,
				SessionID:    session.NativeID,
				Title:        session.Title,
				MessageCount: msgCount,
				UpdatedAt:    session.StartedAt,
			})
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

	slog.Debug("sessions/messages 请求",
		"agent_id", params.AgentID,
		"session_id", truncateString(params.SessionID, 16),
		"cursor", params.Cursor,
		"limit", params.Limit,
	)

	if params.AgentID == "" || params.SessionID == "" {
		return protocol.NewErrorResponse(msg.ID, -32602, "缺少 agent_id 或 session_id")
	}
	if params.Cursor < 0 {
		return protocol.NewErrorResponse(msg.ID, -32602, "cursor 不能小于 0")
	}

	// 加载消息。已持久化的空 Session 是合法的；只有本地无任何记录时
	// 才向 Agent 验证 Session，避免把"空历史"误报为不存在。
	messages := sessionMgr.LoadMessages(params.AgentID, params.SessionID)
	if messages == nil && !sessionMgr.SessionExists(params.AgentID, params.SessionID) {
		a := r.registry.Get(params.AgentID)
		if a == nil {
			return protocol.NewErrorResponse(msg.ID, -31001,
				fmt.Sprintf("未知 Agent: %s", params.AgentID))
		}
		// Agent 启动和 session/load 使用独立超时，避免对 Codex 等慢 Agent 的
		// 等待阻塞整个 WebSocket 连接。
		loadCtx, loadCancel := context.WithTimeout(ctx, 90*time.Second)
		defer loadCancel()
		if _, err := ensureAgentStarted(loadCtx, a); err != nil {
			slog.Warn("查询消息时启动 Agent 失败，降级返回本地数据",
				"agent", params.AgentID,
				"session", truncateString(params.SessionID, 16),
				"error", err,
			)
			messages = []StoredMessage{}
		} else if _, err := NewSessionLoadReplayer(r.registry, sessionMgr.msgStore).LoadAndSaveSession(loadCtx, params.AgentID, params.SessionID); err != nil {
			if a.Status() == agent.AgentBusy {
				slog.Warn("查询消息时 Agent 忙，加载会话失败，降级返回本地数据",
					"agent", params.AgentID,
					"session", truncateString(params.SessionID, 16),
					"error", err,
				)
				messages = []StoredMessage{}
			} else {
				return protocol.NewErrorResponse(msg.ID, -31005,
					fmt.Sprintf("加载会话 %s 失败: %v", params.SessionID, err))
			}
		} else {
			sessionMgr.ActivateSession(params.AgentID, params.SessionID)
			messages = sessionMgr.LoadMessages(params.AgentID, params.SessionID)
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

// handleSessionCancel 处理 session/cancel 请求
// 从 SaaS 转发取消请求到指定 Agent
// 注意：即使会话不在本地存储中（如外部创建），也尝试向 Agent 发送取消请求。
// 如果 sessionId 为空，则取消当前活跃会话。
// Lzm 2026-07-21
func (r *RequestRouter) handleSessionCancel(ctx context.Context, msg *protocol.ANPMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var params struct {
		AgentID   string `json:"agent_id"`
		SessionID string `json:"sessionId,omitempty"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析 session/cancel 参数失败: %v", err))
	}
	if params.AgentID == "" {
		return protocol.NewErrorResponse(msg.ID, -32602, "缺少 agent_id")
	}
	// 如果未指定 sessionId，使用当前活跃会话
	if params.SessionID == "" {
		params.SessionID = sessionMgr.GetSession(params.AgentID)
	}
	// 会话存在性检查：即使本地没有记录，也尝试向 Agent 发送取消
	// Agent 自己管理会话生命周期，Bridge 不应阻止取消未记录的会话
	if params.SessionID != "" && !sessionMgr.SessionExists(params.AgentID, params.SessionID) {
		slog.Debug("session/cancel: 会话不在本地存储中，仍尝试向 Agent 发送取消",
			"agent", params.AgentID,
			"session", params.SessionID,
		)
	}
	a := r.registry.Get(params.AgentID)
	if a == nil {
		return protocol.NewErrorResponse(msg.ID, -31001,
			fmt.Sprintf("未知 Agent: %s", params.AgentID))
	}
	_, err := ensureAgentStarted(ctx, a)
	if err != nil {
		return protocol.NewErrorResponse(msg.ID, -31002,
			fmt.Sprintf("启动 Agent %s 失败: %v", params.AgentID, err))
	}
	if err := a.Cancel(ctx, params.SessionID); err != nil {
		return protocol.NewErrorResponse(msg.ID, -31008,
			fmt.Sprintf("取消 Agent %s 会话失败: %v", params.AgentID, err))
	}
	// 同时标记会话为取消状态，让 streamPrompt 主动检测并中断
	sessionMgr.CancelSession(params.AgentID, params.SessionID)
	// 5 秒后自动清理取消状态，防止已完成流的会话残留 cancel 标记。
	// 这确保了后续请求不会因为旧的取消状态而被错误中断。
	time.AfterFunc(5*time.Second, func() {
		sessionMgr.ClearCancel(params.AgentID, params.SessionID)
	})
	result, _ := json.Marshal(map[string]string{"status": "ok"})
	return protocol.NewResultResponse(msg.ID, result)
}

// handleJSONRPCNotification 处理 $/ 前缀的 JSON-RPC 2.0 通知（如 $/cancel_request）
// 通知为广播模式，转发到所有已注册且正在运行的 Agent
// Lzm 2026-07-20
func (r *RequestRouter) handleJSONRPCNotification(ctx context.Context, msg *protocol.ANPMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var params struct {
		ID      string `json:"id"`
		AgentID string `json:"agent_id,omitempty"`
	}
	if msg.Params != nil {
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			slog.Debug("解析 $/ 通知参数失败",
				"params", string(msg.Params),
				"error", err,
			)
		}
	}
	// 如果指定了 agent_id，只取消该 Agent
	if params.AgentID != "" {
		a := r.registry.Get(params.AgentID)
		if a != nil {
			if _, err := ensureAgentStarted(ctx, a); err == nil {
				if err := a.Cancel(ctx, params.ID); err != nil {
					slog.Warn("取消 Agent 请求失败",
						"agent", params.AgentID,
						"request_id", params.ID,
						"error", err,
					)
				}
			}
		}
		return nil
	}
	// 未指定 agent_id 时，向所有已注册且正在运行的 Agent 广播取消请求
	for _, a := range r.registry.List() {
		if a.Status() == agent.AgentDisconnected {
			continue
		}
		if err := a.Cancel(ctx, params.ID); err != nil {
			slog.Warn("广播取消请求失败",
				"agent", a.ID(),
				"request_id", params.ID,
				"error", err,
			)
		}
	}
	// 通知无响应
	return nil
}
