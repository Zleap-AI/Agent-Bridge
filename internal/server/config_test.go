package server

import "testing"

func TestConfigRejectsUnsupportedPublicURLComponents(t *testing.T) {
	base := Config{
		ListenAddr:   "127.0.0.1:9201",
		DatabasePath: "/tmp/agent-bridge-test.db",
	}
	for _, publicURL := range []string{
		"https://user@example.com",
		"https://example.com/agent-bridge",
		"https://example.com?source=test",
		"https://example.com#console",
	} {
		config := base
		config.PublicURL = publicURL
		if err := config.Validate(); err == nil {
			t.Fatalf("PublicURL %q was accepted", publicURL)
		}
	}

	for _, publicURL := range []string{"http://192.0.2.10:9201", "https://example.com", "https://example.com/"} {
		config := base
		config.PublicURL = publicURL
		if err := config.Validate(); err != nil {
			t.Fatalf("PublicURL %q was rejected: %v", publicURL, err)
		}
	}
}
