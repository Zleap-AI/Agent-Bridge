// -*- coding: utf-8 -*-
// Go 1.25+
//
// router.go
// 消息路由器 — 将 ANP 消息路由到对应 Agent 并处理响应
// 包含：RequestRouter 结构体、路由分发主逻辑、权限处理
//
// Lzm 2026-07-09

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

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
	// permissionTimeout 等待用户手动授权的最长时间
	permissionTimeout = 5 * time.Minute
)

// RequestRouter ANP 消息路由器
type RequestRouter struct {
	registry        *agent.AgentRegistry
	streamCB        StreamCallback      // 流式推送回调（由 TunnelService 设置）
	finalResponseCB InvokeFinalCallback // 流式最终响应回调（由 TunnelService 设置）

	// pendingPermissions 待处理的权限请求，key 为 sessionID
	// 当 Agent 发送 session/request_permission 时，Bridge 将请求转发给前端，
	// 在此映射中注册一个响应通道，等待用户决策后唤醒。
	// Lzm 2026-07-21
	pendingPermissions map[string]chan internal.PermissionResult
	permMu             sync.Mutex

	// LocalMode 标记是否处于本地控制台模式。
	// 本地模式下权限请求自动批准（前端无授权对话框），
	// 远程 Tunnel 模式下交由前端用户手动授权。
	// Lzm 2026-07-21
	LocalMode bool

	// sessionApprovedSessions 记录已获得用户授权的会话 (agentID → sessionID → true)
	// 用于 PermissionModeSessionApproval 模式：首次手动授权后，同会话内后续自动批准
	// Lzm 2026-07-22
	sessionApprovedSessions map[string]map[string]bool
	approveMu              sync.Mutex
}

// NewRequestRouter 创建消息路由器。
// 不再自动调用 setupPermissionCallbacks（改为通过 SetupPermissionCallbacks 显式传入 sessionMgr）。
// Lzm 2026-07-21
func NewRequestRouter(registry *agent.AgentRegistry) *RequestRouter {
	r := &RequestRouter{
		registry:               registry,
		pendingPermissions:     make(map[string]chan internal.PermissionResult),
		sessionApprovedSessions: make(map[string]map[string]bool),
	}
	return r
}

// setupPermissionCallbacks 为所有已注册 Agent 设置权限请求回调。
// 接受 sessionMgr 参数以支持根据会话授权模式（permission_mode）处理权限请求。
// 当 Agent 发送 session/request_permission 请求时，Bridge 根据授权模式
// 决定自动批准、转发给前端或自动批准并通知。
// Lzm 2026-07-21
func (r *RequestRouter) setupPermissionCallbacks(sessionMgr *SessionManager) {
	for _, a := range r.registry.List() {
		r.setAgentPermissionCB(a, sessionMgr)
	}
}

// SetupPermissionCallbacks 可导出版本，供外部调用方（如 local_http.go、tunnel.go）使用。
// Lzm 2026-07-21
func (r *RequestRouter) SetupPermissionCallbacks(sessionMgr *SessionManager) {
	r.setupPermissionCallbacks(sessionMgr)
}

// SetupElicitationCallbacks 为所有已注册 Agent 设置 elicitation 请求回调。
// 当 Agent 发送 session/create_elicitation 请求（请求用户输入：表单或 URL）时，
// Bridge 通过回调将请求转发给 SaaS 平台，等待用户响应。
// 参考：codex-acp CodexElicitationHandler.ts、claude-agent-acp elicitation.ts
// Lzm 2026-07-22
func (r *RequestRouter) SetupElicitationCallbacks() {
	for _, a := range r.registry.List() {
		r.setAgentElicitationCB(a)
	}
}

// setAgentElicitationCB 为单个 Agent 设置 elicitation 请求回调。
// 回调逻辑：通过 streamCB 将 elicitation 请求转发给 SaaS 平台，
// 然后通过 pendingPermissions 通道等待用户响应。
// 参考：codex-acp CodexElicitationHandler.ts、claude-agent-acp elicitation.ts
// Lzm 2026-07-22
func (r *RequestRouter) setAgentElicitationCB(a agent.Agent) {
	type elicitationSetter interface {
		SetElicitationCallback(cb func(params json.RawMessage) (*protocol.ACPMessage, error))
	}
	es, ok := a.(elicitationSetter)
	if !ok {
		slog.Debug("Agent 不支持设置 elicitation 回调",
			"agent", a.ID(),
		)
		return
	}

	es.SetElicitationCallback(func(params json.RawMessage) (*protocol.ACPMessage, error) {
		slog.Debug("Agent elicitation 回调被调用",
			"agent", a.ID(),
		)

		// 解析 elicitation 请求参数
		var req internal.CreateElicitationRequest
		if err := json.Unmarshal(params, &req); err != nil {
			slog.Warn("解析 elicitation 请求参数失败",
				"agent", a.ID(),
				"error", err,
			)
			return nil, err
		}

		// 通过 streamCB 将 elicitation 请求转发给 SaaS 平台
		// 请求格式：{"request_id": "...", "type": "elicitation_request", "content": {...}}
		if r.streamCB != nil {
			slog.Debug("转发 elicitation 请求到 SaaS 平台",
				"agent", a.ID(),
				"session", truncateString(req.SessionID, 16),
				"mode", req.Mode,
			)
			info := internal.PermissionRequestInfo{
				SessionID: req.SessionID,
				AgentID:   a.ID(),
				Message:   req.Message,
				Params:    params,
			}
			infoJSON, _ := json.Marshal(info)
			// 将 elicitation 请求作为权限请求类型的 streamCB 通知转发
			if err := r.streamCB("", "elicitation_request", string(infoJSON)); err != nil {
				slog.Warn("转发 elicitation 请求失败",
					"agent", a.ID(),
					"error", err,
				)
			}
		}

		// 通过 pendingPermissions 通道等待用户响应
		// 使用 "elicitation:" 前缀避免与普通权限请求冲突
		elicitKey := "elicitation:" + req.SessionID
		respCh := make(chan internal.PermissionResult, 1)
		r.permMu.Lock()
		r.pendingPermissions[elicitKey] = respCh
		r.permMu.Unlock()
		defer func() {
			r.permMu.Lock()
			delete(r.pendingPermissions, elicitKey)
			r.permMu.Unlock()
		}()

		// 等待用户响应（超时 5 分钟）
		select {
		case result, ok := <-respCh:
			if !ok {
				// 通道已关闭，返回取消
				return protocol.NewElicitationCreateResponse(
					json.RawMessage(`null`),
					string(internal.ElicitationActionCancel),
					nil,
				), nil
			}
			if result.Allowed {
				// 用户接受：解析 content 并返回
				var content json.RawMessage
				if result.Mode != "" {
					content = json.RawMessage(result.Mode)
				}
				return protocol.NewElicitationCreateResponse(
					json.RawMessage(`null`),
					string(internal.ElicitationActionAccept),
					content,
				), nil
			}
			// 用户拒绝
			return protocol.NewElicitationCreateResponse(
				json.RawMessage(`null`),
				string(internal.ElicitationActionDecline),
				nil,
			), nil

		case <-time.After(5 * time.Minute):
			slog.Warn("elicitation 请求超时（5分钟），自动取消",
				"agent", a.ID(),
				"session", truncateString(req.SessionID, 16),
			)
			return protocol.NewElicitationCreateResponse(
				json.RawMessage(`null`),
				string(internal.ElicitationActionCancel),
				nil,
			), nil
		}
	})
}

// setAgentPermissionCB 为单个 Agent 设置权限请求回调。
// 回调逻辑：提取 sessionID → 获取授权模式 → 根据模式处理：
//   - full_access：自动批准，不通知前端
//   - auto_approve：自动批准，通过 streamCB 推送 auto_approve 通知
//   - request_approval（默认）：转发权限请求到前端 → 等待用户决策 → 返回结果
// Lzm 2026-07-22
func (r *RequestRouter) setAgentPermissionCB(a agent.Agent, sessionMgr *SessionManager) {
	// 检查 Agent 是否支持设置权限回调（通过类型断言）
	// 回调返回: (是否允许, 授权模式, 错误)
	// Lzm 2026-07-22
	type permSetter interface {
		SetPermissionCallback(cb func(params json.RawMessage) (bool, string, error))
	}
	ps, ok := a.(permSetter)
	if !ok {
		slog.Debug("Agent 不支持设置权限回调，将使用自动批准模式",
			"agent", a.ID(),
		)
		return
	}

	ps.SetPermissionCallback(func(params json.RawMessage) (bool, string, error) {
		slog.Debug("Agent 权限回调被调用",
			"agent", a.ID(),
		)

		// 从 params 中提取 sessionId，兼容 session_id 和 sessionId 两种字段名
		var permParams struct {
			SessionID      string          `json:"sessionId"`
			SessionIDSnake string          `json:"session_id"`
			ToolCall       json.RawMessage `json:"toolCall,omitempty"`
			Message        string          `json:"message,omitempty"`
		}
		if err := json.Unmarshal(params, &permParams); err != nil {
			slog.Warn("解析权限请求参数失败，自动拒绝",
				"agent", a.ID(),
				"error", err,
			)
			return false, "", nil
		}

		sessionID := permParams.SessionID
		if sessionID == "" {
			sessionID = permParams.SessionIDSnake
		}
		if sessionID == "" {
			slog.Warn("权限请求缺少 sessionId，自动拒绝",
				"agent", a.ID(),
			)
			return false, "", nil
		}

		// 从 sessionMgr 获取该会话的授权模式
		permMode := sessionMgr.GetSessionPermissionMode(a.ID(), sessionID)
		if permMode == "" {
			permMode = string(internal.DefaultPermissionMode)
		}

		slog.Debug("权限请求决策开始",
			"agent", a.ID(),
			"session", truncateString(sessionID, 16),
			"perm_mode", permMode,
		)

		switch permMode {
		case string(internal.PermissionModeFullAccess):
			// full_access：自动批准，不通知前端
			slog.Info("权限请求自动批准（full_access）",
				"agent", a.ID(),
				"session", truncateString(sessionID, 16),
			)
			return true, permMode, nil

		case string(internal.PermissionModeAutoApprove):
			// auto_approve：自动批准，但通过 streamCB 推送通知
			slog.Info("权限请求自动批准（auto_approve）",
				"agent", a.ID(),
				"session", truncateString(sessionID, 16),
			)
			if r.streamCB != nil {
				info := internal.PermissionRequestInfo{
					SessionID:      sessionID,
					AgentID:        a.ID(),
					Message:        permParams.Message,
					ToolCall:       permParams.ToolCall,
					Params:         params,
					PermissionMode: permMode,
				}
				infoJSON, _ := json.Marshal(info)
				_ = r.streamCB("", "auto_approve", string(infoJSON))
			}
			return true, "auto_approve", nil

		case string(internal.PermissionModeSessionApproval):
			// session_approval：如果该会话已有过授权记录，自动批准
			// 否则转发给前端等待用户决策
			r.approveMu.Lock()
			approved := false
			if sessions, ok := r.sessionApprovedSessions[a.ID()]; ok {
				_, approved = sessions[sessionID]
			}
			r.approveMu.Unlock()

			if approved {
				// 已授权过，自动批准
				slog.Info("权限请求自动批准（session_approval，已授权过）",
					"agent", a.ID(),
					"session", truncateString(sessionID, 16),
				)
				// 但通过 streamCB 推送通知（让前端知道已批准）
				if r.streamCB != nil {
					info := internal.PermissionRequestInfo{
						SessionID:      sessionID,
						AgentID:        a.ID(),
						Message:        permParams.Message,
						ToolCall:       permParams.ToolCall,
						Params:         params,
						PermissionMode: permMode,
					}
					infoJSON, _ := json.Marshal(info)
					_ = r.streamCB("", "auto_approve", string(infoJSON))
				}
				return true, permMode, nil
			}

			// 首次请求，转发给前端等待用户决策
			respCh := make(chan internal.PermissionResult, 1)
			r.permMu.Lock()
			r.pendingPermissions[sessionID] = respCh
			r.permMu.Unlock()

			defer func() {
				r.permMu.Lock()
				delete(r.pendingPermissions, sessionID)
				r.permMu.Unlock()
			}()

			if r.streamCB != nil {
				info := internal.PermissionRequestInfo{
					SessionID:      sessionID,
					AgentID:        a.ID(),
					Message:        permParams.Message,
					ToolCall:       permParams.ToolCall,
					Params:         params,
					PermissionMode: permMode,
				}
				infoJSON, _ := json.Marshal(info)
				_ = r.streamCB("", "permission_request", string(infoJSON))

				select {
				case result := <-respCh:
					slog.Info("接收到用户授权决策",
						"agent", a.ID(),
						"session", truncateString(sessionID, 16),
						"allowed", result.Allowed,
						"mode", result.Mode,
					)
					if result.Allowed {
						// 记录授权，后续自动批准
						r.approveMu.Lock()
						if r.sessionApprovedSessions[a.ID()] == nil {
							r.sessionApprovedSessions[a.ID()] = make(map[string]bool)
						}
						r.sessionApprovedSessions[a.ID()][sessionID] = true
						r.approveMu.Unlock()
					}
					return result.Allowed, result.Mode, nil
				case <-time.After(permissionTimeout):
					slog.Warn("用户授权超时，自动拒绝",
						"agent", a.ID(),
						"session", truncateString(sessionID, 16),
						"timeout", permissionTimeout,
					)
					return false, "", nil
				}
			}
			return true, "", nil

		default:
			// request_approval（默认）：转发给前端等待决策
			// 创建响应通道并注册
			respCh := make(chan internal.PermissionResult, 1)
			r.permMu.Lock()
			r.pendingPermissions[sessionID] = respCh
			r.permMu.Unlock()

			// 确保响应后清理
			defer func() {
				r.permMu.Lock()
				delete(r.pendingPermissions, sessionID)
				r.permMu.Unlock()
			}()

			// 将权限请求转发为流式事件推送给前端
			if r.streamCB != nil {
				// 使用流式事件类型 "permission_request"
				info := internal.PermissionRequestInfo{
					SessionID:      sessionID,
					AgentID:        a.ID(),
					Message:        permParams.Message,
					ToolCall:       permParams.ToolCall,
					Params:         params,
					PermissionMode: permMode,
				}
				infoJSON, _ := json.Marshal(info)
				_ = r.streamCB("", "permission_request", string(infoJSON))

				// 等待用户决策（带超时）
				select {
				case result := <-respCh:
					slog.Info("接收到用户授权决策",
						"agent", a.ID(),
						"session", truncateString(sessionID, 16),
						"allowed", result.Allowed,
						"mode", result.Mode,
					)
					return result.Allowed, result.Mode, nil
				case <-time.After(permissionTimeout):
					slog.Warn("用户授权超时，自动拒绝",
						"agent", a.ID(),
						"session", truncateString(sessionID, 16),
						"timeout", permissionTimeout,
					)
					return false, "", nil
				}
			}

			// 无回调时自动批准（不应到达此处，作为安全兜底）
			slog.Warn("权限请求自动批准（无前端回调）",
				"agent", a.ID(),
				"session", truncateString(sessionID, 16),
			)
			return true, "", nil
		}
	})
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
	case msg.Method == "session/cancel":
		return r.handleSessionCancel(ctx, msg, sessionMgr)
	case msg.Method == "session/permission_response":
		return r.handlePermissionResponse(msg, sessionMgr)
	case msg.Method == "session/exec":
		return r.handleSessionExec(ctx, msg, sessionMgr)
	case strings.HasPrefix(msg.Method, "$/"):
		return r.handleJSONRPCNotification(ctx, msg, sessionMgr)
	default:
		return protocol.NewErrorResponse(msg.ID, -32601, fmt.Sprintf("未知方法: %s", msg.Method))
	}
}

// handlePermissionResponse 处理前端用户对权限请求的响应（session/permission_response）。
// 前端在收到 permission_request 流式事件后，用户做出授权决策，
// 通过此 ANP 方法将决策结果返回给 Bridge，Bridge 再转发给 Agent。
// 兼容 session_id 和 sessionId 两种字段名。
// 请求参数：{"session_id": "..."|"sessionId": "...", "allowed": true|false, "reason": "...", "permission_mode": "..."}
// 支持从 permission_mode 字段更新该会话的授权模式。
// Lzm 2026-07-21
func (r *RequestRouter) handlePermissionResponse(msg *protocol.ANPMessage, sessionMgr *SessionManager) *protocol.ANPMessage {
	slog.Debug("处理权限响应请求",
		"id", msg.ID,
		"params", truncateString(string(msg.Params), 300),
	)

	// 先尝试用标准字段名解析
	var decision internal.PermissionDecision
	if err := json.Unmarshal(msg.Params, &decision); err != nil {
		slog.Warn("解析权限决策参数失败",
			"error", err,
			"params", string(msg.Params),
		)
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析权限决策参数失败: %v", err))
	}

	slog.Debug("handlePermissionResponse 解析结果",
		"session_id", decision.SessionID,
		"allowed", decision.Allowed,
		"reason", decision.Reason,
		"permission_mode", decision.PermissionMode,
		"agent_id", decision.AgentID,
	)

	// 如果 session_id 为空，尝试从 sessionId 字段提取
	// 前端 Vue 版本可能发送 camelCase 格式
	// Lzm 2026-07-21
	if decision.SessionID == "" {
		var alt struct {
			SessionIDAlt string `json:"sessionId"`
		}
		if err := json.Unmarshal(msg.Params, &alt); err == nil && alt.SessionIDAlt != "" {
			decision.SessionID = alt.SessionIDAlt
			slog.Debug("从 sessionId 字段提取 session_id", "session_id", truncateString(decision.SessionID, 16))
		}
	}

	if decision.SessionID == "" {
		slog.Warn("权限决策响应缺少 session_id")
		return protocol.NewErrorResponse(msg.ID, -32602, "缺少 session_id")
	}

	r.permMu.Lock()
	respCh, exists := r.pendingPermissions[decision.SessionID]
	if exists {
		delete(r.pendingPermissions, decision.SessionID)
	} else {
		// 尝试查找 elicitation 通道（使用 "elicitation:" 前缀）
		elicitKey := "elicitation:" + decision.SessionID
		respCh, exists = r.pendingPermissions[elicitKey]
		if exists {
			delete(r.pendingPermissions, elicitKey)
		}
	}
	r.permMu.Unlock()

	slog.Debug("查找 pending 权限请求",
		"session_id", truncateString(decision.SessionID, 16),
		"exists", exists,
		"pending_count", len(r.pendingPermissions),
	)

	if !exists {
		// 会话不存在于 pendingPermissions 中，可能原因：
		// 1. 已超时自动拒绝
		// 2. auto_approve 模式下发起的权限模式更新（如前端"始终允许"点击后后端已是 auto_approve）
		// 对于带 permission_mode 更新的请求，直接更新模式并返回成功
		// Lzm 2026-07-21
		if decision.PermissionMode != "" && decision.AgentID != "" && sessionMgr != nil {
			sessionMgr.UpdateSessionPermissionMode(decision.AgentID, decision.SessionID, decision.PermissionMode)
			slog.Info("已更新会话授权模式（会话无待处理请求，可能已自动批准）",
				"agent", decision.AgentID,
				"session", truncateString(decision.SessionID, 16),
				"permission_mode", decision.PermissionMode,
			)
			return protocol.NewResultResponse(msg.ID, json.RawMessage(`{"result":"ok"}`))
		}
		slog.Warn("收到未知会话的权限响应，可能已超时或会话已被处理",
			"session", truncateString(decision.SessionID, 16),
			"allowed", decision.Allowed,
		)
		return protocol.NewErrorResponse(msg.ID, -32602, "该会话没有待处理的权限请求")
	}

	slog.Info("处理用户授权决策",
		"session", truncateString(decision.SessionID, 16),
		"allowed", decision.Allowed,
		"agent_id", decision.AgentID,
		"permission_mode", decision.PermissionMode,
	)
	if decision.Allowed {
		// 记录 audit: 用户批准了哪些 Agent 的权限请求
		slog.Debug("[AUDIT] 权限批准",
			"agent", decision.AgentID,
			"session", truncateString(decision.SessionID, 16),
			"permission_mode", decision.PermissionMode,
		)
	}

	// 将用户决策发送给等待中的权限回调（携带授权模式）
	respCh <- internal.PermissionResult{Allowed: decision.Allowed, Mode: decision.PermissionMode}

	// 如果前端同时更新了授权模式，同步更新到会话
	if decision.PermissionMode != "" && sessionMgr != nil && decision.AgentID != "" {
		sessionMgr.UpdateSessionPermissionMode(decision.AgentID, decision.SessionID, decision.PermissionMode)
		slog.Info("更新会话授权模式",
			"agent", decision.AgentID,
			"session", truncateString(decision.SessionID, 16),
			"permission_mode", decision.PermissionMode,
		)
	}

	return protocol.NewResultResponse(msg.ID, json.RawMessage(`{"result":"ok"}`))
}

// handlePing 处理 ping 请求
func (r *RequestRouter) handlePing(msg *protocol.ANPMessage) *protocol.ANPMessage {
	return protocol.NewResultResponse(msg.ID, json.RawMessage(`"pong"`))
}

// StreamCallback 流式推送回调（由 TunnelService 设置）
type StreamCallback func(requestID string, chunkType string, text string) error

// InvokeFinalCallback 流式调用最终响应回调（由 TunnelService 设置）
// 在流式输出全部完成后，发送 invoke 的最终 JSON-RPC 结果
type InvokeFinalCallback func(requestID string, result json.RawMessage, responseError *protocol.ANPError)
