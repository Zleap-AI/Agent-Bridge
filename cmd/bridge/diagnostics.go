// -*- coding: utf-8 -*-
// Go 1.25+
//
// diagnostics.go
// 环境诊断 API — 检查运行时、Agent 安装状态、PATH、npm 全局包
//
// Lzm 2026-07-20

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
)

// ─── 诊断响应数据结构 ──────────────────────────────────────────────────────

// RuntimeInfo 运行时环境诊断信息
// Lzm 2026-07-20
type RuntimeInfo struct {
	Name    string  `json:"name"`
	Command string  `json:"command"`
	Found   bool    `json:"found"`
	Path    *string `json:"path"`
	Version *string `json:"version"`
}

// ConfigDirInfo 配置目录诊断信息
// Lzm 2026-07-20
type ConfigDirInfo struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

// EnvKeyInfo 环境变量诊断信息
// Lzm 2026-07-20
type EnvKeyInfo struct {
	Key  string `json:"key"`
	Set  bool   `json:"set"`
}

// AgentDiagInfo Agent 诊断信息
// Lzm 2026-07-20
type AgentDiagInfo struct {
	ID           string         `json:"id"`
	Display      string         `json:"display"`
	Installed    bool           `json:"installed"`
	ACPAvailable bool           `json:"acp_available"`
	Path         *string        `json:"path"`
	ConfigDirs   []ConfigDirInfo `json:"config_dirs"`
	EnvKeys      []EnvKeyInfo    `json:"env_keys"`
	BridgeStatus string         `json:"bridge_status"`
}

// NPMPkgInfo npm 全局包信息
// Lzm 2026-07-20
type NPMPkgInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// PathInfo PATH 环境变量诊断信息
// Lzm 2026-07-20
type PathInfo struct {
	Count          int    `json:"count"`
	HasNodeModules bool   `json:"has_node_modules"`
}

// DiagnosticsResponse 诊断 API 响应
// Lzm 2026-07-20
type DiagnosticsResponse struct {
	Runtime        []RuntimeInfo  `json:"runtime"`
	Agents         []AgentDiagInfo `json:"agents"`
	Path           PathInfo       `json:"path"`
	NPMGlobalAgents []NPMPkgInfo  `json:"npm_global_agents"`
}

// ─── Agent 定义（与 registry.go / check_env.py 保持一致） ──────────────────

// agentDef 描述待诊断的 Agent 静态信息
// Lzm 2026-07-20
type agentDef struct {
	ID         string
	Display    string
	Cmds       []string
	CheckACP   bool
	ACPCmd     []string
	ConfigDirs []string
	EnvKeys    []string
}

var diagnosticAgents = []agentDef{
	{ID: "claude-code", Display: "Claude Code", Cmds: []string{"claude-agent-acp", "claude"}, CheckACP: true, ACPCmd: []string{"claude-agent-acp"}, ConfigDirs: []string{"~/.claude"}, EnvKeys: []string{"ANTHROPIC_API_KEY"}},
	{ID: "opencode", Display: "OpenCode", Cmds: []string{"opencode"}, CheckACP: true, ACPCmd: []string{"opencode", "acp"}, ConfigDirs: []string{"~/.config/opencode", "~/.local/share/opencode"}, EnvKeys: []string{"OPENAI_API_KEY"}},
	{ID: "codex", Display: "Codex", Cmds: []string{"codex-acp", "codex"}, CheckACP: true, ACPCmd: []string{"codex-acp"}, ConfigDirs: []string{"~/.codex"}, EnvKeys: []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"}},
	{ID: "hermes", Display: "Hermes", Cmds: []string{"hermes"}, CheckACP: true, ACPCmd: []string{"hermes", "acp"}, ConfigDirs: []string{"~/.hermes", "~/.local/share/hermes"}, EnvKeys: []string{"ANTHROPIC_API_KEY"}},
	{ID: "kimi", Display: "Kimi", Cmds: []string{"kimi"}, CheckACP: true, ACPCmd: []string{"kimi", "acp"}, ConfigDirs: []string{"~/.kimi-code"}, EnvKeys: []string{"MOONSHOT_API_KEY"}},
	{ID: "gemini", Display: "Gemini", Cmds: []string{"gemini"}, CheckACP: true, ACPCmd: []string{"gemini", "--experimental-acp"}, ConfigDirs: []string{"~/.gemini"}, EnvKeys: []string{"GEMINI_API_KEY"}},
	{ID: "copilot", Display: "GitHub Copilot", Cmds: []string{"copilot"}, CheckACP: true, ACPCmd: []string{"copilot", "--acp"}, ConfigDirs: []string{"~/.copilot"}, EnvKeys: []string{}},
	{ID: "pi", Display: "Pi", Cmds: []string{"pi-acp", "pi"}, CheckACP: true, ACPCmd: []string{"pi-acp"}, ConfigDirs: []string{"~/.pi"}, EnvKeys: []string{}},
	{ID: "cursor", Display: "Cursor", Cmds: []string{"agent"}, CheckACP: true, ACPCmd: []string{"agent", "acp"}, ConfigDirs: []string{"~/.cursor"}, EnvKeys: []string{"CURSOR_API_KEY"}},
	{ID: "glm", Display: "GLM", Cmds: []string{"glm-acp-agent"}, CheckACP: true, ACPCmd: []string{"glm-acp-agent"}, ConfigDirs: []string{"~/.local/state/glm-acp-agent"}, EnvKeys: []string{"Z_AI_API_KEY"}},
	{ID: "openclaw", Display: "OpenClaw", Cmds: []string{"openclaw"}, CheckACP: true, ACPCmd: []string{"openclaw", "acp"}, ConfigDirs: []string{"~/.openclaw"}, EnvKeys: []string{}},
}

// ─── 运行时环境诊断 ──────────────────────────────────────────────────────

// runtimeChecks 定义需要检查的运行时环境和对应命令
// Lzm 2026-07-20
var runtimeChecks = []struct {
	Name    string
	Command string
	Args    []string
}{
	{"Node.js", "node", []string{"--version"}},
	{"npm", "npm", []string{"--version"}},
	{"Python 3", "python3", []string{"--version"}},
	{"Go", "go", []string{"version"}},
	{"Git", "git", []string{"--version"}},
}

// diagnoseRuntime 检查运行时环境（node/npm/python/go/git）是否安装并获取版本
// Lzm 2026-07-20
func diagnoseRuntime() []RuntimeInfo {
	results := make([]RuntimeInfo, 0, len(runtimeChecks))
	for _, check := range runtimeChecks {
		cmd := check.Command
		// Windows 上没有 python3，用 python
		if check.Command == "python3" && runtime.GOOS == "windows" {
			cmd = "python"
		}
		exePath := findCmd(cmd)
		if exePath == "" {
			results = append(results, RuntimeInfo{
				Name:    check.Name,
				Command: check.Command,
				Found:   false,
				Path:    nil,
				Version: nil,
			})
			continue
		}
		ver := runCmdOutput(exePath, check.Args...)
		results = append(results, RuntimeInfo{
			Name:    check.Name,
			Command: check.Command,
			Found:   true,
			Path:    strPtr(exePath),
			Version: strPtr(ver),
		})
	}
	return results
}

// ─── Agent 诊断 ──────────────────────────────────────────────────────────

// diagnoseAgents 遍历所有 Agent，检查安装状态、ACP 可用性、配置目录和环境变量
// Lzm 2026-07-20
func diagnoseAgents(registry *agent.AgentRegistry) []AgentDiagInfo {
	results := make([]AgentDiagInfo, 0, len(diagnosticAgents))
	for _, def := range diagnosticAgents {
		// 1. 检查命令是否存在
		var primaryPath string
		for _, cmd := range def.Cmds {
			if p := findCmd(cmd); p != "" {
				primaryPath = p
				break
			}
		}
		installed := primaryPath != ""

		// 2. 检查 ACP 可用性
		acpAvailable := false
		if installed && def.CheckACP && len(def.ACPCmd) > 0 {
			acpPath := findCmd(def.ACPCmd[0])
			if acpPath == "" {
				acpPath = primaryPath
			}
			// 对 .cmd/.bat 文件跳过 --help 检测（Windows .cmd 文件可能挂死）
			if runtime.GOOS == "windows" && (strings.HasSuffix(strings.ToLower(acpPath), ".cmd") || strings.HasSuffix(strings.ToLower(acpPath), ".bat")) {
				acpAvailable = true
			} else {
				args := def.ACPCmd[1:]
				acpAvailable = runCmdCheck(acpPath, args...)
			}
		}

		// 3. 检查配置目录
		configDirs := make([]ConfigDirInfo, 0, len(def.ConfigDirs))
		for _, d := range def.ConfigDirs {
			expanded := expandHome(d)
			_, err := os.Stat(expanded)
			configDirs = append(configDirs, ConfigDirInfo{
				Path:   d,
				Exists: err == nil,
			})
		}

		// 4. 检查环境变量
		envKeys := make([]EnvKeyInfo, 0, len(def.EnvKeys))
		for _, k := range def.EnvKeys {
			set := checkEnvKey(k)
			envKeys = append(envKeys, EnvKeyInfo{
				Key: k,
				Set: set,
			})
		}

		// 5. 从注册表获取桥接状态
		bridgeStatus := "unknown"
		if registry != nil {
			if a := registry.Get(def.ID); a != nil {
				bridgeStatus = a.Status().String()
			}
		}

		var pathStr *string
		if primaryPath != "" {
			pathStr = strPtr(primaryPath)
		}

		results = append(results, AgentDiagInfo{
			ID:           def.ID,
			Display:      def.Display,
			Installed:    installed,
			ACPAvailable: acpAvailable,
			Path:         pathStr,
			ConfigDirs:   configDirs,
			EnvKeys:      envKeys,
			BridgeStatus: bridgeStatus,
		})
	}
	return results
}

// ─── 辅助函数 ────────────────────────────────────────────────────────────

// checkEnvKey 检查环境变量是否存在
// Lzm 2026-07-22
func checkEnvKey(name string) bool {
	return os.Getenv(name) != ""
}

// expandHome 展开 ~ 为用户 home 目录
// Lzm 2026-07-20
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	return path
}

// findCmd 在 PATH 中查找命令
// Lzm 2026-07-20
func findCmd(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
}

// runCmdOutput 运行命令并返回 stdout 内容（超时 5 秒）
// Lzm 2026-07-20
func runCmdOutput(cmd string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c := exec.CommandContext(ctx, cmd, args...)
	out, err := c.Output()
	if err != nil {
		// 尝试读取 stderr
		if exitErr, ok := err.(*exec.ExitError); ok {
			if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
				return stderr
			}
		}
		return ""
	}
	return strings.TrimSpace(string(out))
}

// runCmdCheck 运行命令并检查是否正常响应（用于 ACP 检测）
// Lzm 2026-07-20
func runCmdCheck(cmd string, args ...string) bool {
	// 尝试 --help 和 -h
	for _, flag := range []string{"--help", "-h"} {
		allArgs := append([]string{flag}, args...)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		c := exec.CommandContext(ctx, cmd, allArgs...)
		out, err := c.Output()
		if err == nil && len(out) > 0 {
			return true
		}
		// 部分命令输出到 stderr
		if exitErr, ok := err.(*exec.ExitError); ok {
			if len(exitErr.Stderr) > 0 {
				return true
			}
		}
	}
	return false
}

// diagnoseNPMGlobal 检查 npm 全局安装的相关 Agent 包
// Lzm 2026-07-20
func diagnoseNPMGlobal() []NPMPkgInfo {
	npmPath := findCmd("npm")
	if npmPath == "" {
		return []NPMPkgInfo{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c := exec.CommandContext(ctx, npmPath, "list", "-g", "--depth=0", "--json")
	out, err := c.Output()
	if err != nil {
		return []NPMPkgInfo{}
	}

	var result struct {
		Dependencies map[string]struct {
			Version string `json:"version"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return []NPMPkgInfo{}
	}

	relevantPackages := map[string]bool{
		"pi-acp":                              true,
		"@earendil-works/pi-coding-agent":     true,
		"openclaw":                            true,
		"@github/copilot-cli":                 true,
		"@agentclientprotocol/codex-acp":      true,
		"glm-acp-agent":                       true,
	}

	var packages []NPMPkgInfo
	for name, info := range result.Dependencies {
		if relevantPackages[name] {
			ver := info.Version
			if ver == "" {
				ver = "unknown"
			}
			packages = append(packages, NPMPkgInfo{
				Name:    name,
				Version: ver,
			})
		}
	}
	return packages
}

// diagnosePath 检查 PATH 环境变量
// Lzm 2026-07-20
func diagnosePath() PathInfo {
	pathStr := os.Getenv("PATH")
	separator := ":"
	if runtime.GOOS == "windows" {
		separator = ";"
	}
	paths := strings.Split(pathStr, separator)
	count := 0
	hasNodeModules := false
	for _, p := range paths {
		if p = strings.TrimSpace(p); p != "" {
			count++
			if strings.Contains(p, "node_modules") {
				hasNodeModules = true
			}
		}
	}
	return PathInfo{
		Count:          count,
		HasNodeModules: hasNodeModules,
	}
}

// strPtr 返回字符串指针
// Lzm 2026-07-20
func strPtr(s string) *string {
	return &s
}

// ─── HTTP Handler ───────────────────────────────────────────────────────

// handleDiagnostics 处理诊断请求
// GET /api/v1/local/diagnostics
// Lzm 2026-07-20
func (app *localApplication) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}

	slog.Debug("开始环境诊断")
	runtime := diagnoseRuntime()
	agents := diagnoseAgents(app.registry)
	path := diagnosePath()
	npmPkgs := diagnoseNPMGlobal()

	response := DiagnosticsResponse{
		Runtime:         runtime,
		Agents:          agents,
		Path:            path,
		NPMGlobalAgents: npmPkgs,
	}

	writeJSON(w, http.StatusOK, response)
	slog.Debug("环境诊断完成")
}
