// -*- coding: utf-8 -*-
// Go 1.26+
//
// ws.go
// WebSocket 客户端与服务端封装（基于 gorilla/websocket）
// 用于 SaaS 与 Bridge 之间的长连接通信
//
// Lzm 2026-07-09

package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// wsWriteTimeout WebSocket 写入超时
	wsWriteTimeout = 10 * time.Second
	// wsReadTimeout WebSocket 读取超时（仅用于初始连接）
	wsReadTimeout = 60 * time.Second
	// wsPingInterval ping 发送间隔
	wsPingInterval = 30 * time.Second
	// wsMaxMessageSize 最大消息大小 (1MB)
	wsMaxMessageSize = 1 * 1024 * 1024
)

// WSClient WebSocket 客户端
type WSClient struct {
	conn      *websocket.Conn
	url       string
	header    http.Header
	mu        sync.Mutex
	closed    bool
	onMessage func(data []byte)
	onError   func(err error)
}

// WSClientConfig WebSocket 客户端配置
type WSClientConfig struct {
	URL        string
	Header     http.Header
	OnMessage  func(data []byte)
	OnError    func(err error)
}

// NewWSClient 创建并连接 WebSocket 客户端
func NewWSClient(ctx context.Context, cfg WSClientConfig) (*WSClient, error) {
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.Dial(cfg.URL, cfg.Header)
	if err != nil {
		return nil, fmt.Errorf("连接 WebSocket %s 失败: %w", cfg.URL, err)
	}

	conn.SetReadLimit(wsMaxMessageSize)
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	})

	client := &WSClient{
		conn:      conn,
		url:       cfg.URL,
		header:    cfg.Header,
		onMessage: cfg.OnMessage,
		onError:   cfg.OnError,
	}

	// 启动读取协程
	go client.readLoop()

	// 启动 ping 协程
	go client.pingLoop(ctx)

	return client, nil
}

// SendJSON 发送 JSON 消息
func (c *WSClient) SendJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("WebSocket 已关闭")
	}

	c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return c.conn.WriteJSON(v)
}

// SendText 发送文本消息
func (c *WSClient) SendText(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("WebSocket 已关闭")
	}

	c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// Close 关闭连接
func (c *WSClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	// 发送关闭帧
	c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	c.conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))

	return c.conn.Close()
}

// readLoop 持续读取 WebSocket 消息
func (c *WSClient) readLoop() {
	defer func() {
		if r := recover(); r != nil {
			if c.onError != nil {
				c.onError(fmt.Errorf("读取协程 panic: %v", r))
			}
		}
	}()

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if c.onError != nil && !c.closed {
				c.onError(fmt.Errorf("WebSocket 读取失败: %w", err))
			}
			return
		}
		if c.onMessage != nil {
			c.onMessage(message)
		}
	}
}

// pingLoop 定期发送 ping 保活
func (c *WSClient) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				return
			}
			c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			err := c.conn.WriteMessage(websocket.PingMessage, nil)
			c.mu.Unlock()
			if err != nil && c.onError != nil {
				c.onError(fmt.Errorf("发送 ping 失败: %w", err))
			}
		}
	}
}

// WSServer WebSocket 服务端（简易版，用于 Admin 接口）
type WSServer struct {
	upgrader websocket.Upgrader
}

// NewWSServer 创建 WebSocket 服务端
func NewWSServer() *WSServer {
	return &WSServer{
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// 开发阶段允许所有来源
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// Upgrade 将 HTTP 连接升级为 WebSocket
func (s *WSServer) Upgrade(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, fmt.Errorf("升级 WebSocket 失败: %w", err)
	}
	conn.SetReadLimit(wsMaxMessageSize)
	return conn, nil
}

// ReadJSON 从 WebSocket 读取并解析 JSON 消息
func ReadJSON(conn *websocket.Conn, v interface{}) error {
	_, data, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// WriteJSON 向 WebSocket 写入 JSON 消息
func WriteJSON(conn *websocket.Conn, v interface{}) error {
	conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return conn.WriteJSON(v)
}
