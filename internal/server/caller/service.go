package caller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/apierror"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/gateway"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/model"
)

// MaxMessageTextBytes keeps the complete JSON-RPC request below the 1 MiB
// Server-to-Device WebSocket limit, including worst-case JSON escaping and envelopes.
const MaxMessageTextBytes = 128 * 1024

// MaxMessagesPageSize bounds one Device response while allowing callers to
// retrieve complete histories with cursor pagination.
const MaxMessagesPageSize = 100

type Repository interface {
	ListDevices(context.Context) ([]model.Device, error)
	Device(context.Context, string) (model.Device, bool, error)
	ListDeviceAgents(context.Context, string) ([]model.Agent, error)
	InsertCallRecord(context.Context, model.CallRecord) error
	ListCallRecords(context.Context, int) ([]model.CallRecord, error)
}

type Gateway interface {
	IsOnline(string) bool
	Request(context.Context, string, protocol.ANPMessage) (json.RawMessage, error)
	Start(string, protocol.ANPMessage) (*gateway.Operation, error)
}

type Service struct {
	repo    Repository
	gateway Gateway
	now     func() time.Time
}

type Device struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Online     bool       `json:"online"`
	AgentCount int        `json:"agent_count"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type MessageStream struct {
	operation *gateway.Operation
}

func (s *MessageStream) Events() <-chan gateway.StreamEvent { return s.operation.Events() }
func (s *MessageStream) Done() <-chan struct{}              { return s.operation.Done() }
func (s *MessageStream) Result() gateway.Result             { return s.operation.Result() }
func (s *MessageStream) Detach()                            { s.operation.Detach() }
func (s *MessageStream) SubscriptionError() error           { return s.operation.SubscriptionError() }

func New(repo Repository, gateway Gateway) *Service {
	return &Service{repo: repo, gateway: gateway, now: time.Now}
}

func (s *Service) ListDevices(ctx context.Context) ([]Device, error) {
	devices, err := s.repo.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Device, 0, len(devices))
	for _, item := range devices {
		agents, err := s.repo.ListDeviceAgents(ctx, item.BridgeID)
		if err != nil {
			return nil, err
		}
		result = append(result, Device{
			ID: item.BridgeID, Name: item.Name, Online: s.gateway.IsOnline(item.BridgeID),
			AgentCount: len(agents), CreatedAt: item.CreatedAt, LastSeenAt: item.LastSeenAt,
		})
	}
	return result, nil
}

func (s *Service) Agents(ctx context.Context, deviceID string) ([]model.Agent, error) {
	if err := s.requireDevice(ctx, deviceID); err != nil {
		return nil, err
	}
	return s.repo.ListDeviceAgents(ctx, deviceID)
}

func (s *Service) Sessions(ctx context.Context, deviceID, agentID string) (json.RawMessage, error) {
	if err := s.requireTarget(ctx, deviceID, agentID); err != nil {
		return nil, err
	}
	params, _ := json.Marshal(map[string]string{"agent_id": agentID})
	return s.request(ctx, deviceID, agentID, protocol.ANPMessage{Method: "sessions/list", Params: params})
}

func (s *Service) CreateSession(ctx context.Context, deviceID, agentID string) (string, error) {
	if err := s.requireTarget(ctx, deviceID, agentID); err != nil {
		return "", err
	}
	started := s.now().UTC()
	message, err := invokeMessage(agentID, "session/new", json.RawMessage(`{}`), false)
	if err != nil {
		s.record(context.Background(), deviceID, agentID, "failed", started)
		return "", err
	}
	raw, err := s.gateway.Request(ctx, deviceID, message)
	if err != nil {
		s.record(context.Background(), deviceID, agentID, "failed", started)
		return "", err
	}
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.SessionID == "" {
		s.record(context.Background(), deviceID, agentID, "failed", started)
		return "", apierror.Wrap(apierror.CodeInternal, "Device returned an invalid Session", http.StatusBadGateway, err)
	}
	s.record(context.Background(), deviceID, agentID, "completed", started)
	return result.SessionID, nil
}

func (s *Service) Messages(ctx context.Context, deviceID, agentID, sessionID string, cursor, limit int) (json.RawMessage, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, apierror.New(apierror.CodeInvalidRequest, "Session ID is required", http.StatusBadRequest)
	}
	if cursor < 0 {
		return nil, apierror.New(apierror.CodeInvalidRequest, "Cursor must not be negative", http.StatusBadRequest)
	}
	if limit < 1 || limit > MaxMessagesPageSize {
		return nil, apierror.New(apierror.CodeInvalidRequest, fmt.Sprintf("Limit must be between 1 and %d", MaxMessagesPageSize), http.StatusBadRequest)
	}
	if err := s.requireTarget(ctx, deviceID, agentID); err != nil {
		return nil, err
	}
	params, _ := json.Marshal(map[string]any{
		"agent_id": agentID, "session_id": sessionID, "cursor": cursor, "limit": limit,
	})
	return s.request(ctx, deviceID, agentID, protocol.ANPMessage{Method: "sessions/messages", Params: params})
}

func (s *Service) SendMessage(ctx context.Context, deviceID, agentID, sessionID string, content []ContentBlock) (*MessageStream, error) {
	started := s.now().UTC()
	if strings.TrimSpace(sessionID) == "" {
		return nil, apierror.New(apierror.CodeInvalidRequest, "Session ID is required", http.StatusBadRequest)
	}
	if len(content) == 0 {
		return nil, apierror.New(apierror.CodeInvalidRequest, "Message content is required", http.StatusBadRequest)
	}
	hasText := false
	totalTextBytes := 0
	for _, block := range content {
		if block.Type != "text" {
			return nil, apierror.New(apierror.CodeUnsupportedContent, fmt.Sprintf("Content type %q is not supported", block.Type), http.StatusUnprocessableEntity)
		}
		totalTextBytes += len(block.Text)
		if totalTextBytes > MaxMessageTextBytes {
			return nil, apierror.New(apierror.CodePayloadTooLarge, fmt.Sprintf("Message text must not exceed %d bytes", MaxMessageTextBytes), http.StatusRequestEntityTooLarge)
		}
		hasText = hasText || strings.TrimSpace(block.Text) != ""
	}
	if !hasText {
		return nil, apierror.New(apierror.CodeInvalidRequest, "Message text is required", http.StatusBadRequest)
	}
	if err := s.requireTarget(ctx, deviceID, agentID); err != nil {
		return nil, err
	}
	params, err := json.Marshal(map[string]any{"sessionId": sessionID, "prompt": content})
	if err != nil {
		s.record(context.Background(), deviceID, agentID, "failed", started)
		return nil, err
	}
	message, err := invokeMessage(agentID, "session/prompt", params, true)
	if err != nil {
		s.record(context.Background(), deviceID, agentID, "failed", started)
		return nil, err
	}
	operation, err := s.gateway.Start(deviceID, message)
	if err != nil {
		s.record(context.Background(), deviceID, agentID, "failed", started)
		return nil, err
	}
	go func() {
		<-operation.Done()
		status := "completed"
		result := operation.Result()
		if result.Err != nil || result.Error != nil {
			status = "failed"
		}
		s.record(context.Background(), deviceID, agentID, status, started)
	}()
	return &MessageStream{operation: operation}, nil
}

func (s *Service) Calls(ctx context.Context) ([]model.CallRecord, error) {
	return s.repo.ListCallRecords(ctx, 1000)
}

func (s *Service) requireDevice(ctx context.Context, deviceID string) error {
	_, found, err := s.repo.Device(ctx, deviceID)
	if err != nil {
		return err
	}
	if !found {
		return apierror.New(apierror.CodeDeviceNotFound, "Device was not found", http.StatusNotFound)
	}
	return nil
}

func (s *Service) requireTarget(ctx context.Context, deviceID, agentID string) error {
	if err := s.requireDevice(ctx, deviceID); err != nil {
		return err
	}
	if !s.gateway.IsOnline(deviceID) {
		return apierror.New(apierror.CodeDeviceOffline, "Device is offline", http.StatusConflict)
	}
	agents, err := s.repo.ListDeviceAgents(ctx, deviceID)
	if err != nil {
		return err
	}
	for _, item := range agents {
		if item.AgentID == agentID {
			return nil
		}
	}
	return apierror.New(apierror.CodeAgentNotFound, "Agent was not found", http.StatusNotFound)
}

func (s *Service) request(ctx context.Context, deviceID, agentID string, message protocol.ANPMessage) (json.RawMessage, error) {
	started := s.now().UTC()
	raw, err := s.gateway.Request(ctx, deviceID, message)
	status := "completed"
	if err != nil {
		status = "failed"
	}
	s.record(context.Background(), deviceID, agentID, status, started)
	return raw, err
}

func (s *Service) record(ctx context.Context, deviceID, agentID, status string, started time.Time) {
	duration := s.now().UTC().Sub(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	_ = s.repo.InsertCallRecord(ctx, model.CallRecord{
		DeviceID: deviceID, AgentID: agentID, Status: status,
		DurationMS: duration, CreatedAt: started,
	})
}

func invokeMessage(agentID, method string, params json.RawMessage, stream bool) (protocol.ANPMessage, error) {
	invoke, err := json.Marshal(protocol.ANPInvokeParams{
		AgentID: agentID, Method: method, Params: params, Stream: stream,
	})
	if err != nil {
		return protocol.ANPMessage{}, err
	}
	return protocol.ANPMessage{Method: "invoke", Params: invoke}, nil
}
