// -*- coding: utf-8 -*-
// Go 1.25+
//
// router_prompt.go
// 消息路由器 — prompt 处理、流式/阻塞推送、消息持久化
// 负责将 prompt 请求转发到 Agent 并流式或阻塞处理响应
//
// Lzm 2026-07-22

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// PromptBlock ACP session/prompt 的内容块
// 支持 text、resource_link、resource 三种类型
// Lzm 2026-07-21
type PromptBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	URI      string          `json:"uri,omitempty"`
	Name     string          `json:"name,omitempty"`
	MIMEType string          `json:"mimeType,omitempty"`
	Resource json.RawMessage `json:"resource,omitempty"`
}

// handleInvokeSessionPrompt 处理 prompt 请求 — 最核心的交互
// 远程服务发来 prompt，Bridge 转发到 Agent，流式回传结果
// 支持：消息自动持久化、流式推送、EPERM/Session失效重试
// Lzm 2026-07-10
func (r *RequestRouter) handleInvokeSessionPrompt(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, params json.RawMessage, stream, agentRestarted bool, sessionMgr *SessionManager) *protocol.ANPMessage {
	// 解析 prompt 参数
	var promptParams struct {
		SessionID string        `json:"sessionId"`
		Prompt    []PromptBlock `json:"prompt"`
	}
	if err := json.Unmarshal(params, &promptParams); err != nil {
		return protocol.NewErrorResponse(msg.ID, -32602,
			fmt.Sprintf("解析 prompt 参数失败: %v", err))
	}

	// 提取用户文本（用于持久化）
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
		if err := a.LoadSession(ctx, sessionID); err != nil {
			return protocol.NewErrorResponse(msg.ID, -31005,
				fmt.Sprintf("加载会话 %s 失败: %v", sessionID, err))
		}
		sessionMgr.ActivateSession(agentID, sessionID)
	}

	isStream := wantsStreaming(stream, msg.ID)

	if isStream {
		return r.streamPrompt(ctx, msg, a, agentID, sessionID, userText, promptParams.Prompt, sessionMgr)
	}

	return r.blockingPrompt(ctx, msg, a, agentID, sessionID, userText, promptParams.Prompt, sessionMgr)
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
func (r *RequestRouter) streamPrompt(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, agentID, sessionID, userText string, promptBlocks []PromptBlock, sessionMgr *SessionManager) *protocol.ANPMessage {
	// 提前设置当前 invoke requestID，供同步路径下的权限/elicitation 回调使用
	r.setCurrentRequestID(sessionID, msg.ID)

	// 构建 ACP prompt 请求（透传原始 prompt 结构）
	acpReq := r.buildACPPromptReq(msg.ID, sessionID, promptBlocks)

	chunkCh, err := a.Stream(ctx, acpReq)
	if err != nil {
		r.clearCurrentRequestID(sessionID)
		return protocol.NewErrorResponse(msg.ID, -31007,
			fmt.Sprintf("发送 prompt 到 Agent 失败: %v", err))
	}

	if r.streamCB == nil {
		slog.Warn("流式回调未设置，结果将仅保存到本地会话")
	}

	// 在后台读取流式块，收集消息用于持久化
	go func() {
		// 在 goroutine 中重新设置 requestID（覆盖外层设置），确保异步处理的
		// 整个生命周期内 requestID 都可用。defer 在 goroutine 结束时清理。
		// BUGFIX(Lzm 2026-07-23): 之前在外层 defer clear，但 goroutine 刚
		// 启动时 streamPrompt 就返回了，导致 requestID 被提前清除，权限回
		// 调无法获取正确的 requestID，转发到 Server 的事件被网关丢弃。
		r.setCurrentRequestID(sessionID, msg.ID)
		defer r.clearCurrentRequestID(sessionID)
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
				slog.Warn("流式连接已断开，自动取消 Agent 请求",
					"request_id", msg.ID,
					"agent", agentID,
					"session", sessionID,
					"error", err,
				)
				// WebSocket 客户端断开后，自动取消 Agent 的进行中的请求。
				// 这样后续的 session/load 等请求不会因为 Agent 忙而阻塞。
				sessionMgr.CancelSession(agentID, sessionID)
				// 设置 5 秒后自动清理取消标记，避免残留影响后续请求。
				// 如果 drainAndClearCancel 先运行，此处的清理是无害的 no-op。
				time.AfterFunc(5*time.Second, func() {
					sessionMgr.ClearCancel(agentID, sessionID)
				})
				go func() {
					if cancelErr := a.Cancel(context.Background(), sessionID); cancelErr != nil {
						slog.Warn("自动取消 Agent 请求失败",
							"agent", agentID,
							"session", sessionID,
							"error", cancelErr,
						)
					}
				}()
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
				slog.Warn("[STREAM_PROMPT] Agent 流式错误",
					"agent", agentID,
					"session", truncateString(sessionID, 16),
					"error", chunkText,
				)
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
			// 检查 final chunk 是否包含 cancelled 状态
			// Agent 主动取消时返回 stopReason: "cancelled"，需通知 SaaS
			if chunk.Type == internal.StreamChunkFinal && chunk.Result != nil {
				if data, err := json.Marshal(chunk.Result); err == nil {
					var cancelledResult struct {
						StopReason string `json:"stopReason"`
					}
					if err := json.Unmarshal(data, &cancelledResult); err == nil && cancelledResult.StopReason == "cancelled" {
						slog.Warn("[STREAM_PROMPT] Agent 返回取消状态（stopReason=cancelled）",
							"agent", agentID,
							"session", truncateString(sessionID, 16),
						)
						r.persistPromptMessages(sessionMgr, agentID, sessionID, userText, thoughtParts, responseParts)
						if r.finalResponseCB != nil {
							r.finalResponseCB(msg.ID, nil, &protocol.ANPError{
								Code:    -32800,
								Message: "Request cancelled by Agent",
							})
						}
						forwarding = false
						return false
					}
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
			if newSID, err := sessionMgr.CreateNewSession(ctx, agentID, "", ""); err == nil {
				sessionID = newSID
				// 通知前端新 session ID
				forward("session_refreshed", newSID)

				acpReq = r.buildACPPromptReq(msg.ID, sessionID, promptBlocks)
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

		// 在 chunk 读取循环中每 200ms 检查 cancel 状态
		// 即使 Agent 不产生新 chunk，也能主动中断
		cancelCheck := time.NewTicker(200 * time.Millisecond)
		defer cancelCheck.Stop()
		chunkDone := false
		streamCancelled := false
		for !chunkDone {
			select {
			case chunk, ok := <-chunkCh:
				if !ok {
					chunkDone = true // channel 已关闭
					continue
				}
				if !handleChunk(chunk) {
					// handleChunk 返回 false 时，goroutine 需要退出。
					// 检查共享 cancel 标记；Agent 的 final chunk 可能在 ticker
					// 检查前到达，同时前端断开也可能早已由 forward 设置了
					// CancelSession。两种路径下都要清理 cancel 状态。
					if streamCancelled || sessionMgr.IsCancelled(agentID, sessionID) {
						r.drainAndClearCancel(chunkCh, sessionMgr, agentID, sessionID)
					}
					return
				}
			case <-cancelCheck.C:
				if sessionMgr.IsCancelled(agentID, sessionID) {
					slog.Info("检测到会话取消，主动中断 streaming",
						"agent", agentID, "session", sessionID,
					)
					streamCancelled = true
					r.persistPromptMessages(sessionMgr, agentID, sessionID, userText, thoughtParts, responseParts)
					if r.finalResponseCB != nil {
						r.finalResponseCB(msg.ID, nil, &protocol.ANPError{
							Code:    -32800,
							Message: "Request cancelled",
						})
					}
					// 不要立即 return！排空 chunkCh 让 doStream 自然结束，
					// 避免触发 invalidateRuntime 杀死 Agent 进程。
					r.drainAndClearCancel(chunkCh, sessionMgr, agentID, sessionID)
					return
				}
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
	// 清理可能残留的 cancel 标记，防止影响后续请求。
	sessionMgr.ClearCancel(agentID, sessionID)
	if r.finalResponseCB == nil {
		return
	}
	result := json.RawMessage(`{}`)
	if fullText := strings.Join(responseParts, ""); fullText != "" {
		result, _ = json.Marshal(map[string]string{"text": fullText})
	}
	r.finalResponseCB(requestID, result, nil)
}

// drainAndClearCancel 排空流式块通道 + 清除取消状态。
// 在检测到 cancel 后调用，等待 Agent 处理完取消并释放管道，
// 避免触发 invalidateRuntime 杀死 Agent 进程。
// 超时 20 秒后强制退出，防止 goroutine 泄漏。
// Lzm 2026-07-21
func (r *RequestRouter) drainAndClearCancel(chunkCh <-chan internal.StreamChunk, sessionMgr *SessionManager, agentID, sessionID string) {
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer drainCancel()
	drained := make(chan struct{}, 1)
	go func() {
		for range chunkCh {
		}
		select {
		case drained <- struct{}{}:
		default:
		}
	}()
	select {
	case <-drained:
	case <-drainCtx.Done():
		slog.Warn("排空流式通道超时（20s），强制清理取消状态",
			"agent", agentID, "session", truncateString(sessionID, 16),
		)
	}
	sessionMgr.ClearCancel(agentID, sessionID)
	slog.Debug("cancel 清理完成",
		"agent", agentID, "session", truncateString(sessionID, 16),
	)
}

// blockingPrompt 非流式 prompt 处理 — 等待完整响应 + 自动保存消息
// 支持主动取消检测：通过 goroutine 包装 a.Send() 并使用可取消 context，
// 使 SaaS 发送 session/cancel 后能主动中断阻塞等待
// Lzm 2026-07-20
func (r *RequestRouter) blockingPrompt(ctx context.Context, msg *protocol.ANPMessage, a agent.Agent, agentID, sessionID, userText string, promptBlocks []PromptBlock, sessionMgr *SessionManager) *protocol.ANPMessage {
	// 记录当前 invoke requestID，供权限/elicitation 回调使用
	r.setCurrentRequestID(sessionID, msg.ID)
	defer r.clearCurrentRequestID(sessionID)

	acpReq := r.buildACPPromptReq(msg.ID, sessionID, promptBlocks)

	// 创建可取消 context，用于中断阻塞的 a.Send()
	promptCtx, promptCancel := context.WithCancel(ctx)
	defer promptCancel()

	type sendResult struct {
		resp *protocol.ACPMessage
		err  error
	}
	resultCh := make(chan sendResult, 1)

	go func() {
		resp, err := a.Send(promptCtx, acpReq)
		resultCh <- sendResult{resp, err}
	}()

	cancelCheck := time.NewTicker(200 * time.Millisecond)
	defer cancelCheck.Stop()

	for {
		select {
		case sr := <-resultCh:
			if sr.err != nil {
				return protocol.NewErrorResponse(msg.ID, -31008,
					fmt.Sprintf("Agent 响应错误: %v", sr.err))
			}
			// 自动保存消息（非流式模式尝试从响应中提取文本）
			if sr.resp.IsSuccess() {
				// 检查响应是否包含 cancelled 状态（Agent 主动取消）
				if isCancelled(sr.resp) {
					r.persistResponseMessage(sessionMgr, agentID, sessionID, userText, sr.resp)
					return protocol.NewErrorResponse(msg.ID, -32800, "Request cancelled by Agent")
				}
				r.persistResponseMessage(sessionMgr, agentID, sessionID, userText, sr.resp)
				return protocol.NewResultResponse(msg.ID, sr.resp.Result)
			}
			if sr.resp.Error != nil {
				return protocol.NewErrorResponse(msg.ID, sr.resp.Error.Code,
					sr.resp.Error.Message)
			}
			return protocol.NewErrorResponse(msg.ID, -31009, "Agent 返回未知响应")

		case <-cancelCheck.C:
			if sessionMgr.IsCancelled(agentID, sessionID) {
				slog.Info("检测到会话取消，等待 Agent 处理取消",
					"agent", agentID, "session", sessionID,
				)
				// 不要立即 cancel promptCtx（那会触发 invalidateRuntime 杀死进程）。
				// 而是等待 Agent 响应 cancel 通知（handleSessionCancel 已发送）。
				// Agent 应该在几秒内返回 cancelled 响应。
				select {
				case sr := <-resultCh:
					sessionMgr.ClearCancel(agentID, sessionID)
					if sr.err != nil {
						return protocol.NewErrorResponse(msg.ID, -32800,
							fmt.Sprintf("Request cancelled: %v", sr.err))
					}
					if sr.resp != nil && sr.resp.Error != nil {
						return protocol.NewErrorResponse(msg.ID, -32800,
							fmt.Sprintf("Request cancelled: [%d] %s", sr.resp.Error.Code, sr.resp.Error.Message))
					}
					return protocol.NewErrorResponse(msg.ID, -32800, "Request cancelled by Agent")
				case <-time.After(30 * time.Second):
					slog.Warn("等待 Agent 取消响应超时，强制中断",
						"agent", agentID, "session", sessionID,
					)
					promptCancel() // 兜底：强制中断
				}
				sessionMgr.ClearCancel(agentID, sessionID)
				return protocol.NewErrorResponse(msg.ID, -32800, "Request cancelled")
			}

		case <-ctx.Done():
			return protocol.NewErrorResponse(msg.ID, -31008,
				fmt.Sprintf("请求已取消: %v", ctx.Err()))
		}
	}
}

// buildACPPromptReq 构建 ACP session/prompt 请求
// 透传原始 prompt 块结构，支持 text、resource_link、resource 三种类型
// Lzm 2026-07-21
func (r *RequestRouter) buildACPPromptReq(requestID, sessionID string, promptBlocks []PromptBlock) *protocol.ACPMessage {
	// 如果没有 prompt blocks，创建一个默认的 text 块
	blocks := promptBlocks
	if len(blocks) == 0 {
		blocks = []PromptBlock{{Type: "text", Text: ""}}
	}

	return &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      protocol.MarshalStringID(requestID),
		Method:  "session/prompt",
		Params: func() json.RawMessage {
			data, _ := json.Marshal(map[string]interface{}{
				"sessionId": sessionID,
				"prompt":    blocks,
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
// 兼容 Codex 多种响应格式：{"text": "..."}、纯字符串、或 result 直接为文本
// Lzm 2026-07-21
func (r *RequestRouter) persistResponseMessage(sessionMgr *SessionManager, agentID, sessionID, userText string, resp *protocol.ACPMessage) {
	messages := make([]StoredMessage, 0, 2)

	if userText != "" {
		messages = append(messages, StoredMessage{Role: "user", Text: userText})
	}

	// 尝试从 result 中提取响应文本（支持多种格式）
	if resp.Result != nil {
		assistantText := extractResponseText(resp.Result)
		if assistantText != "" {
			messages = append(messages, StoredMessage{Role: "assistant", Text: assistantText})
		}
	}

	if len(messages) > 0 {
		sessionMgr.SaveMessages(agentID, sessionID, messages)
	}
}

// extractResponseText 从 ACP 响应 result 中提取文本
// 兼容格式：
//   - {"text": "..."} — 标准格式
//   - "纯文本" — 直接字符串
//   - {"content": [{"type": "text", "text": "..."}]} — 内容数组格式
// Lzm 2026-07-21
func extractResponseText(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}

	// 尝试直接解析为字符串
	var plainText string
	if err := json.Unmarshal(result, &plainText); err == nil && plainText != "" {
		return plainText
	}

	// 尝试解析为对象并提取 text 字段
	var obj map[string]interface{}
	if err := json.Unmarshal(result, &obj); err != nil {
		return ""
	}

	if text, ok := obj["text"].(string); ok && text != "" {
		return text
	}

	// 尝试 content 数组格式（某些 Agent 的嵌套结构）
	if content, ok := obj["content"].([]interface{}); ok {
		for _, c := range content {
			if block, ok := c.(map[string]interface{}); ok {
				if blockType, _ := block["type"].(string); blockType == "text" {
					if text, ok := block["text"].(string); ok {
						return text
					}
				}
			}
		}
	}

	return ""
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

// isCancelled 检查 ACP 响应是否包含 cancelled 状态
// 当 Agent 返回 stopReason: "cancelled" 时，表示请求已被取消
// Lzm 2026-07-20
func isCancelled(resp *protocol.ACPMessage) bool {
	if resp == nil || !resp.IsSuccess() {
		return false
	}
	if resp.Result == nil {
		return false
	}
	var result struct {
		StopReason string `json:"stopReason"`
	}
	if data, err := json.Marshal(resp.Result); err == nil {
		if err := json.Unmarshal(data, &result); err == nil {
			return result.StopReason == "cancelled"
		}
	}
	return false
}
