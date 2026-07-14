package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
		return infra.WriteJSON(conn, value)
	}
	router := service.NewRequestRouter(app.registry)
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
			_ = write(protocol.NewErrorResponse("", -32700, "消息不是有效的 JSON-RPC"))
			continue
		}
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
			response := router.Route(ctx, &message, app.sessions)
			if response != nil {
				_ = write(response)
			}
		}
	}
}

func localListenAddress(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = defaultLocalHost
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port))
}

// isAllowedLocalHost prevents a public hostname that resolves to loopback from
// turning the browser's same-origin policy into access to the Local Console.
func isAllowedLocalHost(listenAddress, requestHost string) bool {
	listenHost, listenPort, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return false
	}
	requestName, requestPort, err := net.SplitHostPort(requestHost)
	if err != nil || requestPort != listenPort {
		return false
	}
	listenHost = strings.Trim(strings.TrimSpace(listenHost), "[]")
	requestName = strings.Trim(strings.TrimSpace(requestName), "[]")
	if requestName == "" {
		return false
	}

	listenIP := net.ParseIP(listenHost)
	requestIP := net.ParseIP(requestName)
	if listenIP != nil && listenIP.IsUnspecified() {
		return requestIP != nil || strings.EqualFold(requestName, "localhost")
	}
	if isLoopbackAddress(listenHost) {
		return isLoopbackAddress(requestName)
	}
	if listenIP != nil {
		return requestIP != nil && listenIP.Equal(requestIP)
	}
	return strings.EqualFold(listenHost, requestName)
}

func isLoopbackAddress(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func requireLocalOrigin(w http.ResponseWriter, r *http.Request) bool {
	if infra.IsAllowedLocalOrigin(r) {
		return true
	}
	writeAPIError(w, http.StatusForbidden, "FORBIDDEN_ORIGIN", "请求来源不允许访问 Local Console")
	return false
}

func allowMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
	return false
}

func decodeJSONRequest(r *http.Request, value any) error {
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		return fmt.Errorf("Content-Type 必须是 application/json")
	}
	if r.ContentLength > maxLocalBodySize {
		return fmt.Errorf("请求正文不能超过 %d 字节", maxLocalBodySize)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxLocalBodySize+1))
	if err != nil {
		return fmt.Errorf("读取请求正文失败: %w", err)
	}
	if len(body) > maxLocalBodySize {
		return fmt.Errorf("请求正文不能超过 %d 字节", maxLocalBodySize)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("请求 JSON 无效: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("请求只能包含一个 JSON 对象")
	}
	return nil
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiErrorResponse{Error: apiError{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Debug("写入 JSON 响应失败", "error", err)
	}
}

func readRecentLocalLogs() []localLog {
	home, err := os.UserHomeDir()
	if err != nil {
		return []localLog{}
	}
	path := filepath.Join(home, infra.LogDir, time.Now().Format("2006-01-02")+".log")
	file, err := os.Open(path)
	if err != nil {
		return []localLog{}
	}
	defer file.Close()

	const limit = 200
	logs := make([]localLog, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		entry := parseLocalLog(scanner.Text())
		if len(logs) == limit {
			copy(logs, logs[1:])
			logs[len(logs)-1] = entry
		} else {
			logs = append(logs, entry)
		}
	}
	return logs
}

func parseLocalLog(line string) localLog {
	var wire struct {
		Time  string `json:"time"`
		Level string `json:"level"`
		Msg   string `json:"msg"`
	}
	if json.Unmarshal([]byte(line), &wire) == nil && wire.Msg != "" {
		return localLog{Timestamp: wire.Time, Level: strings.ToLower(wire.Level), Message: wire.Msg}
	}
	return localLog{Message: line}
}

func shutdownHTTPServer(ctx context.Context, server *http.Server) {
	if err := server.Shutdown(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("关闭 Local HTTP 服务失败", "error", err)
	}
}
