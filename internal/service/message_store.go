// -*- coding: utf-8 -*-
// Go 1.26+
//
// message_store.go
// 会话消息存储器 — 管理 Agent 会话消息的文件持久化
// 与 Python 版 registry.py 的 _save_session_messages / _load_session_messages 兼容
// 存储格式：
//   - 消息数据：~/.zleap/agents/{agent_id}/messages/{session_id}.json
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
	"time"
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
	SessionID    string `json:"session_id"`
	CreatedAt    int64  `json:"created_at"` // 时间戳（东八区）
	UpdatedAt    int64  `json:"updated_at"` // 时间戳（东八区）
	MessageCount int    `json:"message_count"`
}

// MessageStore 会话消息存储器
// 负责 Agent 会话消息的持久化读写
// Lzm 2026-07-10
type MessageStore struct {
	// storeDir 持久化根目录 (~/.zleap/agents/)
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
	home, _ := os.UserHomeDir()
	storeDir := filepath.Join(home, ".zleap", "agents")
	return NewMessageStore(storeDir)
}

// getSessionFile 获取会话消息文件路径（session/ 目录）
// 兼容 Python 版的 _get_session_file()：将特殊字符替换为 _
func (ms *MessageStore) getSessionFile(agentID, sessionID string) string {
	dir := filepath.Join(ms.storeDir, agentID, "sessions")
	safeID := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_",
	).Replace(sessionID)
	return filepath.Join(dir, safeID+".json")
}

// getMessageFile 获取消息文件路径（messages/ 目录）
// 与会话元数据分目录存储，避免文件路径冲突
// Lzm 2026-07-10
func (ms *MessageStore) getMessageFile(agentID, sessionID string) string {
	dir := filepath.Join(ms.storeDir, agentID, "messages")
	safeID := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_",
	).Replace(sessionID)
	return filepath.Join(dir, safeID+".json")
}

// SaveMessages 保存消息到消息文件
// 去重策略：按 (role, text) 二元组去重（与 Python 版一致）
// 存储路径：messages/{session_id}.json（与会话元数据分目录）
// Lzm 2026-07-10
func (ms *MessageStore) SaveMessages(agentID, sessionID string, messages []StoredMessage) {
	if len(messages) == 0 {
		return
	}

	filePath := ms.getMessageFile(agentID, sessionID)

	// 确保目录存在
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		slog.Warn("创建会话目录失败",
			"agent", agentID,
			"error", err,
		)
		return
	}

	// 读取已有消息
	existing := ms.LoadMessages(agentID, sessionID)

	// 合并：已有 + 新增
	merged := append(existing, messages...)

	// 去重：按 (role, text) 去重
	seen := make(map[string]bool)
	unique := make([]StoredMessage, 0, len(merged))
	for _, m := range merged {
		key := m.Role + "\x00" + m.Text
		if !seen[key] {
			seen[key] = true
			unique = append(unique, m)
		}
	}

	// 写入文件
	data, err := json.MarshalIndent(unique, "", "  ")
	if err != nil {
		slog.Warn("序列化消息失败", "error", err)
		return
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		slog.Warn("写入消息文件失败",
			"path", filePath,
			"error", err,
		)
		return
	}

	slog.Debug("消息已持久化",
		"agent", agentID,
		"session", sessionID[:16]+"...",
		"count", len(unique),
	)
}

// LoadMessages 从消息文件加载会话消息
// 兼容 Python 版的 _load_session_messages()
// 优先读取 messages/{session_id}.json，不存在时回退到 sessions/{session_id}.json（旧格式）
// Lzm 2026-07-13
func (ms *MessageStore) LoadMessages(agentID, sessionID string) []StoredMessage {
	// 优先读取 messages/ 目录
	filePath := ms.getMessageFile(agentID, sessionID)
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// messages/ 不存在，回退到 sessions/ 目录（旧格式兼容）
			filePath = ms.getSessionFile(agentID, sessionID)
			data, err = os.ReadFile(filePath)
			if err != nil {
				if !os.IsNotExist(err) {
					slog.Debug("读取旧消息文件失败",
						"path", filePath,
						"error", err,
					)
				}
				return nil
			}
			// sessions/{sessionId}.json 可能是 StoredSession 元数据（非消息）
			// 先尝试解析为 StoredSession，成功则说明是元数据文件，返回空消息
			var meta StoredSession
			if err := json.Unmarshal(data, &meta); err == nil && meta.SessionID != "" {
				slog.Debug("会话文件为元数据格式，无消息数据",
					"path", filePath,
					"session_id", meta.SessionID,
				)
				return nil
			}
		} else {
			slog.Debug("读取消息文件失败",
				"path", filePath,
				"error", err,
			)
			return nil
		}
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

	var summaries []SessionSummary
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		// 尝试解析为 StoredSession 格式（会话元数据）
		var stored StoredSession
		if err := json.Unmarshal(data, &stored); err != nil {
			// 兼容旧格式：尝试解析为 []StoredMessage（消息数组）
			var messages []StoredMessage
			if err2 := json.Unmarshal(data, &messages); err2 != nil {
				continue
			}
			// 旧格式：用 mtime 作为时间，消息数组长度作为计数
			info, err2 := os.Stat(path)
			if err2 != nil {
				continue
			}
			summaries = append(summaries, SessionSummary{
				SessionID:    strings.TrimSuffix(entry.Name(), ".json"),
				MessageCount: len(messages),
				UpdatedAt:    info.ModTime().Unix(),
				CreatedAt:    info.ModTime().Unix(),
			})
			continue
		}

		// 新格式 StoredSession：读取消息文件获取消息数
		msgCount := ms.countMessages(agentID, stored.SessionID)
		summaries = append(summaries, SessionSummary{
			SessionID:    stored.SessionID,
			MessageCount: msgCount,
			UpdatedAt:    stored.UpdatedAt.Unix(),
			CreatedAt:    stored.CreatedAt.Unix(),
		})
	}

	// 按 UpdatedAt 降序排列
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})

	if limit > 0 && len(summaries) > limit {
		summaries = summaries[:limit]
	}

	return summaries
}

// countMessages 统计 messages/{session_id}.json 中的消息数
// Lzm 2026-07-10
func (ms *MessageStore) countMessages(agentID, sessionID string) int {
	data, err := os.ReadFile(ms.getMessageFile(agentID, sessionID))
	if err != nil {
		return 0
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return 0
	}
	return len(raw)
}

// DeleteSession 删除指定会话文件和消息文件
func (ms *MessageStore) DeleteSession(agentID, sessionID string) error {
	// 删除会话元数据文件
	sessionPath := ms.getSessionFile(agentID, sessionID)
	if err := os.Remove(sessionPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	// 删除消息文件
	msgPath := ms.getMessageFile(agentID, sessionID)
	if err := os.Remove(msgPath); err != nil && !os.IsNotExist(err) {
		return err
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

// getMessageFileInfo 获取消息文件的元信息（用于 SessionManager 兼容）
// Lzm 2026-07-10
func (ms *MessageStore) getMessageFileInfo(agentID, sessionID string) (msgCount int, modTime time.Time) {
	messages := ms.LoadMessages(agentID, sessionID)
	if messages == nil {
		return 0, time.Time{}
	}

	filePath := ms.getMessageFile(agentID, sessionID)
	info, err := os.Stat(filePath)
	if err != nil {
		return len(messages), time.Now()
	}

	return len(messages), info.ModTime()
}
