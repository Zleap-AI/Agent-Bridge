package gateway

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
	"unicode/utf8"

	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/apierror"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/model"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/secret"
	"github.com/gorilla/websocket"
)

const (
	connectionReplaced = "connection_replaced"
	// MaxRequestMessageSize bounds Server-to-Device requests. Caller message
	// validation keeps normal requests comfortably below this limit.
	MaxRequestMessageSize = protocol.MaxANPRequestBytes
	// MaxDeviceMessageSize bounds post-registration Device-to-Server messages.
	// Responses are allowed to be larger than requests because a single history
	// page can contain many messages. The fixed cap still bounds allocations for
	// bad peers.
	MaxDeviceMessageSize = protocol.MaxANPDeviceMessageBytes
	// Registration limits match the small, fixed Agent adapter catalog while
	// leaving room for third-party adapters.
	MaxRegisteredAgents      = 64
	MaxAgentIDRunes          = 128
	MaxAgentDisplayNameRunes = 200
	MaxAgentStatusRunes      = 64
	maxQueuedEvents          = 1024
	maxQueuedBytes           = 4 * 1024 * 1024
	maxPendingPerDevice      = 64
	maxPendingGlobal         = 1024
	writeTimeout             = 10 * time.Second
	requestTimeout           = 30 * time.Minute
	registrationTimeout      = 15 * time.Second
)

type Repository interface {
	AuthenticateDevice(context.Context, string, string) (bool, error)
	TouchDevice(context.Context, string, time.Time) error
	ReplaceDeviceAgents(context.Context, string, []model.Agent, time.Time) error
}

type StreamEvent struct {
	Type    string
	Content json.RawMessage
}

type Result struct {
	Value json.RawMessage
	Error *protocol.ANPError
	Err   error
}

type Operation struct {
	pending *pending
	cancel  func(error)
}

func (o *Operation) Events() <-chan StreamEvent { return o.pending.events }
func (o *Operation) Done() <-chan struct{}      { return o.pending.done }
func (o *Operation) Result() Result             { return o.pending.getResult() }
func (o *Operation) Detach()                    { o.pending.detach() }
func (o *Operation) SubscriptionError() error   { return o.pending.getSubscriptionError() }

func (o *Operation) cancelRequest(err error) {
	if o.cancel != nil {
		o.cancel(err)
	}
}

func (o *Operation) Wait(ctx context.Context) Result {
	select {
	case <-ctx.Done():
		return Result{Err: ctx.Err()}
	case <-o.pending.done:
		return o.pending.getResult()
	}
}

type pending struct {
	mu              sync.Mutex
	events          chan StreamEvent
	done            chan struct{}
	wake            chan struct{}
	detached        chan struct{}
	queue           []StreamEvent
	queueBytes      int
	closed          bool
	isDetached      bool
	subscriptionErr error
	result          Result
	timer           *time.Timer
	onComplete      func()
}

func newPending(onComplete func()) *pending {
	item := &pending{
		events: make(chan StreamEvent), done: make(chan struct{}), wake: make(chan struct{}, 1),
		detached: make(chan struct{}), queue: make([]StreamEvent, 0, 32), onComplete: onComplete,
	}
	go item.pump()
	return item
}

func (p *pending) deliver(event StreamEvent) {
	p.mu.Lock()
	if p.closed || p.isDetached {
		p.mu.Unlock()
		return
	}
	eventSize := len(event.Type) + len(event.Content)
	if len(p.queue) >= maxQueuedEvents || p.queueBytes+eventSize > maxQueuedBytes {
		p.detachLocked(apierror.New(apierror.CodeAgentUnavailable, "Streaming client could not keep up with the response", http.StatusTooManyRequests))
		p.mu.Unlock()
		p.signal()
		return
	}
	p.queue = append(p.queue, event)
	p.queueBytes += eventSize
	p.mu.Unlock()
	p.signal()
}

func (p *pending) pump() {
	defer close(p.events)
	for {
		p.mu.Lock()
		if p.isDetached {
			p.queue = nil
			p.mu.Unlock()
			return
		}
		if len(p.queue) != 0 {
			event := p.queue[0]
			p.queueBytes -= len(event.Type) + len(event.Content)
			p.queue[0] = StreamEvent{}
			p.queue = p.queue[1:]
			p.mu.Unlock()
			select {
			case p.events <- event:
			case <-p.detached:
				return
			}
			continue
		}
		if p.closed {
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
		<-p.wake
	}
}

func (p *pending) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *pending) complete(result Result) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.result = result
	if p.timer != nil {
		p.timer.Stop()
	}
	close(p.done)
	onComplete := p.onComplete
	p.onComplete = nil
	p.mu.Unlock()
	p.signal()
	if onComplete != nil {
		onComplete()
	}
}

func (p *pending) getResult() Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.result
}

func (p *pending) getSubscriptionError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.subscriptionErr
}

func (p *pending) setTimer(timer *time.Timer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		timer.Stop()
		return
	}
	p.timer = timer
}

func (p *pending) detach() {
	p.mu.Lock()
	if p.isDetached {
		p.mu.Unlock()
		return
	}
	p.detachLocked(nil)
	p.mu.Unlock()
	p.signal()
}

func (p *pending) detachLocked(err error) {
	p.isDetached = true
	p.subscriptionErr = err
	p.queue = nil
	p.queueBytes = 0
	close(p.detached)
}

type Hub struct {
	repo     Repository
	upgrader websocket.Upgrader
	now      func() time.Time

	mu           sync.RWMutex
	connections  map[string]*deviceConnection
	revoked      map[string]struct{}
	pendingTotal int
}

func New(repo Repository) *Hub {
	return &Hub{
		repo: repo,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     func(*http.Request) bool { return true },
		},
		now:         time.Now,
		connections: make(map[string]*deviceConnection),
		revoked:     make(map[string]struct{}),
	}
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	deviceID := strings.TrimSpace(r.Header.Get("X-Bridge-Id"))
	token := bearerToken(r.Header.Get("Authorization"))
	if deviceID == "" || token == "" {
		http.Error(w, "missing device credentials", http.StatusUnauthorized)
		return
	}
	valid, err := h.repo.AuthenticateDevice(r.Context(), deviceID, secret.Digest(token))
	if err != nil {
		http.Error(w, "device authentication failed", http.StatusInternalServerError)
		return
	}
	if !valid {
		http.Error(w, "invalid device credentials", http.StatusUnauthorized)
		return
	}

	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn := newDeviceConnection(h, deviceID, ws)
	agents, registeredAt, registered := conn.register()
	if !registered {
		conn.close("registration_failed")
		return
	}
	// Revalidate after registration. Together with the revoked tombstone checked
	// by attach, this closes the delete-vs-handshake authentication race.
	authContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	valid, err = h.repo.AuthenticateDevice(authContext, deviceID, secret.Digest(token))
	cancel()
	if err != nil || !valid {
		conn.close("device_revoked")
		return
	}
	attached, err := h.attach(conn, agents, registeredAt)
	if err != nil {
		conn.close("registration_failed")
		return
	}
	if !attached {
		conn.close("device_revoked")
		return
	}
	conn.touch(true)
	conn.run()
}

func (h *Hub) attach(conn *deviceConnection, agents []model.Agent, registeredAt time.Time) (bool, error) {
	h.mu.Lock()
	if _, revoked := h.revoked[conn.deviceID]; revoked {
		h.mu.Unlock()
		return false, nil
	}
	old := h.connections[conn.deviceID]
	h.connections[conn.deviceID] = conn
	// Persist inside the identity swap so observers can see neither an
	// unregistered connection nor a catalog written by a superseded one.
	if err := h.repo.ReplaceDeviceAgents(context.Background(), conn.deviceID, agents, registeredAt); err != nil {
		if old == nil {
			delete(h.connections, conn.deviceID)
		} else {
			h.connections[conn.deviceID] = old
		}
		h.mu.Unlock()
		return false, err
	}
	h.mu.Unlock()
	if old != nil && old != conn {
		old.close(connectionReplaced)
	}
	return true, nil
}

func (h *Hub) replaceCurrentAgents(conn *deviceConnection, agents []model.Agent, registeredAt time.Time) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.connections[conn.deviceID] != conn {
		return false, nil
	}
	// Holding the Hub lock through persistence orders this refresh before any
	// replacement attach or detach of the same connection identity.
	return true, h.repo.ReplaceDeviceAgents(context.Background(), conn.deviceID, agents, registeredAt)
}

func (h *Hub) detach(conn *deviceConnection) {
	h.mu.Lock()
	if h.connections[conn.deviceID] == conn {
		delete(h.connections, conn.deviceID)
	}
	h.mu.Unlock()
}

func (h *Hub) IsOnline(deviceID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.connections[deviceID] != nil
}

func (h *Hub) Disconnect(deviceID, reason string) {
	h.mu.Lock()
	h.revoked[deviceID] = struct{}{}
	conn := h.connections[deviceID]
	if conn != nil {
		delete(h.connections, deviceID)
	}
	h.mu.Unlock()
	if conn != nil {
		conn.close(reason)
	}
}

func (h *Hub) Start(deviceID string, message protocol.ANPMessage) (*Operation, error) {
	h.mu.Lock()
	conn := h.connections[deviceID]
	if conn == nil {
		h.mu.Unlock()
		return nil, apierror.New(apierror.CodeDeviceOffline, "Device is offline", http.StatusConflict)
	}
	if h.pendingTotal >= maxPendingGlobal {
		h.mu.Unlock()
		return nil, apierror.New(apierror.CodeAgentUnavailable, "Server has too many active Agent calls", http.StatusTooManyRequests)
	}
	h.pendingTotal++
	h.mu.Unlock()
	if message.ID == "" {
		id, err := secret.Token("req_", 12)
		if err != nil {
			h.releasePending()
			return nil, err
		}
		message.ID = id
	}
	if message.JSONRPC == "" {
		message.JSONRPC = "2.0"
	}
	item := newPending(h.releasePending)
	if err := conn.sendRequest(message, item); err != nil {
		item.complete(Result{Err: err})
		return nil, err
	}
	item.setTimer(time.AfterFunc(requestTimeout, func() {
		conn.expire(message.ID, errors.New("device request timed out"))
	}))
	return &Operation{
		pending: item,
		cancel: func(err error) {
			conn.cancelRequest(message.ID, item, err)
		},
	}, nil
}

func (h *Hub) releasePending() {
	h.mu.Lock()
	if h.pendingTotal > 0 {
		h.pendingTotal--
	}
	h.mu.Unlock()
}

func (h *Hub) Request(ctx context.Context, deviceID string, message protocol.ANPMessage) (json.RawMessage, error) {
	op, err := h.Start(deviceID, message)
	if err != nil {
		return nil, err
	}
	defer op.Detach()
	var result Result
	select {
	case <-ctx.Done():
		op.cancelRequest(ctx.Err())
		return nil, ctx.Err()
	case <-op.Done():
		result = op.Result()
	}
	if result.Err != nil {
		return nil, result.Err
	}
	if result.Error != nil {
		return nil, mapANPError(result.Error)
	}
	return result.Value, nil
}

type deviceConnection struct {
	hub      *Hub
	deviceID string
	ws       *websocket.Conn

	writeMu   sync.Mutex
	closeMu   sync.Mutex
	touchMu   sync.Mutex
	closed    bool
	lastTouch time.Time

	pendingMu sync.RWMutex
	pending   map[string]*pending
}

func newDeviceConnection(hub *Hub, deviceID string, ws *websocket.Conn) *deviceConnection {
	return &deviceConnection{hub: hub, deviceID: deviceID, ws: ws, pending: make(map[string]*pending)}
}

func (c *deviceConnection) run() {
	defer func() {
		c.hub.detach(c)
		c.close("connection_closed")
		c.touch(true)
	}()
	c.ws.SetReadLimit(MaxDeviceMessageSize)
	c.ws.SetPongHandler(func(string) error {
		c.touch(false)
		return c.ws.SetReadDeadline(time.Now().Add(70 * time.Second))
	})
	_ = c.ws.SetReadDeadline(time.Now().Add(70 * time.Second))
	go c.pingLoop()
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		var message protocol.ANPMessage
		if err := json.Unmarshal(data, &message); err != nil {
			continue
		}
		c.touch(false)
		if message.Method == "bridge/register" {
			if !c.handleRegister(message.Params) {
				return
			}
			continue
		}
		c.dispatch(message)
	}
}

func (c *deviceConnection) register() ([]model.Agent, time.Time, bool) {
	c.ws.SetReadLimit(MaxRequestMessageSize)
	_ = c.ws.SetReadDeadline(time.Now().Add(registrationTimeout))
	_, data, err := c.ws.ReadMessage()
	if err != nil {
		return nil, time.Time{}, false
	}
	var message protocol.ANPMessage
	if err := json.Unmarshal(data, &message); err != nil || message.Method != "bridge/register" {
		return nil, time.Time{}, false
	}
	return c.decodeRegistration(message.Params)
}

func (c *deviceConnection) touch(force bool) {
	c.touchMu.Lock()
	defer c.touchMu.Unlock()
	now := c.hub.now().UTC()
	if !force && now.Sub(c.lastTouch) < 30*time.Second {
		return
	}
	if err := c.hub.repo.TouchDevice(context.Background(), c.deviceID, now); err == nil {
		c.lastTouch = now
	}
}

func (c *deviceConnection) handleRegister(raw json.RawMessage) bool {
	agents, registeredAt, valid := c.decodeRegistration(raw)
	if !valid {
		return false
	}
	current, err := c.hub.replaceCurrentAgents(c, agents, registeredAt)
	if err != nil {
		c.close("registration_failed")
		return false
	}
	if !current {
		c.close(connectionReplaced)
		return false
	}
	return true
}

func (c *deviceConnection) decodeRegistration(raw json.RawMessage) ([]model.Agent, time.Time, bool) {
	var registration protocol.ANPBridgeRegister
	if err := json.Unmarshal(raw, &registration); err != nil || registration.BridgeID != c.deviceID || !validRegisteredAgents(registration.Agents) {
		c.close("invalid_registration")
		return nil, time.Time{}, false
	}
	now := c.hub.now().UTC()
	agents := make([]model.Agent, 0, len(registration.Agents))
	for _, item := range registration.Agents {
		displayName := item.DisplayName
		if displayName == "" {
			displayName = item.AgentID
		}
		agents = append(agents, model.Agent{
			BridgeID: c.deviceID, AgentID: item.AgentID, DisplayName: displayName,
			Status: item.Status, UpdatedAt: now,
		})
	}
	return agents, now, true
}

func validRegisteredAgents(agents []protocol.ANPAgent) bool {
	if len(agents) > MaxRegisteredAgents {
		return false
	}
	seen := make(map[string]struct{}, len(agents))
	for _, item := range agents {
		if item.AgentID == "" || strings.TrimSpace(item.AgentID) != item.AgentID || utf8.RuneCountInString(item.AgentID) > MaxAgentIDRunes {
			return false
		}
		if utf8.RuneCountInString(item.DisplayName) > MaxAgentDisplayNameRunes || utf8.RuneCountInString(item.Status) > MaxAgentStatusRunes {
			return false
		}
		if _, duplicate := seen[item.AgentID]; duplicate {
			return false
		}
		seen[item.AgentID] = struct{}{}
	}
	return true
}

func (c *deviceConnection) dispatch(message protocol.ANPMessage) {
	if message.Method == "session/update" {
		var update protocol.ANPStreamUpdate
		if err := json.Unmarshal(message.Params, &update); err != nil {
			slog.Warn("解析 session/update 参数失败",
				"device", c.deviceID,
				"error", err,
			)
			return
		}
		c.pendingMu.RLock()
		item := c.pending[update.RequestID]
		c.pendingMu.RUnlock()
		if item != nil {
			slog.Debug("交付 session/update 事件到 pending item",
				"device", c.deviceID,
				"request_id", update.RequestID,
				"type", update.Type,
			)
			item.deliver(StreamEvent{Type: update.Type, Content: update.Content})
		} else {
			slog.Warn("session/update 找不到匹配的 pending item",
				"device", c.deviceID,
				"request_id", update.RequestID,
				"type", update.Type,
			)
		}
		return
	}
	if message.ID == "" {
		return
	}
	c.pendingMu.Lock()
	item := c.pending[message.ID]
	delete(c.pending, message.ID)
	c.pendingMu.Unlock()
	if item != nil {
		item.complete(Result{Value: message.Result, Error: message.Error})
	}
}

func (c *deviceConnection) sendRequest(message protocol.ANPMessage, item *pending) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(payload) > MaxRequestMessageSize {
		return apierror.New(apierror.CodePayloadTooLarge, "Device request must not exceed 1048576 bytes", http.StatusRequestEntityTooLarge)
	}
	c.pendingMu.Lock()
	if len(c.pending) >= maxPendingPerDevice {
		c.pendingMu.Unlock()
		return apierror.New(apierror.CodeAgentUnavailable, "Device has too many active Agent calls", http.StatusTooManyRequests)
	}
	if _, exists := c.pending[message.ID]; exists {
		c.pendingMu.Unlock()
		return fmt.Errorf("duplicate request id")
	}
	c.pending[message.ID] = item
	c.pendingMu.Unlock()

	c.writeMu.Lock()
	err = c.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err == nil {
		err = c.ws.WriteMessage(websocket.TextMessage, payload)
	}
	c.writeMu.Unlock()
	if err != nil {
		c.expire(message.ID, err)
		return apierror.Wrap(apierror.CodeDeviceOffline, "Device connection is unavailable", http.StatusConflict, err)
	}
	return nil
}

func (c *deviceConnection) expire(id string, err error) {
	c.pendingMu.Lock()
	item := c.pending[id]
	delete(c.pending, id)
	c.pendingMu.Unlock()
	if item != nil {
		item.complete(Result{Err: err})
	}
}

func (c *deviceConnection) cancelRequest(id string, item *pending, err error) {
	c.pendingMu.Lock()
	if c.pending[id] == item {
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
	item.complete(Result{Err: err})
}

func (c *deviceConnection) failAll(err error) {
	c.pendingMu.Lock()
	items := c.pending
	c.pending = make(map[string]*pending)
	c.pendingMu.Unlock()
	for _, item := range items {
		item.complete(Result{Err: apierror.Wrap(apierror.CodeDeviceOffline, "Device connection closed", http.StatusConflict, err)})
	}
}

func (c *deviceConnection) close(reason string) {
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		return
	}
	c.closed = true
	c.closeMu.Unlock()
	c.writeMu.Lock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4001, reason), time.Now().Add(writeTimeout))
	_ = c.ws.Close()
	c.writeMu.Unlock()
	c.failAll(errors.New(reason))
}

func (c *deviceConnection) pingLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		c.closeMu.Lock()
		closed := c.closed
		c.closeMu.Unlock()
		if closed {
			return
		}
		c.writeMu.Lock()
		err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout))
		c.writeMu.Unlock()
		if err != nil {
			c.close("ping_failed")
			return
		}
	}
}

func bearerToken(value string) string {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func mapANPError(err *protocol.ANPError) error {
	switch err.Code {
	case -31001:
		return apierror.New(apierror.CodeAgentNotFound, "Agent was not found", http.StatusNotFound)
	case -31002, -31004, -31006, -31007, -31008, -31009:
		return apierror.New(apierror.CodeAgentUnavailable, "Agent is unavailable", http.StatusConflict)
	case -31005:
		return apierror.New(apierror.CodeSessionNotFound, "Session was not found", http.StatusNotFound)
	case protocol.ANPErrorResponseTooLarge:
		message := strings.TrimSpace(err.Message)
		if message == "" {
			message = "Device response exceeds the supported size"
		}
		return apierror.New(apierror.CodePayloadTooLarge, message, http.StatusBadGateway)
	default:
		message := strings.TrimSpace(err.Message)
		if message == "" {
			message = "Device returned an error"
		}
		return apierror.New(apierror.CodeInternal, message, http.StatusBadGateway)
	}
}

// ResultError maps an internal ANP result to the stable public API error model.
func ResultError(result Result) error {
	if result.Err != nil {
		return result.Err
	}
	if result.Error != nil {
		return mapANPError(result.Error)
	}
	return nil
}
