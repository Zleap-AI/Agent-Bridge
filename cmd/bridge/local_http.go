// -*- coding: utf-8 -*-
// Go 1.25+
//
// local_http.go
// Local HTTP 服务：路由注册、状态查询、Agent 管理、WebSocket、配对管理
//
// Lzm 2026-07-22

package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/infra"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
	"github.com/Zleap-AI/Agent-Bridge/internal/service"
	"github.com/gorilla/websocket"
)

const (
	defaultLocalHost = "127.0.0.1"
	defaultLocalPort = 9202
	maxLocalBodySize = 1 << 20
)

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type apiErrorResponse struct {
	Error apiError `json:"error"`
}

type localRemoteStatus struct {
	Paired     bool   `json:"paired"`
	Connected  bool   `json:"connected"`
	State      string `json:"state"`
	ServerURL  string `json:"server_url,omitempty"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

type localStatusResponse struct {
	Version string `json:"version"`
	Local   struct {
		Status  string `json:"status"`
		Address string `json:"address"`
	} `json:"local"`
	Remote localRemoteStatus       `json:"remote"`
	Agents []agent.AgentDescriptor `json:"agents"`
}

type localLog struct {
	Timestamp string `json:"timestamp,omitempty"`
	Level     string `json:"level,omitempty"`
	Message   string `json:"message"`
}

type localApplication struct {
	version       string
	listenAddress string
	registry      *agent.AgentRegistry
	sessions      *service.SessionManager
	config        *configState
	pairer        pairingClaimer
	tunnels       tunnelController
	hostname      func() (string, error)
	readLogs      func() []localLog

	remoteOperationMu sync.Mutex
	remoteGeneration  uint64
	deviceMu          sync.RWMutex
	deviceName        string
}

var errPairingSuperseded = errors.New("pairing operation was superseded")

func newLocalHandler(app *localApplication, console http.Handler) http.Handler {
	if console == nil {
		console = http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/agents", app.handleAgents)
	mux.HandleFunc("/api/sessions", app.handleLegacySessions)
	mux.HandleFunc("/api/messages", app.handleLegacyMessages)
	mux.HandleFunc("/api/v1/local/status", app.handleLocalStatus)
	mux.HandleFunc("/api/v1/local/pairings", app.handleLocalPairing)
	mux.HandleFunc("/api/v1/local/pairing", app.handleLocalUnpair)
	mux.HandleFunc("/api/v1/local/logs", app.handleLocalLogs)
	mux.HandleFunc("/api/v1/local/settings", app.handleLocalSettings)
	mux.HandleFunc("/api/v1/local/storage", app.handleLocalStorage)
	mux.HandleFunc("/api/v1/local/diagnostics", app.handleDiagnostics)
	mux.HandleFunc("/api/v1/local/agents/", app.handleAgentCapabilities)
	mux.HandleFunc("/api/v1/local/browse/drives", app.handleBrowseDrives)
	mux.HandleFunc("/api/v1/local/browse", app.handleBrowse)
	mux.HandleFunc("/ws/admin", app.handleAdminWebSocket)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
			http.NotFound(w, r)
			return
		}
		console.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !isAllowedLocalHost(app.listenAddress, r.Host) {
			writeAPIError(w, http.StatusMisdirectedRequest, "INVALID_HOST", "请求地址不允许访问 Local Console")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (app *localApplication) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	agentStatus := make(map[string]string)
	for _, availableAgent := range app.registry.List() {
		agentStatus[availableAgent.ID()] = availableAgent.Status().String()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "version": app.version, "agents": agentStatus,
	})
}

func (app *localApplication) handleAgents(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, app.registry.ListDescriptors())
}

// handleAgentCapabilities 处理 Agent 能力查询和启停请求。
// GET  /api/v1/local/agents/capabilities     — 返回所有 Agent 的能力
// GET  /api/v1/local/agents/{id}/capabilities — 返回指定 Agent 的能力
// POST /api/v1/local/agents/{id}/start        — 启动指定 Agent
// POST /api/v1/local/agents/{id}/stop         — 停止指定 Agent
// Lzm 2026-07-20
func (app *localApplication) handleAgentCapabilities(w http.ResponseWriter, r *http.Request) {
	// 从路径中提取 agent ID 和动作，格式: /api/v1/local/agents/{id}/{action}
	// 前缀已在注册时匹配（/api/v1/local/agents/），剩余部分如 "claude-code/capabilities"
	remaining := strings.TrimPrefix(r.URL.Path, "/api/v1/local/agents/")
	remaining = strings.TrimSuffix(remaining, "/")
	parts := strings.SplitN(remaining, "/", 2)

	if len(parts) < 2 {
		writeAPIError(w, http.StatusBadRequest, "INVALID_PATH", "路径格式应为 /api/v1/local/agents/{id}/{action}")
		return
	}

	id := strings.TrimSpace(parts[0])
	action := parts[1]

	// 未指定 ID 时，有些操作可以针对所有 Agent
	if id == "" {
		if action == "capabilities" {
			if !allowMethod(w, r, http.MethodGet) {
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"agents": app.registry.ListCapabilities(),
			})
			return
		}
		writeAPIError(w, http.StatusBadRequest, "INVALID_PATH", "路径格式应为 /api/v1/local/agents/{id}/{action}")
		return
	}

	switch action {
	case "capabilities":
		// GET /api/v1/local/agents/{id}/capabilities — 查询指定 Agent 的能力
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		cap, err := app.registry.GetCapabilities(id)
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "AGENT_NOT_FOUND", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, cap)

	case "start":
		// POST /api/v1/local/agents/{id}/start — 启动指定 Agent
		if !allowMethod(w, r, http.MethodPost) || !requireLocalOrigin(w, r) {
			return
		}
		if err := app.registry.Connect(r.Context(), id); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "AGENT_START_FAILED", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "agent_id": id})

	case "stop":
		// POST /api/v1/local/agents/{id}/stop — 停止指定 Agent
		if !allowMethod(w, r, http.MethodPost) || !requireLocalOrigin(w, r) {
			return
		}
		if err := app.registry.Disconnect(r.Context(), id); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "AGENT_STOP_FAILED", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "agent_id": id})

	default:
		writeAPIError(w, http.StatusBadRequest, "INVALID_ACTION", "不支持的操作: "+action)
	}
}

func (app *localApplication) handleLocalStatus(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	response := localStatusResponse{Version: app.version, Remote: app.remoteStatus("")}
	response.Local.Status = "ok"
	response.Local.Address = app.listenAddress
	response.Agents = app.registry.ListDescriptors()
	if response.Agents == nil {
		response.Agents = []agent.AgentDescriptor{}
	}
	writeJSON(w, http.StatusOK, response)
}

func (app *localApplication) handleLocalPairing(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodPost) || !requireLocalOrigin(w, r) {
		return
	}
	var request struct {
		ServerURL   string `json:"server_url"`
		PairingCode string `json:"pairing_code"`
		Code        string `json:"code"`
		Replace     bool   `json:"replace"`
	}
	if err := decodeJSONRequest(r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	normalizedServerURL, _, err := normalizeHTTPServerURL(request.ServerURL)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_SERVER_URL", err.Error())
		return
	}
	code := firstNonEmpty(strings.TrimSpace(request.PairingCode), strings.TrimSpace(request.Code))
	if code == "" {
		writeAPIError(w, http.StatusBadRequest, "PAIRING_CODE_INVALID", "Pairing Code 不能为空")
		return
	}

	generation, requiresConfirmation := app.beginPairingOperation(normalizedServerURL, request.Replace)
	if requiresConfirmation {
		writeAPIError(w, http.StatusConflict, "PAIRING_REPLACE_CONFIRMATION_REQUIRED", "当前设备已连接其他 Server，请确认后再切换")
		return
	}

	hostname, err := app.hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "Agent-Bridge Local"
	}
	// A one-time code may already be consumed when the browser goes away. Keep
	// the short claim/save transaction alive so credentials are not lost merely
	// because the UI was refreshed mid-request.
	claimCtx, cancelClaim := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelClaim()
	result, err := app.pairer.Claim(claimCtx, normalizedServerURL, code, hostname)
	if err != nil {
		var pairErr *pairingError
		if errors.As(err, &pairErr) {
			status := pairErr.Status
			if status < 400 || status > 599 {
				status = http.StatusBadGateway
			}
			writeAPIError(w, status, pairErr.Code, pairErr.Message)
			return
		}
		writeAPIError(w, http.StatusBadGateway, "PAIRING_FAILED", err.Error())
		return
	}

	remote, err := app.commitPairingOperation(generation, result, hostname)
	if err != nil {
		if errors.Is(err, errPairingSuperseded) {
			writeAPIError(w, http.StatusConflict, "PAIRING_OPERATION_SUPERSEDED", "此次远程连接操作已被更新的操作替代")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "CONFIG_SAVE_FAILED", "保存远程连接配置失败")
		return
	}
	remote.ServerURL = normalizedServerURL
	writeJSON(w, http.StatusOK, map[string]any{"remote": remote})
}

func (app *localApplication) handleLocalUnpair(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodDelete) || !requireLocalOrigin(w, r) {
		return
	}
	remote, err := app.clearPairingOperation()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "CONFIG_SAVE_FAILED", "保存远程连接配置失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"remote": remote})
}

// beginPairingOperation establishes the ordering point for a remote-config
// change. Network I/O happens outside the lock, while the generation prevents
// an older Claim response from committing after a newer Pairing or Unpair.
func (app *localApplication) beginPairingOperation(serverURL string, replace bool) (uint64, bool) {
	app.remoteOperationMu.Lock()
	defer app.remoteOperationMu.Unlock()

	current := app.config.Snapshot()
	currentServerURL := ordinaryServerURL(current.ServerURL)
	if current.HasRemoteConnection() && currentServerURL != "" && currentServerURL != serverURL && !replace {
		return 0, true
	}
	app.remoteGeneration++
	return app.remoteGeneration, false
}

func (app *localApplication) commitPairingOperation(generation uint64, result pairingResult, hostname string) (localRemoteStatus, error) {
	app.remoteOperationMu.Lock()
	defer app.remoteOperationMu.Unlock()

	if generation != app.remoteGeneration {
		return localRemoteStatus{}, errPairingSuperseded
	}
	updated, err := app.config.Update(func(cfg *infra.Config) {
		cfg.ServerURL = result.ServerURL
		cfg.BridgeID = result.BridgeID
		cfg.Token = result.Token
	})
	if err != nil {
		return localRemoteStatus{}, err
	}
	deviceName := firstNonEmpty(result.DeviceName, hostname)
	app.setDeviceName(deviceName)
	app.tunnels.Switch(tunnelConfigFrom(updated))
	return app.remoteStatus(deviceName), nil
}

func (app *localApplication) clearPairingOperation() (localRemoteStatus, error) {
	app.remoteOperationMu.Lock()
	defer app.remoteOperationMu.Unlock()

	if _, err := app.config.Update(func(cfg *infra.Config) {
		cfg.ServerURL = ""
		cfg.BridgeID = ""
		cfg.Token = ""
	}); err != nil {
		return localRemoteStatus{}, err
	}
	// Increment only after persistence succeeds. A failed Unpair preserves the
	// existing configuration and its previous in-flight operation semantics.
	app.remoteGeneration++
	app.tunnels.Stop()
	app.setDeviceName("")
	return app.remoteStatus(""), nil
}

func (app *localApplication) handleLocalLogs(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	logs := []localLog{}
	if app.readLogs != nil {
		logs = app.readLogs()
		if logs == nil {
			logs = []localLog{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
}

func (app *localApplication) handleLocalSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		app.writeSettings(w, false)
	case http.MethodPatch:
		if !requireLocalOrigin(w, r) {
			return
		}
		var request struct {
			Debug              *bool   `json:"debug"`
			ClaudeSettingsFile *string `json:"claude_settings_file"`
		}
		if err := decodeJSONRequest(r, &request); err != nil {
			writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
			return
		}
		changed := request.Debug != nil || request.ClaudeSettingsFile != nil
		if _, err := app.config.Update(func(cfg *infra.Config) {
			if request.Debug != nil {
				cfg.Debug = *request.Debug
			}
			if request.ClaudeSettingsFile != nil {
				cfg.ClaudeSettingsFile = strings.TrimSpace(*request.ClaudeSettingsFile)
			}
		}); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "CONFIG_SAVE_FAILED", "保存设置失败")
			return
		}
		app.writeSettings(w, changed)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPatch)
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
	}
}

func (app *localApplication) writeSettings(w http.ResponseWriter, restartRequired bool) {
	cfg := app.config.Snapshot()
	workingDirectory, _ := os.Getwd()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":              app.version,
		"listen_address":       app.listenAddress,
		"working_directory":    workingDirectory,
		"debug":                cfg.Debug,
		"claude_settings_file": cfg.ClaudeSettingsFile,
		"restart_required":     restartRequired,
	})
}

func (app *localApplication) remoteStatus(deviceName string) localRemoteStatus {
	cfg := app.config.Snapshot()
	status := app.tunnels.Status()
	paired := cfg.HasRemoteConnection()
	if deviceName == "" {
		deviceName = app.getDeviceName()
	}
	if deviceName == "" && paired {
		deviceName, _ = app.hostname()
	}
	state := status.State
	if !paired {
		state = "unpaired"
	}
	if state == "" {
		state = "disconnected"
	}
	return localRemoteStatus{
		Paired:     paired,
		Connected:  paired && status.Connected,
		State:      state,
		ServerURL:  ordinaryServerURL(cfg.ServerURL),
		DeviceID:   cfg.BridgeID,
		DeviceName: deviceName,
		LastError:  status.LastError,
	}
}

func (app *localApplication) setDeviceName(name string) {
	app.deviceMu.Lock()
	app.deviceName = name
	app.deviceMu.Unlock()
}

func (app *localApplication) getDeviceName() string {
	app.deviceMu.RLock()
	defer app.deviceMu.RUnlock()
	return app.deviceName
}

func (app *localApplication) handleLegacySessions(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	agentID := r.URL.Query().Get("agent_id")
	if agentID != "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "agent_id": agentID, "sessions": app.sessions.ListSessions(agentID, 20),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "agents": app.sessions.ListAllSessions()})
}

func (app *localApplication) handleLegacyMessages(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	agentID := r.URL.Query().Get("agent_id")
	sessionID := r.URL.Query().Get("session_id")
	if agentID == "" || sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "message": "缺少 agent_id 或 session_id"})
		return
	}
	messages := app.sessions.LoadMessages(agentID, sessionID)
	if messages == nil {
		messages = []service.StoredMessage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "agent_id": agentID, "session": sessionID, "messages": messages, "count": len(messages),
	})
}

// handleLocalStorage 获取存储状态统计信息
// GET /api/v1/local/storage
// Lzm 2026-07-20
func (app *localApplication) handleLocalStorage(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	info := app.sessions.GetStorageInfo()
	writeJSON(w, http.StatusOK, info)
}

func (app *localApplication) handleAdminWebSocket(w http.ResponseWriter, r *http.Request) {
	wsServer := infra.NewWSServer()
	conn, err := wsServer.Upgrade(w, r)
	if err != nil {
		slog.Warn("升级 Local WebSocket 失败", "error", err)
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex
	write := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		// 调试日志：记录 WebSocket 响应内容
		if data, err := json.Marshal(value); err == nil {
			slog.Debug("WebSocket 发送响应",
				"data", truncateString(string(data), 300),
			)
		}
		return infra.WriteJSON(conn, value)
	}
	router := service.NewRequestRouter(app.registry)
	router.SetupPermissionCallbacks(app.sessions)
	router.LocalMode = true
	router.SetStreamCallback(func(requestID, chunkType, text string) error {
		return write(protocol.NewStreamUpdate(requestID, chunkType, text))
	})
	router.SetFinalResponseCallback(func(requestID string, result json.RawMessage, responseError *protocol.ANPError) {
		if responseError != nil {
			_ = write(protocol.NewErrorResponse(requestID, responseError.Code, responseError.Message))
			return
		}
		if result == nil {
			result = json.RawMessage(`{}`)
		}
		_ = write(protocol.NewResultResponse(requestID, result))
	})

	cfg := app.config.Snapshot()
	descriptors := app.registry.ListDescriptors()
	_ = write(map[string]any{
		"method": "bridge/list",
		"params": map[string]any{"bridges": []map[string]any{{
			"bridge_id": cfg.BridgeID, "connected": true, "agents": descriptors,
		}}},
	})

	ctx := r.Context()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				slog.Debug("Local WebSocket 已断开", "error", err)
			}
			return
		}
		var message protocol.ANPMessage
		if err := json.Unmarshal(data, &message); err != nil {
			slog.Warn("WebSocket 消息 JSON 解析失败",
				"raw", truncateString(string(data), 200),
				"error", err,
			)
			_ = write(protocol.NewErrorResponse("", -32700, "消息不是有效的 JSON-RPC"))
			continue
		}
		slog.Debug("WebSocket 收到消息",
			"method", message.Method,
			"id", message.ID,
			"params", truncateString(string(message.Params), 200),
		)
		switch message.Method {
		case "admin/agents", "bridge/health":
			result, _ := json.Marshal(app.registry.ListDescriptors())
			_ = write(protocol.NewResultResponse(message.ID, result))
		case "bridge/list":
			result, _ := json.Marshal(map[string]any{"bridges": []map[string]any{{
				"bridge_id": app.config.Snapshot().BridgeID, "connected": true, "agents": app.registry.ListDescriptors(),
			}}})
			_ = write(protocol.NewResultResponse(message.ID, result))
		default:
			// 在 goroutine 中处理路由，避免 blockingPrompt 阻塞消息读取循环
			// 否则 session/cancel 等控制消息无法被读取和处理
			go func() {
				response := router.Route(ctx, &message, app.sessions)
				if response != nil {
					_ = write(response)
				}
			}()
		}
	}
}
