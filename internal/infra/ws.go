// -*- coding: utf-8 -*-
// Go 1.25+
//
// ws.go
// WebSocket 客户端与服务端封装（基于 gorilla/websocket）
// 用于远程服务与 Bridge 之间的长连接通信
//
// Lzm 2026-07-20

package infra

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// wsWriteTimeout WebSocket 写入超时
	wsWriteTimeout = 10 * time.Second
	// wsDefaultReadTimeout WebSocket 默认读取超时
	wsDefaultReadTimeout = 60 * time.Second
	// wsDefaultPingInterval WebSocket 默认 ping 发送间隔
	wsDefaultPingInterval = 30 * time.Second
	// wsMaxMessageSize 最大消息大小 (1MB)
	wsMaxMessageSize = 1 * 1024 * 1024
)

// WSClient WebSocket 客户端
type WSClient struct {
	conn         *websocket.Conn
	url          string
	header       http.Header
	mu           sync.Mutex
	startOnce    sync.Once
	closed       bool
	onMessage    func(data []byte)
	onError      func(client *WSClient, err error)
	pingInterval time.Duration
	readTimeout  time.Duration
}

// WSClientConfig WebSocket 客户端配置
type WSClientConfig struct {
	URL          string
	Header       http.Header
	OnMessage    func(data []byte)
	OnError      func(client *WSClient, err error)
	// PingInterval ping 发送间隔，默认 30s；macOS OpenCode 场景建议 15s
	PingInterval time.Duration
	// ReadTimeout 读取超时（无任何数据包括 pong 到达的超时），默认 60s
	ReadTimeout time.Duration
}

// WebSocketHandshakeError preserves the HTTP response status returned when a
// WebSocket upgrade is rejected. Callers can use errors.As or
// IsWebSocketHandshakeStatus without depending on Gorilla's dialer behavior.
type WebSocketHandshakeError struct {
	URL        string
	StatusCode int
	Err        error
}

func (e *WebSocketHandshakeError) Error() string {
	return fmt.Sprintf("WebSocket handshake %s returned HTTP %d: %v", e.URL, e.StatusCode, e.Err)
}

func (e *WebSocketHandshakeError) Unwrap() error { return e.Err }

// NewWSClient 创建并连接 WebSocket 客户端
// Lzm 2026-07-20
func NewWSClient(ctx context.Context, cfg WSClientConfig) (*WSClient, error) {
	dialer := *websocket.DefaultDialer
	conn, response, err := dialer.DialContext(ctx, cfg.URL, cfg.Header)
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, newWebSocketDialError(cfg.URL, response, err)
	}

	// 使用配置值或默认值
	readTimeout := cfg.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = wsDefaultReadTimeout
	}
	pingInterval := cfg.PingInterval
	if pingInterval <= 0 {
		pingInterval = wsDefaultPingInterval
	}

	conn.SetReadLimit(wsMaxMessageSize)

	// 设置 Ping 处理器：收到服务端 Ping 时自动回复 Pong
	conn.SetPingHandler(func(appData string) error {
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(wsWriteTimeout))
	})

	// 设置 Pong 处理器：收到服务端 Pong 时延长读取截止时间
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(readTimeout))
	})

	if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("设置 WebSocket 读取超时失败: %w", err)
	}

	client := &WSClient{
		conn:         conn,
		url:          cfg.URL,
		header:       cfg.Header,
		onMessage:    cfg.OnMessage,
		onError:      cfg.OnError,
		pingInterval: pingInterval,
		readTimeout:  readTimeout,
	}

	return client, nil
}

func newWebSocketDialError(address string, response *http.Response, err error) error {
	statusCode := 0
	if response != nil {
		statusCode = response.StatusCode
		if response.Body != nil {
			_ = response.Body.Close()
		}
	}
	if statusCode != 0 {
		return &WebSocketHandshakeError{URL: address, StatusCode: statusCode, Err: err}
	}
	return fmt.Errorf("连接 WebSocket %s 失败: %w", address, err)
}

// Start begins the read and keepalive loops after the owner has installed the
// client as its active connection. Delaying these loops closes the race where
// an immediately closed socket reported an error before TunnelService could
// associate the callback with the new connection.
func (c *WSClient) Start(ctx context.Context) {
	c.startOnce.Do(func() {
		if c.isClosed() {
			return
		}
		go c.readLoop()
		go c.pingLoop(ctx)
	})
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
				c.onError(c, fmt.Errorf("读取协程 panic: %v", r))
			}
		}
	}()

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if c.onError != nil && !c.isClosed() {
				c.onError(c, fmt.Errorf("WebSocket 读取失败: %w", err))
			}
			return
		}
		if c.onMessage != nil {
			c.onMessage(message)
		}
	}
}

func (c *WSClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// pingLoop 定期发送 ping 保活
// Lzm 2026-07-20
func (c *WSClient) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(c.pingInterval)
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
				c.onError(c, fmt.Errorf("发送 ping 失败: %w", err))
			}
		}
	}
}

// IsConnected 检查 WebSocket 连接是否仍然有效
// Lzm 2026-07-20
func (c *WSClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed
}

// IsWebSocketClose reports whether err, including a wrapped error, carries the
// specified close code and reason. This keeps Gorilla-specific error handling
// inside the WebSocket adapter.
func IsWebSocketClose(err error, code int, reason string) bool {
	var closeError *websocket.CloseError
	return errors.As(err, &closeError) && closeError.Code == code && closeError.Text == reason
}

// IsWebSocketHandshakeStatus reports whether a rejected WebSocket handshake
// returned one of the supplied HTTP status codes.
func IsWebSocketHandshakeStatus(err error, statusCodes ...int) bool {
	var handshakeError *WebSocketHandshakeError
	if !errors.As(err, &handshakeError) {
		return false
	}
	for _, statusCode := range statusCodes {
		if handshakeError.StatusCode == statusCode {
			return true
		}
	}
	return false
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
			CheckOrigin:     IsAllowedLocalOrigin,
		},
	}
}

// IsAllowedLocalOrigin applies same-origin checks to the Local Console while
// retaining support for non-browser clients, which do not send Origin. The two
// loopback names are treated as equivalent so localhost pages may connect to a
// server bound to 127.0.0.1.
func IsAllowedLocalOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}

	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return false
	}

	if strings.EqualFold(u.Host, r.Host) {
		return true
	}

	originHost, originPort := u.Hostname(), normalizedPort(u.Port(), u.Scheme == "https")
	requestHost, requestPort := splitRequestHost(r)
	return isLoopbackHost(originHost) && isLoopbackHost(requestHost) && originPort == requestPort
}

func splitRequestHost(r *http.Request) (string, string) {
	host := r.Host
	if parsedHost, parsedPort, err := net.SplitHostPort(host); err == nil {
		return parsedHost, normalizedPort(parsedPort, r.TLS != nil)
	}
	return strings.Trim(host, "[]"), normalizedPort("", r.TLS != nil)
}

func normalizedPort(port string, secure bool) string {
	if port != "" {
		return port
	}
	if secure {
		return "443"
	}
	return "80"
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
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

// WriteJSON 向 WebSocket 写入 JSON 消息
func WriteJSON(conn *websocket.Conn, v interface{}) error {
	conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return conn.WriteJSON(v)
}
