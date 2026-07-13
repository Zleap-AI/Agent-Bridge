// -*- coding: utf-8 -*-
// Go 1.26+
//
// tunnel.go
// TunnelService — SaaS WebSocket 与本地 Agent 之间的协议桥接核心
// 负责：连接 SaaS、接收 invoke 请求、转发到 Agent、回传流式结果
//
// Lzm 2026-07-09

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/zleap/bridge/internal/agent"
	"github.com/zleap/bridge/internal/infra"
	"github.com/zleap/bridge/internal/protocol"
)

// TunnelService 桥接 SaaS WebSocket 与本地 Agent
type TunnelService struct {
	registry   *agent.AgentRegistry
	wsClient   *infra.WSClient
	cfg        TunnelConfig

	// 消息路由器
	router     *RequestRouter

	// 会话管理器
	sessionMgr *SessionManager

	// 控制
	ctx    context.Context
	cancel context.CancelFunc
}

// TunnelConfig TunnelService 配置
type TunnelConfig struct {
	// ServerURL SaaS WebSocket 地址
	ServerURL string
	// BridgeID Bridge 标识
	BridgeID string
	// Token 认证令牌
	Token string
	// ReconnectInterval 重连间隔
	ReconnectInterval time.Duration
	// MaxReconnectAttempts 最大重连次数（0 表示无限）
	MaxReconnectAttempts int
}

// DefaultTunnelConfig 返回默认隧道配置
func DefaultTunnelConfig() TunnelConfig {
	return TunnelConfig{
		ReconnectInterval:   5 * time.Second,
		MaxReconnectAttempts: 0, // 无限重连
	}
}

// NewTunnelService 创建 TunnelService 实例
// Lzm 2026-07-09
func NewTunnelService(registry *agent.AgentRegistry, cfg TunnelConfig) *TunnelService {
	ctx, cancel := context.WithCancel(context.Background())

	svc := &TunnelService{
		registry:   registry,
		cfg:        cfg,
		ctx:        ctx,
		cancel:     cancel,
		sessionMgr: NewSessionManager(registry),
		router:     NewRequestRouter(registry),
	}

	// 设置流式回调 — 将 Agent 的流式块推送到 SaaS
	svc.router.SetStreamCallback(func(requestID string, chunkType string, text string) error {
		msg := protocol.NewStreamUpdate(requestID, chunkType, text)
		if svc.wsClient != nil {
			return svc.wsClient.SendJSON(msg)
		}
		return fmt.Errorf("WebSocket 未连接")
	})

	// 设置流式最终响应回调 — 流式完成后发送 invoke 结果
	svc.router.SetFinalResponseCallback(func(requestID string, result json.RawMessage, errMsg string) {
		if svc.wsClient == nil {
			return
		}
		if errMsg != "" {
			// 发送错误响应
			resp := protocol.NewErrorResponse(requestID, -31008, errMsg)
			svc.wsClient.SendJSON(resp)
		} else if result != nil {
			// 发送正常结果
			resp := protocol.NewResultResponse(requestID, result)
			svc.wsClient.SendJSON(resp)
		}
	})

	return svc
}

// Start 启动 TunnelService
// 连接 SaaS WebSocket，注册 Bridge，开始处理消息
// Lzm 2026-07-09
func (s *TunnelService) Start() error {
	slog.Info("TunnelService 启动",
		"server_url", s.cfg.ServerURL,
		"bridge_id", s.cfg.BridgeID,
	)

	// 构建 HTTP Header（SaaS 模拟器需要 X-Bridge-Id 和 Authorization）
	header := make(http.Header)
	header.Set("X-Bridge-Id", s.cfg.BridgeID)

	// 动态获取 agent ID 列表（从 registry 中读取实际发现的 Agent）
	agentIDs := make([]string, 0)
	for _, a := range s.registry.List() {
		agentIDs = append(agentIDs, a.ID())
	}
	header.Set("X-Agent-Ids", strings.Join(agentIDs, ","))
	if s.cfg.Token != "" {
		header.Set("Authorization", "Bearer "+s.cfg.Token)
	}

	// 连接 SaaS WebSocket
	wsCfg := infra.WSClientConfig{
		URL:    s.cfg.ServerURL,
		Header: header,
		OnMessage: func(data []byte) {
			s.handleMessage(data)
		},
		OnError: func(err error) {
			slog.Error("WebSocket 错误", "error", err)
			// 自动重连
			go s.reconnectLoop()
		},
	}

	client, err := infra.NewWSClient(s.ctx, wsCfg)
	if err != nil {
		return fmt.Errorf("连接 SaaS WebSocket 失败: %w", err)
	}
	s.wsClient = client

	// 注册 Bridge
	s.registerBridge()

	return nil
}

// Stop 停止 TunnelService
func (s *TunnelService) Stop() {
	s.cancel()
	if s.wsClient != nil {
		s.wsClient.Close()
	}
}

// registerBridge 向 SaaS 注册 Bridge 和可用 Agent 列表
// Lzm 2026-07-09
func (s *TunnelService) registerBridge() {
	agents := s.registry.ListDescriptors()
	msg := &protocol.ANPMessage{
		JSONRPC: "2.0",
		Method:  "bridge/register",
		Params: func() json.RawMessage {
			data, _ := json.Marshal(protocol.ANPBridgeRegister{
				BridgeID: s.cfg.BridgeID,
				Agents:   toANPAgents(agents),
			})
			return data
		}(),
	}

	if err := s.wsClient.SendJSON(msg); err != nil {
		slog.Error("注册 Bridge 失败", "error", err)
	} else {
		slog.Info("Bridge 注册成功",
			"bridge_id", s.cfg.BridgeID,
			"agents", len(agents),
		)
	}
}

// handleMessage 处理从 SaaS 收到的 WebSocket 消息
// Lzm 2026-07-09
func (s *TunnelService) handleMessage(data []byte) {
	var anpMsg protocol.ANPMessage
	if err := json.Unmarshal(data, &anpMsg); err != nil {
		slog.Warn("收到无效 ANP 消息", "error", err)
		return
	}

	slog.Debug("收到 SaaS 消息",
		"method", anpMsg.Method,
		"id", anpMsg.ID,
	)

	// 路由到对应处理器
	response := s.router.Route(s.ctx, &anpMsg, s.sessionMgr)

	// 如果有响应，发送回 SaaS
	if response != nil && s.wsClient != nil {
		if err := s.wsClient.SendJSON(response); err != nil {
			slog.Error("发送响应到 SaaS 失败",
				"id", response.ID,
				"error", err,
			)
		}
	}
}

// reconnectLoop 自动重连循环
// Lzm 2026-07-09
func (s *TunnelService) reconnectLoop() {
	attempt := 0
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(s.cfg.ReconnectInterval):
			attempt++
			if s.cfg.MaxReconnectAttempts > 0 && attempt > s.cfg.MaxReconnectAttempts {
				slog.Error("重连次数已达上限", "max_attempts", s.cfg.MaxReconnectAttempts)
				return
			}

			slog.Info("尝试重连...", "attempt", attempt)

			// 重建 WebSocket 连接
			header := make(http.Header)
			header.Set("X-Bridge-Id", s.cfg.BridgeID)

			// 动态获取 agent ID 列表
			agentIDs := make([]string, 0)
			for _, a := range s.registry.List() {
				agentIDs = append(agentIDs, a.ID())
			}
			header.Set("X-Agent-Ids", strings.Join(agentIDs, ","))
			if s.cfg.Token != "" {
				header.Set("Authorization", "Bearer "+s.cfg.Token)
			}

			wsCfg := infra.WSClientConfig{
				URL:    s.cfg.ServerURL,
				Header: header,
				OnMessage: func(data []byte) {
					s.handleMessage(data)
				},
				OnError: func(err error) {
					slog.Error("WebSocket 错误（重连后）", "error", err)
				},
			}

			client, err := infra.NewWSClient(s.ctx, wsCfg)
			if err != nil {
				slog.Warn("重连失败", "error", err)
				continue
			}

			// 替换 wsClient
			old := s.wsClient
			s.wsClient = client
			if old != nil {
				old.Close()
			}

			s.registerBridge()
			return
		}
	}
}

// toANPAgents 将 AgentDescriptor 转为 ANPAgent 列表
func toANPAgents(descriptors []agent.AgentDescriptor) []protocol.ANPAgent {
	result := make([]protocol.ANPAgent, len(descriptors))
	for i, d := range descriptors {
		result[i] = protocol.ANPAgent{
			AgentID:     d.AgentID,
			DisplayName: d.DisplayName,
			Status:      d.Status,
		}
	}
	return result
}
