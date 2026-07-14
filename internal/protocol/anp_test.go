package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestInvokeRequestPreservesLocalServerContract(t *testing.T) {
	request := NewInvokeRequest(
		"req-1",
		"codex",
		"session/prompt",
		json.RawMessage(`{"sessionId":"sess-1","prompt":[{"type":"text","text":"hello"}]}`),
	)

	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var wire struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Method  string `json:"method"`
		Params  struct {
			AgentID string          `json:"agent_id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
			Stream  bool            `json:"stream"`
		} `json:"params"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("unmarshal wire request: %v", err)
	}

	if wire.JSONRPC != "2.0" || wire.ID != "req-1" || wire.Method != "invoke" {
		t.Fatalf("unexpected envelope: %+v", wire)
	}
	if wire.Params.AgentID != "codex" || wire.Params.Method != "session/prompt" {
		t.Fatalf("unexpected invoke target: %+v", wire.Params)
	}
	if wire.Params.Stream {
		t.Fatal("NewInvokeRequest unexpectedly enabled streaming")
	}

	var prompt struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	if err := json.Unmarshal(wire.Params.Params, &prompt); err != nil {
		t.Fatalf("unmarshal prompt params: %v", err)
	}
	if prompt.SessionID != "sess-1" || len(prompt.Prompt) != 1 || prompt.Prompt[0].Text != "hello" {
		t.Fatalf("unexpected prompt payload: %+v", prompt)
	}
}

func TestBridgeRegisterPreservesBridgeAndAgentFields(t *testing.T) {
	registration := ANPBridgeRegister{
		BridgeID: "bridge-1",
		Agents: []ANPAgent{
			{AgentID: "codex", DisplayName: "Codex", Status: "idle"},
			{AgentID: "kimi", DisplayName: "Kimi", Status: "busy"},
		},
	}

	encoded, err := json.Marshal(registration)
	if err != nil {
		t.Fatalf("marshal registration: %v", err)
	}

	var decoded ANPBridgeRegister
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal registration: %v", err)
	}
	if !reflect.DeepEqual(decoded, registration) {
		t.Fatalf("registration changed across wire: got %+v want %+v", decoded, registration)
	}
}

func TestStreamUpdateUsesSessionUpdateNotification(t *testing.T) {
	update := NewStreamUpdate("req-1", "response", "partial")
	if update.JSONRPC != "2.0" || update.Method != "session/update" || update.ID != "" {
		t.Fatalf("unexpected stream envelope: %+v", update)
	}

	var params ANPStreamUpdate
	if err := json.Unmarshal(update.Params, &params); err != nil {
		t.Fatalf("unmarshal stream params: %v", err)
	}
	if params.RequestID != "req-1" || params.Type != "response" {
		t.Fatalf("unexpected stream params: %+v", params)
	}

	var content map[string]string
	if err := json.Unmarshal(params.Content, &content); err != nil {
		t.Fatalf("unmarshal stream content: %v", err)
	}
	if content["text"] != "partial" {
		t.Fatalf("stream text = %q, want partial", content["text"])
	}
}

func TestANPErrorResponsePreservesJSONRPCShape(t *testing.T) {
	response := NewErrorResponse("req-1", -31001, "unknown agent")
	if response.JSONRPC != "2.0" || response.ID != "req-1" || response.Result != nil {
		t.Fatalf("unexpected error envelope: %+v", response)
	}
	if response.Error == nil || response.Error.Code != -31001 || response.Error.Message != "unknown agent" {
		t.Fatalf("unexpected error payload: %+v", response.Error)
	}
}
