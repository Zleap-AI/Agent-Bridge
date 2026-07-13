// -*- coding: utf-8 -*-
// Go 1.26+
//
// base.go
// 共享的 Agent 基础实现 — 进程管理、ACP 读写、状态跟踪
// 所有具体 Agent 类型通过组合此结构快速实现 Agent 接口
//
// Lzm 2026-07-09

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/zleap/bridge/internal"
	"github.com/zleap/bridge/internal/infra"
	"github.com/zleap/bridge/internal/protocol"
)

// baseAgent 封装 Agent 的公共逻辑
// 具体 Agent 类型（ClaudeCode、OpenCode 等）通过嵌入此结构实现 Agent 接口
type baseAgent struct {
	meta   AgentMeta
	status AgentStatus

	pm     *infra.ProcessManager // 子进程管理器
	writer *protocol.ACPWriter   // ACP 写入器（stdin）
	reader *protocol.ACPReader   // ACP 读取器（stdout）

	mu       sync.Mutex   // 序列化 ACP 请求（ACP 是请求-响应式）
	statusMu sync.RWMutex // 保护 status 字段

	msgIDCounter int // ACP 消息 ID 计数器

	// 流式读取相关
	readCh   chan *protocol.ACPMessage // 从 stdout 读取的所有消息
	stopRead context.CancelFunc        // 停止读取协程
	readCtx  context.Context
	wg       sync.WaitGroup // 等待后台协程退出

	stderrBuf   bytes.Buffer // 子进程 stderr 环形缓冲区（用于故障诊断）
	stderrBufMu sync.Mutex   // 保护 stderrBuf
}

// newBaseAgent 创建 baseAgent 实例
func newBaseAgent(meta AgentMeta) *baseAgent {
	if meta.StartupTimeout <= 0 {
		meta.StartupTimeout = DefaultStartupTimeout
	}
	if meta.ReadTimeout <= 0 {
		meta.ReadTimeout = DefaultReadTimeout
	}
	return &baseAgent{
		meta:         meta,
		status:       AgentDisconnected,
		msgIDCounter: 0,
	}
}

// ID 返回 Agent 唯一标识
func (a *baseAgent) ID() string { return a.meta.ID }

// DisplayName 返回显示名称
func (a *baseAgent) DisplayName() string { return a.meta.DisplayName }

// Status 返回当前状态
func (a *baseAgent) Status() AgentStatus {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	return a.status
}

// setStatus 设置状态（线程安全）
func (a *baseAgent) setStatus(s AgentStatus) {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	a.status = s
}

// nextID 生成下一个 ACP 消息 ID
func (a *baseAgent) nextID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.msgIDCounter++
	return fmt.Sprintf("%d", a.msgIDCounter)
}

// --- 生命周期 ---

// startProcess 启动 Agent 子进程
// Lzm 2026-07-09
func (a *baseAgent) startProcess(ctx context.Context) error {
	pm, err := infra.StartProcess(ctx, infra.StartProcessConfig{
		Command: a.meta.Cmd,
		Args:    a.meta.Args,
		WorkDir: a.meta.WorkDir,
		Env:     a.meta.Env,
	})
	if err != nil {
		return &AgentStartError{AgentID: a.meta.ID, Err: err}
	}
	a.pm = pm
	a.writer = protocol.NewACPWriter(pm.Stdin())
	a.reader = protocol.NewACPReader(pm.Stdout())

	slog.Info("Agent 进程已启动",
		"agent", a.meta.ID,
		"pid", pm.PID(),
	)
	return nil
}

// startReadLoop 启动后台读取协程
// 持续从 ACP stdout 读取消息，路由到等待中的请求
// Lzm 2026-07-10
func (a *baseAgent) startReadLoop(ctx context.Context) {
	readCtx, cancel := context.WithCancel(ctx)
	a.stopRead = cancel
	a.readCtx = readCtx
	a.readCh = make(chan *protocol.ACPMessage, 100)

	a.wg.Add(2)

	// ACP stdout 读取协程
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("Agent 读取协程 panic",
					"agent", a.meta.ID,
					"panic", r,
				)
			}
		}()
		defer a.wg.Done()
		defer close(a.readCh)

		for {
			msg, err := a.reader.ReadMessage()
			if err != nil {
				slog.Debug("Agent 读取结束",
					"agent", a.meta.ID,
					"error", err,
				)
				return
			}
			if msg == nil {
				// 流已结束（进程退出）
				return
			}

			select {
			case a.readCh <- msg:
			case <-readCtx.Done():
				return
			}
		}
	}()

	// stderr 读取协程
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Debug("stderr 读取协程退出", "agent", a.meta.ID)
			}
		}()
		defer a.wg.Done()
		buf := make([]byte, 4096)
		for {
			select {
			case <-readCtx.Done():
				return
			default:
				n, err := a.pm.StderrReader().Read(buf)
				if n > 0 {
					chunk := string(buf[:n])
					// 写入环形缓冲区（最多保留 16KB）
					a.stderrBufMu.Lock()
					if a.stderrBuf.Len() > 16*1024 {
						a.stderrBuf.Reset()
					}
					a.stderrBuf.WriteString(chunk)
					a.stderrBufMu.Unlock()
					slog.Warn("Agent stderr",
						"agent", a.meta.ID,
						"output", chunk,
					)
				}
				if err != nil {
					return
				}
			}
		}
	}()
}

// doHandshake 执行 ACP initialize 握手
// Lzm 2026-07-09
func (a *baseAgent) doHandshake(ctx context.Context) error {
	req := protocol.NewInitializeRequest(a.nextID())
	if err := a.writer.WriteMessage(req); err != nil {
		return fmt.Errorf("发送 initialize 请求失败: %w", err)
	}

	// 等待响应
	select {
	case msg := <-a.readCh:
		if msg == nil {
			// Agent 进程过早退出，读取 stderr 帮助诊断
			a.stderrBufMu.Lock()
			stderrOut := strings.TrimSpace(a.stderrBuf.String())
			a.stderrBufMu.Unlock()
			if stderrOut != "" {
				// 截取最后 512 字节（通常是真正的原因）
				if len(stderrOut) > 512 {
					stderrOut = "..." + stderrOut[len(stderrOut)-512:]
				}
				return fmt.Errorf("ACP 进程过早退出（stderr: %s）", stderrOut)
			}
			return fmt.Errorf("ACP 进程过早退出")
		}
		if msg.IsSuccess() {
			var info struct {
				ProtocolVersion int                    `json:"protocolVersion"`
				Capabilities    map[string]interface{} `json:"capabilities"`
				ServerInfo      struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"serverInfo"`
			}
			if err := json.Unmarshal(msg.Result, &info); err == nil {
				slog.Info("ACP 握手成功",
					"agent", a.meta.ID,
					"name", info.ServerInfo.Name,
					"version", info.ServerInfo.Version,
					"protocol", info.ProtocolVersion,
				)
			} else {
				slog.Info("ACP 握手成功",
					"agent", a.meta.ID,
				)
			}
			return nil
		}
		if msg.Error != nil {
			return fmt.Errorf("ACP 握手失败: code=%d message=%s",
				msg.Error.Code, msg.Error.Message)
		}
		return fmt.Errorf("ACP 握手返回异常消息: %+v", msg)
	case <-ctx.Done():
		return fmt.Errorf("ACP 握手超时: %w", ctx.Err())
	}
}

// Stop 终止 Agent 进程
// Lzm 2026-07-10
func (a *baseAgent) Stop(ctx context.Context) error {
	a.setStatus(AgentDisconnected)

	if a.stopRead != nil {
		a.stopRead()
	}

	if a.pm != nil {
		err := a.pm.Stop()
		// 等待后台协程退出（带超时）
		waitDone := make(chan struct{})
		go func() {
			a.wg.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
			slog.Warn("后台协程退出超时", "agent", a.meta.ID)
		}
		return err
	}
	return nil
}

// Health 检查 Agent 进程是否健康
func (a *baseAgent) Health(ctx context.Context) error {
	if a.pm == nil || !a.pm.IsRunning() {
		return fmt.Errorf("agent %s 进程未运行", a.meta.ID)
	}
	return nil
}

// --- ACP 通信 ---

// doSend 发送请求并等待完整响应（互斥访问 ACP 管道）
// Lzm 2026-07-09
func (a *baseAgent) doSend(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.setStatus(AgentBusy)
	defer a.setStatus(AgentIdle)

	// 发送请求
	if err := a.writer.WriteMessage(req); err != nil {
		return nil, fmt.Errorf("发送 ACP 消息失败: %w", err)
	}

	// 读取响应（跳过流式通知，直到找到匹配 ID 的响应）
	for {
		select {
		case msg := <-a.readCh:
			if msg == nil {
				return nil, fmt.Errorf("ACP 进程过早退出")
			}
			// 跳过流式通知
			if msg.IsStreamUpdate() {
				continue
			}
			// 检查是否匹配请求 ID
			if msg.ID == req.ID {
				return msg, nil
			}
			// ID 不匹配，继续等待
			slog.Debug("收到非匹配响应",
				"expected_id", req.ID,
				"got_id", msg.ID,
			)
			continue
		case <-ctx.Done():
			return nil, fmt.Errorf("等待 ACP 响应超时: %w", ctx.Err())
		}
	}
}

// doStream 发送请求并返回流式块通道
// 调用方负责消耗通道直至关闭
//
// ⚠️ 序列化约束：ACP 是请求-响应式协议（共享 stdin/stdout），
// doStream 在流式期间持有 a.mu 锁，防止并发写入 stdin。
// 在流式通道关闭前，其他 doSend/doStream 调用会阻塞等待。
// 调用方不应在流式处理中再调用 doSend。
//
// Lzm 2026-07-10
func (a *baseAgent) doStream(ctx context.Context, req *protocol.ACPMessage) (<-chan internal.StreamChunk, error) {
	a.mu.Lock()

	a.setStatus(AgentBusy)

	// 发送请求
	if err := a.writer.WriteMessage(req); err != nil {
		a.mu.Unlock()
		a.setStatus(AgentIdle)
		return nil, fmt.Errorf("发送 ACP 流式请求失败: %w", err)
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
		defer a.mu.Unlock()
		defer a.setStatus(AgentIdle)
		defer close(chunkCh)

		for {
			select {
			case msg := <-a.readCh:
				if msg == nil {
					// 进程退出
					chunkCh <- internal.StreamChunk{
						Type: internal.StreamChunkError,
						Error: &internal.ACPError{
							Code:    -1,
							Message: "ACP 进程过早退出",
						},
					}
					return
				}

				if msg.IsStreamUpdate() {
					// 流式通知 → 解析为 StreamChunk
					chunk, err := protocol.ParseStreamChunk(msg)
					if err == nil && chunk != nil {
						chunkCh <- *chunk
						// 流结束条件：final 或 error
						if chunk.IsFinal || chunk.Type == internal.StreamChunkError {
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
				if msg.ID == req.ID {
					if msg.IsSuccess() {
						chunkCh <- internal.StreamChunk{
							Type:   internal.StreamChunkFinal,
							Result: msg.Result,
						}
					} else if msg.Error != nil {
						chunkCh <- internal.StreamChunk{
							Type:  internal.StreamChunkError,
							Error: msg.Error,
						}
					}
					return
				}
				// ID 不匹配，继续等待
				slog.Debug("收到非匹配响应（流式模式）",
					"expected_id", req.ID,
					"got_id", msg.ID,
				)
				continue

			case <-ctx.Done():
				chunkCh <- internal.StreamChunk{
					Type: internal.StreamChunkError,
					Error: &internal.ACPError{
						Code:    -2,
						Message: fmt.Sprintf("请求取消: %v", ctx.Err()),
					},
				}
				return
			}
		}
	}()

	return chunkCh, nil
}

// --- 会话管理 ---

// doNewSession 创建新 ACP 会话并返回 sessionId
// Lzm 2026-07-10
func (a *baseAgent) doNewSession(ctx context.Context) (string, error) {
	req := protocol.NewSessionRequest(a.nextID(), a.meta.WorkDir)
	resp, err := a.doSend(ctx, req)
	if err != nil {
		return "", &SessionError{Err: err}
	}

	if !resp.IsSuccess() {
		if resp.Error != nil {
			// 记录原始错误以便调试
			slog.Warn("session/new 返回错误",
				"agent", a.meta.ID,
				"req_id", req.ID,
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

	// 提取 sessionId
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		// 无法解析 sessionId，记录原始内容
		slog.Warn("解析 session/new 结果失败",
			"agent", a.meta.ID,
			"req_id", req.ID,
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

	return result.SessionID, nil
}

// doLoadSession 加载已有会话
// 注意：必须传递 cwd 和 mcpServers 参数，部分 Agent（如 Kimi）校验参数完整性
// Lzm 2026-07-10
func (a *baseAgent) doLoadSession(ctx context.Context, sessionID string) error {
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId":  sessionID,
		"cwd":        a.meta.WorkDir,
		"mcpServers": []interface{}{},
	})
	req := &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      a.nextID(),
		Method:  "session/load",
		Params:  params,
	}

	slog.Debug("doLoadSession: 发送 ACP session/load 请求",
		"agent", a.meta.ID,
		"session_id", sessionID,
		"cwd", a.meta.WorkDir,
		"request_id", req.ID,
		"params", string(params),
	)

	// session/load 可能会先发流式通知再返回结果
	// 使用流式读取处理
	ch, err := a.doStream(ctx, req)
	if err != nil {
		slog.Warn("doLoadSession: doStream 失败",
			"agent", a.meta.ID,
			"session_id", sessionID,
			"error", err,
		)
		return &SessionError{SessionID: sessionID, Err: err}
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

// detectAgentTimeout 检查新 Agent 是否在合理时间内产生输出
// 用于判断 Agent 是否支持 ACP
// Lzm 2026-07-09
func detectAgentTimeout(ctx context.Context, pm *infra.ProcessManager, timeout time.Duration) bool {
	reader := protocol.NewACPReader(pm.Stdout())
	done := make(chan struct{}, 1)

	go func() {
		msg, err := reader.ReadMessage()
		if err == nil && msg != nil {
			close(done)
		}
	}()

	select {
	case <-done:
		return true // 有输出，支持 ACP
	case <-time.After(timeout):
		return false // 超时，不支持 ACP
	case <-ctx.Done():
		return false
	}
}

// truncateString 截断字符串到指定长度（用于日志记录）
// Lzm 2026-07-10
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
