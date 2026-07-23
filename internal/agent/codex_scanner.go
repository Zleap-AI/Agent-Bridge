// -*- coding: utf-8 -*-
// Go 1.25+
//
// codex_scanner.go
// CodexScanner — Codex CLI 原生会话日志扫描器
// 解析 ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl 格式
// 提取会话元数据和消息，实现 LogScanner 接口
//
// Lzm 2026-07-22

package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// codexSessionEventTypeMeta 会话元数据事件
	codexSessionEventTypeMeta = "session_meta"
	// codexSessionEventTypeEvent 事件消息
	codexSessionEventTypeEvent = "event_msg"
	// codexSessionEventTypeResponse 响应项
	codexSessionEventTypeResponse = "response_item"
	// codexSessionEventTypeContext 上下文
	codexSessionEventTypeContext = "turn_context"
)

// CodexScanner 实现 LogScanner 接口，用于扫描 Codex 原生会话
// Lzm 2026-07-22
type CodexScanner struct {
	sessionsDir string
}

// getCodexSessionsDir 获取 Codex 会话目录路径
// 优先级：
//   1. CODEX_HOME 环境变量（兼容 Codex IDE 的重定向）
//   2. CHATGPT_HOME 环境变量（兼容品牌重命名后的路径）
//   3. ~/.codex/sessions（默认路径）
// Lzm 2026-07-22
func getCodexSessionsDir() string {
	// 优先级 1: CODEX_HOME 环境变量
	if home := os.Getenv("CODEX_HOME"); home != "" {
		sessionsDir := filepath.Join(home, "sessions")
		if info, err := os.Stat(sessionsDir); err == nil && info.IsDir() {
			return sessionsDir
		}
	}

	// 优先级 2: CHATGPT_HOME 环境变量（品牌重命名兼容）
	if home := os.Getenv("CHATGPT_HOME"); home != "" {
		sessionsDir := filepath.Join(home, "sessions")
		if info, err := os.Stat(sessionsDir); err == nil && info.IsDir() {
			slog.Debug("使用 CHATGPT_HOME 会话目录", "dir", sessionsDir)
			return sessionsDir
		}
	}

	// 优先级 3: ~/.codex/sessions（默认路径）
	home, err := os.UserHomeDir()
	if err == nil {
		return filepath.Join(home, ".codex", "sessions")
	}
	return ""
}

// NewCodexScanner 创建 Codex 会话扫描器
// sessionsDir: Codex 会话目录，通常为 ~/.codex/sessions
// Lzm 2026-07-22
func NewCodexScanner(sessionsDir string) *CodexScanner {
	if sessionsDir == "" {
		sessionsDir = getCodexSessionsDir()
	}
	return &CodexScanner{sessionsDir: sessionsDir}
}

// Name 返回适配器名称
// Lzm 2026-07-22
func (s *CodexScanner) Name() string { return "codex" }

// codexEvent 通用 JSONL 事件结构
// Lzm 2026-07-22
type codexEvent struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// codexSessionMeta 会话元数据
// Lzm 2026-07-22
type codexSessionMeta struct {
	SessionID      string `json:"session_id"`
	ID             string `json:"id"`
	Timestamp      string `json:"timestamp"`
	CWD            string `json:"cwd"`
	CliVersion     string `json:"cli_version"`
	ModelProvider  string `json:"model_provider"`
	Originator     string `json:"originator"`
	Source         string `json:"source"`
}

// codexResponseItem 响应消息项
// Lzm 2026-07-22
type codexResponseItemPayload struct {
	Type    string             `json:"type"`
	Role    string             `json:"role"`
	Content []codexContentItem `json:"content"`
}

// codexContentItem 消息内容项
// Lzm 2026-07-22
type codexContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// --- session_index.jsonl 索引（前置于 DiscoverSessions） ---

// sessionIndexEntry session_index.jsonl 的单行条目
// Lzm 2026-07-22
type sessionIndexEntry struct {
	SessionID string `json:"session_id"`
	Timestamp string `json:"timestamp"`
	Title     string `json:"title"`
}

// sessionIndexPath 返回 session_index.jsonl 的完整路径
// 索引文件位于 sessionsDir 的父目录（~/.codex/session_index.jsonl）
// Lzm 2026-07-22
func (s *CodexScanner) sessionIndexPath() string {
	return filepath.Join(filepath.Dir(s.sessionsDir), "session_index.jsonl")
}

// ParseSessionIndex 解析 session_index.jsonl，返回会话引用列表
// 索引文件提供了轻量的会话发现方式，无需遍历所有 rollout JSONL。
// Lzm 2026-07-22
func (s *CodexScanner) ParseSessionIndex() ([]SessionRef, error) {
	return s.parseSessionIndex()
}

// parseSessionIndex 内部实现：解析 session_index.jsonl
// 读取索引文件 → 解析条目 → 推导 rollout 路径 → 验证文件存在
// → 从 rollout 文件补充详细元数据（cwd/originator/message_count）
// → 返回 SessionRef
// Lzm 2026-07-22
func (s *CodexScanner) parseSessionIndex() ([]SessionRef, error) {
	indexPath := s.sessionIndexPath()
	f, err := os.Open(indexPath)
	if err != nil {
		return nil, fmt.Errorf("打开索引文件失败: %w", err)
	}
	defer f.Close()

	var sessions []SessionRef
	scanner := bufio.NewScanner(f)
	maxLineSize := 512 * 1024 // 512KB
	scanner.Buffer(make([]byte, maxLineSize), maxLineSize)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry sessionIndexEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			slog.Debug("CodexScanner 解析索引行失败",
				"line", truncateString(line, 200),
				"error", err,
			)
			continue
		}
		if entry.SessionID == "" {
			continue
		}

		// 解析时间戳
		var createdAt time.Time
		if entry.Timestamp != "" {
			createdAt, _ = time.Parse(time.RFC3339Nano, entry.Timestamp)
		}
		if createdAt.IsZero() {
			createdAt = time.Now()
		}

		// 生成 rollout JSONL 路径并验证文件存在
		rolloutPath := s.codexSessionFilePath(entry.SessionID, createdAt)
		if _, err := os.Stat(rolloutPath); err != nil {
			slog.Debug("CodexScanner 索引条目对应的 rollout 文件不存在",
				"session_id", truncateSessionID(entry.SessionID),
				"path", rolloutPath,
			)
			continue
		}

		ref := SessionRef{
			Harness:   "codex",
			NativeID:  entry.SessionID,
			Locator:   rolloutPath,
			Title:     entry.Title,
			StartedAt: createdAt.Unix(),
			Meta:      make(map[string]string),
		}

		// 从 rollout 文件补充详细元数据（轻量：只解析 session_meta 首事件 + 消息计数）
		if enrichMeta := parseRolloutQuickMeta(rolloutPath); len(enrichMeta) > 0 {
			ref.Meta = enrichMeta
		}

		sessions = append(sessions, ref)
	}

	if err := scanner.Err(); err != nil {
		return sessions, fmt.Errorf("扫描索引文件出错: %w", err)
	}

	return sessions, nil
}

// parseRolloutQuickMeta 快速从 rollout JSONL 文件中提取会话元数据
// 读取 session_meta 事件提取 cwd/originator 等 + 统计 response_item 消息数
// 轻量实现：单次文件扫描同时完成两种提取，避免多次 I/O
// Lzm 2026-07-22
func parseRolloutQuickMeta(rolloutPath string) map[string]string {
	meta := make(map[string]string)
	f, err := os.Open(rolloutPath)
	if err != nil {
		return meta
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	maxLineSize := 512 * 1024
	scanner.Buffer(make([]byte, maxLineSize), maxLineSize)

	msgCount := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event codexEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		switch event.Type {
		case codexSessionEventTypeMeta:
			// 只解析第一个 session_meta 事件（只出现一次）
			if meta["cwd"] == "" {
				var sessionMeta codexSessionMeta
				if err := json.Unmarshal(event.Payload, &sessionMeta); err == nil {
					if sessionMeta.CWD != "" {
						meta["cwd"] = sessionMeta.CWD
					}
					if sessionMeta.CliVersion != "" {
						meta["cli_version"] = sessionMeta.CliVersion
					}
					if sessionMeta.ModelProvider != "" {
						meta["model_provider"] = sessionMeta.ModelProvider
					}
					if sessionMeta.Originator != "" {
						meta["originator"] = sessionMeta.Originator
					}
					if sessionMeta.Source != "" {
						meta["source"] = sessionMeta.Source
					}
				}
			}
		case codexSessionEventTypeResponse:
			msgCount++
		}
	}
	meta["message_count"] = fmt.Sprintf("%d", msgCount)
	return meta
}

// DiscoverSessions 发现 Codex 所有历史会话
// 策略：
//   1. 优先使用 session_index.jsonl（轻量、快速）
//   2. 降级为目录扫描（兼容旧版本 Codex 或索引损坏的场景）
// 返回按 StartedAt 降序排列的会话列表
// Lzm 2026-07-22
func (s *CodexScanner) DiscoverSessions() ([]SessionRef, error) {
	if s.sessionsDir == "" {
		return nil, fmt.Errorf("Codex 会话目录未设置")
	}

	info, err := os.Stat(s.sessionsDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("Codex 会话目录不可用: %s", s.sessionsDir)
	}

	// 策略 1：优先使用 session_index.jsonl
	indexSessions, idxErr := s.parseSessionIndex()
	if idxErr == nil && len(indexSessions) > 0 {
		sort.Slice(indexSessions, func(i, j int) bool {
			return indexSessions[i].StartedAt > indexSessions[j].StartedAt
		})
		slog.Debug("CodexScanner 使用 session_index.jsonl",
			"count", len(indexSessions),
			"index_path", s.sessionIndexPath(),
		)
		return indexSessions, nil
	}
	if idxErr != nil {
		slog.Debug("CodexScanner 索引文件不可用，降级为目录扫描",
			"error", idxErr,
		)
	}

	// 策略 2：降级为目录扫描（遍历 rollout JSONL 文件）
	var jsonlFiles []string
	err = filepath.WalkDir(s.sessionsDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			jsonlFiles = append(jsonlFiles, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("扫描 Codex 会话目录失败: %w", err)
	}

	slog.Debug("CodexScanner 发现 JSONL 文件",
		"dir", s.sessionsDir,
		"count", len(jsonlFiles),
	)

	sessions := make([]SessionRef, 0, len(jsonlFiles))
	for _, jsonlPath := range jsonlFiles {
		ref, err := s.parseSessionJSONL(jsonlPath)
		if err != nil {
			slog.Debug("CodexScanner 解析会话文件失败",
				"path", jsonlPath,
				"error", err,
			)
			continue
		}
		sessions = append(sessions, ref)
	}

	// 按时间降序排列
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartedAt > sessions[j].StartedAt
	})

	return sessions, nil
}

// parseSessionJSONL 解析单个 JSONL 文件，提取会话元数据
// Lzm 2026-07-22
func (s *CodexScanner) parseSessionJSONL(jsonlPath string) (SessionRef, error) {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return SessionRef{}, fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()

	ref := SessionRef{
		Harness: "codex",
		Locator: jsonlPath,
		Meta:    make(map[string]string),
	}

	scanner := bufio.NewScanner(f)
	// JSONL 单行可能较长，增大 buffer（Codex JSONL 有些行包含完整 world_state）
	maxLineSize := 512 * 1024 // 512KB
	scanner.Buffer(make([]byte, maxLineSize), maxLineSize)

	lineNum := 0
	firstUserText := ""
	messageCount := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var event codexEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		switch event.Type {
		case codexSessionEventTypeMeta:
			var meta codexSessionMeta
			if err := json.Unmarshal(event.Payload, &meta); err != nil {
				continue
			}
			ref.NativeID = meta.SessionID
			ref.Meta["cwd"] = meta.CWD
			ref.Meta["cli_version"] = meta.CliVersion
			ref.Meta["model_provider"] = meta.ModelProvider
			ref.Meta["originator"] = meta.Originator
			ref.Meta["source"] = meta.Source

			// 解析时间戳
			if meta.Timestamp != "" {
				if t, err := time.Parse(time.RFC3339Nano, meta.Timestamp); err == nil {
					ref.StartedAt = t.Unix()
				}
			}

		case codexSessionEventTypeResponse:
			var item codexResponseItemPayload
			if err := json.Unmarshal(event.Payload, &item); err != nil {
				continue
			}
			// 统计用户和助手的消息数
			if item.Role == "user" || item.Role == "assistant" {
				messageCount++
				// 提取第一条真实用户消息作为标题
				// Codex 经常注入 <environment_context> XML 作为首条 user 消息，
				// 需要过滤这些系统注入内容
				if item.Role == "user" && firstUserText == "" {
					firstUserText = extractUserMessage(item.Content)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Debug("CodexScanner 读取文件出错",
			"path", jsonlPath,
			"error", err,
		)
	}

	// 回退时间：使用文件修改时间
	if ref.StartedAt == 0 {
		if fi, err := os.Stat(jsonlPath); err == nil {
			ref.StartedAt = fi.ModTime().Unix()
		}
	}

	// 设置标题
	if firstUserText != "" {
		ref.Title = firstUserText
	}
	ref.Meta["message_count"] = fmt.Sprintf("%d", messageCount)

	return ref, nil
}

// extractUserMessage 从 content 数组中提取用户消息文本
// 跳过 Codex 注入的 XML 环境上下文（以 < 开头的内容）
// Lzm 2026-07-22
func extractUserMessage(content []codexContentItem) string {
	for _, item := range content {
		if item.Type == "input_text" && item.Text != "" {
			text := strings.TrimSpace(item.Text)
			// 跳过 Codex 注入的 XML 环境上下文（<environment_context> 等）
			if strings.HasPrefix(text, "<") {
				continue
			}
			// 截取前 60 字符作为标题
			if len([]rune(text)) > 60 {
				text = string([]rune(text)[:60]) + "..."
			}
			return text
		}
	}
	return ""
}

// ReadMessages 读取指定 Codex 会话的消息
// 将 JSONL 事件桥转为 role/text 对
// Lzm 2026-07-22
func (s *CodexScanner) ReadMessages(ref SessionRef, cursor int, limit int) ([]string, int, error) {
	f, err := os.Open(ref.Locator)
	if err != nil {
		return nil, 0, fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()

	var messages []string
	scanner := bufio.NewScanner(f)
	maxLineSize := 512 * 1024
	scanner.Buffer(make([]byte, maxLineSize), maxLineSize)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var event codexEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		if event.Type != codexSessionEventTypeResponse {
			continue
		}

		var item codexResponseItemPayload
		if err := json.Unmarshal(event.Payload, &item); err != nil {
			continue
		}

		// 只提取用户和助理的消息
		if item.Role != "user" && item.Role != "assistant" {
			continue
		}

		text := extractFullText(item.Content)
		if text == "" {
			continue
		}

		// 跳过 Codex 注入的环境上下文消息（以 XML 标签开头）
		if item.Role == "user" && strings.HasPrefix(text, "<") {
			continue
		}

		msg, _ := json.Marshal(map[string]string{
			"role": item.Role,
			"text": text,
		})
		messages = append(messages, string(msg))
	}

	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("读取文件出错: %w", err)
	}

	return messages, len(messages), nil
}

// extractFullText 从 content 数组中提取完整文本
// Lzm 2026-07-22
func extractFullText(content []codexContentItem) string {
	var parts []string
	for _, item := range content {
		if item.Text != "" {
			parts = append(parts, item.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// --- 原生会话写入（NativeSessionWriter） ---

// codexSessionFilePath 生成 Codex 风格会话文件路径
// 格式：~/.codex/sessions/YYYY/MM/DD/rollout-{timestamp}-{session_id}.jsonl
// Lzm 2026-07-22
func (s *CodexScanner) codexSessionFilePath(sessionID string, createdAt time.Time) string {
	dateDir := createdAt.Format("2006/01/02")
	timeStr := createdAt.Format("2006-01-02T15-04-05")
	return filepath.Join(
		s.sessionsDir,
		dateDir,
		fmt.Sprintf("rollout-%s-%s.jsonl", timeStr, sessionID),
	)
}

// WriteSessionMeta 将会话元数据写入 Codex 原生 JSONL 格式
// 同时更新 session_index.jsonl 索引
// 实现 NativeSessionWriter 接口
// Lzm 2026-07-22
func (s *CodexScanner) WriteSessionMeta(sessionID, cwd string, createdAt time.Time) error {
	path := s.codexSessionFilePath(sessionID, createdAt)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建 Codex 会话目录失败: %w", err)
	}

	ts := createdAt.Format(time.RFC3339Nano)
	metaPayload, _ := json.Marshal(map[string]interface{}{
		"session_id":     sessionID,
		"id":             sessionID,
		"timestamp":      ts,
		"cwd":            cwd,
		"originator":     "zleap-bridge",
		"cli_version":    "1.0.0",
		"source":         "acp",
		"model_provider": "zleap-bridge",
	})
	line, _ := json.Marshal(map[string]interface{}{
		"timestamp": ts,
		"type":      "session_meta",
		"payload":   json.RawMessage(metaPayload),
	})
	line = append(line, '\n')

	if err := os.WriteFile(path, line, 0644); err != nil {
		return fmt.Errorf("写入 Codex 会话文件失败: %w", err)
	}

	// 同步更新 session_index.jsonl（追加索引条目）
	if err := s.appendSessionIndexEntry(sessionID, ts, ""); err != nil {
		slog.Warn("更新 session_index.jsonl 失败",
			"session_id", truncateSessionID(sessionID),
			"error", err,
		)
		// 索引写入失败不阻断主流程，仅记录警告
	}

	slog.Debug("Codex 原生会话已创建",
		"session_id", truncateSessionID(sessionID),
		"path", path,
	)
	return nil
}

// appendSessionIndexEntry 追加条目到 session_index.jsonl
// 格式：{"session_id":"...","timestamp":"...","title":"..."}
// 如果索引文件不存在则自动创建
// Lzm 2026-07-22
func (s *CodexScanner) appendSessionIndexEntry(sessionID, timestamp, title string) error {
	indexPath := s.sessionIndexPath()
	indexDir := filepath.Dir(indexPath)

	// 确保目录存在（~/.codex/）
	if err := os.MkdirAll(indexDir, 0755); err != nil {
		return fmt.Errorf("创建索引目录失败: %w", err)
	}

	entry := sessionIndexEntry{
		SessionID: sessionID,
		Timestamp: timestamp,
		Title:     title,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("序列化索引条目失败: %w", err)
	}
	line = append(line, '\n')

	// 追加写入（文件不存在则创建）
	f, err := os.OpenFile(indexPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("打开索引文件失败: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("写入索引条目失败: %w", err)
	}

	slog.Debug("session_index.jsonl 已更新",
		"session_id", truncateSessionID(sessionID),
		"index_path", indexPath,
	)
	return nil
}

// WriteMessages 将消息追加到 Codex 原生 JSONL
// 实现 NativeSessionWriter 接口
// Lzm 2026-07-22
func (s *CodexScanner) WriteMessages(sessionID string, msgs []NativeMessage, createdAt time.Time) error {
	if len(msgs) == 0 {
		return nil
	}

	path := s.codexSessionFilePath(sessionID, createdAt)
	// 检查文件是否存在（由 WriteSessionMeta 创建）
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("Codex 会话文件不存在，必须先调用 WriteSessionMeta: %s", path)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("打开 Codex 会话文件失败: %w", err)
	}
	defer f.Close()

	now := time.Now()
	ts := now.Format(time.RFC3339Nano)

	for _, msg := range msgs {
		// 跳过 system / thought 消息（Codex 只存 user/assistant）
		role := msg.Role
		if role != "user" && role != "assistant" {
			continue
		}

		content, _ := json.Marshal([]map[string]string{
			{"type": "input_text", "text": msg.Text},
		})
		payload, _ := json.Marshal(map[string]interface{}{
			"type":    "message",
			"role":    role,
			"content": json.RawMessage(content),
		})
		line, _ := json.Marshal(map[string]interface{}{
			"timestamp": ts,
			"type":      "response_item",
			"payload":   json.RawMessage(payload),
		})
		line = append(line, '\n')

		if _, err := f.Write(line); err != nil {
			return fmt.Errorf("追加消息到 Codex 会话文件失败: %w", err)
		}
	}

	slog.Debug("Codex 原生会话消息已同步",
		"session_id", truncateSessionID(sessionID),
		"count", len(msgs),
	)
	return nil
}

// 确保 CodexScanner 实现了 NativeSessionWriter
var _ NativeSessionWriter = (*CodexScanner)(nil)

// 自注册到 LogScanner 注册中心（类似 @register_source 装饰器）
// Lzm 2026-07-22
func init() {
	sessionsDir := getCodexSessionsDir()
	if sessionsDir == "" {
		return
	}
	if info, err := os.Stat(sessionsDir); err == nil && info.IsDir() {
		RegisterScanner(NewCodexScanner(sessionsDir))
		slog.Debug("CodexScanner 已注册", "dir", sessionsDir)
	}
}

// 确保 CodexScanner 实现了 LogScanner 接口
var _ LogScanner = (*CodexScanner)(nil)

// truncateSessionID 安全截取会话 ID 前 16 位用于日志显示
// Lzm 2026-07-22
func truncateSessionID(id string) string {
	if len(id) > 16 {
		return id[:16] + "..."
	}
	return id
}

// 确保 CodexScanner 实现了 NativeSessionWriter
