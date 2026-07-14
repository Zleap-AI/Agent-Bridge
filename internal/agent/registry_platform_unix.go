// -*- coding: utf-8 -*-
// Go 1.25+
//
// registry_platform_unix.go
// macOS/Linux 平台的 Agent 搜索路径和环境配置
//
// Lzm 2026-07-11

//go:build darwin || linux

package agent

import (
	"os"
	"path/filepath"
)

// getExtraSearchPaths 返回 Unix 平台常见的可执行文件搜索路径
// Lzm 2026-07-11
func getExtraSearchPaths() []string {
	home, _ := os.UserHomeDir()
	return []string{
		"/usr/local/bin",
		"/usr/bin",
		"/opt/homebrew/bin", // Apple Silicon Homebrew
		filepath.Join(home, ".npm-global", "bin"), // npm 全局安装
		filepath.Join(home, "node_modules", ".bin"),
		filepath.Join(home, ".local", "bin"), // Cursor CLI 安装路径
		filepath.Join(home, ".volta", "bin"), // Volta Node.js 版本管理器
	}
}

// getExecutableExtensions 返回 Unix 可执行文件扩展名
// Lzm 2026-07-11
func getExecutableExtensions() []string {
	return []string{""} // macOS/Linux 不依赖扩展名，靠可执行权限位
}

// prioritizeNames macOS/Linux 保持原始顺序（Unix 无扩展名脚本是标准做法）
// Lzm 2026-07-13
func prioritizeNames(names []string) []string {
	return names
}

// getNPMCommand 返回 Unix 上的 npm 命令名
// Lzm 2026-07-11
func getNPMCommand() string {
	return "npm"
}

// getClaudeScriptNames 返回 Unix 上 Claude ACP 的脚本名列表
// Lzm 2026-07-11
func getClaudeScriptNames() []string {
	return []string{"claude-agent-acp", "claude"}
}

// getNPMRootCandidates 返回 Unix npm 全局根目录候选
// Lzm 2026-07-11
func getNPMRootCandidates() []string {
	home, _ := os.UserHomeDir()
	return []string{
		"/usr/local/lib/node_modules",
		"/usr/lib/node_modules",
		"/opt/homebrew/lib/node_modules",
		filepath.Join(home, ".npm-global", "lib", "node_modules"),
		filepath.Join(home, "node_modules"),
	}
}

// getCodexCandidates 返回 Unix 上 Codex 的额外候选安装路径
// Lzm 2026-07-11
func getCodexCandidates() []string {
	// Codex 通常位于 PATH 或 npm 全局 bin 目录，无需额外候选路径。
	return nil
}

// setupAgentEnv 为指定 Agent 配置 Unix 环境变量
// Lzm 2026-07-11
func setupAgentEnv(kind string) map[string]string {
	switch kind {
	case "codex":
		return setupCodexEnv()
	case "kimi":
		return setupKimiEnv()
	default:
		return nil
	}
}

// setupCodexEnv 配置 Codex 的 Unix 环境变量
// Lzm 2026-07-11
func setupCodexEnv() map[string]string {
	env := make(map[string]string)

	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		env["ANTHROPIC_API_KEY"] = key
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		env["OPENAI_API_KEY"] = key
	}

	return env
}

// setupKimiEnv 配置 Kimi 的 Unix 环境变量
// Lzm 2026-07-11
func setupKimiEnv() map[string]string {
	env := make(map[string]string)

	if key := os.Getenv("MOONSHOT_API_KEY"); key != "" {
		env["MOONSHOT_API_KEY"] = key
	}

	return env
}
