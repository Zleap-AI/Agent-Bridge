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
	"sort"
	"strconv"
)

// getExtraSearchPaths 返回 Unix 平台常见的可执行文件搜索路径
// Lzm 2026-07-11
func getExtraSearchPaths() []string {
	home, _ := os.UserHomeDir()
	paths := []string{
		os.Getenv("NVM_BIN"),
		os.Getenv("PNPM_HOME"),
		envSubdir("VOLTA_HOME", "bin"),
		envSubdir("MISE_DATA_DIR", "shims"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".volta", "bin"),
		filepath.Join(home, ".local", "share", "mise", "shims"),
		filepath.Join(home, ".mise", "shims"),
		filepath.Join(home, ".asdf", "shims"),
		filepath.Join(home, ".nodenv", "shims"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(home, "node_modules", ".bin"),
	}

	// launchd/systemd do not load version-manager shell hooks. Include their
	// concrete Node installations so #!/usr/bin/env node remains executable.
	paths = append(paths, newestGlobMatches(
		filepath.Join(home, ".nvm", "versions", "node", "*", "bin"),
		filepath.Join(home, ".local", "share", "mise", "installs", "node", "*", "bin"),
		filepath.Join(home, ".local", "share", "fnm", "node-versions", "*", "installation", "bin"),
	)...)
	paths = append(paths, newestGlobMatches(
		"/opt/homebrew/opt/node@*/bin",
		"/usr/local/opt/node@*/bin",
	)...)

	paths = append(paths,
		"/opt/homebrew/opt/node/bin",
		"/usr/local/opt/node/bin",
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	)
	return uniqueExistingDirs(paths)
}

func newestGlobMatches(patterns ...string) []string {
	var matches []string
	for _, pattern := range patterns {
		found, err := filepath.Glob(pattern)
		if err == nil {
			matches = append(matches, found...)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		left := pathVersion(matches[i])
		right := pathVersion(matches[j])
		for index := 0; index < len(left) || index < len(right); index++ {
			var leftPart, rightPart int
			if index < len(left) {
				leftPart = left[index]
			}
			if index < len(right) {
				rightPart = right[index]
			}
			if leftPart != rightPart {
				return leftPart > rightPart
			}
		}
		return matches[i] > matches[j]
	})
	return matches
}

func pathVersion(path string) []int {
	for dir := filepath.Dir(path); dir != "." && dir != string(os.PathSeparator); dir = filepath.Dir(dir) {
		if version := numericParts(filepath.Base(dir)); len(version) > 0 {
			return version
		}
	}
	return nil
}

func numericParts(value string) []int {
	var parts []int
	for start := 0; start < len(value); {
		for start < len(value) && (value[start] < '0' || value[start] > '9') {
			start++
		}
		end := start
		for end < len(value) && value[end] >= '0' && value[end] <= '9' {
			end++
		}
		if start < end {
			part, _ := strconv.Atoi(value[start:end])
			parts = append(parts, part)
		}
		start = end
	}
	return parts
}

func envSubdir(name, child string) string {
	root := os.Getenv(name)
	if root == "" {
		return ""
	}
	return filepath.Join(root, child)
}

func uniqueExistingDirs(paths []string) []string {
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) {
			continue
		}
		path = filepath.Clean(path)
		if _, exists := seen[path]; exists {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	return result
}

// getExecutableExtensions 返回 Unix 可执行文件扩展名
// Lzm 2026-07-11
func getExecutableExtensions() []string {
	return []string{""}
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
