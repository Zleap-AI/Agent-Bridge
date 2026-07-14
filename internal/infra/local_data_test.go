package infra

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSecureLocalDataPermissionsMigratesExistingTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows permissions are enforced by the user profile ACL")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, LocalDataDir)
	nested := filepath.Join(root, "agents", "codex", "messages")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(nested, "session.json")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "npm", "bin", "agent-wrapper")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SecureLocalDataPermissions(); err != nil {
		t.Fatal(err)
	}
	assertPermission(t, root, 0o700)
	assertPermission(t, nested, 0o700)
	assertPermission(t, secret, 0o600)
	assertPermission(t, executable, 0o700)
}

func assertPermission(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s permissions = %04o, want %04o", path, got, want)
	}
}
