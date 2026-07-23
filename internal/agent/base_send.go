// -*- coding: utf-8 -*-
// Go 1.25+
//
// base_send.go
// Agent ACP 发送/流式通信方法 — Send、Stream 及其内部实现
//
// Lzm 2026-07-22

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// --- ACP 通信（全局实现，所有 Agent 共用） ---

// Send 发送请求并等待完整响应（非流式）。
// 检查 Agent 状态后委托给 doSend。
// Lzm 2026-07-21
func (a *baseAgent) Send(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doSend(ctx, req)
}

// Stream 发送请求并返回流式块通道。
// 检查 Agent 状态后委托给 doStream。
// Lzm 2026-07-21
func (a *baseAgent) Stream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error) {
	if a.Status() != AgentIdle && a.Status() != AgentBusy {
		return nil, fmt.Errorf("agent %s 未就绪，状态: %s", a.meta.ID, a.Status())
	}
	return a.doStream(ctx, req)
}

// doSend 发送请求并等待完整响应（互斥访问 ACP 管道）
// Lzm 2026-07-09
func (a *baseAgent) doSend(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	if err := a.acquireRequest(ctx); err != nil {
		return nil, MapError(err)
	}
	defer a.releaseRequest()

	run := a.currentRuntime()
	if run == nil || !run.pm.IsRunning() {
		return nil, MapError(fmt.Errorf("agent %s 进程未运行", a.meta.ID))
	}

	a.setStatusForRuntime(run, AgentBusy)
	defer a.finishRequest(run)

	// 发送请求
	if err := run.writer.WriteMessage(req); err != nil {
		a.invalidateRuntime(run)
		return nil, MapError(fmt.Errorf("发送 ACP 消息失败: %w", err))
	}

	readTimer := time.NewTimer(a.meta.ReadTimeout)
	defer readTimer.Stop()

	// 读取响应（跳过流式通知，直到找到匹配 ID 的响应）
	for {
		select {
		case msg := <-run.readCh:
			if msg == nil {
				a.invalidateRuntime(run)
				return nil, MapError(fmt.Errorf("ACP 进程过早退出"))
			}
			a.resetReadTimer(readTimer)

			// 处理 Agent 权限请求（Codex 特有的 session/request_permission）
			if a.handlePermissionRequest(run, msg) {
				continue
			}

			// 跳过流式通知
			if msg.IsStreamUpdate() {
				continue
			}
			// 检查是否匹配请求 ID
			if msg.IDMatch(req.IDString()) {
				// [Codex 503] 检测 Agent 返回的 503 Service Unavailable 错误
				// Codex 的上游 API 经常返回 503，需要重试
				// Lzm 2026-07-22
				if msg.Error != nil && IsCodex503(msg.Error.Message) {
					return nil, &RetryableError{
						Err:        fmt.Errorf("Codex 503 Service Unavailable: %s", msg.Error.Message),
						RetryAfter: 2 * time.Second,
					}
				}
				return msg, nil
			}
			// 处理 Agent→Client 请求（fs/read_text_file, terminal/create 等）
			// Agent 在执行任务时会通过这些方法请求 Bridge 执行本地操作
			if a.handleACPRequest(run, msg) {
				continue
			}
			// ID 不匹配，继续等待
			slog.Debug("收到非匹配响应",
				"expected_id", req.IDString(),
				"got_id", msg.IDString(),
			)
			continue
		case <-ctx.Done():
			a.invalidateRuntime(run)
			return nil, MapError(fmt.Errorf("等待 ACP 响应超时: %w", ctx.Err()))
		case <-readTimer.C:
			a.invalidateRuntime(run)
			return nil, MapError(fmt.Errorf("等待 ACP 响应超时: %s 内未收到消息", a.meta.ReadTimeout))
		}
	}
}

// doStream 发送请求并返回流式块通道
// 调用方负责消耗通道直至关闭
//
// 序列化约束：ACP 是请求-响应式协议（共享 stdin/stdout），doStream
// 在 Agent 返回终止消息前占用 requestGate。其他调用可以通过自己的
// context 取消等待；输出通道使用背压保证响应文本不会静默丢失。
//
// Lzm 2026-07-10
func (a *baseAgent) doStream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error) {
	if err := a.acquireRequest(ctx); err != nil {
		return nil, MapError(err)
	}

	run := a.currentRuntime()
	if run == nil || !run.pm.IsRunning() {
		a.releaseRequest()
		return nil, MapError(fmt.Errorf("agent %s 进程未运行", a.meta.ID))
	}

	a.setStatusForRuntime(run, AgentBusy)

	// 发送请求
	if err := run.writer.WriteMessage(req); err != nil {
		a.releaseRequest()
		a.invalidateRuntime(run)
		return nil, MapError(fmt.Errorf("发送 ACP 流式请求失败: %w", err))
	}

	// 创建流式块通道
	chunkCh := make(chan internal.StreamChunk, 50)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("流式读取协程 panic",
					"agent", a.meta.ID,
					"panic", r,
				)
			}
		}()
		defer a.releaseRequest()
		defer a.finishRequest(run)
		defer close(chunkCh)

		readTimer := time.NewTimer(a.meta.ReadTimeout)
		defer readTimer.Stop()

		for {
			select {
			case msg := <-run.readCh:
				if msg == nil {
					// 进程退出
					a.invalidateRuntime(run)
					a.deliverTerminalChunk(chunkCh, internal.StreamChunk{
						Type: internal.StreamChunkError,
						Error: &internal.ACPError{
							Code:    -1,
							Message: "ACP 进程过早退出",
						},
					})
					return
				}
				a.resetReadTimer(readTimer)

				slog.Debug("流式读取到 ACP 消息",
					"agent", a.meta.ID,
					"method", msg.Method,
					"msg_id", msg.IDString(),
					"is_stream_update", msg.IsStreamUpdate(),
				)

				if msg.IsStreamUpdate() {
					// 流式通知 → 解析为 StreamChunk
					chunk, err := protocol.ParseStreamChunk(msg)
					if err == nil && chunk != nil {
						terminal := chunk.IsFinal || chunk.Type == internal.StreamChunkError
						if !a.deliverStreamChunk(ctx, run, chunkCh, *chunk) {
							a.invalidateRuntime(run)
							return
						}
						// 流结束条件：final 或 error
						if terminal {
							return
						}
					} else if err != nil {
						slog.Warn("解析流式块失败",
							"agent", a.meta.ID,
							"error", err,
						)
					} else {
						// chunk == nil && err == nil：无法识别的格式
						slog.Warn("忽略无法识别的流式通知",
							"agent", a.meta.ID,
							"raw", truncateString(string(msg.Params), 200),
						)
					}
					continue
				}

				// 最终响应
				if msg.IDMatch(req.IDString()) {
					// [Codex 503] 检测 Agent 返回的 503 Service Unavailable 错误，
					// 通过 error chunk 传递给上层，调用方自行决定是否重试
					// Lzm 2026-07-22
					if msg.Error != nil && IsCodex503(msg.Error.Message) {
						a.deliverTerminalChunk(chunkCh, internal.StreamChunk{
							Type: internal.StreamChunkError,
							Error: &internal.ACPError{
								Code:    -32000,
								Message: fmt.Sprintf("503 Service Unavailable: %s", msg.Error.Message),
							},
						})
						return
					}
					if msg.IsSuccess() {
						if !a.deliverStreamChunk(ctx, run, chunkCh, finalStreamChunk(msg.Result)) {
							a.invalidateRuntime(run)
						}
					} else if msg.Error != nil {
						if !a.deliverStreamChunk(ctx, run, chunkCh, internal.StreamChunk{
							Type:  internal.StreamChunkError,
							Error: msg.Error,
						}) {
							a.invalidateRuntime(run)
						}
					}
					return
				}
				// 处理 Agent 权限请求（session/request_permission）
				if a.handlePermissionRequest(run, msg) {
					slog.Debug("权限请求处理完成",
						"agent", a.meta.ID,
					)
					continue
				}
				// 非流式非权限消息（工具调用或最终响应），记录概要
				// Lzm 2026-07-22
				slog.Debug("收到 Agent 非流式消息",
					"agent", a.meta.ID,
					"method", msg.Method,
					"has_result", msg.Result != nil,
					"result_snippet", truncateString(string(msg.Result), 200),
					"has_params", len(msg.Params) > 0,
					"has_error", msg.Error != nil,
				)
				// 处理 Agent→Client 请求（fs/read_text_file, terminal/create 等）
				if handled := a.handleACPRequest(run, msg); handled {
					slog.Debug("工具调用处理完成",
						"agent", a.meta.ID,
						"method", msg.Method,
					)
					// 工具调用可能阻塞较长时间（如 terminal/wait_for_exit），
					// 处理完后立即重置 readTimer，防止超时级联杀死进程。
					a.resetReadTimer(readTimer)
					continue
				}
				// 未处理的请求 → 可能是 Codex 调用了不支持的 ACP 方法（如 terminal/write_stdin）
				slog.Warn("Agent 调用了未处理的 ACP 请求，可能引起挂起",
					"agent", a.meta.ID,
					"method", msg.Method,
					"msg_id", msg.IDString(),
					"expected_id", req.IDString(),
				)
				continue

			case <-ctx.Done():
				a.invalidateRuntime(run)
				a.deliverTerminalChunk(chunkCh, internal.StreamChunk{
					Type: internal.StreamChunkError,
					Error: &internal.ACPError{
						Code:    -2,
						Message: fmt.Sprintf("请求取消: %v", ctx.Err()),
					},
				})
				return
			case <-readTimer.C:
				slog.Warn("ACP 流式读取超时，Agent 可能挂起",
					"agent", a.meta.ID,
					"timeout", a.meta.ReadTimeout,
					"req_id", req.IDString(),
				)
				a.deliverTerminalChunk(chunkCh, internal.StreamChunk{
					Type: internal.StreamChunkError,
					Error: &internal.ACPError{
						Code:    -2,
						Message: fmt.Sprintf("等待 ACP 流式响应超时: %s 内未收到消息", a.meta.ReadTimeout),
					},
				})
				a.invalidateRuntime(run)
				return
			}
		}
	}()

	return chunkCh, nil
}

// deliverStreamChunk applies backpressure without losing Agent output. The
// caller's context or the current process generation ending can always release
// the producer and therefore the request gate.
func (a *baseAgent) deliverStreamChunk(ctx context.Context, run *agentRuntime, ch chan internal.StreamChunk, chunk internal.StreamChunk) bool {
	select {
	case ch <- chunk:
		return true
	case <-ctx.Done():
		return false
	case <-run.done:
		return false
	}
}

// deliverTerminalChunk is used after cancellation/process failure, when the
// normal delivery select is no longer available. It never removes buffered
// response data; an active consumer receives the explicit terminal error,
// while an abandoned consumer cannot keep the runtime locked.
func (a *baseAgent) deliverTerminalChunk(ch chan internal.StreamChunk, chunk internal.StreamChunk) {
	select {
	case ch <- chunk:
	default:
	}
}

func finalStreamChunk(result json.RawMessage) internal.StreamChunk {
	chunk := internal.StreamChunk{
		Type:   internal.StreamChunkFinal,
		Result: result,
	}
	var payload struct {
		Text       string `json:"text"`
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(result, &payload); err == nil {
		chunk.Text = payload.Text
		if payload.StopReason != "" {
			slog.Debug("finalStreamChunk 包含 stopReason",
				"stop_reason", payload.StopReason,
				"text_snippet", truncateString(payload.Text, 120),
			)
		}
	}
	// 记录 final chunk 原始 result，用于排查 Agent 授权后返回空文本的问题
	// Lzm 2026-07-22
	slog.Debug("finalStreamChunk 原始 result",
		"raw_json", truncateString(string(result), 300),
		"has_text", chunk.Text != "",
		"text_len", len(chunk.Text),
	)
	return chunk
}

// --- 权限请求处理 ---

// handlePermissionRequest 处理 Agent 的权限请求（session/request_permission）
// 优先使用权限回调（由 Router 设置，用于转发给用户手动授权）；
// 若未设置回调则自动批准（向后兼容）。
// 返回 true 表示消息已被处理（已发送同意/拒绝响应），调用方应继续等待。
// Lzm 2026-07-21
func (a *baseAgent) handlePermissionRequest(run *agentRuntime, msg *protocol.ACPMessage) bool {
	if msg.Method != "session/request_permission" {
		return false
	}

	paramsStr := string(msg.Params)
	slog.Debug("收到 Agent 权限请求",
		"agent", a.meta.ID,
		"method", msg.Method,
		"msg_id", msg.IDString(),
	)

	// 判断是否使用权限回调（手动授权模式）
	allowed := true     // 默认允许（无回调时）
	permMode := ""      // 授权模式：""（允许一次）/ "auto_approve"（始终允许）
	if a.permissionCB != nil {
		slog.Info("收到 Agent 权限请求（转发用户手动授权）",
			"agent", a.meta.ID,
			"params", paramsStr,
			"msg_id", msg.IDString(),
		)
		var err error
		allowed, permMode, err = a.permissionCB(msg.Params)
		if err != nil {
			slog.Warn("权限回调处理失败，默认拒绝",
				"agent", a.meta.ID,
				"error", err,
			)
			allowed = false
		}
		slog.Debug("权限回调结果",
			"agent", a.meta.ID,
			"allowed", allowed,
			"perm_mode", permMode,
		)
	} else {
		slog.Info("收到 Agent 权限请求（自动批准）",
			"agent", a.meta.ID,
			"params", paramsStr,
			"msg_id", msg.IDString(),
		)
	}

	// 从原始请求参数中解析 Agent 提供的选项列表
	// 不同 Agent 使用不同的 optionId：
	//   - Codex: "allow" / "deny"
	//   - Kimi:  "approve" (kind:"allow") / "approve_once" (kind:"allow_once") / "deny" (kind:"deny")
	//   - Claude Code: "allow_always" (kind:"allow_always") / "allow" (kind:"allow_once") / "reject" (kind:"reject_once")
	// 我们必须根据 Agent 实际提供的选项来构造响应，否则 Agent 无法识别。
	// Lzm 2026-07-22
	optionID := resolvePermissionOptionID(msg.Params, allowed, permMode)
	resp := &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  json.RawMessage(fmt.Sprintf(`{"outcome":{"outcome":"selected","optionId":"%s"}}`, optionID)),
	}
	if err := run.writer.WriteMessage(resp); err != nil {
		slog.Warn("发送权限响应失败",
			"agent", a.meta.ID,
			"error", err,
			"option", optionID,
		)
	} else {
		slog.Info("权限响应已成功发送给 Agent",
			"agent", a.meta.ID,
			"option", optionID,
			"msg_id", msg.IDString(),
		)
	}
	return true
}

// permissionOption 权限请求中的单个选项
// Lzm 2026-07-22
type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

// resolvePermissionOptionID 从 Agent 的权限请求参数中解析出合适的 optionId。
// 优先匹配 Agent 提供的选项列表中的 optionId，兜底使用 "allow"/"deny"。
// mode 参数指示授权模式：
//   - "" 或 "request_approval"：允许一次，优先匹配 kind 为 "allow" 或 "allow_once" 的选项
//   - "auto_approve"：始终允许，优先匹配 kind 为 "allow_always" 或 "always_allow" 的选项
//   - "full_access"：同 auto_approve，也优先匹配 "allow_always"
func resolvePermissionOptionID(params json.RawMessage, allowed bool, mode string) string {
	// 解析选项列表
	var req struct {
		Options []permissionOption `json:"options"`
	}
	if err := json.Unmarshal(params, &req); err != nil || len(req.Options) == 0 {
		if allowed {
			return "allow"
		}
		return "deny"
	}

	if allowed {
		// ── 允许模式 ──
		// 根据授权模式选择目标 kind
		// "auto_approve" / "full_access" → 找 allow_always / always_allow
		// "" / "request_approval" → 找 allow / allow_once
		// Lzm 2026-07-22
		targetKind := ""
		fallbackKinds := []string{}
		if mode == "auto_approve" || mode == "full_access" {
			// 始终允许模式：优先匹配 allow_always
			targetKind = "allow_always"
			fallbackKinds = []string{"always_allow", "allow_once", "allow"}
		} else {
			// 允许一次模式：优先匹配 allow_once，然后 allow
			targetKind = "allow_once"
			fallbackKinds = []string{"allow", "allow_always", "always_allow"}
		}

		// 精确匹配目标 kind
		for _, opt := range req.Options {
			if opt.Kind == targetKind {
				return opt.OptionID
			}
		}

		// 回退匹配 fallback kinds
		for _, fk := range fallbackKinds {
			for _, opt := range req.Options {
				if opt.Kind == fk {
					return opt.OptionID
				}
			}
		}

		// 最后回退：使用第一个非 deny 的选项
		for _, opt := range req.Options {
			if opt.Kind != "deny" && opt.Kind != "reject" && opt.Kind != "reject_once" {
				return opt.OptionID
			}
		}

		// 兜底：使用第一个选项
		return req.Options[0].OptionID
	}

	// ── 拒绝模式 ──
	// 优先匹配 deny / reject / reject_once
	rejectKinds := []string{"deny", "reject", "reject_once"}
	for _, rk := range rejectKinds {
		for _, opt := range req.Options {
			if opt.Kind == rk {
				return opt.OptionID
			}
		}
	}

	return "deny"
}

// truncateString 截断字符串到指定长度（用于日志记录）
// Lzm 2026-07-10
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
