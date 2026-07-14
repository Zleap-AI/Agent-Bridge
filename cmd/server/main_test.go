package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteConsoleUsesSPAFallbackWithoutSwallowingBackendRoutes(t *testing.T) {
	handler := remoteConsoleHandler()
	for _, target := range []string{"/", "/devices/dev_1", "/settings/api-keys"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Agent-Bridge Server") {
			t.Fatalf("SPA route %s: status=%d body=%q", target, response.Code, response.Body.String())
		}
	}
	for _, target := range []string{"/api/v1/unknown", "/ws", "/docs", "/openapi.json"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("backend route %s was swallowed by SPA: status=%d", target, response.Code)
		}
	}
}

func TestRemoteConsoleHTMLIsNotCached(t *testing.T) {
	handler := remoteConsoleHandler()
	for _, target := range []string{"/", "/devices/dev_1"} {
		t.Run(target, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, target, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if got := response.Header().Get("Cache-Control"); got != "no-cache" {
				t.Fatalf("Cache-Control = %q, want no-cache", got)
			}
		})
	}
}

func TestParseConfigKeepsExplicitDatabaseEnvironment(t *testing.T) {
	t.Setenv("AGENT_BRIDGE_DATA_DIR", "/var/lib/from-environment")
	t.Setenv("AGENT_BRIDGE_DATABASE_PATH", "/srv/agent-bridge/custom.db")
	config, _, err := parseConfig("serve", []string{"--data-dir", "/srv/agent-bridge/data"})
	if err != nil {
		t.Fatal(err)
	}
	if config.DatabasePath != "/srv/agent-bridge/custom.db" {
		t.Fatalf("database path = %q, want explicit environment value", config.DatabasePath)
	}
}

func TestParseConfigDerivesDatabaseFromDataDirectory(t *testing.T) {
	t.Setenv("AGENT_BRIDGE_DATA_DIR", "")
	t.Setenv("AGENT_BRIDGE_DATABASE_PATH", "")
	config, _, err := parseConfig("serve", []string{"--data-dir", "/srv/agent-bridge/data"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/srv/agent-bridge/data", "agent-bridge.db")
	if config.DatabasePath != want {
		t.Fatalf("database path = %q, want %q", config.DatabasePath, want)
	}
}
