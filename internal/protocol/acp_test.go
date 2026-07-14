package protocol

import (
	"encoding/json"
	"testing"
)

func TestNewInitializeRequestUsesACPVersionOne(t *testing.T) {
	request := NewInitializeRequest("1")
	var params struct {
		ProtocolVersion    int            `json:"protocolVersion"`
		ClientCapabilities map[string]any `json:"clientCapabilities"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatalf("unmarshal initialize params: %v", err)
	}
	if params.ProtocolVersion != 1 {
		t.Fatalf("protocolVersion = %d, want 1", params.ProtocolVersion)
	}
	if params.ClientCapabilities == nil {
		t.Fatal("clientCapabilities is missing")
	}
}
