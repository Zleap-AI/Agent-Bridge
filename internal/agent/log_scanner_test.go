package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexScannerFindsNestedNativeSessionsFromUserHome(t *testing.T) {
	home := t.TempDir()
	sessionsDir := filepath.Join(home, ".codex", "sessions")

	wantID := "019f322b-6ad9-7b31-94a5-29ede7d2f87f"
	path := filepath.Join(
		sessionsDir,
		"2026",
		"07",
		"05",
		"rollout-2026-07-05T20-05-34-"+wantID+".jsonl",
	)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// 写入有效的 session_meta JSONL（CodexScanner 要求至少能解析 session_meta）
	metaLine := `{"timestamp":"2026-07-05T12:05:34Z","type":"session_meta","payload":{"session_id":"` + wantID + `","id":"` + wantID + `","timestamp":"2026-07-05T12:05:34Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(metaLine), 0o600); err != nil {
		t.Fatal(err)
	}

	scanner := NewCodexScanner(sessionsDir)
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
