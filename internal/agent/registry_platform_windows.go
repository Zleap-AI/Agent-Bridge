// -*- coding: utf-8 -*-
// Go 1.25+
//
// registry_platform_windows.go
// Windows 平台特有的 Agent 搜索路径和环境配置
//
// Lzm 2026-07-11

//go:build windows

package agent

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// getExtraSearchPaths 返回 Windows 特有的可执行文件搜索路径
// Lzm 2026-07-13
func getExtraSearchPaths() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, "AppData", "Roaming", "npm"),
		os.Getenv("APPDATA") + "\\npm",
		filepath.Join(home, ".local", "bin"),                             // WSL/Cursor CLI 安装路径
		filepath.Join(home, "AppData", "Local", "Cursor"),                // Cursor Windows 路径
		filepath.Join(home, "AppData", "Local", "cursor-agent"),          // Cursor Agent CLI 安装路径
		filepath.Join(home, "AppData", "Local", "GitHub CLI", "copilot"), // Copilot CLI (gh extension) 安装路径
	}
}

// getExecutableExtensions 返回 Windows 可执行文件扩展名列表（来自 PATHEXT）
// Lzm 2026-07-11
func getExecutableExtensions() []string {
	exts := []string{""} // 无扩展名优先
	pathext := os.Getenv("PATHEXT")
	if pathext == "" {
		return []string{".exe", ".cmd", ".bat", ".com"}
	}
	for _, ext := range strings.Split(strings.ToLower(pathext), ";") {
		ext = strings.TrimSpace(ext)
		if ext != "" {
			exts = append(exts, ext)
		}
	}
	return exts
}

// getNPMCommand 返回 Windows 上的 npm 命令名
// Lzm 2026-07-11
func getNPMCommand() string {
	return "npm.cmd"
}

// getClaudeScriptNames 返回 Windows 上 Claude ACP 的脚本名列表
// Lzm 2026-07-11
func getClaudeScriptNames() []string {
	return []string{"claude-agent-acp.cmd", "claude-agent-acp", "claude"}
}

// getNPMRootCandidates 返回 Windows npm 全局根目录候选
// Lzm 2026-07-11
func getNPMRootCandidates() []string {
	home, _ := os.UserHomeDir()
	return []string{
		os.Getenv("APPDATA") + "\\npm\\node_modules",
		filepath.Join(home, "AppData", "Roaming", "npm", "node_modules"),
		os.Getenv("LOCALAPPDATA") + "\\npm\\node_modules",
	}
}

// getCodexCandidates 返回 Windows 上 Codex 的候选安装路径
// 遍历 %LOCALAPPDATA%\OpenAI\Codex\bin\{version}\codex.exe
// Lzm 2026-07-11
func getCodexCandidates() []string {
	var candidates []string
	codexBase := filepath.Join(os.Getenv("LOCALAPPDATA"), "OpenAI", "Codex", "bin")
	if entries, err := os.ReadDir(codexBase); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				candidates = append(candidates,
					filepath.Join(codexBase, entry.Name()),
				)
			}
		}
	}
	return candidates
}

// setupAgentEnv 为指定 Agent 配置 Windows 特有的环境变量
// 针对 Windows EPERM 问题，重定向 HOME 到 TEMP 目录
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

// setupCodexEnv 配置 Codex 的 Windows 环境变量
// 重定向 CODEX_HOME 到 %TEMP%/codex-home 解决 EPERM
// Lzm 2026-07-11
func setupCodexEnv() map[string]string {
	env := make(map[string]string)

	// 透传 API Key
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		env["ANTHROPIC_API_KEY"] = key
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		env["OPENAI_API_KEY"] = key
	}

	// 创建临时 CODEX_HOME 目录
	codexHome := filepath.Join(os.TempDir(), "codex-home")
	if err := os.MkdirAll(codexHome, 0755); err != nil {
		slog.Warn("创建 codex-home 失败，跳过重定向", "error", err)
		return env
	}

	env["CODEX_HOME"] = codexHome

	// 复制 ~/.codex/ 中的已有数据
	home, _ := os.UserHomeDir()
	srcCodex := filepath.Join(home, ".codex")
	if info, err := os.Stat(srcCodex); err == nil && info.IsDir() {
		if entries, err := os.ReadDir(srcCodex); err == nil {
			for _, entry := range entries {
				srcPath := filepath.Join(srcCodex, entry.Name())
				dstPath := filepath.Join(codexHome, entry.Name())
				if _, err := os.Stat(dstPath); os.IsNotExist(err) {
					if entry.IsDir() {
						if err := copyDir(srcPath, dstPath); err != nil {
							slog.Warn("复制 Codex 目录失败",
								"name", entry.Name(), "error", err)
						}
					} else {
						if err := copyFile(srcPath, dstPath); err != nil {
							slog.Warn("复制 Codex 文件失败",
								"name", entry.Name(), "error", err)
						}
					}
				}
			}
		}
	}

	return env
}

// setupKimiEnv 配置 Kimi 的 Windows 环境变量
// 重定向 HOME/USERPROFILE 到 %TEMP%/kimi-home 解决 EPERM
// Lzm 2026-07-11
func setupKimiEnv() map[string]string {
	env := make(map[string]string)

	// 透传 MOONSHOT_API_KEY
	if key := os.Getenv("MOONSHOT_API_KEY"); key != "" {
		env["MOONSHOT_API_KEY"] = key
	}

	// 创建临时 HOME 目录
	kimiHome := filepath.Join(os.TempDir(), "kimi-home")
	kimiCodeDir := filepath.Join(kimiHome, ".kimi-code")
	kimiSessions := filepath.Join(kimiCodeDir, "sessions")

	if err := os.MkdirAll(kimiSessions, 0755); err != nil {
		slog.Warn("创建 kimi-home 失败，跳过重定向", "error", err)
		return env
	}

	env["HOME"] = kimiHome
	env["USERPROFILE"] = kimiHome

	// 复制 credentials（保持认证登录状态）
	srcCredentials := filepath.Join(os.Getenv("USERPROFILE"), ".kimi-code", "credentials")
	dstCredentials := filepath.Join(kimiCodeDir, "credentials")
	if info, err := os.Stat(srcCredentials); err == nil && info.IsDir() {
		if _, err := os.Stat(dstCredentials); os.IsNotExist(err) {
			if err := copyDir(srcCredentials, dstCredentials); err != nil {
				slog.Warn("复制 Kimi credentials 失败", "error", err)
			}
		}
	}

	// 复制 config.toml
	srcConfig := filepath.Join(os.Getenv("USERPROFILE"), ".kimi-code", "config.toml")
	dstConfig := filepath.Join(kimiCodeDir, "config.toml")
	if _, err := os.Stat(srcConfig); err == nil {
		if _, err := os.Stat(dstConfig); os.IsNotExist(err) {
			if err := copyFile(srcConfig, dstConfig); err != nil {
				slog.Warn("复制 Kimi config.toml 失败", "error", err)
			}
		}
	}

	// 复制 device_id
	srcDeviceID := filepath.Join(os.Getenv("USERPROFILE"), ".kimi-code", "device_id")
	dstDeviceID := filepath.Join(kimiCodeDir, "device_id")
	if _, err := os.Stat(srcDeviceID); err == nil {
		if _, err := os.Stat(dstDeviceID); os.IsNotExist(err) {
			if err := copyFile(srcDeviceID, dstDeviceID); err != nil {
				slog.Warn("复制 Kimi device_id 失败", "error", err)
			}
		}
	}

	// 复制 session_index.jsonl
	srcIndex := filepath.Join(os.Getenv("USERPROFILE"), ".kimi-code", "session_index.jsonl")
	dstIndex := filepath.Join(kimiCodeDir, "session_index.jsonl")
	if _, err := os.Stat(srcIndex); err == nil {
		if _, err := os.Stat(dstIndex); os.IsNotExist(err) {
			if err := copyFile(srcIndex, dstIndex); err != nil {
				slog.Warn("复制 Kimi session_index 失败", "error", err)
			}
		}
	}

	return env
}
