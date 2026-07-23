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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

	// permissionCB 权限请求回调。当 Agent 发送 session/request_permission
	// 请求时，若此回调已设置，则使用回调处理（如转发给用户手动授权）；
	// 否则自动批准（向后兼容）。
	// 返回: (是否允许, 授权模式, 错误)
	// 授权模式: "request_approval"（允许一次）/ "auto_approve"（始终允许）
	// Lzm 2026-07-22
	permissionCB func(params json.RawMessage) (bool, string, error)

	// elicitationCB elicitation 请求回调。当 Agent 发送 session/create_elicitation
	// 请求（请求用户输入：表单或 URL 弹窗）时，若此回调已设置，则使用回调处理
	// （如转发给 SaaS 平台让用户填写表单）；否则自动取消。
	// 返回: ACP 响应消息（需包含 action 和 content 字段），错误
	// 参考：codex-acp CodexElicitationHandler.ts、claude-agent-acp elicitation.ts
	// Lzm 2026-07-22
	elicitationCB func(params json.RawMessage) (*protocol.ACPMessage, error)

	// --- 自动重连 ---
	// Lzm 2026-07-22

	// stopped 标记 Stop() 已主动调用，watchRuntime 不应自动重启。
	stopped atomic.Bool
	// restartCount 连续重启次数，用于退避延迟计算。
	restartCount atomic.Int32
}
// agentRuntime owns every resource belonging to one child-process generation.
// Keeping these values together prevents a late goroutine from a dead process
// from closing or mutating the pipes of a newly restarted process.
type agentRuntime struct {
	pm      *infra.ProcessManager
	writer  *protocol.ACPWriter
	reader  *protocol.ACPReader
	readCh  chan *protocol.ACPMessage
	cancel  context.CancelFunc
	done    <-chan struct{}
	prepare func() error

	readWG    sync.WaitGroup
	readDone  chan struct{}

	stderrBuf   bytes.Buffer
	stderrBufMu sync.Mutex

	// capabilities 从 ACP initialize 握手响应中解析得到
	// 仅在 doHandshake 完成后有效
	capabilities CapabilityInfo
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

// SetPermissionCallback 设置权限请求回调。
// 当 Agent 发送 session/request_permission 请求时，Bridge 调用此回调
// 让调用方决定如何处理（自动批准、转发给用户手动授权等）。
// cb - 回调函数，接收 request_permission 的 params
// 返回: (是否允许, 授权模式, 错误)
// Lzm 2026-07-22
func (a *baseAgent) SetPermissionCallback(cb func(params json.RawMessage) (bool, string, error)) {
	a.permissionCB = cb
}

// SetElicitationCallback 设置 elicitation 请求回调。
// 当 Agent 发送 session/create_elicitation 请求（请求用户输入）时，
// Bridge 调用此回调让调用方处理（如转发给 SaaS 平台）。
// cb - 回调函数，接收 create_elicitation 的 params
// 返回: ACP 响应消息（应包含 action 和 content），错误
// 参考：codex-acp CodexElicitationHandler.ts、claude-agent-acp elicitation.ts
// Lzm 2026-07-22
func (a *baseAgent) SetElicitationCallback(cb func(params json.RawMessage) (*protocol.ACPMessage, error)) {
	a.elicitationCB = cb
}

// Priority 返回 Agent 优先级（值越小优先级越高）
// Lzm 2026-07-20
func (a *baseAgent) Priority() int { return a.meta.Priority }

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
// 每一代 runtime 保存自己的 prepare，供 watchRuntime 自动重连时复用。
// Lzm 2026-07-22
func (a *baseAgent) start(ctx context.Context, prepare func() error) error {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()

	// 新启动尝试重置自动重连状态（允许 watchRuntime 在退出后重启）
	a.stopped.Store(false)

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
			prepare:  prepare,
			readDone: make(chan struct{}),
		}
		a.setRuntime(run)
		slog.Info("Agent 进程已启动（WebSocket 模式）",
			"agent", a.meta.ID,
			"pid", run.pm.PID(),
		)
	} else {
		// 标准 stdin/stdout 管道模式
		// Codex 在 Windows Job Object 包装下无法正常初始化（PATH 别名创建失败），
		// 因此对其禁用进程树管理。
		disableProcessTree := a.meta.ID == "codex" && runtime.GOOS == "windows"
		pm, err := infra.StartProcess(processCtx, infra.StartProcessConfig{
			Command:            a.meta.Cmd,
			Args:               a.meta.Args,
			WorkDir:            a.meta.WorkDir,
			Env:                a.meta.Env,
			PathDirs:           a.meta.PathDirs,
			DisableProcessTree: disableProcessTree,
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
			prepare:  prepare,
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

	// 自动重连成功后重置重启计数器，避免无限递增导致退避时间过长。
	// watchRuntime 检测到进程退出后使用 restartCount 计算退避延迟；
	// 成功启动后归零确保下次退避从初始值开始。
	// Lzm 2026-07-22
	a.restartCount.Store(0)

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
					// 写入环形缓冲区（最多保留 16KB），不主动输出到日志
					// 需要排查具体 Agent 时可调 health/agents 接口查看 stderr_buf
					// Lzm 2026-07-20
					run.stderrBufMu.Lock()
					if run.stderrBuf.Len() > 16*1024 {
						run.stderrBuf.Reset()
					}
					run.stderrBuf.WriteString(chunk)
					run.stderrBufMu.Unlock()
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

// watchRuntime 等待 Agent 进程退出，必要时自动重连。
// 退出后带指数退避自动重启，避免持续崩溃的 Agent 消耗 CPU。
// Stop() 已设置 stopped 标记时不会重启。
// Lzm 2026-07-22
func (a *baseAgent) watchRuntime(run *agentRuntime) {
	err := run.pm.Wait()
	run.cancel()
	a.clearRuntime(run)

	if err != nil {
		slog.Warn("Agent 进程已退出", "agent", a.meta.ID, "error", err)
	} else {
		slog.Info("Agent 进程已退出", "agent", a.meta.ID)
	}

	// Stop() 已主动关闭，不自动重连
	if a.stopped.Load() {
		slog.Info("Agent 已由 Stop 关闭，跳过自动重连",
			"agent", a.meta.ID,
		)
		return
	}

	// 指数退避：1s, 2s, 4s, 8s, 16s, max 30s
	attempt := a.restartCount.Add(1)
	delay := time.Duration(1<<min(attempt-1, 5)) * time.Second
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	slog.Info("Agent 即将自动重连",
		"agent", a.meta.ID,
		"delay", delay,
		"attempt", attempt,
	)
	time.Sleep(delay)

	// 重连前再次检查 stopped，以防等待期间 Stop() 被调用
	if a.stopped.Load() {
		slog.Info("重连等待期间 Stop() 已调用，取消重连",
			"agent", a.meta.ID,
		)
		return
	}

	if err := a.start(context.Background(), run.prepare); err != nil {
		slog.Warn("Agent 自动重连失败",
			"agent", a.meta.ID,
			"error", err,
			"attempt", attempt,
		)
		return
	}
	slog.Info("Agent 自动重连成功",
		"agent", a.meta.ID,
		"attempt", attempt,
	)
}

// initializeResponseInfo 解析 ACP initialize 响应
// 支持 agentInfo（V1 规范）和 serverInfo（旧格式）两种字段名
// Lzm 2026-07-21
type initializeResponseInfo struct {
	ProtocolVersion  int                    `json:"protocolVersion"`
	AgentCaps        *AgentCapabilitiesV1   `json:"agentCapabilities"`
	LegacyCaps       map[string]interface{} `json:"capabilities"`
	AuthMethods      []authMethodEntry      `json:"authMethods"`
	ServerInfo       struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Title   string `json:"title,omitempty"`
	} `json:"serverInfo"`
	AgentInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Title   string `json:"title,omitempty"`
	} `json:"agentInfo,omitempty"`
}

type authMethodEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

const supportedACPProtocolVersion = 1

// doHandshake 执行 ACP initialize 握手
// 包含：协议版本协商 → 能力解析 → 可选 authenticate
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
			if msg.IsStreamUpdate() || !msg.IDMatch(req.IDString()) {
				slog.Debug("握手阶段收到非匹配消息",
					"agent", a.meta.ID,
					"expected_id", req.IDString(),
					"got_id", msg.IDString(),
				)
				continue
			}
			if msg.IsSuccess() {
				var info initializeResponseInfo
				if err := json.Unmarshal(msg.Result, &info); err == nil {
					// 版本协商：检查 Agent 返回的协议版本是否受支持
					if info.ProtocolVersion > 0 && info.ProtocolVersion != supportedACPProtocolVersion {
						slog.Warn("Agent 返回不同的 ACP 协议版本",
							"agent", a.meta.ID,
							"agent_version", info.ProtocolVersion,
							"supported_version", supportedACPProtocolVersion,
						)
						// 仅记录警告，不阻断启动：所有 V1 Agent 实际都兼容低版本
					}

					// agentInfo（V1 规范）优先，降级到 serverInfo（旧格式）
					agentName := info.ServerInfo.Name
					agentVersion := info.ServerInfo.Version
					agentTitle := info.ServerInfo.Title
					if info.AgentInfo.Name != "" {
						agentName = info.AgentInfo.Name
						agentVersion = info.AgentInfo.Version
						agentTitle = info.AgentInfo.Title
					}

					cap := CapabilityInfo{
						Name:            agentName,
						Version:         agentVersion,
						Title:           agentTitle,
						ProtocolVersion: info.ProtocolVersion,
					}

					// ACP V1 规范：优先解析 agentCapabilities 字段
					if info.AgentCaps != nil {
						cap.AgentCapabilities = info.AgentCaps
						cap.SupportsStreaming = info.AgentCaps.Streaming
						cap.SupportsSessions = info.AgentCaps.Sessions
						cap.SupportsLoadSession = info.AgentCaps.LoadSession

						if sc := info.AgentCaps.SessionCaps; sc != nil {
							cap.SupportsResume = sc.SupportsResume()
							cap.SupportsSessionDelete = sc.SupportsDelete()
							cap.SupportsSessionClose = sc.SupportsClose()
						}
					}

					// 兼容旧格式 Agent：从 legacy capabilities 读取
					if info.LegacyCaps != nil {
						cap.RawCapabilities = info.LegacyCaps
						if !cap.SupportsStreaming {
							if streaming, ok := info.LegacyCaps["streaming"].(bool); ok {
								cap.SupportsStreaming = streaming
							}
						}
						if !cap.SupportsSessions {
							if sessions, ok := info.LegacyCaps["sessions"].(bool); ok {
								cap.SupportsSessions = sessions
							}
						}
						if !cap.SupportsLoadSession {
							if loadSession, ok := info.LegacyCaps["loadSession"].(bool); ok {
								cap.SupportsLoadSession = loadSession
							}
						}
					}

					// 处理认证：如果 Agent 声明了 authMethods，自动调用 authenticate
					// Lzm 2026-07-21
					if len(info.AuthMethods) > 0 {
						slog.Info("Agent 需要认证，自动调用 authenticate",
							"agent", a.meta.ID,
							"method_count", len(info.AuthMethods),
						)
						if err := a.doAuthenticate(ctx, run, info.AuthMethods[0].ID); err != nil {
							slog.Warn("Agent 认证失败，继续使用未认证状态",
								"agent", a.meta.ID,
								"error", err,
							)
							// 认证失败不阻断启动流程，后续 session 操作可能返回 auth_required
						}
					}

					run.capabilities = cap

					slog.Info("ACP 握手成功",
						"agent", a.meta.ID,
						"name", cap.Name,
						"version", cap.Version,
						"protocol", cap.ProtocolVersion,
						"streaming", cap.SupportsStreaming,
						"sessions", cap.SupportsSessions,
						"load_session", cap.SupportsLoadSession,
						"resume", cap.SupportsResume,
						"agent_caps_v1", info.AgentCaps != nil,
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
// 设置 stopped 标记，watchRuntime 检测到后不自动重启。
// Lzm 2026-07-22
func (a *baseAgent) Stop(ctx context.Context) error {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()

	a.stopped.Store(true)

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

// Capabilities 返回 Agent 的能力声明。
// 优先从当前 runtime 的 ACP 握手结果中读取；若进程未运行或握手未完成，
// 返回 AgentMeta 中预设的静态默认值。
// Lzm 2026-07-20
func (a *baseAgent) Capabilities() CapabilityInfo {
	run := a.currentRuntime()
	if run != nil {
		// runtime 存在且已握手完成则返回收集到的能力
		if run.capabilities.Name != "" || run.capabilities.ProtocolVersion != 0 {
			return run.capabilities
		}
	}
	// 兜底：返回静态默认值
	return a.meta.Capabilities
}

// --- Runtime 管理 ---

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
// --- ACP 请求锁 ---
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
// --- 认证管理 ---
// doAuthenticate 调用 ACP authenticate 方法。
// 在 initialize 握手后调用，处理需要认证的 Agent。
// Lzm 2026-07-21
func (a *baseAgent) doAuthenticate(ctx context.Context, run *agentRuntime, methodID string) error {
	req := protocol.NewAuthenticateRequest(a.nextID(), methodID)
	if err := run.writer.WriteMessage(req); err != nil {
		return fmt.Errorf("发送 authenticate 请求失败: %w", err)
	}

	// 等待响应
	authCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	for {
		select {
		case msg := <-run.readCh:
			if msg == nil {
				return fmt.Errorf("Agent 进程过早退出（authenticate 阶段）")
			}
			if msg.IsStreamUpdate() || !msg.IDMatch(req.IDString()) {
				continue
			}
			if msg.IsSuccess() {
				slog.Info("ACP authenticate 成功",
					"agent", a.meta.ID,
					"method_id", methodID,
				)
				return nil
			}
			if msg.Error != nil {
				return fmt.Errorf("authenticate 失败: code=%d message=%s",
					msg.Error.Code, msg.Error.Message)
			}
		case <-authCtx.Done():
			return fmt.Errorf("authenticate 超时: %w", authCtx.Err())
		}
	}
}
// doLogout 调用 ACP logout 方法。
// 仅在 Agent 声明了 agentCapabilities.auth.logout 时调用。
// Lzm 2026-07-21
func (a *baseAgent) doLogout(ctx context.Context, run *agentRuntime) error {
	req := protocol.NewLogoutRequest(a.nextID())
	if err := run.writer.WriteMessage(req); err != nil {
		return fmt.Errorf("发送 logout 请求失败: %w", err)
	}

	logoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	for {
		select {
		case msg := <-run.readCh:
			if msg == nil {
				return fmt.Errorf("Agent 进程过早退出（logout 阶段）")
			}
			if msg.IsStreamUpdate() || !msg.IDMatch(req.IDString()) {
				continue
			}
			if msg.IsSuccess() {
				slog.Info("ACP logout 成功",
					"agent", a.meta.ID,
				)
				return nil
			}
			if msg.Error != nil {
				return fmt.Errorf("logout 失败: code=%d message=%s",
					msg.Error.Code, msg.Error.Message)
			}
		case <-logoutCtx.Done():
			return fmt.Errorf("logout 超时: %w", logoutCtx.Err())
		}
	}
}
// Logout 公开方法：调用 Agent 的 logout（可由 SaaS 通过 ANP 触发）。
// 仅在 Agent 声明了 agentCapabilities.auth.logout 能力时可用。
// Lzm 2026-07-21
func (a *baseAgent) Logout(ctx context.Context) error {
	run := a.currentRuntime()
	if run == nil || !run.pm.IsRunning() {
		return fmt.Errorf("agent %s 进程未运行", a.meta.ID)
	}
	return a.doLogout(ctx, run)
}
