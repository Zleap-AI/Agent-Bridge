package infra

import (
	"path/filepath"
	"testing"
)

func TestCanReplacePortOwnerOnlyForSameExecutable(t *testing.T) {
	sameExecutable := filepath.Join(t.TempDir(), "agent-bridge")
	if !filepath.IsAbs(sameExecutable) {
		t.Fatalf("test executable path is not absolute: %q", sameExecutable)
	}
	tests := []struct {
		name    string
		owner   string
		current string
		want    bool
	}{
		{name: "same executable", owner: sameExecutable, current: sameExecutable, want: true},
		{name: "same published name at another path", owner: "/usr/local/bin/agent-bridge_v0.4.0_linux_amd64", current: "/tmp/agent-bridge_v0.4.0_linux_amd64", want: false},
		{name: "same generic name at another path", owner: "/usr/local/bin/agent-bridge", current: "/tmp/agent-bridge", want: false},
		{name: "windows local binary at another path", owner: `C:\\Users\\me\\agent-bridge.exe`, current: `C:\\Temp\\agent-bridge.exe`, want: false},
		{name: "server must never be killed", owner: "/usr/local/bin/agent-bridge-server", current: "/tmp/agent-bridge", want: false},
		{name: "unrelated node process", owner: "/usr/local/bin/node", current: "/tmp/agent-bridge", want: false},
		{name: "name only appears in arguments", owner: "/usr/bin/python agent-bridge.py", current: "/tmp/agent-bridge", want: false},
		{name: "generic bridge binary", owner: "/usr/local/bin/bridge", current: "/tmp/bridge", want: false},
		{name: "missing owner", owner: "", current: "/tmp/agent-bridge", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := canReplacePortOwner(test.owner, test.current); got != test.want {
				t.Fatalf("canReplacePortOwner(%q, %q) = %v, want %v", test.owner, test.current, got, test.want)
			}
		})
	}
}
