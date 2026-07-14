package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexScannerFindsNestedNativeSessionsFromUserHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	wantID := "019f322b-6ad9-7b31-94a5-29ede7d2f87f"
	path := filepath.Join(
		home,
		".codex",
		"sessions",
		"2026",
		"07",
		"05",
		"rollout-2026-07-05T20-05-34-"+wantID+".jsonl",
	)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	scanner := ScannerFromAgentID("codex")
	if scanner == nil {
		t.Fatal("ScannerFromAgentID(codex) returned nil")
	}
	sessions, err := scanner.DiscoverSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("discovered sessions = %+v, want one nested Codex session", sessions)
	}
	if sessions[0].NativeID != wantID {
		t.Fatalf("native Session ID = %q, want %q", sessions[0].NativeID, wantID)
	}
}
