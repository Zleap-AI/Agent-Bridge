// -*- coding: utf-8 -*-
// Go 1.25+
//
// log_scanner.go
// LogScanner — 日志扫描器，借鉴 OpenViking 的 LogSource 适配器模式
// 用于扫描 Agent 历史日志文件，发现历史会话
// 与 @register_source 装饰器异曲同工：每个 Agent 一个适配器 + 注册中心
//
// Lzm 2026-07-10

package agent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SessionRef 会话引用 — 对应 OpenViking 的 SessionRef
// Lzm 2026-07-10
type SessionRef struct {
	Harness   string            `json:"harness"`        // Agent 类型名（如 "kimi", "codex"）
	NativeID  string            `json:"native_id"`      // Agent 自己的会话 ID
	Locator   string            `json:"locator"`        // 文件路径
	Title     string            `json:"title"`          // 会话标题
	StartedAt int64             `json:"started_at"`     // 开始时间戳
	Meta      map[string]string `json:"meta,omitempty"` // 额外信息（模型名、cwd 等）
}

// LogScanner 日志扫描器接口 — 对应 OpenViking 的 LogSource 抽象基类
// 每个 Agent 类型实现一个适配器
// Lzm 2026-07-10
type LogScanner interface {
	// Name 返回适配器名称（如 "kimi", "codex"）
	Name() string
	// DiscoverSessions 发现所有可读取的历史会话
	// 对应 OpenViking 的 discover_sessions()
	DiscoverSessions() ([]SessionRef, error)
	// ReadMessages 读取指定会话的消息
	// limit <= 0 表示不限
	ReadMessages(ref SessionRef, cursor int, limit int) ([]string, int, error)
}

// --- 注册中心（类似 OpenViking 的 @register_source 装饰器） ---

// scannerRegistry LogScanner 注册中心
// 对应 OpenViking 的 SOURCE_REGISTRY 字典
// Lzm 2026-07-10
var scannerRegistry = struct {
	scanners map[string]LogScanner
}{
	scanners: make(map[string]LogScanner),
}

// RegisterScanner 注册 LogScanner 适配器
// 对应 OpenViking 的 @register_source("name") 装饰器
// Lzm 2026-07-10
func RegisterScanner(s LogScanner) {
	scannerRegistry.scanners[s.Name()] = s
	slog.Debug("LogScanner 已注册", "name", s.Name())
}

// GetScanner 获取指定名称的 LogScanner
func GetScanner(name string) LogScanner {
	return scannerRegistry.scanners[name]
}

// ListScanners 列出所有已注册的 LogScanner
func ListScanners() []LogScanner {
	var result []LogScanner
	for _, s := range scannerRegistry.scanners {
		result = append(result, s)
	}
	return result
}

// --- 已知路径扫描（OpenViking 的 `default_paths()` 映射） ---

// knownSessionDirs 各 Agent 的历史会话日志目录
// Lzm 2026-07-10
var knownSessionDirs = map[string][]string{
	"kimi": {
		filepath.Join(homeDir(), ".kimi-code", "sessions"),
	},
	"codex": {
		filepath.Join(homeDir(), ".codex", "sessions"),
	},
	"claude-code": {
		filepath.Join(homeDir(), ".claude", "projects"),
	},
	"opencode": {
		filepath.Join(homeDir(), ".local", "share", "opencode"),
	},
	"gemini": {
		filepath.Join(homeDir(), ".gemini", "sessions"),
	},
	"copilot": {
		filepath.Join(homeDir(), ".copilot", "sessions"),
	},
	"pi": {
		filepath.Join(homeDir(), ".pi", "agent", "sessions"),
	},
	"cursor": {
		filepath.Join(homeDir(), ".cursor", "sessions"),
	},
	"glm": {
		filepath.Join(homeDir(), ".local", "state", "glm-acp-agent", "sessions"),
	},
	"openclaw": {
		filepath.Join(homeDir(), ".openclaw", "state"),
	},
}

// homeDir 返回当前用户的 home 目录，跨平台兼容
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.Getenv("HOME")
	}
	return home
}

// ScannerFromAgentID 根据 Agent ID 自动创建对应的 LogScanner
// Lzm 2026-07-10
func ScannerFromAgentID(agentID string) LogScanner {
	// 优先返回注册的 Scanner
	if s := GetScanner(agentID); s != nil {
		return s
	}
	// 退化为目录扫描器
	dirs, ok := knownSessionDirs[agentID]
	if !ok {
		return nil
	}
	return &dirScanner{
		name: agentID,
		dirs: dirs,
		ext:  ".jsonl",
	}
}

// --- 通用目录扫描器（退路方案） ---

// dirScanner 基于目录的通用日志扫描器
type dirScanner struct {
	name string
	dirs []string
	ext  string
}

func (d *dirScanner) Name() string { return d.name }

func (d *dirScanner) DiscoverSessions() ([]SessionRef, error) {
	var sessions []SessionRef
	for _, dir := range d.dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), d.ext) {
				continue
			}
			path := filepath.Join(dir, e.Name())
			info, _ := os.Stat(path)
			sessions = append(sessions, SessionRef{
				Harness:   d.name,
				NativeID:  strings.TrimSuffix(e.Name(), d.ext),
				Locator:   path,
				StartedAt: info.ModTime().Unix(),
			})
		}
	}
	return sessions, nil
}

func (d *dirScanner) ReadMessages(ref SessionRef, cursor int, limit int) ([]string, int, error) {
	return nil, 0, fmt.Errorf("目录扫描器不支持消息读取: %s", ref.NativeID)
}

// --- 会话目录探测（用于 Agent 发现增强） ---

// DiscoverAgentHistoryDirs 探测所有 Agent 的历史会话目录
// 返回 agentID → 目录列表
// Lzm 2026-07-10
func DiscoverAgentHistoryDirs() map[string][]string {
	result := make(map[string][]string)
	for agentID, dirs := range knownSessionDirs {
		var found []string
		for _, dir := range dirs {
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				found = append(found, dir)
			}
		}
		if len(found) > 0 {
			result[agentID] = found
		}
	}
	return result
}

// DiscoverHistoricalSessions 发现所有 Agent 的历史会话
// 按 StartedAt 降序排列
// Lzm 2026-07-10
func DiscoverHistoricalSessions(agentID string, limit int) ([]SessionRef, error) {
	scanner := ScannerFromAgentID(agentID)
	if scanner == nil {
		return nil, fmt.Errorf("没有可用的日志扫描器: %s", agentID)
	}

	sessions, err := scanner.DiscoverSessions()
	if err != nil {
		return nil, err
	}

	// 按时间降序
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartedAt > sessions[j].StartedAt
	})

	if limit > 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}

	return sessions, nil
}

// DefaultLogScannerPaths 默认日志扫描路径（供配置使用）
func DefaultLogScannerPaths() []string {
	var paths []string
	for _, dirs := range knownSessionDirs {
		paths = append(paths, dirs...)
	}
	return paths
}

// stringPtr 辅助函数
func stringPtr(s string) *string { return &s }

// int64Ptr 辅助函数
func int64Ptr(i int64) *int64 { return &i }

// init 初始化 — 注册已知 Agent 的默认 Scanner（如果有）
func init() {
	// 注册系统时间计算
	_ = time.Now
}
