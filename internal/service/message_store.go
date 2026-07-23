// -*- coding: utf-8 -*-
// Go 1.25+
//
// message_store.go
// 会话消息存储器 — 管理 Agent 会话消息的文件持久化
// 与 Python 版 registry.py 的 _save_session_messages / _load_session_messages 兼容
// 存储格式：
//   - 消息数据：~/.agent-bridge/agents/{agent_id}/messages/{session_id}.json
//   - 会话元数据：由 session.go 的 persistSession 写入 sessions/ 目录
//
// Lzm 2026-07-10

package service

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StoredMessage 持久化存储的消息
// 与 Python 版兼容：{role, text}
// Lzm 2026-07-10
type StoredMessage struct {
	Role string `json:"role"` // user | assistant | thought
	Text string `json:"text"`
}

// SessionSummary 会话概要信息
type SessionSummary struct {
	SessionID      string `json:"session_id"`
	Title          string `json:"title"`                   // 会话标题（从首条用户消息提取）
	CWD            string `json:"cwd,omitempty"`           // 会话关联的工作目录。Lzm 2026-07-21
	PermissionMode string `json:"permission_mode,omitempty"` // 会话授权模式。Lzm 2026-07-21
	CreatedAt      int64  `json:"created_at"`              // 时间戳（东八区）
	UpdatedAt      int64  `json:"updated_at"`              // 时间戳（东八区）
	MessageCount   int    `json:"message_count"`
}

// MessageStore 会话消息存储器
// 负责 Agent 会话消息的持久化读写
// Lzm 2026-07-10
type MessageStore struct {
	// storeDir 持久化根目录 (~/.agent-bridge/agents/)
	storeDir string
}

// NewMessageStore 创建消息存储器
func NewMessageStore(storeDir string) *MessageStore {
	return &MessageStore{
		storeDir: storeDir,
	}
}

// DefaultMessageStore 使用默认路径创建消息存储器
func DefaultMessageStore() *MessageStore {
	return NewMessageStore(getAgentBridgeStoreDir())
}

// getAgentBridgeStoreDir 自动检测 Agent-Bridge 数据存储根目录
// 多优先级检测（优先级从高到低）：
//
//	 1. ZLEAP_STORE_DIR 环境变量（用户显式指定，直接使用）
//	 2. {可执行文件所在目录}/data/agents/（便携模式）
//	 3. %APPDATA%/agent-bridge/agents/（Windows 标准应用数据目录）
//	 4. ~/.agent-bridge/agents/（最终 fallback）
//		5. ./data/agents（所有候选目录均不可写时的兜底路径）
//
// Lzm 2026-07-20
func getAgentBridgeStoreDir() string {
	// 优先级 1: ZLEAP_STORE_DIR 环境变量（用户显式指定，直接使用）
	if dir := os.Getenv("ZLEAP_STORE_DIR"); dir != "" {
		return dir
	}

	// 优先级 2: 可执行文件所在目录下的 data/agents/（便携模式）
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Join(filepath.Dir(exe), "data", "agents")
		if isDirWritable(dir) {
			return dir
		}
	}

	// 优先级 3: Windows APPDATA 目录（标准应用数据目录）
	if appData := os.Getenv("APPDATA"); appData != "" {
		dir := filepath.Join(appData, "agent-bridge", "agents")
		if isDirWritable(dir) {
			return dir
		}
	}

	// 优先级 4: 用户家目录（最终 fallback）
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, ".agent-bridge", "agents")
		if isDirWritable(dir) {
			return dir
		}
	}

	// 所有候选目录均不可写时，返回相对路径作为兜底，不阻断程序运行
	return filepath.Join(".", "data", "agents")
}

// GetAgentBridgeStoreDir 返回当前检测到的数据存储目录（导出版本）
// 可供外部模块在启动时记录日志
// Lzm 2026-07-20
func GetAgentBridgeStoreDir() string {
	return getAgentBridgeStoreDir()
}

// isDirWritable 检测目录是否可写
// 尝试创建目录并写入临时文件验证写入权限，最后清理临时文件
// Lzm 2026-07-20
func isDirWritable(dir string) bool {
	// 确保目录存在
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}

	// 创建临时文件验证写入权限
	tmpFile := filepath.Join(dir, ".zleap_write_test")
	if err := os.WriteFile(tmpFile, []byte("test"), 0o600); err != nil {
		return false
	}

	// 清理临时文件
	_ = os.Remove(tmpFile)
	return true
}

// getSessionFile returns the collision-resistant metadata path for a Session.
func (ms *MessageStore) getSessionFile(agentID, sessionID string) string {
	dir := filepath.Join(ms.storeDir, agentID, "sessions")
	return filepath.Join(dir, safeSessionFileID(sessionID)+".json")
}

func (ms *MessageStore) getLegacySessionFile(agentID, sessionID string) string {
	dir := filepath.Join(ms.storeDir, agentID, "sessions")
	return filepath.Join(dir, legacySessionFileID(sessionID)+".json")
}

// getMessageFile 获取消息文件路径（messages/ 目录）
// 与会话元数据分目录存储，避免文件路径冲突
// Lzm 2026-07-10
func (ms *MessageStore) getMessageFile(agentID, sessionID string) string {
	dir := filepath.Join(ms.storeDir, agentID, "messages")
	return filepath.Join(dir, safeSessionFileID(sessionID)+".json")
}

func (ms *MessageStore) getLegacyMessageFile(agentID, sessionID string) string {
	dir := filepath.Join(ms.storeDir, agentID, "messages")
	return filepath.Join(dir, legacySessionFileID(sessionID)+".json")
}

// SaveMessages 保存消息到消息文件
// 调用方传入的是新产生的消息，因此始终按顺序追加，包括内容完全相同的消息。
// 存储路径：messages/{session_id}.json（与会话元数据分目录）
// Lzm 2026-07-10
func (ms *MessageStore) SaveMessages(agentID, sessionID string, messages []StoredMessage) {
	if len(messages) == 0 {
		return
	}
	existing := ms.LoadMessages(agentID, sessionID)
	merged := make([]StoredMessage, 0, len(existing)+len(messages))
	merged = append(merged, existing...)
	merged = append(merged, messages...)
	ms.writeMessages(agentID, sessionID, merged)
}

// SaveReplayedMessages 合并 Agent 回放的历史记录。Agent 可能先重复发送文件末尾
// 已有的消息，因此这里只消除已有末尾与回放开头的最大重叠。
func (ms *MessageStore) SaveReplayedMessages(agentID, sessionID string, messages []StoredMessage) {
	if len(messages) == 0 {
		return
	}
	existing := ms.LoadMessages(agentID, sessionID)
	ms.writeMessages(agentID, sessionID, mergeMessagesAtBoundary(existing, messages))
}

func (ms *MessageStore) writeMessages(agentID, sessionID string, messages []StoredMessage) {
	filePath := ms.getMessageFile(agentID, sessionID)

	// 确保目录存在
	dir := filepath.Dir(filePath)
	if err := ensurePrivateDirectory(dir); err != nil {
		slog.Warn("创建会话目录失败",
			"agent", agentID,
			"error", err,
		)
		return
	}

	// 写入文件
	data, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		slog.Warn("序列化消息失败", "error", err)
		return
	}

	if err := writeFileAtomically(filePath, data, 0o600); err != nil {
		slog.Warn("写入消息文件失败",
			"path", filePath,
			"error", err,
		)
		return
	}

	slog.Debug("消息已持久化",
		"agent", agentID,
		"session", truncateString(sessionID, 16),
		"count", len(messages),
	)
}

// mergeMessagesAtBoundary removes the largest suffix/prefix overlap produced
// when an Agent replays already-persisted history before returning new items.
// It deliberately does not deduplicate the merged history globally: identical
// messages at different positions are valid conversation data.
func mergeMessagesAtBoundary(existing, incoming []StoredMessage) []StoredMessage {
	overlap := len(existing)
	if len(incoming) < overlap {
		overlap = len(incoming)
	}
	for overlap > 0 {
		matches := true
		for i := 0; i < overlap; i++ {
			if existing[len(existing)-overlap+i] != incoming[i] {
				matches = false
				break
			}
		}
		if matches {
			break
		}
		overlap--
	}

	merged := make([]StoredMessage, 0, len(existing)+len(incoming)-overlap)
	merged = append(merged, existing...)
	merged = append(merged, incoming[overlap:]...)
	return merged
}

// LoadMessages 从消息文件加载会话消息
// 兼容 Python 版的 _load_session_messages()
// 优先读取 messages/{session_id}.json，不存在时回退到 sessions/{session_id}.json（旧格式）
// Lzm 2026-07-13
func (ms *MessageStore) LoadMessages(agentID, sessionID string) []StoredMessage {
	type candidate struct {
		path             string
		mayBeSessionMeta bool
	}
	candidates := []candidate{
		{path: ms.getMessageFile(agentID, sessionID)},
		{path: ms.getLegacyMessageFile(agentID, sessionID)},
		{path: ms.getLegacySessionFile(agentID, sessionID), mayBeSessionMeta: true},
	}

	var data []byte
	var filePath string
	for _, item := range candidates {
		var err error
		data, err = os.ReadFile(item.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			slog.Debug("读取消息文件失败", "path", item.path, "error", err)
			return nil
		}
		filePath = item.path
		if item.mayBeSessionMeta {
			var meta StoredSession
			if err := json.Unmarshal(data, &meta); err == nil && meta.SessionID != "" {
				return nil
			}
		}
		break
	}
	if filePath == "" {
		return nil
	}

	// 兼容 Python 版输出的两种格式：
	// 1. 新版：[]StoredMessage（数组格式）
	// 2. 旧版：[]interface{}（需要二次转换）
	var rawMessages []json.RawMessage
	if err := json.Unmarshal(data, &rawMessages); err != nil {
		slog.Debug("消息文件格式异常（非数组格式）",
			"path", filePath,
			"error", err,
		)
		return nil
	}

	messages := make([]StoredMessage, 0, len(rawMessages))
	for _, raw := range rawMessages {
		var msg StoredMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			// 跳过无法解析的条目
			continue
		}
		messages = append(messages, msg)
	}

	return messages
}

// ListSessions 列出指定 Agent 的所有历史会话
// 读取 sessions/ 目录中的 StoredSession 文件
// 消息计数从 messages/ 目录获取
// 按 UpdatedAt 降序排列，limit <= 0 表示不限
// Lzm 2026-07-10
func (ms *MessageStore) ListSessions(agentID string, limit int) []SessionSummary {
	dir := filepath.Join(ms.storeDir, agentID, "sessions")

	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Debug("读取会话目录失败",
				"agent", agentID,
				"error", err,
			)
		}
		return nil
	}

	type sessionFile struct {
		entry      os.DirEntry
		path       string
		data       []byte
		stored     StoredSession
		isMetadata bool
	}
	files := make([]sessionFile, 0, len(entries))
	canonicalLegacyFiles := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		item := sessionFile{entry: entry, path: path, data: data}
		if err := json.Unmarshal(data, &item.stored); err == nil {
			item.isMetadata = true
			if item.stored.SessionID != "" {
				canonicalLegacyFiles[legacySessionFileID(item.stored.SessionID)+".json"] = struct{}{}
			}
		}
		files = append(files, item)
	}

	bySessionID := make(map[string]SessionSummary)
	addSummary := func(summary SessionSummary) {
		if summary.SessionID == "" {
			return
		}
		current, exists := bySessionID[summary.SessionID]
		if !exists || summary.UpdatedAt > current.UpdatedAt {
			bySessionID[summary.SessionID] = summary
		}
	}
	for _, item := range files {
		// 尝试解析为 StoredSession 格式（会话元数据）
		if !item.isMetadata {
			// 兼容旧格式：尝试解析为 []StoredMessage（消息数组）
			var messages []StoredMessage
			if err := json.Unmarshal(item.data, &messages); err != nil {
				continue
			}
			// Canonical metadata carries the opaque Session ID. A legacy Python
			// array named from that ID is its message fallback, not a second
			// filename-derived Session (for example a/b must not also list a_b).
			if _, owned := canonicalLegacyFiles[item.entry.Name()]; owned {
				continue
			}
			// 旧格式：用 mtime 作为时间，消息数组长度作为计数
			info, err := os.Stat(item.path)
			if err != nil {
				continue
			}
			addSummary(SessionSummary{
				SessionID:    strings.TrimSuffix(item.entry.Name(), ".json"),
				MessageCount: len(messages),
				UpdatedAt:    info.ModTime().Unix(),
				CreatedAt:    info.ModTime().Unix(),
			})
			continue
		}

		// 新格式 StoredSession：读取消息文件获取消息数
		msgCount := ms.countMessages(agentID, item.stored.SessionID)
		title := item.stored.Title
		if title == "" {
			title = ms.firstUserMessage(agentID, item.stored.SessionID)
		}
		addSummary(SessionSummary{
			SessionID:      item.stored.SessionID,
			Title:          title,
			CWD:            item.stored.CWD,
			PermissionMode: item.stored.PermissionMode,
			MessageCount:   msgCount,
			UpdatedAt:      item.stored.UpdatedAt.Unix(),
			CreatedAt:      item.stored.CreatedAt.Unix(),
		})
	}
	summaries := make([]SessionSummary, 0, len(bySessionID))
	for _, summary := range bySessionID {
		summaries = append(summaries, summary)
	}

	// 按 UpdatedAt 降序排列；持久化时间只有秒级精度，因此用 Session ID
	// 作为稳定的次级键，确保同秒更新时分页和 limit 结果不会随机变化。
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].UpdatedAt != summaries[j].UpdatedAt {
			return summaries[i].UpdatedAt > summaries[j].UpdatedAt
		}
		return summaries[i].SessionID < summaries[j].SessionID
	})

	if limit > 0 && len(summaries) > limit {
		summaries = summaries[:limit]
	}

	return summaries
}

// countMessages 统计 messages/{session_id}.json 中的消息数
// Lzm 2026-07-10
func (ms *MessageStore) countMessages(agentID, sessionID string) int {
	return len(ms.LoadMessages(agentID, sessionID))
}

// firstUserMessage 提取会话中首条用户消息作为标题
// 取首行、去换行、截断至 50 字
// Lzm 2026-07-20
func (ms *MessageStore) firstUserMessage(agentID, sessionID string) string {
	messages := ms.LoadMessages(agentID, sessionID)
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		text := strings.TrimSpace(msg.Text)
		// 只取首行
		if idx := strings.Index(text, "\n"); idx > 0 {
			text = text[:idx]
		}
		text = strings.TrimSpace(text)
		if len(text) > 50 {
			text = text[:50] + "..."
		}
		return text
	}
	return ""
}

// DeleteSession 删除指定会话文件和消息文件
func (ms *MessageStore) DeleteSession(agentID, sessionID string) error {
	paths := []string{
		ms.getSessionFile(agentID, sessionID),
		ms.getMessageFile(agentID, sessionID),
		ms.getLegacySessionFile(agentID, sessionID),
		ms.getLegacyMessageFile(agentID, sessionID),
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// GetAllSessions 列出所有 Agent 的会话
// Lzm 2026-07-10
func (ms *MessageStore) GetAllSessions() map[string][]SessionSummary {
	result := make(map[string][]SessionSummary)

	agentsDir, err := os.ReadDir(ms.storeDir)
	if err != nil {
		return result
	}

	for _, entry := range agentsDir {
		if !entry.IsDir() {
			continue
		}
		sessions := ms.ListSessions(entry.Name(), 0)
		if len(sessions) > 0 {
			result[entry.Name()] = sessions
		}
	}

	return result
}
