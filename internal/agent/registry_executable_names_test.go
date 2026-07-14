package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExecutableCandidateNamesSkipsExtensionlessWindowsShim(t *testing.T) {
	got := executableCandidateNames("codex-acp", []string{".exe", ".cmd", ".bat", ".com"})
	want := []string{"codex-acp.exe", "codex-acp.cmd", "codex-acp.bat", "codex-acp.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Windows executable candidates = %#v, want %#v", got, want)
	}
}

func TestExecutableCandidateNamesKeepsExplicitWindowsExtension(t *testing.T) {
	got := executableCandidateNames("npm.CMD", []string{".exe", ".cmd", ".bat"})
	want := []string{"npm.CMD"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit executable candidates = %#v, want %#v", got, want)
	}
}

func TestExecutableCandidateNamesKeepsUnixName(t *testing.T) {
	got := executableCandidateNames("opencode", []string{""})
	want := []string{"opencode"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Unix executable candidates = %#v, want %#v", got, want)
	}
}

func TestFindExecutableWithWindowsExtensionsIgnoresUnixShim(t *testing.T) {
	dir := t.TempDir()
	unixShim := filepath.Join(dir, "codex-acp")
	windowsShim := filepath.Join(dir, "codex-acp.cmd")
	if err := os.WriteFile(unixShim, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write Unix shim: %v", err)
	}
	if err := os.WriteFile(windowsShim, []byte("@echo off\r\n"), 0o644); err != nil {
		t.Fatalf("write Windows shim: %v", err)
	}

	got := findExecutableWithExtensions("codex-acp", []string{dir}, []string{".exe", ".cmd", ".bat"})
	if got != windowsShim {
		t.Fatalf("selected executable = %q, want Windows shim %q", got, windowsShim)
	}
}

func TestFindExecutableRejectsNonExecutableUnixFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-agent-acp")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write non-executable wrapper: %v", err)
	}

	if got := findExecutableWithExtensions("claude-agent-acp", []string{dir}, []string{""}); got != "" {
		t.Fatalf("selected non-executable Unix wrapper %q", got)
	}
}
