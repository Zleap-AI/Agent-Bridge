package caller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/gateway"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/model"
)

type messageTestRepository struct {
	records []model.CallRecord
}

func (r *messageTestRepository) ListDevices(context.Context) ([]model.Device, error) {
	return []model.Device{{BridgeID: "device-1"}}, nil
}

func (r *messageTestRepository) Device(_ context.Context, id string) (model.Device, bool, error) {
	if id != "device-1" {
		return model.Device{}, false, nil
	}
	return model.Device{BridgeID: id}, true, nil
}

func (r *messageTestRepository) ListDeviceAgents(context.Context, string) ([]model.Agent, error) {
	return []model.Agent{{BridgeID: "device-1", AgentID: "codex"}}, nil
}

func (r *messageTestRepository) InsertCallRecord(_ context.Context, record model.CallRecord) error {
	r.records = append(r.records, record)
	return nil
}

func (r *messageTestRepository) ListCallRecords(context.Context, int) ([]model.CallRecord, error) {
	return r.records, nil
}

type messageTestGateway struct {
	request  protocol.ANPMessage
	response json.RawMessage
}

func (g *messageTestGateway) IsOnline(string) bool { return true }

func (g *messageTestGateway) Request(_ context.Context, _ string, request protocol.ANPMessage) (json.RawMessage, error) {
	g.request = request
	if g.response != nil {
		return g.response, nil
	}
	return json.RawMessage(`{"messages":[],"total":0,"cursor":0}`), nil
}

func (g *messageTestGateway) Start(string, protocol.ANPMessage) (*gateway.Operation, error) {
	return nil, nil
}

func TestMessagesForwardsCursorAndLimitToDevice(t *testing.T) {
	repository := &messageTestRepository{}
	deviceGateway := &messageTestGateway{}
	service := New(repository, deviceGateway)

	if _, err := service.Messages(context.Background(), "device-1", "codex", "session-1", 23, 7); err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if deviceGateway.request.Method != "sessions/messages" {
		t.Fatalf("method = %q", deviceGateway.request.Method)
	}
	var params struct {
		AgentID   string `json:"agent_id"`
		SessionID string `json:"session_id"`
		Cursor    int    `json:"cursor"`
		Limit     int    `json:"limit"`
	}
	if err := json.Unmarshal(deviceGateway.request.Params, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if params.AgentID != "codex" || params.SessionID != "session-1" || params.Cursor != 23 || params.Limit != 7 {
		t.Fatalf("params = %+v", params)
	}
	if len(repository.records) != 1 || repository.records[0].Status != "completed" {
		t.Fatalf("call records = %+v", repository.records)
	}
}

func TestMessagesRejectsInvalidPageBeforeTargetLookup(t *testing.T) {
	repository := &messageTestRepository{}
	deviceGateway := &messageTestGateway{}
	service := New(repository, deviceGateway)

	for _, page := range []struct {
		cursor int
		limit  int
	}{{cursor: -1, limit: 1}, {cursor: 0, limit: 0}, {cursor: 0, limit: MaxMessagesPageSize + 1}} {
		if _, err := service.Messages(context.Background(), "missing", "codex", "session-1", page.cursor, page.limit); err == nil {
			t.Fatalf("Messages(%d, %d) accepted invalid page", page.cursor, page.limit)
		}
	}
	if len(repository.records) != 0 || deviceGateway.request.Method != "" {
		t.Fatalf("invalid requests reached Device: request=%+v records=%+v", deviceGateway.request, repository.records)
	}
}

func TestCreateSessionRecordsInvalidDeviceResponseAsFailure(t *testing.T) {
	repository := &messageTestRepository{}
	deviceGateway := &messageTestGateway{response: json.RawMessage(`{"unexpected":true}`)}
	service := New(repository, deviceGateway)

	if _, err := service.CreateSession(context.Background(), "device-1", "codex"); err == nil {
		t.Fatal("CreateSession accepted a response without sessionId")
	}
	if len(repository.records) != 1 || repository.records[0].Status != "failed" {
		t.Fatalf("call records = %+v, want one failed record", repository.records)
	}
}

func TestUntrustedTargetIdentifiersAreNeverPersistedAsCallRecords(t *testing.T) {
	repository := &messageTestRepository{}
	deviceGateway := &messageTestGateway{}
	service := New(repository, deviceGateway)
	ctx := context.Background()
	const untrustedDeviceID = "CONVERSATION_BODY_MUST_NOT_PERSIST"

	_, _ = service.Sessions(ctx, untrustedDeviceID, "codex")
	_, _ = service.CreateSession(ctx, untrustedDeviceID, "codex")
	_, _ = service.Messages(ctx, untrustedDeviceID, "codex", "session-1", 0, 100)
	_, _ = service.SendMessage(ctx, untrustedDeviceID, "codex", "session-1", []ContentBlock{{Type: "text", Text: "hello"}})

	if len(repository.records) != 0 {
		t.Fatalf("invalid target identifiers were persisted: %+v", repository.records)
	}
	if deviceGateway.request.Method != "" {
		t.Fatalf("invalid target reached Device gateway: %+v", deviceGateway.request)
	}
}
