package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
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

func TestACPReaderSupportsLargeResponsesAndUpdates(t *testing.T) {
	payload := strings.Repeat("x", 128*1024)
	content, err := json.Marshal(map[string]string{"text": payload})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		message   ACPMessage
		wantReply bool
	}{
		{
			name:      "response",
			message:   ACPMessage{JSONRPC: "2.0", ID: "request-1", Result: content},
			wantReply: true,
		},
		{
			name:    "update",
			message: ACPMessage{JSONRPC: "2.0", Method: "session/update", Params: content},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			line, err := json.Marshal(test.message)
			if err != nil {
				t.Fatal(err)
			}
			if len(line) <= 64*1024 {
				t.Fatalf("test message is only %d bytes", len(line))
			}
			line = append(line, '\n')

			message, err := NewACPReader(bytes.NewReader(line)).ReadMessage()
			if err != nil {
				t.Fatalf("read %s larger than 64 KiB: %v", test.name, err)
			}
			if message == nil {
				t.Fatalf("read %s returned no message", test.name)
			}
			if message.IsResponse() != test.wantReply {
				t.Fatalf("IsResponse() = %v, want %v", message.IsResponse(), test.wantReply)
			}
			if message.IsStreamUpdate() == test.wantReply {
				t.Fatalf("IsStreamUpdate() = %v, want %v", message.IsStreamUpdate(), !test.wantReply)
			}
			raw := message.Params
			if test.wantReply {
				raw = message.Result
			}
			var decoded struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Text != payload {
				t.Fatalf("decoded payload length = %d, want %d", len(decoded.Text), len(payload))
			}
		})
	}
}
