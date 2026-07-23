// -*- coding: utf-8 -*-
// Go 1.25+
//
// base_session.go
// Agent ACP 会话管理方法 — 创建/加载/恢复/关闭/删除会话及配置管理
//
// Lzm 2026-07-22

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// --- 会话管理 ---

// sessionNewResult session/new 响应中的可选字段
// ACP V1 规范：可包含 configOptions 和 modes
// Lzm 2026-07-21
type sessionNewResult struct {
	SessionID    string            `json:"sessionId"`
	ConfigOptions []ConfigOption   `json:"configOptions,omitempty"`
	Modes        *SessionModeState `json:"modes,omitempty"`
}

// NewSession 创建新 ACP 会话。
// 所有 Agent 类型创建会话的逻辑完全一致，委托给 doNewSession。
// Lzm 2026-07-21
func (a *baseAgent) NewSession(ctx context.Context, cwd string) (string, error) {
	return a.doNewSession(ctx, cwd)
}

// doNewSession 创建新 ACP 会话并返回 sessionId
// 同时解析 configOptions 和 modes（可选）
// 支持动态传入 cwd，空值时使用 Agent 的默认工作目录。
// Lzm 2026-07-21
func (a *baseAgent) doNewSession(ctx context.Context, cwd string) (string, error) {
	if cwd == "" {
		cwd = a.meta.WorkDir
	}
	req := protocol.NewSessionRequest(a.nextID(), cwd)
	resp, err := a.doSend(ctx, req)
	if err != nil {
		return "", &SessionError{Err: err}
	}

	if !resp.IsSuccess() {
		if resp.Error != nil {
			slog.Warn("session/new 返回错误",
				"agent", a.meta.ID,
				"req_id", req.IDString(),
				"code", resp.Error.Code,
				"message", resp.Error.Message,
			)
			return "", &SessionError{
				Err: fmt.Errorf("创建会话失败: code=%d message=%s",
					resp.Error.Code, resp.Error.Message),
			}
		}
		return "", &SessionError{Err: fmt.Errorf("创建会话返回异常响应")}
	}

	// 提取 sessionId 及可选字段（configOptions、modes）
	var result sessionNewResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		slog.Warn("解析 session/new 结果失败",
			"agent", a.meta.ID,
			"req_id", req.IDString(),
			"raw_result", string(resp.Result),
			"error", err,
		)
		return "", &SessionError{
			Err: fmt.Errorf("解析 sessionId 失败: %w", err),
		}
	}
	if result.SessionID == "" {
		return "", &SessionError{Err: fmt.Errorf("sessionId 为空")}
	}

	// 如果 Agent 返回了 configOptions，记录日志
	if len(result.ConfigOptions) > 0 {
		slog.Debug("Agent 返回 Session Config Options",
			"agent", a.meta.ID,
			"count", len(result.ConfigOptions),
		)
	}

	// 如果 Agent 返回了 modes，记录日志
	if result.Modes != nil && len(result.Modes.AvailableModes) > 0 {
		slog.Debug("Agent 返回可用模式",
			"agent", a.meta.ID,
			"current_mode", result.Modes.CurrentModeID,
			"available_modes", len(result.Modes.AvailableModes),
		)
	}

	return result.SessionID, nil
}

// LoadSession 加载已有会话（重放历史消息）。
// 所有 Agent 类型加载会话的逻辑完全一致，委托给 doLoadSession。
// Lzm 2026-07-21
func (a *baseAgent) LoadSession(ctx context.Context, sessionID string) error {
	return a.doLoadSession(ctx, sessionID)
}

// LoadSessionStream loads a Session and exposes the replay notifications.
// cwd and mcpServers stay here because they are Agent protocol details.
func (a *baseAgent) LoadSessionStream(ctx context.Context, sessionID string) (<-chan internal.StreamChunk, error) {
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId":  sessionID,
		"cwd":        a.meta.WorkDir,
		"mcpServers": []interface{}{},
	})
	req := &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      protocol.MarshalStringID(a.nextID()),
		Method:  "session/load",
		Params:  params,
	}

	slog.Debug("doLoadSession: 发送 ACP session/load 请求",
		"agent", a.meta.ID,
		"session_id", sessionID,
		"cwd", a.meta.WorkDir,
		"request_id", req.IDString(),
		"params", string(params),
	)

	ch, err := a.doStream(ctx, req)
	if err != nil {
		slog.Warn("doLoadSession: doStream 失败",
			"agent", a.meta.ID,
			"session_id", sessionID,
			"error", err,
		)
		return nil, &SessionError{SessionID: sessionID, Err: err}
	}
	return ch, nil
}

// doLoadSession loads a Session while discarding replay notifications. Callers
// that need the native history use LoadSessionStream instead.
func (a *baseAgent) doLoadSession(ctx context.Context, sessionID string) error {
	ch, err := a.LoadSessionStream(ctx, sessionID)
	if err != nil {
		return err
	}

	for chunk := range ch {
		if chunk.Type == internal.StreamChunkError {
			slog.Warn("doLoadSession: Agent 返回错误",
				"agent", a.meta.ID,
				"session_id", sessionID,
				"error_code", chunk.Error.Code,
				"error_msg", chunk.Error.Message,
			)
			return &SessionError{
				SessionID: sessionID,
				Err:       fmt.Errorf("加载会话失败: %v", chunk.Error),
			}
		}
		slog.Debug("doLoadSession: 收到流式块",
			"agent", a.meta.ID,
			"type", chunk.Type.String(),
			"text_len", len(chunk.Text),
		)
	}
	slog.Debug("doLoadSession: 会话加载成功",
		"agent", a.meta.ID,
		"session_id", sessionID,
	)
	return nil
}

// ResumeSession 恢复已有会话但不重放历史消息。
// 与 session/load 不同，session/resume 不触发历史消息重放。
// 需要 Agent 在 initialize 中声明 sessionCapabilities.resume 能力。
// 实现为阻塞 doSend（无流式通知）。
// Lzm 2026-07-20
func (a *baseAgent) ResumeSession(ctx context.Context, sessionID string) error {
	// ACP V1 规范：调用 session/resume 前 MUST 检查 Agent 是否声明了 sessionCapabilities.resume 能力
	if !a.Capabilities().SupportsResume {
		return &SessionError{
			SessionID: sessionID,
			Err:       fmt.Errorf("Agent %s 未声明 sessionCapabilities.resume 能力", a.meta.ID),
		}
	}

	req := protocol.NewResumeRequest(a.nextID(), sessionID, a.meta.WorkDir)

	slog.Debug("ResumeSession: 发送 ACP session/resume 请求",
		"agent", a.meta.ID,
		"session_id", sessionID,
		"request_id", req.IDString(),
	)

	// doSend 内部管理运行时生命周期，无需手动获取
	resp, err := a.doSend(ctx, req)
	if err != nil {
		slog.Warn("ResumeSession: 发送失败",
			"agent", a.meta.ID,
			"session_id", sessionID,
			"error", err,
		)
		return &SessionError{SessionID: sessionID, Err: err}
	}
	if resp != nil && resp.Error != nil {
		slog.Warn("ResumeSession: Agent 返回错误",
			"agent", a.meta.ID,
			"session_id", sessionID,
			"error_code", resp.Error.Code,
			"error_msg", resp.Error.Message,
		)
		return &SessionError{
			SessionID: sessionID,
			Err:       fmt.Errorf("恢复会话失败: [%d] %s", resp.Error.Code, resp.Error.Message),
		}
	}

	slog.Debug("ResumeSession: 会话恢复成功",
		"agent", a.meta.ID,
		"session_id", sessionID,
	)
	return nil
}

// CloseSession 关闭指定活跃会话。
// 需要 Agent 在 initialize 中声明 sessionCapabilities.close 能力。
// Lzm 2026-07-21
func (a *baseAgent) CloseSession(ctx context.Context, sessionID string) error {
	req := protocol.NewCloseSessionRequest(a.nextID(), sessionID)

	resp, err := a.doSend(ctx, req)
	if err != nil {
		return &SessionError{SessionID: sessionID, Err: fmt.Errorf("关闭会话失败: %w", err)}
	}
	if resp != nil && resp.Error != nil {
		return &SessionError{
			SessionID: sessionID,
			Err:       fmt.Errorf("关闭会话失败: [%d] %s", resp.Error.Code, resp.Error.Message),
		}
	}

	slog.Info("会话已关闭",
		"agent", a.meta.ID,
		"session_id", sessionID,
	)
	return nil
}

// DeleteSession 删除指定会话（含历史消息）。
// 需要 Agent 在 initialize 中声明 sessionCapabilities.delete 能力。
// Lzm 2026-07-21
func (a *baseAgent) DeleteSession(ctx context.Context, sessionID string) error {
	req := protocol.NewDeleteSessionRequest(a.nextID(), sessionID)

	resp, err := a.doSend(ctx, req)
	if err != nil {
		return &SessionError{SessionID: sessionID, Err: fmt.Errorf("删除会话失败: %w", err)}
	}
	if resp != nil && resp.Error != nil {
		return &SessionError{
			SessionID: sessionID,
			Err:       fmt.Errorf("删除会话失败: [%d] %s", resp.Error.Code, resp.Error.Message),
		}
	}

	slog.Info("会话已删除",
		"agent", a.meta.ID,
		"session_id", sessionID,
	)
	return nil
}

// GetConfig 读取会话配置选项值（session/get_config）。
// 通过 doSend 阻塞调用 Agent 的 session/get_config 方法，返回当前配置值。
// Lzm 2026-07-21
func (a *baseAgent) GetConfig(ctx context.Context, sessionID string) (interface{}, error) {
	run := a.currentRuntime()
	if run == nil || !run.pm.IsRunning() {
		return nil, fmt.Errorf("agent %s 进程未运行", a.meta.ID)
	}

	req := protocol.NewGetConfigRequest(a.nextID(), sessionID)

	resp, err := a.doSend(ctx, req)
	if err != nil {
		return nil, &SessionError{SessionID: sessionID, Err: fmt.Errorf("读取配置失败: %w", err)}
	}
	if resp != nil && resp.Error != nil {
		return nil, &SessionError{
			SessionID: sessionID,
			Err:       fmt.Errorf("读取配置失败: [%d] %s", resp.Error.Code, resp.Error.Message),
		}
	}

	// 解析配置值
	var configResult interface{}
	if resp != nil && resp.Result != nil {
		if err := json.Unmarshal(resp.Result, &configResult); err == nil {
			return configResult, nil
		}
	}

	slog.Info("配置读取成功",
		"agent", a.meta.ID,
		"session_id", sessionID,
	)
	return nil, nil
}

// SetConfig 修改会话配置选项值（session/set_config）。
// config 参数为配置选项 ID 到值的映射。
// Lzm 2026-07-21
func (a *baseAgent) SetConfig(ctx context.Context, sessionID string, config map[string]interface{}) error {
	run := a.currentRuntime()
	if run == nil || !run.pm.IsRunning() {
		return fmt.Errorf("agent %s 进程未运行", a.meta.ID)
	}

	req := protocol.NewSetConfigRequest(a.nextID(), sessionID, config)

	slog.Debug("SetConfig: 发送 ACP session/set_config 请求",
		"agent", a.meta.ID,
		"session_id", sessionID,
		"request_id", req.IDString(),
	)

	resp, err := a.doSend(ctx, req)
	if err != nil {
		return &SessionError{
			SessionID: sessionID,
			Err:       fmt.Errorf("设置配置失败: %w", err),
		}
	}
	if resp != nil && resp.Error != nil {
		return &SessionError{
			SessionID: sessionID,
			Err:       fmt.Errorf("设置配置失败: [%d] %s", resp.Error.Code, resp.Error.Message),
		}
	}

	slog.Info("配置设置成功",
		"agent", a.meta.ID,
		"session_id", sessionID,
	)
	return nil
}

// SetTitle 设置会话标题（session/set_title）。
// Lzm 2026-07-21
func (a *baseAgent) SetTitle(ctx context.Context, sessionID, title string) error {
	run := a.currentRuntime()
	if run == nil || !run.pm.IsRunning() {
		return fmt.Errorf("agent %s 进程未运行", a.meta.ID)
	}

	req := protocol.NewSetTitleRequest(a.nextID(), sessionID, title)

	slog.Debug("SetTitle: 发送 ACP session/set_title 请求",
		"agent", a.meta.ID,
		"session_id", sessionID,
		"title", title,
		"request_id", req.IDString(),
	)

	resp, err := a.doSend(ctx, req)
	if err != nil {
		return &SessionError{
			SessionID: sessionID,
			Err:       fmt.Errorf("设置标题失败: %w", err),
		}
	}
	if resp != nil && resp.Error != nil {
		return &SessionError{
			SessionID: sessionID,
			Err:       fmt.Errorf("设置标题失败: [%d] %s", resp.Error.Code, resp.Error.Message),
		}
	}

	slog.Info("标题设置成功",
		"agent", a.meta.ID,
		"session_id", sessionID,
		"title", title,
	)
	return nil
}

// SetMode 切换 Agent 的操作模式（session/set_mode）。
// modeID 必须是 Agent 在 session/new 响应中 availableModes 列表中的 ID。
// 典型模式： "ask"（询问权限）、"code"（直接编码）、"architect"（架构设计）
// Lzm 2026-07-21
func (a *baseAgent) SetMode(ctx context.Context, sessionID, modeID string) error {
	run := a.currentRuntime()
	if run == nil || !run.pm.IsRunning() {
		return fmt.Errorf("agent %s 进程未运行", a.meta.ID)
	}

	req := protocol.NewSetModeRequest(a.nextID(), sessionID, modeID)

	slog.Debug("SetMode: 发送 ACP session/set_mode 请求",
		"agent", a.meta.ID,
		"session_id", sessionID,
		"mode_id", modeID,
		"request_id", req.IDString(),
	)

	resp, err := a.doSend(ctx, req)
	if err != nil {
		return &SessionError{
			SessionID: sessionID,
			Err:       fmt.Errorf("设置模式失败: %w", err),
		}
	}
	if resp != nil && resp.Error != nil {
		return &SessionError{
			SessionID: sessionID,
			Err:       fmt.Errorf("设置模式失败: [%d] %s", resp.Error.Code, resp.Error.Message),
		}
	}

	slog.Info("Agent 模式切换成功",
		"agent", a.meta.ID,
		"session_id", sessionID,
		"mode_id", modeID,
	)
	return nil
}

// Cancel 取消指定会话的当前操作（session/cancel 通知）
// ACP 协议中 session/cancel 是通知，无需等待响应，也不分配消息 ID
// Lzm 2026-07-20
func (a *baseAgent) Cancel(ctx context.Context, sessionID string) error {
	run := a.currentRuntime()
	if run == nil || !run.pm.IsRunning() {
		return fmt.Errorf("agent %s 进程未运行", a.meta.ID)
	}

	// session/cancel 是通知，不需要 id
	msg := &protocol.ACPMessage{
		JSONRPC: "2.0",
		Method:  "session/cancel",
		Params: func() json.RawMessage {
			data, _ := json.Marshal(map[string]string{
				"sessionId": sessionID,
			})
			return data
		}(),
	}

	slog.Debug("[CANCEL] 发送 session/cancel 通知到 Agent",
		"agent", a.meta.ID,
		"session", sessionID,
	)

	if err := run.writer.WriteMessage(msg); err != nil {
		return fmt.Errorf("发送 session/cancel 失败: %w", err)
	}

	return nil
}
