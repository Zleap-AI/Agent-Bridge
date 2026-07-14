package infra

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInitLoggerWritesInfoFileByDefaultAndRedactsSensitiveFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := InitLogger(false); err != nil {
		t.Fatalf("InitLogger: %v", err)
	}
	slog.Info("default logger test", "token", "do-not-log-this-token", "message", "do-not-log-this-message")
	slog.Debug("debug entry must stay hidden")

	data, err := os.ReadFile(getLogFilePath())
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "default logger test") {
		t.Fatalf("INFO entry missing from log: %s", text)
	}
	if strings.Contains(text, "debug entry must stay hidden") {
		t.Fatalf("DEBUG entry written with debug disabled: %s", text)
	}
	if strings.Contains(text, "do-not-log-this-token") || strings.Contains(text, "do-not-log-this-message") {
		t.Fatalf("sensitive field was written to log: %s", text)
	}
	if runtime.GOOS != "windows" {
		assertPermission(t, filepath.Dir(getLogFilePath()), 0o700)
		assertPermission(t, getLogFilePath(), 0o600)
	}
}

func TestInitLoggerWritesDebugFileWhenEnabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := InitLogger(true); err != nil {
		t.Fatalf("InitLogger: %v", err)
	}
	slog.Debug("debug logger test")

	data, err := os.ReadFile(getLogFilePath())
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), "debug logger test") {
		t.Fatalf("DEBUG entry missing from log: %s", data)
	}
}
