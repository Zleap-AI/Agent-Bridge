//go:build darwin || linux

package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/infra"
)

func TestDiscoveredShebangCLIStartsWithMinimalServicePath(t *testing.T) {
	homeDir := t.TempDir()
	cliDir := t.TempDir()
	runtimeDir := filepath.Join(homeDir, ".nvm", "versions", "node", "v22.0.0", "bin")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("create runtime directory: %v", err)
	}

	writeExecutable(t, filepath.Join(runtimeDir, "node"), "#!/bin/sh\nprintf 'nvm-node-ok\\n'\n")
	writeExecutable(t, filepath.Join(cliDir, "opencode"), "#!/usr/bin/env node\n")
	// Prevent system Codex/Claude installations from triggering network-backed
	// wrapper auto-install while this test exercises discovery.
	writeExecutable(t, filepath.Join(cliDir, "codex-acp"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(cliDir, "claude-agent-acp"), "#!/bin/sh\nexit 0\n")

	t.Setenv("HOME", homeDir)
	t.Setenv("PATH", cliDir)

	registry := NewAgentRegistry(AgentRegistryConfig{WorkDir: homeDir})
	if err := registry.Discover(); err != nil {
		t.Fatalf("discover agents: %v", err)
	}
	discovered := registry.Get("opencode")
	if discovered == nil {
		t.Fatal("opencode was not discovered")
	}
	meta := baseForTest(t, discovered).meta
	if !containsPath(meta.PathDirs, runtimeDir) {
		t.Fatalf("Agent PATH does not contain NVM runtime %q: %#v", runtimeDir, meta.PathDirs)
	}

	process, err := infra.StartProcess(context.Background(), infra.StartProcessConfig{
		Command:  meta.Cmd,
		WorkDir:  meta.WorkDir,
		Env:      meta.Env,
		PathDirs: meta.PathDirs,
	})
	if err != nil {
		t.Fatalf("start shebang CLI: %v", err)
	}
	output, err := io.ReadAll(process.Stdout())
	if err != nil {
		t.Fatalf("read shebang CLI output: %v", err)
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("wait for shebang CLI: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "nvm-node-ok" {
		t.Fatalf("shebang CLI output = %q, want %q", got, "nvm-node-ok")
	}
}

func TestNewestGlobMatchesUsesNumericVersionOrder(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{"v9.1.0", "v22.3.0", "v20.11.1"} {
		if err := os.MkdirAll(filepath.Join(root, version, "bin"), 0o755); err != nil {
			t.Fatalf("create version directory: %v", err)
		}
	}

	got := newestGlobMatches(filepath.Join(root, "*", "bin"))
	if len(got) != 3 {
		t.Fatalf("glob returned %d paths, want 3: %#v", len(got), got)
	}
	if version := filepath.Base(filepath.Dir(got[0])); version != "v22.3.0" {
		t.Fatalf("first runtime version = %q, want %q", version, "v22.3.0")
	}
}

func TestPrioritizeExecutionPathsMovesServicePathBehindRuntime(t *testing.T) {
	got := prioritizeExecutionPaths(
		[]string{"/usr/bin", "/custom/bin", "/bin"},
		[]string{"/runtime/bin", "/opt/homebrew/bin"},
	)
	want := []string{"/custom/bin", "/runtime/bin", "/opt/homebrew/bin", "/usr/bin", "/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("execution paths = %#v, want %#v", got, want)
	}
}

func TestGetNPMGlobalRootUsesDiscoveredNodeRuntime(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("PATH", "/usr/bin:/bin")
	writeExecutable(t, filepath.Join(runtimeDir, "node"), `#!/bin/sh
script="$1"
shift
exec /bin/sh "$script" "$@"
`)
	writeExecutable(t, filepath.Join(runtimeDir, "npm"), `#!/usr/bin/env node
[ "$1" = "root" ] && [ "$2" = "-g" ] || exit 20
printf '/example/global/node_modules\n'
`)

	got := getNPMGlobalRoot([]string{runtimeDir}, []string{"/usr/bin", "/bin"})
	if got != "/example/global/node_modules" {
		t.Fatalf("npm global root = %q, want %q", got, "/example/global/node_modules")
	}
}

func TestAutoInstallNPMWrapperUsesDiscoveredNodeAndApplicationPath(t *testing.T) {
	homeDir := t.TempDir()
	runtimeDir := t.TempDir()
	applicationDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("PATH", "/usr/bin:/bin")

	writeExecutable(t, filepath.Join(runtimeDir, "node"), `#!/bin/sh
script="$1"
shift
exec /bin/sh "$script" "$@"
`)
	writeExecutable(t, filepath.Join(runtimeDir, "npm"), `#!/usr/bin/env node
prefix=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --prefix)
      prefix="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
[ -n "$prefix" ] || exit 20
mkdir -p "$prefix/bin"
printf '#!/bin/sh\nexit 0\n' > "$prefix/bin/test-acp"
chmod 644 "$prefix/bin/test-acp"
printf '%s' "$PATH" > "$prefix/install-path.txt"
`)

	searchPaths := []string{runtimeDir}
	executionPaths := []string{applicationDir, "/usr/bin", "/bin"}
	installed := autoInstallNPMWrapper("test-acp", "example/test-acp", &searchPaths, executionPaths)
	prefix := filepath.Join(homeDir, ".agent-bridge", "npm")
	wantInstalled := filepath.Join(prefix, "bin", "test-acp")
	if installed != wantInstalled {
		t.Fatalf("installed wrapper = %q, want %q", installed, wantInstalled)
	}
	prefixInfo, err := os.Stat(prefix)
	if err != nil {
		t.Fatalf("stat npm wrapper prefix: %v", err)
	}
	if prefixInfo.Mode().Perm() != 0o700 {
		t.Fatalf("npm wrapper prefix mode = %v, want 0700", prefixInfo.Mode())
	}
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("stat installed wrapper: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installed wrapper mode = %v, want executable", info.Mode())
	}

	pathData, err := os.ReadFile(filepath.Join(prefix, "install-path.txt"))
	if err != nil {
		t.Fatalf("read npm child PATH: %v", err)
	}
	wantPath := []string{runtimeDir, applicationDir, "/usr/bin", "/bin"}
	if got := filepath.SplitList(string(pathData)); !reflect.DeepEqual(got, wantPath) {
		t.Fatalf("npm child PATH = %#v, want %#v", got, wantPath)
	}
}

func TestRunNPMInstallHonorsTimeout(t *testing.T) {
	npmPath := filepath.Join(t.TempDir(), "npm")
	writeExecutable(t, npmPath, "#!/bin/sh\nsleep 5\n")

	started := time.Now()
	_, err := runNPMInstall(npmPath, t.TempDir(), "example/test-acp", []string{"/usr/bin", "/bin"}, 25*time.Millisecond)
	if err != context.DeadlineExceeded {
		t.Fatalf("runNPMInstall() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("timed out npm install returned after %s, want under 2s", elapsed)
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}
