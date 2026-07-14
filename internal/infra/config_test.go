package infra

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultConfigStartsUnpaired(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ServerURL != "" {
		t.Fatalf("default server_url = %q, want empty", cfg.ServerURL)
	}
	if cfg.BridgeID != "" || cfg.Token != "" {
		t.Fatalf("default credentials are not empty: bridge_id=%q token=%q", cfg.BridgeID, cfg.Token)
	}
	if cfg.AdminPort != 9202 {
		t.Fatalf("default admin_port = %d, want 9202", cfg.AdminPort)
	}
}

func TestLoadAndSaveConfigPreservesRemoteContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENT_BRIDGE_SERVER_URL", "")

	want := &Config{
		BridgeID:  "bridge-1",
		Token:     "token-1",
		ServerURL: "wss://bridge.example.com/ws",
		AdminPort: 9202,
	}
	if err := SaveConfig(want); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.BridgeID != want.BridgeID || got.Token != want.Token || got.ServerURL != want.ServerURL {
		t.Fatalf("remote config changed: got %+v want %+v", got, want)
	}

	data, err := os.ReadFile(filepath.Join(home, DefaultConfigDir, DefaultConfigFile))
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("saved config is not JSON: %v", err)
	}
	for _, key := range []string{"bridge_id", "token", "server_url"} {
		if _, ok := wire[key]; !ok {
			t.Fatalf("saved config is missing %q: %s", key, data)
		}
	}
	if runtime.GOOS != "windows" {
		assertPermission(t, filepath.Join(home, LocalDataDir), 0o700)
		assertPermission(t, filepath.Join(home, DefaultConfigDir), 0o700)
		assertPermission(t, filepath.Join(home, DefaultConfigDir, DefaultConfigFile), 0o600)
	}
}

func TestConfigHasRemoteConnectionOnlyWithURLAndBridgeID(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "empty", cfg: Config{}, want: false},
		{name: "url only", cfg: Config{ServerURL: "wss://example.com/ws"}, want: false},
		{name: "bridge only", cfg: Config{BridgeID: "bridge-1"}, want: false},
		{name: "legacy tokenless connection", cfg: Config{ServerURL: "wss://example.com/ws", BridgeID: "bridge-1"}, want: true},
		{name: "paired", cfg: Config{ServerURL: "wss://example.com/ws", BridgeID: "bridge-1", Token: "secret"}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.cfg.HasRemoteConnection(); got != test.want {
				t.Fatalf("HasRemoteConnection() = %v, want %v", got, test.want)
			}
		})
	}
}
