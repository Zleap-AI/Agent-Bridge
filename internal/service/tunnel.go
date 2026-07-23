package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/infra"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

const (
	connectionReplacedCode   = 4001
	connectionReplacedReason = "connection_replaced"
	deviceDeletedReason      = "device_deleted"
)

// ErrConnectionReplaced means the Server accepted a newer connection for the
// same Device. Reconnecting this older process would violate latest-wins.
var ErrConnectionReplaced = errors.New("Device connection was replaced by a newer connection")

// ErrCredentialsRevoked means the Server no longer accepts the Device token.
// Retrying cannot recover; the user must pair the Device again.
var ErrCredentialsRevoked = errors.New("Device credentials were revoked; pair this Device again")

var errANPDeviceMessageTooLarge = errors.New("ANP Device message exceeds the transport limit")

// TunnelService owns the single outbound Local-to-Server connection and keeps
// the existing JSON-RPC bridge contract isolated from Local HTTP concerns.
type TunnelService struct {
	registry *agent.AgentRegistry
	cfg      TunnelConfig
	router   *RequestRouter
	sessions *SessionManager

	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.RWMutex
	wsClient     *infra.WSClient
	reconnecting bool
	terminalErr  error
	refreshOnce  sync.Once
}

type TunnelConfig struct {
	ServerURL             string
	BridgeID              string
	Token                 string
	ReconnectInterval     time.Duration
	StatusRefreshInterval time.Duration
	MaxReconnectAttempts  int
	OnConnectionChange    func(connected bool, err error)
}

func DefaultTunnelConfig() TunnelConfig {
	return TunnelConfig{
		ReconnectInterval:     5 * time.Second,
		StatusRefreshInterval: 10 * time.Second,
		MaxReconnectAttempts:  0,
	}
}

func NewTunnelService(registry *agent.AgentRegistry, cfg TunnelConfig) *TunnelService {
	return NewTunnelServiceWithSessionManager(registry, cfg, nil)
}

// NewTunnelServiceWithSessionManager lets the Local Console and outbound
// tunnel share one session index and one serialized message store.
func NewTunnelServiceWithSessionManager(registry *agent.AgentRegistry, cfg TunnelConfig, sessions *SessionManager) *TunnelService {
	ctx, cancel := context.WithCancel(context.Background())
	if cfg.ReconnectInterval <= 0 {
		cfg.ReconnectInterval = 5 * time.Second
	}
	if cfg.StatusRefreshInterval <= 0 {
		cfg.StatusRefreshInterval = 10 * time.Second
	}
	if sessions == nil {
		sessions = NewSessionManager(registry)
	}
	router := NewRequestRouter(registry)
	router.SetupPermissionCallbacks(sessions)
	router.SetupElicitationCallbacks()
	service := &TunnelService{
		registry: registry,
		cfg:      cfg,
		ctx:      ctx,
		cancel:   cancel,
		sessions: sessions,
		router:   router,
	}
	service.router.SetStreamCallback(func(requestID, chunkType, text string) error {
		return service.sendJSON(protocol.NewStreamUpdate(requestID, chunkType, text))
	})
	service.router.SetFinalResponseCallback(func(requestID string, result json.RawMessage, responseError *protocol.ANPError) {
		if responseError != nil {
			_ = service.sendJSON(protocol.NewErrorResponse(requestID, responseError.Code, responseError.Message))
			return
		}
		if result == nil {
			result = json.RawMessage(`{}`)
		}
		if err := service.sendJSON(protocol.NewResultResponse(requestID, result)); errors.Is(err, errANPDeviceMessageTooLarge) {
			_ = service.sendJSON(protocol.NewErrorResponse(requestID, protocol.ANPErrorResponseTooLarge,
				fmt.Sprintf("Device response exceeds the %d-byte transport limit", protocol.MaxANPDeviceMessageBytes)))
		}
	})
	return service
}

func (s *TunnelService) Start() error {
	if strings.TrimSpace(s.cfg.ServerURL) == "" || strings.TrimSpace(s.cfg.BridgeID) == "" {
		return fmt.Errorf("远程连接缺少 server_url 或 bridge_id")
	}
	slog.Info("TunnelService 启动", "server_url", s.cfg.ServerURL, "bridge_id", s.cfg.BridgeID)
	client, err := s.connectClient()
	if err != nil {
		if terminalError, stopped := s.stopAfterTerminalError(err); stopped {
			return fmt.Errorf("连接远程 WebSocket 服务失败: %w", terminalError)
		}
		return fmt.Errorf("连接远程 WebSocket 服务失败: %w", err)
	}

	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		_ = client.Close()
		return context.Canceled
	}
	s.wsClient = client
	s.reconnecting = false
	s.mu.Unlock()
	if err := s.registerBridge(); err != nil {
		s.mu.Lock()
		if s.wsClient == client {
			s.wsClient = nil
		}
		s.mu.Unlock()
		_ = client.Close()
		return err
	}
	s.notifyConnection(true, nil)
	client.Start(s.ctx)
	s.startStatusRefreshLoop()
	return nil
}

func (s *TunnelService) Stop() {
	s.cancel()
	s.mu.Lock()
	client := s.wsClient
	s.wsClient = nil
	s.reconnecting = false
	s.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

func (s *TunnelService) connectClient() (*infra.WSClient, error) {
	return infra.NewWSClient(s.ctx, infra.WSClientConfig{
		URL:       s.cfg.ServerURL,
		Header:    s.connectionHeaders(),
		OnMessage: s.handleMessage,
		OnError:   s.handleConnectionError,
	})
}

func (s *TunnelService) connectionHeaders() http.Header {
	header := make(http.Header)
	header.Set("X-Bridge-Id", s.cfg.BridgeID)
	agentIDs := make([]string, 0, len(s.registry.List()))
	for _, availableAgent := range s.registry.List() {
		agentIDs = append(agentIDs, availableAgent.ID())
	}
	header.Set("X-Agent-Ids", strings.Join(agentIDs, ","))
	if s.cfg.Token != "" {
		header.Set("Authorization", "Bearer "+s.cfg.Token)
	}
	return header
}

func (s *TunnelService) handleConnectionError(client *infra.WSClient, err error) {
	if s.ctx.Err() != nil {
		return
	}
	terminalError := classifyTerminalConnectionError(err)
	s.mu.Lock()
	if s.terminalErr != nil || s.wsClient != client {
		s.mu.Unlock()
		return
	}
	s.wsClient = nil
	if terminalError != nil {
		s.terminalErr = terminalError
		s.reconnecting = false
	} else {
		if s.reconnecting {
			s.mu.Unlock()
			return
		}
		s.reconnecting = true
	}
	s.mu.Unlock()
	_ = client.Close()

	if terminalError != nil {
		s.notifyConnection(false, terminalError)
		if errors.Is(terminalError, ErrConnectionReplaced) {
			slog.Info("远程连接已被更新的 Device 连接替换")
		} else {
			slog.Info("远程 Device 凭据已撤销，需要重新配对")
		}
		return
	}
	s.notifyConnection(false, err)
	slog.Warn("远程连接已断开，准备重连", "error", err)
	go s.reconnectLoop()
}

func (s *TunnelService) reconnectLoop() {
	for attempt := 1; ; attempt++ {
		if s.cfg.MaxReconnectAttempts > 0 && attempt > s.cfg.MaxReconnectAttempts {
			err := fmt.Errorf("重连次数已达上限: %d", s.cfg.MaxReconnectAttempts)
			s.mu.Lock()
			s.reconnecting = false
			s.mu.Unlock()
			s.notifyConnection(false, err)
			return
		}
		timer := time.NewTimer(s.cfg.ReconnectInterval)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		slog.Info("尝试重连远程 Server", "attempt", attempt)
		client, err := s.connectClient()
		if err != nil {
			if terminalError, stopped := s.stopAfterTerminalError(err); stopped {
				slog.Info("远程 Device 凭据已撤销，停止重连", "error", terminalError)
				return
			}
			s.notifyConnection(false, err)
			continue
		}

		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			_ = client.Close()
			return
		}
		old := s.wsClient
		s.wsClient = client
		s.reconnecting = false
		s.mu.Unlock()
		if old != nil {
			_ = old.Close()
		}
		if err := s.registerBridge(); err != nil {
			s.mu.Lock()
			if s.wsClient == client {
				s.wsClient = nil
			}
			s.mu.Unlock()
			_ = client.Close()
			s.notifyConnection(false, err)
			continue
		}
		s.notifyConnection(true, nil)
		client.Start(s.ctx)
		return
	}
}

func (s *TunnelService) registerBridge() error {
	agents := s.registry.ListDescriptors()
	params, _ := json.Marshal(protocol.ANPBridgeRegister{
		BridgeID: s.cfg.BridgeID,
		Agents:   toANPAgents(agents),
	})
	message := &protocol.ANPMessage{
		JSONRPC: "2.0",
		Method:  "bridge/register",
		Params:  params,
	}
	if err := s.sendJSON(message); err != nil {
		slog.Warn("注册 Bridge 失败", "error", err)
		return fmt.Errorf("注册 Bridge 失败: %w", err)
	}
	slog.Info("Bridge 注册成功", "bridge_id", s.cfg.BridgeID, "agents", len(agents))
	return nil
}

func (s *TunnelService) startStatusRefreshLoop() {
	s.refreshOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(s.cfg.StatusRefreshInterval)
			defer ticker.Stop()
			for {
				select {
				case <-s.ctx.Done():
					return
				case <-ticker.C:
					if !s.canRefreshStatus() {
						continue
					}
					if err := s.registerBridge(); err != nil {
						slog.Debug("刷新 Agent 状态失败", "error", err)
					}
				}
			}
		}()
	})
}

func (s *TunnelService) canRefreshStatus() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.wsClient != nil && !s.reconnecting && s.terminalErr == nil
}

func classifyTerminalConnectionError(err error) error {
	if errors.Is(err, ErrConnectionReplaced) || errors.Is(err, ErrCredentialsRevoked) {
		return err
	}
	if infra.IsWebSocketClose(err, connectionReplacedCode, connectionReplacedReason) {
		return fmt.Errorf("%w: %v", ErrConnectionReplaced, err)
	}
	if infra.IsWebSocketClose(err, connectionReplacedCode, deviceDeletedReason) ||
		infra.IsWebSocketHandshakeStatus(err, http.StatusUnauthorized, http.StatusForbidden) {
		return fmt.Errorf("%w: %v", ErrCredentialsRevoked, err)
	}
	return nil
}

// stopAfterTerminalError records a non-recoverable connection failure exactly
// once and reports it to the Local Console. It is used for rejected handshakes;
// active-socket closes must first verify the client identity in
// handleConnectionError.
func (s *TunnelService) stopAfterTerminalError(err error) (error, bool) {
	terminalError := classifyTerminalConnectionError(err)
	if terminalError == nil {
		return nil, false
	}

	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return nil, false
	}
	if s.terminalErr != nil {
		terminalError = s.terminalErr
		s.mu.Unlock()
		return terminalError, true
	}
	s.terminalErr = terminalError
	s.reconnecting = false
	s.mu.Unlock()

	s.notifyConnection(false, terminalError)
	return terminalError, true
}

func (s *TunnelService) handleMessage(data []byte) {
	var message protocol.ANPMessage
	if err := json.Unmarshal(data, &message); err != nil {
		slog.Warn("收到无效 ANP 消息", "error", err)
		return
	}
	// WSClient invokes this callback from its read loop. Agent startup and
	// blocking calls must not stop that loop from reading pongs or independent
	// JSON-RPC messages, otherwise one slow Agent can tear down the tunnel.
	go s.routeMessage(message)
}

func (s *TunnelService) routeMessage(message protocol.ANPMessage) {
	response := s.router.Route(s.ctx, &message, s.sessions)
	if response != nil {
		if err := s.sendJSON(response); err != nil {
			if errors.Is(err, errANPDeviceMessageTooLarge) {
				_ = s.sendJSON(protocol.NewErrorResponse(response.ID, protocol.ANPErrorResponseTooLarge,
					fmt.Sprintf("Device response exceeds the %d-byte transport limit", protocol.MaxANPDeviceMessageBytes)))
			}
			slog.Warn("发送远程响应失败", "id", response.ID, "error", err)
		}
	}
}

func (s *TunnelService) sendJSON(value any) error {
	s.mu.RLock()
	client := s.wsClient
	s.mu.RUnlock()
	if client == nil {
		return fmt.Errorf("WebSocket 未连接")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("序列化 ANP 消息失败: %w", err)
	}
	if len(payload) > protocol.MaxANPDeviceMessageBytes {
		return fmt.Errorf("%w: got %d bytes, maximum is %d", errANPDeviceMessageTooLarge, len(payload), protocol.MaxANPDeviceMessageBytes)
	}
	return client.SendText(payload)
}

func (s *TunnelService) notifyConnection(connected bool, err error) {
	if s.cfg.OnConnectionChange != nil {
		s.cfg.OnConnectionChange(connected, err)
	}
}

func toANPAgents(descriptors []agent.AgentDescriptor) []protocol.ANPAgent {
	result := make([]protocol.ANPAgent, len(descriptors))
	for index, descriptor := range descriptors {
		result[index] = protocol.ANPAgent{
			AgentID:     descriptor.AgentID,
			DisplayName: descriptor.DisplayName,
			Status:      descriptor.Status,
		}
	}
	return result
}
