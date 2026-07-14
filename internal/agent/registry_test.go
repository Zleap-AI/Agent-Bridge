package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestEmptyAgentDescriptorsEncodeAsArray(t *testing.T) {
	registry := NewAgentRegistry(AgentRegistryConfig{})
	encoded, err := json.Marshal(registry.ListDescriptors())
	if err != nil {
		t.Fatalf("marshal empty descriptors: %v", err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("empty descriptors JSON = %s, want []", encoded)
	}
}

func TestRegistryListsAgentsInStableIDOrder(t *testing.T) {
	registry := NewAgentRegistry(AgentRegistryConfig{})
	registry.Register(NewCodexAgent(AgentMeta{ID: "z-agent", DisplayName: "Z Agent"}))
	registry.Register(NewCodexAgent(AgentMeta{ID: "a-agent", DisplayName: "A Agent"}))

	if got, want := registry.IDs(), []string{"a-agent", "z-agent"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("IDs() = %v, want %v", got, want)
	}
	if got, want := registry.ListAgentIDs(), []string{"a-agent", "z-agent"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ListAgentIDs() = %v, want %v", got, want)
	}
	if got := []string{registry.List()[0].ID(), registry.List()[1].ID()}; !reflect.DeepEqual(got, []string{"a-agent", "z-agent"}) {
		t.Fatalf("List() order = %v", got)
	}
	if got := []string{registry.ListDescriptors()[0].AgentID, registry.ListDescriptors()[1].AgentID}; !reflect.DeepEqual(got, []string{"a-agent", "z-agent"}) {
		t.Fatalf("ListDescriptors() order = %v", got)
	}
}

func TestDiscoverRegistersSupportedAgentCommands(t *testing.T) {
	binDir := t.TempDir()
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("PATH", binDir)

	for _, name := range []string{
		"claude-agent-acp",
		"opencode",
		"codex-acp",
		"hermes",
		"kimi",
		"gemini",
		"copilot",
		"pi-acp",
		"agent",
		"glm-acp-agent",
		"openclaw",
	} {
		path := fakeExecutablePath(t, binDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("create fake executable %s: %v", name, err)
		}
	}

	registry := NewAgentRegistry(AgentRegistryConfig{WorkDir: homeDir})
	if err := registry.Discover(); err != nil {
		t.Fatalf("discover agents: %v", err)
	}

	type wantAgent struct {
		name string
		cmd  string
		args []string
	}
	want := map[string]wantAgent{
		"claude-code": {name: "Claude Code", cmd: "claude-agent-acp"},
		"opencode":    {name: "OpenCode", cmd: "opencode", args: []string{"acp"}},
		"codex":       {name: "Codex CLI", cmd: "codex-acp"},
		"hermes":      {name: "Hermes Agent", cmd: "hermes", args: []string{"acp"}},
		"kimi":        {name: "Kimi", cmd: "kimi", args: []string{"acp"}},
		"gemini":      {name: "Gemini CLI", cmd: "gemini", args: []string{"--experimental-acp"}},
		"copilot":     {name: "GitHub Copilot", cmd: "copilot", args: []string{"--acp"}},
		"pi":          {name: "pi", cmd: "pi-acp"},
		"cursor":      {name: "Cursor", cmd: "agent", args: []string{"acp"}},
		"glm":         {name: "GLM Agent", cmd: "glm-acp-agent"},
		"openclaw":    {name: "OpenClaw", cmd: "openclaw", args: []string{"acp"}},
	}

	if len(registry.agents) != len(want) {
		ids := registry.ListAgentIDs()
		sort.Strings(ids)
		t.Fatalf("discovered %d agents, want %d: %v", len(registry.agents), len(want), ids)
	}

	for id, expected := range want {
		discovered, ok := registry.agents[id]
		if !ok {
			t.Errorf("agent %q was not discovered", id)
			continue
		}
		base := baseForTest(t, discovered)
		if discovered.ID() != id {
			t.Errorf("agent %q ID = %q", id, discovered.ID())
		}
		if discovered.DisplayName() != expected.name {
			t.Errorf("agent %q display name = %q, want %q", id, discovered.DisplayName(), expected.name)
		}
		if !containsString(executableCandidateNames(expected.cmd, getExecutableExtensions()), filepath.Base(base.meta.Cmd)) {
			t.Errorf("agent %q command = %q, want %q", id, base.meta.Cmd, expected.cmd)
		}
		if !reflect.DeepEqual(base.meta.Args, expected.args) {
			t.Errorf("agent %q args = %#v, want %#v", id, base.meta.Args, expected.args)
		}
	}
}

func TestFindExecutablePreservesSearchPathOrder(t *testing.T) {
	preferredDir := t.TempDir()
	fallbackDir := t.TempDir()
	want := fakeExecutablePath(t, preferredDir, "node")
	for _, dir := range []string{preferredDir, fallbackDir} {
		if err := os.WriteFile(fakeExecutablePath(t, dir, "node"), []byte("runtime"), 0o755); err != nil {
			t.Fatalf("create runtime in %s: %v", dir, err)
		}
	}

	for attempt := 0; attempt < 100; attempt++ {
		if got := findExecutable("node", []string{preferredDir, fallbackDir}); got != want {
			t.Fatalf("findExecutable() = %q, want ordered first match %q", got, want)
		}
	}
}

func fakeExecutablePath(t *testing.T, dir, name string) string {
	t.Helper()
	candidates := executableCandidateNames(name, getExecutableExtensions())
	if len(candidates) == 0 {
		t.Fatalf("no executable candidate for %q", name)
	}
	return filepath.Join(dir, candidates[0])
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestNPMWrapperInstallBudgetFitsLocalInstallerWindow(t *testing.T) {
	const (
		maximumWrapperInstalls = 2 // Codex and Claude wrappers may both be missing.
		localStartupDelay      = 2 * time.Second
		installerHealthWindow  = 120 * time.Second
		minimumHealthMargin    = 30 * time.Second
	)

	discoveryBudget := npmGlobalRootQueryTimeout + npmWrapperInstallWaitDelay +
		maximumWrapperInstalls*(npmWrapperInstallTimeout+npmWrapperInstallWaitDelay) + localStartupDelay
	if margin := installerHealthWindow - discoveryBudget; margin < minimumHealthMargin {
		t.Fatalf("startup health margin = %s, want at least %s", margin, minimumHealthMargin)
	}
}

func baseForTest(t *testing.T, discovered Agent) *baseAgent {
	t.Helper()
	switch typed := discovered.(type) {
	case *ClaudeCodeAgent:
		return typed.baseAgent
	case *OpenCodeAgent:
		return typed.baseAgent
	case *CodexAgent:
		return typed.baseAgent
	case *HermesAgent:
		return typed.baseAgent
	case *KimiAgent:
		return typed.baseAgent
	case *GeminiAgent:
		return typed.baseAgent
	case *CopilotAgent:
		return typed.baseAgent
	case *PiAgent:
		return typed.baseAgent
	case *CursorAgent:
		return typed.baseAgent
	case *GlmAgent:
		return typed.baseAgent
	case *OpenClawAgent:
		return typed.baseAgent
	default:
		t.Fatalf("unsupported discovered agent type %T", discovered)
		return nil
	}
}
