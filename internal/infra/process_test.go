package infra

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildProcessPathPrioritizesCommandAndExtraDirectories(t *testing.T) {
	root := t.TempDir()
	commandDir := filepath.Join(root, "command")
	runtimeDir := filepath.Join(root, "runtime")
	systemDir := filepath.Join(root, "system")

	got := filepath.SplitList(buildProcessPath(
		filepath.Join(commandDir, "agent"),
		[]string{runtimeDir, commandDir},
		strings.Join([]string{systemDir, runtimeDir}, string(os.PathListSeparator)),
	))
	want := []string{commandDir, runtimeDir, systemDir}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("process PATH = %#v, want %#v", got, want)
	}
}

func TestMergeEnvironmentOverridesWithoutDuplicateKeys(t *testing.T) {
	got := mergeEnvironment(
		[]string{"HOME=/home/test", "PATH=/old", "EMPTY="},
		map[string]string{"PATH": "/new", "TOKEN": "secret"},
	)

	want := map[string]string{
		"EMPTY": "",
		"HOME":  "/home/test",
		"PATH":  "/new",
		"TOKEN": "secret",
	}
	if !reflect.DeepEqual(environmentMap(got), want) {
		t.Fatalf("merged environment = %#v, want %#v", environmentMap(got), want)
	}
}

func TestProcessEnvironmentExtendsConfiguredPath(t *testing.T) {
	root := t.TempDir()
	commandDir := filepath.Join(root, "command")
	configuredDir := filepath.Join(root, "configured")
	got := environmentMap(processEnvironment([]string{"PATH=/parent"}, StartProcessConfig{
		Command: filepath.Join(commandDir, "agent"),
		Env: map[string]string{
			"PATH": configuredDir,
		},
	}))

	wantPath := strings.Join([]string{commandDir, configuredDir}, string(os.PathListSeparator))
	if got["PATH"] != wantPath {
		t.Fatalf("process PATH = %q, want %q", got["PATH"], wantPath)
	}
}

func environmentMap(entries []string) map[string]string {
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}
