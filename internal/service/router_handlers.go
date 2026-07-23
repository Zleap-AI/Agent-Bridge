// -*- coding: utf-8 -*-
// Go 1.25+
//
// router_handlers.go
// 消息路由器 — handleInvoke* 系列函数及会话管理处理
// 负责将 invoke 请求转发到对应 Agent 并处理会话 CRUD 操作
//
// Lzm 2026-07-22

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

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
		return r.handleInvokeSessionNew(ctx, msg, a, params.Params, sessionMgr)
	case "session/load":
		return r.handleInvokeSessionLoad(ctx, msg, a, params.Params, sessionMgr)
	case "session/resume":
		return r.handleInvokeSessionResume(ctx, msg, a, params.Params, sessionMgr)
	case "session/close":
		return r.handleInvokeSessionClose(ctx, msg, a, params.Params, sessionMgr)
	case "session/delete":
		return r.handleInvokeSessionDelete(ctx, msg, a, params.Params, sessionMgr)
	case "session/prompt":
		return r.handleInvokeSessionPrompt(ctx, msg, a, params.Params, params.Stream, agentRestarted, sessionMgr)
	case "session/set_mode":
		return r.handleInvokeSessionSetMode(ctx, msg, a, params.Params, sessionMgr)
	case "session/get_config":
		return r.handleInvokeSessionGetConfig(ctx, msg, a, params.Params, sessionMgr)
	case "session/set_config":
		return r.handleInvokeSessionSetConfig(ctx, msg, a, params.Params, sessionMgr)
	case "session/set_title":
		return r.handleInvokeSessionSetTitle(ctx, msg, a, params.Params, sessionMgr)
	case "logout":
		return r.handleInvokeLogout(ctx, msg, a)
	default:
		return protocol.NewErrorResponse(msg.ID, -31003,
			fmt.Sprintf("Agent %s 不支持方法: %s", params.AgentID, params.Method))
	}
}

// handleInvokeSessionNew 处理创建会话的 invoke 请求
// 支持从 innerParams 中解析 cwd（工作目录）和 permission_mode（授权模式）。
// 示例 innerParams: {"cwd":"D:/project","permission_mode":"request_approval"}
// BUGFIX(Lzm 2026-07-21): 之前错误地解析了外层 invoke params（含 agent_id/method/params），
// 导致 cwd 永远为空。现已修正为接收解嵌套后的 params.Params。
// NOTE(Lzm 2026-07-21): cwd 必须为绝对路径。前端通过后端 /api/v1/local/browse 接口
// 获取目录绝对路径，不应发送相对路径。如果收到非绝对路径，直接返回错误提示。
func (r *RequestRouter) handleInvokeSessionNew(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, innerParams json.RawMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var createParams struct {
		CWD            string `json:"cwd"`
		PermissionMode string `json:"permission_mode"`
	}
	if len(innerParams) > 0 {
		json.Unmarshal(innerParams, &createParams)
	}

	slog.Debug("handleInvokeSessionNew",
		"cwd", createParams.CWD,
		"permission_mode", createParams.PermissionMode,
		"raw_inner_params", string(innerParams),
	)

	// 校验 cwd：必须为空或绝对路径
	if createParams.CWD != "" && !filepath.IsAbs(createParams.CWD) {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("cwd 必须是绝对路径，收到: %s（请使用浏览按钮选择目录或手动输入完整路径，例如 D:/project/A）", createParams.CWD))
	}

	sid, err := sessionMgr.CreateNewSession(ctx, a.ID(), createParams.CWD, createParams.PermissionMode)
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

// handleInvokeSessionResume 处理恢复会话的 invoke 请求。
// session/resume 用于在 Agent 重启后恢复已有会话，但不重放历史消息。
// 与 session/load 的区别：不触发历史重放，仅恢复上下文。
//
// 降级策略：如果 Agent 未声明 sessionCapabilities.resume 能力，
// 自动降级为 session/load（触发历史消息重放），确保兼容性。
//
// Lzm 2026-07-21
func (r *RequestRouter) handleInvokeSessionResume(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, params json.RawMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var resumeParams struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &resumeParams); err != nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析 session/resume 参数失败: %v", err))
	}

	if resumeParams.SessionID == "" {
		return protocol.NewErrorResponse(msg.ID, -32602,
			"session/resume 参数缺少 sessionId")
	}

	// 检查 Agent 是否支持 resume 能力
	if a.Capabilities().SupportsResume {
		// 调用 Agent 的 ResumeSession（无历史重放，阻塞请求-响应）
		if err := a.ResumeSession(ctx, resumeParams.SessionID); err != nil {
			return protocol.NewErrorResponse(msg.ID, -31005,
				fmt.Sprintf("恢复会话 %s 失败: %v", resumeParams.SessionID, err))
		}

		slog.Info("会话已恢复（无历史重放）",
			"agent", a.ID(),
			"session_id", resumeParams.SessionID,
		)
	} else {
		// Agent 未声明 resume 能力，降级为 session/load
		slog.Debug("Agent 未声明 resume 能力，降级为 session/load",
			"agent", a.ID(),
			"session_id", resumeParams.SessionID,
		)
		if err := a.LoadSession(ctx, resumeParams.SessionID); err != nil {
			return protocol.NewErrorResponse(msg.ID, -31005,
				fmt.Sprintf("加载会话 %s 失败（resume 降级）: %v", resumeParams.SessionID, err))
		}
		slog.Info("会话已通过 load 恢复（resume 降级）",
			"agent", a.ID(),
			"session_id", resumeParams.SessionID,
		)
	}

	// 记录恢复的会话为活跃会话
	sessionMgr.ActivateSession(a.ID(), resumeParams.SessionID)

	result, _ := json.Marshal(map[string]string{
		"status":    "ok",
		"sessionId": resumeParams.SessionID,
	})
	return protocol.NewResultResponse(msg.ID, result)
}

// handleInvokeSessionClose 处理关闭会话的 invoke 请求。
// 需要 Agent 在 initialize 中声明 sessionCapabilities.close 能力。
// Lzm 2026-07-21
func (r *RequestRouter) handleInvokeSessionClose(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, params json.RawMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var closeParams struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &closeParams); err != nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析 session/close 参数失败: %v", err))
	}
	if closeParams.SessionID == "" {
		return protocol.NewErrorResponse(msg.ID, -32602,
			"session/close 参数缺少 sessionId")
	}

	if err := a.CloseSession(ctx, closeParams.SessionID); err != nil {
		return protocol.NewErrorResponse(msg.ID, -31005,
			fmt.Sprintf("关闭会话 %s 失败: %v", closeParams.SessionID, err))
	}

	sessionMgr.DeactivateSession(a.ID(), closeParams.SessionID)

	result, _ := json.Marshal(map[string]string{
		"status":    "ok",
		"sessionId": closeParams.SessionID,
	})
	return protocol.NewResultResponse(msg.ID, result)
}

// handleInvokeSessionDelete 处理删除会话的 invoke 请求。
// 需要 Agent 在 initialize 中声明 sessionCapabilities.delete 能力。
// Lzm 2026-07-21
func (r *RequestRouter) handleInvokeSessionDelete(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, params json.RawMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var deleteParams struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &deleteParams); err != nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析 session/delete 参数失败: %v", err))
	}
	if deleteParams.SessionID == "" {
		return protocol.NewErrorResponse(msg.ID, -32602,
			"session/delete 参数缺少 sessionId")
	}

	if err := a.DeleteSession(ctx, deleteParams.SessionID); err != nil {
		return protocol.NewErrorResponse(msg.ID, -31005,
			fmt.Sprintf("删除会话 %s 失败: %v", deleteParams.SessionID, err))
	}

	sessionMgr.RemoveSession(a.ID(), deleteParams.SessionID)

	result, _ := json.Marshal(map[string]string{
		"status":    "ok",
		"sessionId": deleteParams.SessionID,
	})
	return protocol.NewResultResponse(msg.ID, result)
}

// handleInvokeSessionSetMode 处理切换 Agent 操作模式的 invoke 请求。
// 需要 Agent 在 initialize 中声明 modes 支持。
// Lzm 2026-07-21
func (r *RequestRouter) handleInvokeSessionSetMode(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, params json.RawMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var modeParams struct {
		SessionID string `json:"sessionId"`
		ModeID    string `json:"modeId"`
	}
	if err := json.Unmarshal(params, &modeParams); err != nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析 session/set_mode 参数失败: %v", err))
	}
	if modeParams.SessionID == "" {
		return protocol.NewErrorResponse(msg.ID, -32602,
			"session/set_mode 参数缺少 sessionId")
	}
	if modeParams.ModeID == "" {
		return protocol.NewErrorResponse(msg.ID, -32602,
			"session/set_mode 参数缺少 modeId")
	}

	if err := a.SetMode(ctx, modeParams.SessionID, modeParams.ModeID); err != nil {
		return protocol.NewErrorResponse(msg.ID, -31005,
			fmt.Sprintf("切换 Agent 模式失败: %v", err))
	}

	result, _ := json.Marshal(map[string]string{
		"status":    "ok",
		"sessionId": modeParams.SessionID,
		"modeId":    modeParams.ModeID,
	})
	return protocol.NewResultResponse(msg.ID, result)
}

// handleInvokeSessionGetConfig 处理读取会话配置的 invoke 请求。
// 需要 Agent 在 session/new 响应中声明 configOptions。
// Lzm 2026-07-21
func (r *RequestRouter) handleInvokeSessionGetConfig(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, params json.RawMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var configParams struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &configParams); err != nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析 session/get_config 参数失败: %v", err))
	}
	if configParams.SessionID == "" {
		return protocol.NewErrorResponse(msg.ID, -32602,
			"session/get_config 参数缺少 sessionId")
	}

	configResult, err := a.GetConfig(ctx, configParams.SessionID)
	if err != nil {
		return protocol.NewErrorResponse(msg.ID, -31005,
			fmt.Sprintf("读取会话配置失败: %v", err))
	}

	result, _ := json.Marshal(map[string]interface{}{
		"status":    "ok",
		"sessionId": configParams.SessionID,
		"config":    configResult,
	})
	return protocol.NewResultResponse(msg.ID, result)
}

// handleInvokeSessionSetConfig 处理修改会话配置的 invoke 请求。
// 需要 Agent 在 session/new 响应中声明 configOptions。
// Lzm 2026-07-21
func (r *RequestRouter) handleInvokeSessionSetConfig(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, params json.RawMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var setConfigParams struct {
		SessionID string                 `json:"sessionId"`
		Config    map[string]interface{} `json:"config"`
	}
	if err := json.Unmarshal(params, &setConfigParams); err != nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析 session/set_config 参数失败: %v", err))
	}
	if setConfigParams.SessionID == "" {
		return protocol.NewErrorResponse(msg.ID, -32602,
			"session/set_config 参数缺少 sessionId")
	}
	if setConfigParams.Config == nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			"session/set_config 参数缺少 config")
	}

	if err := a.SetConfig(ctx, setConfigParams.SessionID, setConfigParams.Config); err != nil {
		return protocol.NewErrorResponse(msg.ID, -31005,
			fmt.Sprintf("设置会话配置失败: %v", err))
	}

	result, _ := json.Marshal(map[string]string{
		"status":    "ok",
		"sessionId": setConfigParams.SessionID,
	})
	return protocol.NewResultResponse(msg.ID, result)
}

// handleInvokeSessionSetTitle 处理设置会话标题的 invoke 请求。
// Lzm 2026-07-21
func (r *RequestRouter) handleInvokeSessionSetTitle(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, params json.RawMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	var titleParams struct {
		SessionID string `json:"sessionId"`
		Title     string `json:"title"`
	}
	if err := json.Unmarshal(params, &titleParams); err != nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析 session/set_title 参数失败: %v", err))
	}
	if titleParams.SessionID == "" {
		return protocol.NewErrorResponse(msg.ID, -32602,
			"session/set_title 参数缺少 sessionId")
	}
	if titleParams.Title == "" {
		return protocol.NewErrorResponse(msg.ID, -32602,
			"session/set_title 参数缺少 title")
	}

	if err := a.SetTitle(ctx, titleParams.SessionID, titleParams.Title); err != nil {
		return protocol.NewErrorResponse(msg.ID, -31005,
			fmt.Sprintf("设置会话标题失败: %v", err))
	}

	// 同时更新本地持久化的会话标题
	sessionMgr.SetSessionTitle(a.ID(), titleParams.SessionID, titleParams.Title)

	result, _ := json.Marshal(map[string]string{
		"status":    "ok",
		"sessionId": titleParams.SessionID,
		"title":     titleParams.Title,
	})
	return protocol.NewResultResponse(msg.ID, result)
}

// handleInvokeLogout 处理注销认证状态的 invoke 请求。
// 需要 Agent 在 initialize 中声明 agentCapabilities.auth.logout 能力。
// Lzm 2026-07-21
func (r *RequestRouter) handleInvokeLogout(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent) *protocol.ANPMessage {
	if err := a.Logout(ctx); err != nil {
		return protocol.NewErrorResponse(msg.ID, -31005,
			fmt.Sprintf("Agent 注销失败: %v", err))
	}
	result, _ := json.Marshal(map[string]string{"status": "ok"})
	return protocol.NewResultResponse(msg.ID, result)
}
