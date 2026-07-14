// -*- coding: utf-8 -*-
// Go 1.25+
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

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/infra"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// baseAgent 封装 Agent 的公共逻辑
// 具体 Agent 类型（ClaudeCode、OpenCode 等）通过嵌入此结构实现 Agent 接口
type baseAgent struct {
	meta   AgentMeta
	status AgentStatus

	lifecycleMu sync.Mutex   // 序列化启动和停止，防止重复创建子进程
	runtimeMu   sync.RWMutex // 保护 runtime，并协调退出后的状态更新
	runtime     *agentRuntime
	requestGate chan struct{} // ACP 是请求-响应式，同一时间只允许一个请求
	idMu        sync.Mutex
	statusMu    sync.RWMutex // 保护 status 字段

	msgIDCounter int            // ACP 消息 ID 计数器
	wsAdapter    *wsACPAdapter  // 可选：WebSocket ACP 适配器（macOS opencode 用）
}

// agentRuntime owns every resource belonging to one child-process generation.
// Keeping these values together prevents a late goroutine from a dead process
// from closing or mutating the pipes of a newly restarted process.
type agentRuntime struct {
	pm     *infra.ProcessManager
	writer *protocol.ACPWriter
	reader *protocol.ACPReader
	readCh chan *protocol.ACPMessage
	cancel context.CancelFunc
	done   <-chan struct{}

	readWG    sync.WaitGroup
	readDone  chan struct{}

	stderrBuf   bytes.Buffer
	stderrBufMu sync.Mutex
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
		requestGate:  make(chan struct{}, 1),
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
	a.idMu.Lock()
	defer a.idMu.Unlock()
	a.msgIDCounter++
	return fmt.Sprintf("%d", a.msgIDCounter)
}

// --- 生命周期 ---

// start 启动一个 Agent 子进程并完成握手。整个启动过程受同一把生命周期
// 锁保护；并发 Start 会复用已经成功启动的进程，而不会再创建一个副本。
// Lzm 2026-07-14
func (a *baseAgent) start(ctx context.Context, prepare func() error) error {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()

	if err := ctx.Err(); err != nil {
		return &AgentStartError{AgentID: a.meta.ID, Err: err}
	}
	if current := a.currentRuntime(); current != nil {
		if current.pm.IsRunning() {
			return nil
		}
		a.clearRuntime(current)
		current.cancel()
	}
	if prepare != nil {
		if err := prepare(); err != nil {
			return err
		}
	}

	// The child lifetime is controlled by Stop rather than by the request that
	// happened to start it. A canceled HTTP/WebSocket request must not kill an
	// otherwise healthy Agent process after Start has returned.
	processCtx, processCancel := context.WithCancel(context.Background())

	var run *agentRuntime

	// WebSocket 模式：使用 wsAdapter 代替 stdin/stdout 管道
	if a.wsAdapter != nil {
		run = &agentRuntime{
			pm:       a.wsAdapter.cmd,
			writer:   protocol.NewACPWriter(a.wsAdapter),
			reader:   protocol.NewACPReader(a.wsAdapter),
			readCh:   make(chan *protocol.ACPMessage, 100),
			cancel:   processCancel,
			done:     processCtx.Done(),
			readDone: make(chan struct{}),
		}
		a.setRuntime(run)
		slog.Info("Agent 进程已启动（WebSocket 模式）",
			"agent", a.meta.ID,
			"pid", run.pm.PID(),
		)
	} else {
		// 标准 stdin/stdout 管道模式
		pm, err := infra.StartProcess(processCtx, infra.StartProcessConfig{
			Command:  a.meta.Cmd,
			Args:     a.meta.Args,
			WorkDir:  a.meta.WorkDir,
			Env:      a.meta.Env,
			PathDirs: a.meta.PathDirs,
		})
		if err != nil {
			processCancel()
			return &AgentStartError{AgentID: a.meta.ID, Err: err}
		}

		run = &agentRuntime{
			pm:       pm,
			writer:   protocol.NewACPWriter(pm.Stdin()),
			reader:   protocol.NewACPReader(pm.Stdout()),
			readCh:   make(chan *protocol.ACPMessage, 100),
			cancel:   processCancel,
			done:     processCtx.Done(),
			readDone: make(chan struct{}),
		}
		a.setRuntime(run)
		slog.Info("Agent 进程已启动",
			"agent", a.meta.ID,
			"pid", pm.PID(),
		)
	}

	a.startReadLoop(run, processCtx)
	go a.watchRuntime(run)

	startCtx, cancel := context.WithTimeout(ctx, a.meta.StartupTimeout)
	defer cancel()
	if err := a.doHandshake(startCtx, run); err != nil {
		a.clearRuntime(run)
		run.cancel()
		_ = run.pm.Stop()
		a.waitForReaders(ctx, run)
		return err
	}

	a.setStatusForRuntime(run, AgentIdle)
	return nil
}

// startReadLoop 启动后台读取协程
// 持续从 ACP stdout 读取消息，路由到等待中的请求
// Lzm 2026-07-10
func (a *baseAgent) startReadLoop(run *agentRuntime, readCtx context.Context) {
	run.readWG.Add(2)

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
		defer run.readWG.Done()
		defer close(run.readCh)

		for {
			msg, err := run.reader.ReadMessage()
			if err != nil {
				slog.Debug("Agent 读取结束",
					"agent", a.meta.ID,
					"error", err,
				)
				run.cancel()
				return
			}
			if msg == nil {
				// stdout 已关闭后 ACP 已不可用；终止仍存活的异常子进程，
				// 让 watcher 将状态切回 disconnected 并允许重新启动。
				run.cancel()
				return
			}

			select {
			case run.readCh <- msg:
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
		defer run.readWG.Done()
		buf := make([]byte, 4096)
		for {
			select {
			case <-readCtx.Done():
				return
			default:
				n, err := run.pm.StderrReader().Read(buf)
				if n > 0 {
					chunk := string(buf[:n])
					// 写入环形缓冲区（最多保留 16KB）
					run.stderrBufMu.Lock()
					if run.stderrBuf.Len() > 16*1024 {
						run.stderrBuf.Reset()
					}
					run.stderrBuf.WriteString(chunk)
					run.stderrBufMu.Unlock()
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

	go func() {
		run.readWG.Wait()
		close(run.readDone)
	}()
}

func (a *baseAgent) watchRuntime(run *agentRuntime) {
	err := run.pm.Wait()
	run.cancel()
	a.clearRuntime(run)
	if err != nil {
		slog.Warn("Agent 进程已退出", "agent", a.meta.ID, "error", err)
	} else {
		slog.Info("Agent 进程已退出", "agent", a.meta.ID)
	}
}

// doHandshake 执行 ACP initialize 握手
// Lzm 2026-07-09
func (a *baseAgent) doHandshake(ctx context.Context, run *agentRuntime) error {
	req := protocol.NewInitializeRequest(a.nextID())
	if err := run.writer.WriteMessage(req); err != nil {
		return fmt.Errorf("发送 initialize 请求失败: %w", err)
	}

	// 等待响应（循环处理，跳过流式通知和非匹配 ID）
	for {
		select {
		case msg := <-run.readCh:
			if msg == nil {
				// Agent 进程过早退出，读取 stderr 帮助诊断
				run.stderrBufMu.Lock()
				stderrOut := strings.TrimSpace(run.stderrBuf.String())
				run.stderrBufMu.Unlock()
				if stderrOut != "" {
					// 截取最后 512 字节（通常是真正的原因）
					if len(stderrOut) > 512 {
						stderrOut = "..." + stderrOut[len(stderrOut)-512:]
					}
					return fmt.Errorf("ACP 进程过早退出（stderr: %s）", stderrOut)
				}
				return fmt.Errorf("ACP 进程过早退出")
			}
			if msg.IsStreamUpdate() || msg.ID != req.ID {
				slog.Debug("握手阶段收到非匹配消息",
					"agent", a.meta.ID,
					"expected_id", req.ID,
					"got_id", msg.ID,
				)
				continue
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
}

// Stop 终止 Agent 进程
// Lzm 2026-07-10
func (a *baseAgent) Stop(ctx context.Context) error {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()

	run := a.currentRuntime()
	if run == nil {
		a.setStatus(AgentDisconnected)
		return nil
	}

	a.clearRuntime(run)
	run.cancel()
	err := run.pm.Stop()
	a.waitForReaders(ctx, run)
	return err
}

// Health 检查 Agent 进程是否健康
func (a *baseAgent) Health(ctx context.Context) error {
	run := a.currentRuntime()
	if run == nil || !run.pm.IsRunning() {
		return fmt.Errorf("agent %s 进程未运行", a.meta.ID)
	}
	return nil
}

func (a *baseAgent) currentRuntime() *agentRuntime {
	a.runtimeMu.RLock()
	defer a.runtimeMu.RUnlock()
	return a.runtime
}

func (a *baseAgent) setRuntime(run *agentRuntime) {
	a.runtimeMu.Lock()
	a.runtime = run
	a.runtimeMu.Unlock()
}

func (a *baseAgent) clearRuntime(run *agentRuntime) {
	a.runtimeMu.Lock()
	defer a.runtimeMu.Unlock()
	if a.runtime != run {
		return
	}
	a.runtime = nil
	a.setStatus(AgentDisconnected)
}

func (a *baseAgent) setStatusForRuntime(run *agentRuntime, status AgentStatus) {
	a.runtimeMu.RLock()
	defer a.runtimeMu.RUnlock()
	if a.runtime == run {
		a.setStatus(status)
	}
}

// invalidateRuntime is required when an in-flight ACP request is canceled or
// times out. ACP has no portable per-request cancellation primitive; reusing
// the same stdio stream could otherwise feed a late response to the next
// request. A fresh Start creates a clean protocol generation.
func (a *baseAgent) invalidateRuntime(run *agentRuntime) {
	a.clearRuntime(run)
	run.cancel()
}

func (a *baseAgent) waitForReaders(ctx context.Context, run *agentRuntime) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-run.readDone:
	case <-ctx.Done():
		slog.Warn("等待 Agent 后台协程时上下文取消", "agent", a.meta.ID, "error", ctx.Err())
	case <-timer.C:
		slog.Warn("Agent 后台协程退出超时", "agent", a.meta.ID)
	}
}

// --- ACP 通信 ---

func (a *baseAgent) acquireRequest(ctx context.Context) error {
	select {
	case a.requestGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待 ACP 请求锁失败: %w", ctx.Err())
	}
}

func (a *baseAgent) releaseRequest() {
	<-a.requestGate
}

func (a *baseAgent) finishRequest(run *agentRuntime) {
	if run.pm.IsRunning() {
		a.setStatusForRuntime(run, AgentIdle)
	}
}

func (a *baseAgent) resetReadTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(a.meta.ReadTimeout)
}

// doSend 发送请求并等待完整响应（互斥访问 ACP 管道）
// Lzm 2026-07-09
func (a *baseAgent) doSend(ctx context.Context, req *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	if err := a.acquireRequest(ctx); err != nil {
		return nil, err
	}
	defer a.releaseRequest()

	run := a.currentRuntime()
	if run == nil || !run.pm.IsRunning() {
		return nil, fmt.Errorf("agent %s 进程未运行", a.meta.ID)
	}

	a.setStatusForRuntime(run, AgentBusy)
	defer a.finishRequest(run)

	// 发送请求
	if err := run.writer.WriteMessage(req); err != nil {
		a.invalidateRuntime(run)
		return nil, fmt.Errorf("发送 ACP 消息失败: %w", err)
	}

	readTimer := time.NewTimer(a.meta.ReadTimeout)
	defer readTimer.Stop()

	// 读取响应（跳过流式通知，直到找到匹配 ID 的响应）
	for {
		select {
		case msg := <-run.readCh:
			if msg == nil {
				a.invalidateRuntime(run)
				return nil, fmt.Errorf("ACP 进程过早退出")
			}
			a.resetReadTimer(readTimer)
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
			a.invalidateRuntime(run)
			return nil, fmt.Errorf("等待 ACP 响应超时: %w", ctx.Err())
		case <-readTimer.C:
			a.invalidateRuntime(run)
			return nil, fmt.Errorf("等待 ACP 响应超时: %s 内未收到消息", a.meta.ReadTimeout)
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
		return nil, err
	}

	run := a.currentRuntime()
	if run == nil || !run.pm.IsRunning() {
		a.releaseRequest()
		return nil, fmt.Errorf("agent %s 进程未运行", a.meta.ID)
	}

	a.setStatusForRuntime(run, AgentBusy)

	// 发送请求
	if err := run.writer.WriteMessage(req); err != nil {
		a.releaseRequest()
		a.invalidateRuntime(run)
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
				if msg.ID == req.ID {
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
				// ID 不匹配，继续等待
				slog.Debug("收到非匹配响应（流式模式）",
					"expected_id", req.ID,
					"got_id", msg.ID,
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
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result, &payload); err == nil {
		chunk.Text = payload.Text
	}
	return chunk
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

// truncateString 截断字符串到指定长度（用于日志记录）
// Lzm 2026-07-10
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
