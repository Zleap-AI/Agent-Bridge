// -*- coding: utf-8 -*-
// Go 1.25+
//
// registry.go
// Agent 注册表 — 检测系统 Agent、管理连接生命周期
//
// Lzm 2026-07-09

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/infra"
)

const (
	npmGlobalRootQueryTimeout  = 5 * time.Second
	npmWrapperInstallTimeout   = 30 * time.Second
	npmWrapperInstallWaitDelay = 500 * time.Millisecond
)

// AgentRegistry 管理所有 Agent 实例
type AgentRegistry struct {
	agents map[string]Agent
	cfg    AgentRegistryConfig
}

// AgentRegistryConfig 注册表配置
type AgentRegistryConfig struct {
	// BridgeID Bridge 标识
	BridgeID string
	// ClaudeSettingsFile Claude 配置文件路径（空则用默认）
	ClaudeSettingsFile string
	// WorkDir 默认工作目录
	WorkDir string
}

// DefaultAgentRegistryConfig 返回默认配置
func DefaultAgentRegistryConfig() AgentRegistryConfig {
	home, _ := os.UserHomeDir()
	return AgentRegistryConfig{
		WorkDir: home,
	}
}

// NewAgentRegistry 创建 Agent 注册表
func NewAgentRegistry(cfg AgentRegistryConfig) *AgentRegistry {
	return &AgentRegistry{
		agents: make(map[string]Agent),
		cfg:    cfg,
	}
}

// AgentDescriptor Agent 注册描述信息（用于向远程服务注册）
type AgentDescriptor struct {
	AgentID     string `json:"agent_id"`
	DisplayName string `json:"display_name,omitempty"`
	Status      string `json:"status,omitempty"`
}

// ListDescriptors 返回所有 Agent 的描述列表（给远程服务注册用）
func (r *AgentRegistry) ListDescriptors() []AgentDescriptor {
	descriptors := make([]AgentDescriptor, 0, len(r.agents))
	for _, a := range r.List() {
		descriptors = append(descriptors, AgentDescriptor{
			AgentID:     a.ID(),
			DisplayName: a.DisplayName(),
			Status:      a.Status().String(),
		})
	}
	return descriptors
}

// ListAgentIDs 返回所有 Agent 的 ID 列表（给前端展示用）
// Lzm 2026-07-11
func (r *AgentRegistry) ListAgentIDs() []string {
	return r.IDs()
}

// --- 检测方法 ---

// Discover 扫描系统并注册所有可用的 Agent
// Lzm 2026-07-09
func (r *AgentRegistry) Discover() error {
	home, _ := os.UserHomeDir()
	workDir := r.cfg.WorkDir
	if workDir == "" {
		workDir = home
	}

	// 获取 PATH 中的目录
	parentPathDirs := filepath.SplitList(os.Getenv("PATH"))

	// 常见 npm 全局安装目录等（平台特有）
	extraPaths := getExtraSearchPaths()
	executionPaths := prioritizeExecutionPaths(parentPathDirs, extraPaths)

	// 搜索顺序与进程 PATH 一致，避免 map 遍历随机选择旧版本运行时。
	searchPaths := uniquePathDirs(executionPaths)
	if npmPrefix := npmWrapperPrefix(); npmPrefix != "" {
		searchPaths = appendUniquePathDirs(searchPaths, npmWrapperSearchPaths(npmPrefix)...)
	}

	// 获取当前目录（用于当前目录查找）
	cwd, _ := os.Getwd()
	searchPaths = appendUniquePathDirs(searchPaths, cwd)

	// --- 扫描 Agent 专属安装路径（不在 PATH 中的） ---

	// 1. npm 全局安装目录（codex-acp 等 wrapper 包安装在此）
	if npmRoot := getNPMGlobalRoot(searchPaths, executionPaths); npmRoot != "" {
		npmBinDir := filepath.Dir(npmRoot) // node_modules 的父目录
		searchPaths = appendUniquePathDirs(searchPaths, npmBinDir)
		// 部分 npm 版本将 .cmd 放在 node_modules/.bin/
		searchPaths = appendUniquePathDirs(searchPaths, filepath.Join(npmRoot, ".bin"))
		slog.Debug("添加 npm 全局目录", "bin", npmBinDir)
	}

	// 2. Codex 专属安装路径（平台特有）
	for _, codexDir := range getCodexCandidates() {
		searchPaths = appendUniquePathDirs(searchPaths, codexDir)
		slog.Debug("添加 Codex 搜索路径", "path", codexDir)
	}

	// 3. 检查 OpenCode 配置目录（判断是否曾经安装过）
	opencodeConfig := filepath.Join(home, ".config", "opencode", "opencode.json")
	if _, err := os.Stat(opencodeConfig); err == nil {
		slog.Info("检测到 OpenCode 配置文件，但可执行文件未找到",
			"path", opencodeConfig,
		)
		// 可尝试通过 npm/pip 自动安装
	}

	// 4. 检查 Hermes 配置目录（判断是否安装过 Hermes Agent）
	hermesConfig := filepath.Join(home, ".hermes", ".env")
	if _, err := os.Stat(hermesConfig); err == nil {
		slog.Info("检测到 Hermes 配置文件",
			"path", hermesConfig,
		)
	}

	// 4. 检查 Claude Code 配置目录
	claudeDir := filepath.Join(home, ".claude")
	if claudeEntries, err := os.ReadDir(claudeDir); err == nil && len(claudeEntries) > 0 {
		slog.Info("检测到 Claude Code 配置目录",
			"path", claudeDir,
		)
		// 检查是否存在 ACP 入口脚本
		for _, scriptName := range getClaudeScriptNames() {
			if fp := findExecutable(scriptName, searchPaths); fp != "" {
				slog.Info("Claude ACP 已可用", "path", fp)
			}
		}
	}

	// --- 候选 Agent 定义 ---
	type candidate struct {
		id          string
		displayName string
		cmd         string
		args        []string          // Agent 启动参数（如 Kimi 需 "acp" 子命令）
		env         map[string]string // 额外环境变量
		newAgent    func(meta AgentMeta) Agent
	}

	// --- 特殊处理 Codex: 优先使用 codex-acp ACP wrapper ---
	// codex.exe 检测 TTY（stdin is a terminal），拒绝在管道模式下运行。
	// codex-acp wrapper 绕过此限制，提供原生 ACP 协议支持。
	// 参考 Python 原型：ACP_AGENTS["codex"] = {wrapper_cmd: "codex-acp", wrapper_pkg: "@agentclientprotocol/codex-acp"}
	codexCmd := "codex"
	var codexArgs []string

	if acpPath := findExecutable("codex-acp", searchPaths); acpPath != "" {
		// codex-acp wrapper 已安装，优先使用
		codexCmd = acpPath
		codexArgs = nil
		slog.Info("Codex: 使用 ACP wrapper", "path", acpPath)
	} else if directPath := findExecutable("codex", searchPaths); directPath != "" {
		// codex.exe 存在但 codex-acp 未安装，自动安装
		slog.Info("Codex: ACP wrapper 未安装，尝试自动安装 @agentclientprotocol/codex-acp ...")
		if installedPath := autoInstallNPMWrapper("codex-acp", "@agentclientprotocol/codex-acp", &searchPaths, executionPaths); installedPath != "" {
			codexCmd = installedPath
			codexArgs = nil
			slog.Info("Codex: ACP wrapper 自动安装成功", "path", installedPath)
		} else {
			// 自动安装失败，使用原生 codex.exe（可能因 TTY 检测失败）
			codexCmd = directPath
			slog.Warn("Codex: ACP wrapper 安装失败，使用原生 codex.exe（可能因 TTY 检测无法启动）",
				"path", directPath,
			)
		}
	}

	piCmd := "pi-acp"
	var piArgs []string
	var piEnv map[string]string
	if runtime.GOOS == "windows" {
		piCmd = "node"
		piArgs = []string{filepath.Join(
			os.Getenv("USERPROFILE"),
			".trae-cn", "binaries", "node", "versions", "24.18.0",
			"node_modules", "pi-acp", "dist", "index.js",
		)}
		piEnv = map[string]string{
			"HOME":         os.Getenv("USERPROFILE"),
			"USERPROFILE":  os.Getenv("USERPROFILE"),
			"APPDATA":      os.Getenv("APPDATA"),
			"LOCALAPPDATA": os.Getenv("LOCALAPPDATA"),
		}
	}

	candidates := []candidate{
		{
			id:          "claude-code",
			displayName: "Claude Code",
			cmd:         detectClaudeCmd(&searchPaths, executionPaths),
			newAgent: func(meta AgentMeta) Agent {
				return NewClaudeCodeAgent(meta)
			},
		},
		{
			id:          "opencode",
			displayName: "OpenCode",
			cmd:         "opencode",
			args:        []string{"acp"},
			newAgent: func(meta AgentMeta) Agent {
				return NewOpenCodeAgent(meta)
			},
		},
		{
			id:          "codex",
			displayName: "Codex CLI",
			cmd:         codexCmd,
			args:        codexArgs,
			newAgent: func(meta AgentMeta) Agent {
				return NewCodexAgent(meta)
			},
		},
		{
			id:          "hermes",
			displayName: "Hermes Agent",
			cmd:         "hermes",
			args:        []string{"acp"},
			newAgent: func(meta AgentMeta) Agent {
				return NewHermesAgent(meta)
			},
		},
		{
			id:          "kimi",
			displayName: "Kimi",
			cmd:         "kimi",
			args:        []string{"acp"},
			newAgent: func(meta AgentMeta) Agent {
				return NewKimiAgent(meta)
			},
		},
		{
			id:          "gemini",
			displayName: "Gemini CLI",
			cmd:         "gemini",
			args:        []string{"--experimental-acp"},
			newAgent: func(meta AgentMeta) Agent {
				return NewGeminiAgent(meta)
			},
		},
		{
			id:          "copilot",
			displayName: "GitHub Copilot",
			cmd:         "copilot",
			args:        []string{"--acp"},
			newAgent: func(meta AgentMeta) Agent {
				return NewCopilotAgent(meta)
			},
		},
		{
			id:          "pi",
			displayName: "pi",
			cmd:         piCmd,
			args:        piArgs,
			env:         piEnv,
			newAgent: func(meta AgentMeta) Agent {
				return NewPiAgent(meta)
			},
		},
		{
			id:          "cursor",
			displayName: "Cursor",
			cmd:         "agent",
			args:        []string{"acp"},
			newAgent: func(meta AgentMeta) Agent {
				return NewCursorAgent(meta)
			},
		},
		{
			id:          "glm",
			displayName: "GLM Agent",
			cmd:         "glm-acp-agent",
			newAgent: func(meta AgentMeta) Agent {
				return NewGlmAgent(meta)
			},
		},
		{
			id:          "openclaw",
			displayName: "OpenClaw",
			cmd:         "openclaw",
			args:        []string{"acp"},
			newAgent: func(meta AgentMeta) Agent {
				return NewOpenClawAgent(meta)
			},
		},
	}

	for _, c := range candidates {
		// 在 PATH 和搜索路径中查找可执行文件
		fullPath := findExecutable(c.cmd, searchPaths)
		if fullPath == "" {
			slog.Debug("Agent 未找到", "id", c.id, "cmd", c.cmd)
			continue
		}

		meta := AgentMeta{
			ID:          c.id,
			DisplayName: c.displayName,
			Cmd:         fullPath,
			Args:        c.args,
			WorkDir:     workDir,
			Env:         mergeEnv(r.resolveEnv(c.id), c.env),
			PathDirs:    orderedSearchPaths(fullPath, executionPaths),
		}

		agent := c.newAgent(meta)
		r.agents[c.id] = agent
		slog.Info("发现 Agent", "id", c.id, "path", fullPath)
	}

	return nil
}

// prioritizeExecutionPaths keeps an interactive user's custom PATH order, but
// moves minimal service paths behind runtime-manager locations discovered from
// the home directory. This avoids accidentally selecting an older system node.
func prioritizeExecutionPaths(parent, extra []string) []string {
	preferred := make([]string, 0, len(parent)+len(extra))
	fallback := make([]string, 0, len(parent)+len(extra))
	appendPath := func(path string) {
		if isSystemPath(path) {
			fallback = append(fallback, path)
		} else {
			preferred = append(preferred, path)
		}
	}
	for _, path := range parent {
		appendPath(path)
	}
	for _, path := range extra {
		appendPath(path)
	}
	return append(preferred, fallback...)
}

func isSystemPath(path string) bool {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if runtime.GOOS != "windows" {
		switch cleaned {
		case "/usr/bin", "/bin", "/usr/sbin", "/sbin":
			return true
		default:
			return false
		}
	}

	systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
	if systemRoot == "" {
		return false
	}
	cleaned = strings.ToLower(cleaned)
	systemRoot = strings.ToLower(filepath.Clean(systemRoot))
	return cleaned == systemRoot || strings.HasPrefix(cleaned, systemRoot+string(os.PathSeparator))
}

// orderedSearchPaths builds the PATH passed to an Agent process. The command's
// directory comes first so an npm shim and its matching node runtime stay
// together. Discovery-only locations such as the current directory do not leak
// into child PATH unless that is where the command itself was found.
func orderedSearchPaths(command string, preferred []string) []string {
	paths := make([]string, 0, len(preferred)+1)
	paths = append(paths, filepath.Dir(command))
	paths = append(paths, preferred...)
	return uniquePathDirs(paths)
}

func uniquePathDirs(paths []string) []string {
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		key := path
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, path)
	}
	return result
}

func appendUniquePathDirs(paths []string, additions ...string) []string {
	combined := make([]string, 0, len(paths)+len(additions))
	combined = append(combined, paths...)
	combined = append(combined, additions...)
	return uniquePathDirs(combined)
}

func mergeEnv(base, override map[string]string) map[string]string {
	if base == nil && override == nil {
		return nil
	}
	result := make(map[string]string, len(base)+len(override))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range override {
		result[key] = value
	}
	return result
}

// resolveEnv 解析特定 Agent 需要的环境变量（平台特有）
// Lzm 2026-07-11
func (r *AgentRegistry) resolveEnv(kind string) map[string]string {
	home, _ := os.UserHomeDir()

	switch kind {
	case "claude-code":
		// 从 ~/.claude/settings.json 读取环境变量
		settingsPath := r.cfg.ClaudeSettingsFile
		if settingsPath == "" {
			settingsPath = filepath.Join(home, ".claude", "settings.json")
		}
		return readClaudeSettings(settingsPath)

	case "opencode":
		// 直接从 OS 环境读取 OPENAI_API_KEY
		if key := os.Getenv("OPENAI_API_KEY"); key != "" {
			return map[string]string{"OPENAI_API_KEY": key}
		}
		return nil

	case "gemini":
		// Gemini CLI 需要 GEMINI_API_KEY 环境变量
		// 安装：npm install -g @google/gemini-cli
		if key := os.Getenv("GEMINI_API_KEY"); key != "" {
			return map[string]string{"GEMINI_API_KEY": key}
		}
		return nil

	case "copilot":
		// GitHub Copilot CLI 使用 GitHub 账号登录认证，无需 API Key
		// 安装：npm install -g @github/copilot-cli
		// 首次使用需运行 copilot login 完成 GitHub 认证
		return nil

	case "pi":
		// pi 使用自有配置管理 API Key
		// 配置存储在 ~/.pi/agent/auth.json 和 settings.json 中
		// 前置安装：npm install -g @earendil-works/pi-coding-agent
		return nil

	case "cursor":
		// Cursor CLI 需要 CURSOR_API_KEY 或 CURSOR_AUTH_TOKEN 环境变量
		// 安装：curl https://cursor.com/install -fsS | bash（macOS/Linux）
		//       irm 'https://cursor.com/install?win32=true' | iex（Windows）
		if key := os.Getenv("CURSOR_API_KEY"); key != "" {
			return map[string]string{"CURSOR_API_KEY": key}
		}
		if token := os.Getenv("CURSOR_AUTH_TOKEN"); token != "" {
			return map[string]string{"CURSOR_AUTH_TOKEN": token}
		}
		return nil

	case "glm":
		// GLM ACP Agent 需要 Z_AI_API_KEY 环境变量
		// 安装：npm install -g glm-acp-agent
		// API Key 申请：https://z.ai/manage-apikey/apikey-list
		// 也支持通过 glm-acp-agent --setup 交互式配置认证文件
		if key := os.Getenv("Z_AI_API_KEY"); key != "" {
			return map[string]string{"Z_AI_API_KEY": key}
		}
		return nil

	case "openclaw":
		// OpenClaw ACP 桥接器使用自有配置连接 Gateway
		// 安装：npm install -g openclaw 或 https://openclaw.ai
		// 前置条件：OpenClaw Gateway 必须运行中，且已通过 config 或 --url 配置好
		// 可选环境变量：OPENCLAW_GATEWAY_TOKEN, OPENCLAW_GATEWAY_PASSWORD
		return map[string]string{
			"OPENCLAW_HIDE_BANNER":    "1",
			"OPENCLAW_SUPPRESS_NOTES": "1",
		}

	default:
		// codex、kimi 等使用平台特有的环境变量配置
		return setupAgentEnv(kind)
	}
}

// readClaudeSettings 读取 Claude 配置文件中的环境变量
// Lzm 2026-07-09
func readClaudeSettings(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		// 后备：从 OS 环境读取
		if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
			return map[string]string{"ANTHROPIC_API_KEY": key}
		}
		return nil
	}

	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil
	}

	env := make(map[string]string)
	for k, v := range settings.Env {
		if strings.HasPrefix(k, "ANTHROPIC_") {
			if k == "ANTHROPIC_AUTH_TOKEN" {
				env["ANTHROPIC_API_KEY"] = v
			} else {
				env[k] = v
			}
		}
	}

	if len(env) > 0 {
		return env
	}

	// 后备
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return map[string]string{"ANTHROPIC_API_KEY": key}
	}
	return nil
}

// findExecutable 按给定 PATH 顺序查找可执行文件
// 使用平台特有的扩展名列表（Windows 上自动尝试 .exe/.cmd 等）
// Lzm 2026-07-11
func findExecutable(name string, searchPaths []string) string {
	return findExecutableWithExtensions(name, searchPaths, getExecutableExtensions())
}

func findExecutableWithExtensions(name string, searchPaths, extensions []string) string {
	names := executableCandidateNames(name, extensions)
	requireExecuteBit := len(extensions) == 1 && normalizeExecutableExtension(extensions[0]) == ""

	// 先检查显式路径。Windows 候选不包含无扩展名的 npm Unix shim。
	for _, candidate := range names {
		if executableFile(candidate, requireExecuteBit) {
			abs, _ := filepath.Abs(candidate)
			return abs
		}
	}

	// 平台特有的优先级调整（Windows 上将无扩展名原始名移到最后）
	names = prioritizeNames(names)

	// 在搜索路径中查找
	for _, dir := range searchPaths {
		for _, n := range names {
			fullPath := filepath.Join(dir, n)
			if executableFile(fullPath, requireExecuteBit) {
				return fullPath
			}
		}
	}

	return ""
}

func executableFile(path string, requireExecuteBit bool) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return !requireExecuteBit || info.Mode().Perm()&0o111 != 0
}

// --- 注册方法 ---

// Register 注册一个 Agent 实例
func (r *AgentRegistry) Register(a Agent) {
	r.agents[a.ID()] = a
}

// Get 根据 ID 获取 Agent
func (r *AgentRegistry) Get(id string) Agent {
	return r.agents[id]
}

// List 返回所有已注册的 Agent
func (r *AgentRegistry) List() []Agent {
	ids := r.IDs()
	result := make([]Agent, 0, len(ids))
	for _, id := range ids {
		result = append(result, r.agents[id])
	}
	return result
}

// IDs 返回所有 Agent ID 列表
func (r *AgentRegistry) IDs() []string {
	ids := make([]string, 0, len(r.agents))
	for id := range r.agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Connect 启动 Agent 并完成 ACP 握手
// Lzm 2026-07-09
func (r *AgentRegistry) Connect(ctx context.Context, id string) error {
	a := r.agents[id]
	if a == nil {
		return fmt.Errorf("未知 Agent: %s", id)
	}
	return a.Start(ctx)
}

// Disconnect 断开 Agent 连接
func (r *AgentRegistry) Disconnect(ctx context.Context, id string) error {
	a := r.agents[id]
	if a == nil {
		return nil
	}
	return a.Stop(ctx)
}

// ConnectAll 启动所有已注册的 Agent
func (r *AgentRegistry) ConnectAll(ctx context.Context) {
	for id := range r.agents {
		if err := r.Connect(ctx, id); err != nil {
			slog.Error("Agent 启动失败", "id", id, "error", err)
		}
	}
}

// DisconnectAll 断开所有 Agent
func (r *AgentRegistry) DisconnectAll(ctx context.Context) {
	for id := range r.agents {
		if err := r.Disconnect(ctx, id); err != nil {
			slog.Warn("Agent 停止异常", "id", id, "error", err)
		}
	}
}

// getNPMGlobalRoot 获取 npm 全局安装根目录（node_modules 路径）
// 使用平台特有的 npm 命令名和候选目录
// Lzm 2026-07-11
func getNPMGlobalRoot(searchPaths, executionPaths []string) string {
	// 1. 尝试 `npm root -g` 获取
	npmCmd := getNPMCommand()
	npmPath := findExecutable(npmCmd, searchPaths)
	if npmPath == "" {
		if path, err := exec.LookPath(npmCmd); err == nil {
			npmPath = path
		}
	}
	if npmPath != "" {
		ctx, cancel := context.WithTimeout(context.Background(), npmGlobalRootQueryTimeout)
		cmd := infra.CommandContext(ctx, npmPath, "root", "-g")
		cmd.Env = environmentWithPath(orderedSearchPaths(npmPath, executionPaths))
		cmd.WaitDelay = npmWrapperInstallWaitDelay
		output, err := cmd.Output()
		cancel()
		if err == nil {
			root := strings.TrimSpace(string(output))
			if root != "" {
				return root
			}
		}
	}

	// 2. 后备：搜索平台特有的常见 npm 全局目录
	for _, dir := range getNPMRootCandidates() {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}

	return ""
}

// autoInstallNPMWrapper 自动安装 npm ACP wrapper 包
// 参数：
//   - cmdName: wrapper 命令名（如 "codex-acp"）
//   - pkgName: npm 包名（如 "@agentclientprotocol/codex-acp"）
//   - searchPaths: 有序搜索路径（安装后会将目录加入此列表）
//   - executionPaths: 应用为 Agent 子进程准备的完整 PATH
//
// 返回安装后的完整路径，失败返回空字符串
// Lzm 2026-07-10
func autoInstallNPMWrapper(cmdName, pkgName string, searchPaths *[]string, executionPaths []string) string {
	// 1. 确认 npm 可用
	npmCmd := getNPMCommand()
	npmPath := findExecutable(npmCmd, *searchPaths)
	if npmPath == "" {
		// 尝试从 PATH 查找
		if p, err := exec.LookPath(npmCmd); err == nil {
			npmPath = p
		} else {
			slog.Warn("npm 不可用，无法自动安装 ACP wrapper", "pkg", pkgName)
			return ""
		}
	}

	// 2. 使用当前用户的私有 Local 数据目录，避免修改系统 npm 全局目录，
	// 也避免 Linux 共享 /tmp 中可被其他用户替换的可执行文件。
	npmPrefix := npmWrapperPrefix()
	if npmPrefix == "" {
		slog.Warn("无法确定 ACP wrapper 私有安装目录", "pkg", pkgName)
		return ""
	}
	if err := os.MkdirAll(npmPrefix, 0o700); err != nil {
		slog.Warn("创建 npm prefix 目录失败", "error", err)
		return ""
	}
	if err := os.Chmod(npmPrefix, 0o700); err != nil {
		slog.Warn("保护 npm prefix 目录失败", "error", err)
		return ""
	}

	slog.Info("正在安装 "+pkgName, "npm", npmPath, "prefix", npmPrefix)
	stderr, err := runNPMInstall(npmPath, npmPrefix, pkgName, executionPaths, npmWrapperInstallTimeout)
	if err != nil {
		if err == context.DeadlineExceeded {
			slog.Warn(pkgName+" 安装超时", "timeout", npmWrapperInstallTimeout.String())
		} else {
			slog.Warn(pkgName+" 安装失败",
				"error", err,
				"stderr", stderr,
			)
		}
		return ""
	}

	slog.Info(pkgName + " 安装成功")

	// 3. 将 npm prefix 的 bin 目录加入搜索路径
	// --prefix 安装后，.cmd 文件放在 {prefix} 目录本身（Windows）
	*searchPaths = appendUniquePathDirs(*searchPaths, npmWrapperSearchPaths(npmPrefix)...)
	repairInstalledNPMWrapper(cmdName, npmPrefix)

	// 4. 重新查找安装的 wrapper
	if installedPath := findExecutable(cmdName, *searchPaths); installedPath != "" {
		return installedPath
	}

	// 5. 最终尝试：按平台规则检查 npm prefix。Windows 仍然只会
	// 选择 .exe/.cmd/.bat/.com，不会回退到 npm 同时生成的 Unix shim。
	if installedPath := findExecutable(cmdName, []string{
		npmPrefix,
		filepath.Join(npmPrefix, "node_modules", ".bin"),
		filepath.Join(npmPrefix, "bin"),
	}); installedPath != "" {
		slog.Info("找到安装的 wrapper", "path", installedPath)
		return installedPath
	}

	return ""
}

func npmWrapperPrefix() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, infra.LocalDataDir, "npm")
}

func npmWrapperSearchPaths(prefix string) []string {
	return []string{
		prefix,
		filepath.Join(prefix, "node_modules", ".bin"),
		filepath.Join(prefix, "bin"),
	}
}

// Some npm packages publish a shebang entry point without an executable mode.
// npm still creates the bin symlink, so repair only that freshly installed
// private-prefix entry before discovery tries to launch it.
func repairInstalledNPMWrapper(cmdName, npmPrefix string) {
	if runtime.GOOS == "windows" {
		return
	}
	for _, dir := range []string{
		filepath.Join(npmPrefix, "bin"),
		filepath.Join(npmPrefix, "node_modules", ".bin"),
		npmPrefix,
	} {
		path := filepath.Join(dir, cmdName)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 != 0 {
			continue
		}
		if err := os.Chmod(path, info.Mode().Perm()|0o111); err != nil {
			slog.Warn("无法修复 npm wrapper 执行权限", "path", path, "error", err)
		}
		return
	}
}

func runNPMInstall(npmPath, npmPrefix, pkgName string, executionPaths []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	installCmd := infra.CommandContext(ctx, npmPath, "install", "--prefix", npmPrefix, "-g", pkgName)
	installCmd.Env = environmentWithPath(orderedSearchPaths(npmPath, executionPaths))
	installCmd.WaitDelay = npmWrapperInstallWaitDelay
	var stderr bytes.Buffer
	installCmd.Stderr = &stderr
	err := installCmd.Run()
	if ctx.Err() != nil {
		return stderr.String(), ctx.Err()
	}
	return stderr.String(), err
}

func environmentWithPath(pathDirs []string) []string {
	env := os.Environ()
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		key, _, found := strings.Cut(entry, "=")
		if found && environmentKeyEqual(key, "PATH") {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "PATH="+strings.Join(uniquePathDirs(pathDirs), string(os.PathListSeparator)))
}

func environmentKeyEqual(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// detectClaudeCmd 检测 Claude Code ACP wrapper
// 优先使用 claude-agent-acp，不存在时尝试自动安装
// 注意：原始 claude 命令不支持 ACP 协议，不能直接注册使用
// Lzm 2026-07-13
func detectClaudeCmd(searchPaths *[]string, executionPaths []string) string {
	// 1. 优先查找 claude-agent-acp wrapper（这才是真正的 ACP 入口）
	// 注：不能遍历 getClaudeScriptNames()，因为其中包含 "claude"，会绕过自动安装
	if path := findExecutable("claude-agent-acp", *searchPaths); path != "" {
		return path
	}

	// 2. 查找原始 claude 命令（ACP wrapper 可能未安装）
	claudePath := findExecutable("claude", *searchPaths)
	if claudePath == "" {
		return ""
	}

	// 3. 自动安装 @agentclientprotocol/claude-agent-acp 以提供 claude-agent-acp wrapper
	slog.Info("Claude: ACP wrapper 未安装，尝试自动安装 @agentclientprotocol/claude-agent-acp ...")
	installedPath := autoInstallNPMWrapper("claude-agent-acp", "@agentclientprotocol/claude-agent-acp", searchPaths, executionPaths)
	if installedPath != "" {
		slog.Info("Claude: ACP wrapper 自动安装成功", "path", installedPath)
		return installedPath
	}

	// 4. 安装失败 — 不注册 claude-code（原始 claude 不支持 ACP 协议）
	slog.Warn("Claude: ACP wrapper 安装失败，跳过注册（原始 claude 命令不支持 ACP 协议）")
	return ""
}
