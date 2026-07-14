// -*- coding: utf-8 -*-
// Go 1.25+
//
// session.go
// 会话管理器 — 管理 Agent 的 ACP 会话生命周期
// 支持：创建新会话、加载已有会话、自动重试
//
// Lzm 2026-07-09

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
)

// SessionManager 管理所有 Agent 的 ACP 会话
type SessionManager struct {
	registry *agent.AgentRegistry

	// agentID → sessionID 映射
	sessions map[string]string
	mu       sync.RWMutex
	// Session recovery/creation may call an Agent and touch disk, so it cannot
	// hold mu. A per-Agent operation lock makes that full check-and-act sequence
	// atomic without making unrelated Agents wait for one another.
	agentSessionLocks sync.Map // map[string]*sync.Mutex

	// 会话持久化路径
	storeDir string

	// 消息存储器（持久化会话消息）
	msgStore  *MessageStore
	messageMu sync.RWMutex
}

// StoredSession 持久化存储的会话信息
type StoredSession struct {
	AgentID   string    `json:"agent_id"`
	SessionID string    `json:"session_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewSessionManager 创建会话管理器
func NewSessionManager(registry *agent.AgentRegistry) *SessionManager {
	home, _ := os.UserHomeDir()
	storeDir := filepath.Join(home, ".agent-bridge", "agents")
	return newSessionManagerWithStoreDir(registry, storeDir)
}

func newSessionManagerWithStoreDir(registry *agent.AgentRegistry, storeDir string) *SessionManager {
	return &SessionManager{
		registry: registry,
		sessions: make(map[string]string),
		storeDir: storeDir,
		msgStore: NewMessageStore(storeDir),
	}
}

// CreateNewSession always asks the Agent for a fresh session. It deliberately
// does not inspect the active or persisted session, unlike GetOrCreateSession.
func (sm *SessionManager) CreateNewSession(ctx context.Context, agentID string) (string, error) {
	unlock := sm.lockAgentSession(agentID)
	defer unlock()
	return sm.createSessionWithRetry(ctx, agentID)
}

// ActivateSession records a session that has already been loaded by the Agent.
// This keeps explicit session/load separate from startup recovery.
func (sm *SessionManager) ActivateSession(agentID, sessionID string) {
	sm.mu.Lock()
	sm.sessions[agentID] = sessionID
	sm.mu.Unlock()
}

// GetOrCreateSession 获取 Agent 的当前会话，不存在则创建
// 先尝试加载持久化的会话，无效则创建新会话
// Lzm 2026-07-09
func (sm *SessionManager) GetOrCreateSession(ctx context.Context, agentID string) (string, error) {
	unlock := sm.lockAgentSession(agentID)
	defer unlock()

	// 1. 检查内存中是否有会话
	sm.mu.RLock()
	sid, exists := sm.sessions[agentID]
	sm.mu.RUnlock()

	if exists && sid != "" {
		return sid, nil
	}

	// 2. 尝试从磁盘恢复
	sid = sm.loadStoredSession(agentID)
	if sid != "" {
		// 尝试加载到 Agent
		a := sm.registry.Get(agentID)
		if a != nil {
			if err := a.LoadSession(ctx, sid); err == nil {
				sm.mu.Lock()
				sm.sessions[agentID] = sid
				sm.mu.Unlock()
				slog.Info("会话已从磁盘恢复",
					"agent", agentID,
					"session_id", sid,
				)
				return sid, nil
			}
			// 会话无效，继续创建新会话
			slog.Debug("磁盘会话无效，创建新会话",
				"agent", agentID,
				"session_id", sid,
			)
		}
	}

	// 3. 创建新会话（带重试）
	return sm.createSessionWithRetry(ctx, agentID)
}

func (sm *SessionManager) lockAgentSession(agentID string) func() {
	value, _ := sm.agentSessionLocks.LoadOrStore(agentID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

// createSessionWithRetry 创建新会话，最多重试 3 次
// Lzm 2026-07-09
func (sm *SessionManager) createSessionWithRetry(ctx context.Context, agentID string) (string, error) {
	a := sm.registry.Get(agentID)
	if a == nil {
		return "", fmt.Errorf("未知 Agent: %s", agentID)
	}

	var lastErr error
	for i := 0; i < 3; i++ {
		if i > 0 {
			// 指数退避
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("创建会话被取消: %w", ctx.Err())
			case <-time.After(time.Duration(i) * time.Second):
			}
		}

		sid, err := a.NewSession(ctx)
		if err == nil && sid != "" {
			// 保存会话
			sm.mu.Lock()
			sm.sessions[agentID] = sid
			sm.mu.Unlock()

			sm.persistSession(agentID, sid)
			slog.Info("新会话创建成功",
				"agent", agentID,
				"session_id", sid,
			)
			return sid, nil
		}
		if err == nil {
			err = fmt.Errorf("Agent 返回了空 Session ID")
		}
		lastErr = err
		slog.Warn("创建会话失败，重试",
			"agent", agentID,
			"attempt", i+1,
			"error", err,
		)
	}

	return "", fmt.Errorf("创建会话失败（重试 3 次）: %w", lastErr)
}

// ReleaseSession 释放 Agent 的当前会话
func (sm *SessionManager) ReleaseSession(agentID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.sessions, agentID)
}

// GetSession 获取 Agent 当前会话 ID
func (sm *SessionManager) GetSession(agentID string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.sessions[agentID]
}

// --- 消息持久化（委托给 MessageStore） ---

// SaveMessages 持久化会话消息
// Lzm 2026-07-10
func (sm *SessionManager) SaveMessages(agentID, sessionID string, messages []StoredMessage) {
	sm.messageMu.Lock()
	defer sm.messageMu.Unlock()
	sm.msgStore.SaveMessages(agentID, sessionID, messages)
	if len(messages) > 0 {
		sm.touchSession(agentID, sessionID)
	}
}

// LoadMessages 加载会话消息
// Lzm 2026-07-10
func (sm *SessionManager) LoadMessages(agentID, sessionID string) []StoredMessage {
	sm.messageMu.RLock()
	defer sm.messageMu.RUnlock()
	return sm.msgStore.LoadMessages(agentID, sessionID)
}

// SessionExists reports whether a Session is active or has persisted metadata.
// Empty Sessions intentionally have no message file, so message presence alone
// cannot distinguish them from an unknown Session.
func (sm *SessionManager) SessionExists(agentID, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	sm.mu.RLock()
	active := sm.sessions[agentID] == sessionID
	sm.mu.RUnlock()
	if active {
		return true
	}

	sm.messageMu.RLock()
	defer sm.messageMu.RUnlock()
	for _, stored := range sm.msgStore.ListSessions(agentID, 0) {
		if stored.SessionID == sessionID {
			return true
		}
	}
	return false
}

// ListSessions 列出指定 Agent 的所有历史会话
// 按 UpdatedAt 降序排列，limit <= 0 表示不限
// Lzm 2026-07-10
func (sm *SessionManager) ListSessions(agentID string, limit int) []SessionSummary {
	sm.messageMu.RLock()
	defer sm.messageMu.RUnlock()
	return sm.msgStore.ListSessions(agentID, limit)
}

// ListAllSessions 列出所有 Agent 的会话
// Lzm 2026-07-10
func (sm *SessionManager) ListAllSessions() map[string][]SessionSummary {
	sm.messageMu.RLock()
	defer sm.messageMu.RUnlock()
	return sm.msgStore.GetAllSessions()
}

// --- 持久化 ---

// persistSession 将会话信息保存到磁盘
// Lzm 2026-07-09
func (sm *SessionManager) persistSession(agentID, sessionID string) {
	sm.messageMu.Lock()
	defer sm.messageMu.Unlock()

	dir := filepath.Join(sm.storeDir, agentID, "sessions")
	if err := ensurePrivateDirectory(dir); err != nil {
		slog.Warn("创建会话存储目录失败",
			"agent", agentID,
			"error", err,
		)
		return
	}

	now := time.Now()
	stored := StoredSession{
		AgentID:   agentID,
		SessionID: sessionID,
		CreatedAt: now,
		UpdatedAt: now,
	}

	path := filepath.Join(dir, safeSessionFileID(sessionID)+".json")
	if err := writeStoredSessionAtomically(path, stored); err != nil {
		slog.Warn("写入会话文件失败",
			"path", path,
			"error", err,
		)
	}
}

func (sm *SessionManager) touchSession(agentID, sessionID string) {
	path := filepath.Join(sm.storeDir, agentID, "sessions", safeSessionFileID(sessionID)+".json")
	now := time.Now()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if mkdirErr := ensurePrivateDirectory(filepath.Dir(path)); mkdirErr != nil {
			slog.Warn("创建会话存储目录失败", "agent", agentID, "error", mkdirErr)
			return
		}
		stored := StoredSession{
			AgentID: agentID, SessionID: sessionID, CreatedAt: now, UpdatedAt: now,
		}
		if writeErr := writeStoredSessionAtomically(path, stored); writeErr != nil {
			slog.Warn("写入会话文件失败", "path", path, "error", writeErr)
		}
		return
	}
	if err != nil {
		return
	}
	var stored StoredSession
	if err := json.Unmarshal(data, &stored); err != nil || stored.SessionID != sessionID {
		return
	}
	stored.UpdatedAt = now
	if err := writeStoredSessionAtomically(path, stored); err != nil {
		slog.Warn("更新会话时间失败", "path", path, "error", err)
	}
}

func writeStoredSessionAtomically(path string, stored StoredSession) (err error) {
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomically(path, data, 0o600)
}

// loadStoredSession 从磁盘加载会话
func (sm *SessionManager) loadStoredSession(agentID string) string {
	dir := filepath.Join(sm.storeDir, agentID, "sessions")

	// 读取目录下最新的 .json 文件
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	var latestSession string
	var latestTime time.Time

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var stored StoredSession
		if err := json.Unmarshal(data, &stored); err != nil {
			continue
		}

		if stored.UpdatedAt.After(latestTime) {
			latestTime = stored.UpdatedAt
			latestSession = stored.SessionID
		}
	}

	return latestSession
}
