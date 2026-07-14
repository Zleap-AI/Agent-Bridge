package httpapi

import (
	"strings"
	"testing"
)

func TestDocsHTMLCoversCallerAPIContract(t *testing.T) {
	required := []string{
		`id="authentication"`,
		`id="quick-start"`,
		`id="endpoints"`,
		`id="messages"`,
		`id="streaming"`,
		`id="errors"`,
		`id="limits"`,
		"Authorization: Bearer abk_your_api_key",
		"/api/v1/devices",
		"/api/v1/devices/{device_id}/agents",
		"/api/v1/devices/{device_id}/agents/{agent_id}/sessions",
		"/api/v1/devices/{device_id}/agents/{agent_id}/sessions/{session_id}/messages",
		"message.delta",
		"reasoning.delta",
		"session.updated",
		"DEVICE_OFFLINE",
		"UNSUPPORTED_CONTENT_TYPE",
		"PAYLOAD_TOO_LARGE",
		"131,072 UTF-8 bytes",
		"2,097,152 bytes",
		`href="/openapi.json"`,
	}

	for _, value := range required {
		if !strings.Contains(docsHTML, value) {
			t.Errorf("API documentation is missing %q", value)
		}
	}
}
