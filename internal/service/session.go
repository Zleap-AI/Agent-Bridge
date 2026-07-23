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
	"strings"
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

	// cancelledSessions 记录已取消的会话（agentID → sessionID → struct{}）
	// 用于让 streamPrompt 主动检测取消，避免等 Agent 响应
	cancelledSessions map[string]map[string]struct{}
	cancelMu          sync.Mutex

	// permissionModes 记录每个会话的授权模式（agentID → sessionID → mode）
	// 用于权限回调中根据模式决定处理方式。
	// Lzm 2026-07-21
	permissionModes map[string]map[string]string
	permModeMu      sync.RWMutex

	// sessionCreatedAt 会话创建时间缓存（agentID:sessionID → time.Time）
	// 用于 NativeSessionWriter 需要创建时间时使用
	// Lzm 2026-07-22
	sessionCreatedAt sync.Map
}

// StoredSession 持久化存储的会话信息
// Lzm 2026-07-20
type StoredSession struct {
	AgentID        string    `json:"agent_id"`
	SessionID      string    `json:"session_id"`
	Title          string    `json:"title,omitempty"`            // 会话标题（从首条用户消息提取）
	CWD            string    `json:"cwd,omitempty"`              // 会话工作目录。Lzm 2026-07-21
	PermissionMode string    `json:"permission_mode,omitempty"`  // 授权模式。Lzm 2026-07-21
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// NewSessionManager 创建会话管理器
func NewSessionManager(registry *agent.AgentRegistry) *SessionManager {
	return newSessionManagerWithStoreDir(registry, getAgentBridgeStoreDir())
}

func newSessionManagerWithStoreDir(registry *agent.AgentRegistry, storeDir string) *SessionManager {
	return &SessionManager{
		registry:          registry,
		sessions:          make(map[string]string),
		storeDir:          storeDir,
		msgStore:          NewMessageStore(storeDir),
		cancelledSessions: make(map[string]map[string]struct{}),
		permissionModes:   make(map[string]map[string]string),
	}
}

// CreateNewSession always asks the Agent for a fresh session. It deliberately
// does not inspect the active or persisted session, unlike GetOrCreateSession.
func (sm *SessionManager) CreateNewSession(ctx context.Context, agentID, cwd, permissionMode string) (string, error) {
	slog.Debug("创建新会话",
		"agent", agentID,
		"cwd", cwd,
		"permission_mode", permissionMode,
	)
	unlock := sm.lockAgentSession(agentID)
	defer unlock()
	return sm.createSessionWithRetry(ctx, agentID, cwd, permissionMode)
}

// ActivateSession records a session that has already been loaded by the Agent.
// This keeps explicit session/load separate from startup recovery.
func (sm *SessionManager) ActivateSession(agentID, sessionID string) {
	sm.mu.Lock()
	sm.sessions[agentID] = sessionID
	sm.mu.Unlock()
}

// DeactivateSession 将 Agent 的会话标记为非活跃（不删除持久化数据）。
// 用于 session/close 后清理内存状态。
// Lzm 2026-07-21
func (sm *SessionManager) DeactivateSession(agentID, sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.sessions[agentID] == sessionID {
		delete(sm.sessions, agentID)
	}
	slog.Debug("会话已解除活跃绑定",
		"agent", agentID,
		"session_id", sessionID,
	)
}

// RemoveSession 删除 Agent 的会话（含内存和磁盘持久化数据）。
// 用于 session/delete 后彻底清理。
// Lzm 2026-07-21
func (sm *SessionManager) RemoveSession(agentID, sessionID string) {
	sm.mu.Lock()
	if sm.sessions[agentID] == sessionID {
		delete(sm.sessions, agentID)
	}
	sm.mu.Unlock()

	// 清除磁盘持久化的消息数据
	go func() {
		sm.messageMu.Lock()
		defer sm.messageMu.Unlock()
		sm.msgStore.DeleteSession(agentID, sessionID)
	}()

	// 清除磁盘会话元数据
	sm.clearStoredSession(agentID)

	slog.Debug("会话已移除",
		"agent", agentID,
		"session_id", sessionID,
	)
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
			sm.clearStoredSession(agentID)
		}
	}

	// 3. 创建新会话（带重试）
	return sm.createSessionWithRetry(ctx, agentID, "", "")
}

func (sm *SessionManager) lockAgentSession(agentID string) func() {
	value, _ := sm.agentSessionLocks.LoadOrStore(agentID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

// createSessionWithRetry 创建新会话，最多重试 3 次
// Lzm 2026-07-09
func (sm *SessionManager) createSessionWithRetry(ctx context.Context, agentID, cwd, permissionMode string) (string, error) {
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

		slog.Debug("调用 Agent.NewSession",
			"agent", agentID,
			"cwd", cwd,
			"attempt", i+1,
		)
		sid, err := a.NewSession(ctx, cwd)
		if err == nil && sid != "" {
			// 保存会话
			sm.mu.Lock()
			sm.sessions[agentID] = sid
			sm.mu.Unlock()

			sm.persistSession(agentID, sid, cwd, permissionMode)
			sm.SetSessionPermissionMode(agentID, sid, permissionMode)
			// 同步到 Agent 原生存储（如果支持）
			if writer := agent.GetNativeWriter(agentID); writer != nil {
				now := time.Now()
				sm.sessionCreatedAt.Store(agentID+":"+sid, now)
				if err := writer.WriteSessionMeta(sid, cwd, now); err != nil {
					slog.Warn("写入原生会话元数据失败",
						"agent", agentID,
						"session_id", safeSessionID(sid),
						"error", err,
					)
				}
			}
			slog.Info("新会话创建成功",
				"agent", agentID,
				"session_id", sid,
				"cwd", cwd,
				"permission_mode", permissionMode,
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

// CancelSession 标记指定会话为已取消状态
// 即使 Agent 不响应 session/cancel，bridge 也能主动中断 streaming
// Lzm 2026-07-20
func (sm *SessionManager) CancelSession(agentID, sessionID string) {
	sm.cancelMu.Lock()
	defer sm.cancelMu.Unlock()
	if _, ok := sm.cancelledSessions[agentID]; !ok {
		sm.cancelledSessions[agentID] = make(map[string]struct{})
	}
	sm.cancelledSessions[agentID][sessionID] = struct{}{}
	slog.Debug("会话已标记为取消",
		"agent", agentID,
		"session_id", sessionID,
	)
}

// IsCancelled 检查指定会话是否已被取消
// Lzm 2026-07-20
func (sm *SessionManager) IsCancelled(agentID, sessionID string) bool {
	sm.cancelMu.Lock()
	defer sm.cancelMu.Unlock()
	if sessions, ok := sm.cancelledSessions[agentID]; ok {
		_, cancelled := sessions[sessionID]
		return cancelled
	}
	return false
}

// ClearCancel 清除会话的取消标记（取消后重置时调用）
// Lzm 2026-07-20
func (sm *SessionManager) ClearCancel(agentID, sessionID string) {
	sm.cancelMu.Lock()
	defer sm.cancelMu.Unlock()
	if sessions, ok := sm.cancelledSessions[agentID]; ok {
		if _, exists := sessions[sessionID]; exists {
			delete(sessions, sessionID)
			slog.Debug("cancel 标记已清理",
				"agent", agentID,
				"session", truncateString(sessionID, 16),
			)
		}
	}
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

	// 同步到 Agent 原生存储（如果支持）
	if writer := agent.GetNativeWriter(agentID); writer != nil && len(messages) > 0 {
		// 获取会话创建时间
		var createdAt time.Time
		if cached, ok := sm.sessionCreatedAt.Load(agentID + ":" + sessionID); ok {
			createdAt = cached.(time.Time)
		} else {
			createdAt = time.Now()
		}

		// 转换消息格式
		nativeMsgs := make([]agent.NativeMessage, 0, len(messages))
		for _, m := range messages {
			nativeMsgs = append(nativeMsgs, agent.NativeMessage{
				Role: m.Role,
				Text: m.Text,
			})
		}
		if err := writer.WriteMessages(sessionID, nativeMsgs, createdAt); err != nil {
			slog.Warn("写入原生会话消息失败",
				"agent", agentID,
				"session_id", safeSessionID(sessionID),
				"error", err,
			)
		}
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
func (sm *SessionManager) persistSession(agentID, sessionID, cwd, permissionMode string) {
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
		AgentID:        agentID,
		SessionID:      sessionID,
		CWD:            cwd,
		PermissionMode: permissionMode,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	path := filepath.Join(dir, safeSessionFileID(sessionID)+".json")
	if err := writeStoredSessionAtomically(path, stored); err != nil {
		slog.Warn("写入会话文件失败",
			"path", path,
			"error", err,
		)
	}
}

// touchSession 更新会话的最后活跃时间，并自动从首条用户消息提取标题
// Lzm 2026-07-20
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

	// 标题为空时，从首条用户消息自动提取
	if stored.Title == "" {
		if title := sm.extractFirstUserMessage(agentID, sessionID); title != "" {
			stored.Title = title
		}
	}

	if err := writeStoredSessionAtomically(path, stored); err != nil {
		slog.Warn("更新会话时间失败", "path", path, "error", err)
	}
}

// extractFirstUserMessage 从消息文件中提取首条用户消息作为会话标题
// 截断至 50 个字符，多余替换为 ...
// 调用方需已持有 messageMu 锁
// Lzm 2026-07-20
func (sm *SessionManager) extractFirstUserMessage(agentID, sessionID string) string {
	messages := sm.msgStore.LoadMessages(agentID, sessionID)
	for _, msg := range messages {
		if msg.Role == "user" {
			text := strings.TrimSpace(msg.Text)
			// 替换换行为空格，取首行
			if idx := strings.Index(text, "\n"); idx > 0 {
				text = text[:idx]
			}
			text = strings.TrimSpace(text)
			if len(text) > 50 {
				text = text[:50] + "..."
			}
			return text
		}
	}
	return ""
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

// clearStoredSession 清除 Agent 的磁盘会话记录（当会话无效时调用）
// Lzm 2026-07-14
func (sm *SessionManager) clearStoredSession(agentID string) {
	dir := filepath.Join(sm.storeDir, agentID, "sessions")
	if err := os.RemoveAll(dir); err != nil {
		slog.Warn("清除无效会话目录失败",
			"agent", agentID,
			"path", dir,
			"error", err,
		)
		return
	}
	slog.Debug("已清除无效会话",
		"agent", agentID,
		"path", dir,
	)
}

// SetSessionTitle 设置会话的标题（持久化到磁盘）。
// 该标题会覆盖自动提取的标题（session/set_title 时从外部显式指定）。
// Lzm 2026-07-21
func (sm *SessionManager) SetSessionTitle(agentID, sessionID, title string) {
	sm.messageMu.Lock()
	defer sm.messageMu.Unlock()

	path := filepath.Join(sm.storeDir, agentID, "sessions", safeSessionFileID(sessionID)+".json")
	data, err := os.ReadFile(path)
	var stored StoredSession
	if os.IsNotExist(err) {
		// 会话文件不存在，创建新的
		stored = StoredSession{
			AgentID:   agentID,
			SessionID: sessionID,
			Title:     title,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
	} else if err == nil {
		if err := json.Unmarshal(data, &stored); err != nil {
			slog.Warn("解析会话元数据失败", "path", path, "error", err)
			return
		}
		stored.Title = title
		stored.UpdatedAt = time.Now()
	} else {
		slog.Warn("读取会话元数据失败", "path", path, "error", err)
		return
	}

	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		slog.Warn("创建会话存储目录失败", "path", filepath.Dir(path), "error", err)
		return
	}
	if err := writeStoredSessionAtomically(path, stored); err != nil {
		slog.Warn("写入会话标题失败", "path", path, "error", err)
	}

	slog.Debug("会话标题已更新",
		"agent", agentID,
		"session_id", sessionID,
		"title", title,
	)
}

// GetSessionPermissionMode 获取会话的授权模式。
// 先检查内存缓存（permissionModes 映射），未命中时从磁盘读取 StoredSession。
// Lzm 2026-07-21
func (sm *SessionManager) GetSessionPermissionMode(agentID, sessionID string) string {
	// 先检查内存缓存
	sm.permModeMu.RLock()
	if modes, ok := sm.permissionModes[agentID]; ok {
		if mode, ok := modes[sessionID]; ok && mode != "" {
			sm.permModeMu.RUnlock()
			return mode
		}
	}
	sm.permModeMu.RUnlock()

	// 从磁盘读取
	sm.messageMu.RLock()
	defer sm.messageMu.RUnlock()

	dir := filepath.Join(sm.storeDir, agentID, "sessions")
	path := filepath.Join(dir, safeSessionFileID(sessionID)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var stored StoredSession
	if err := json.Unmarshal(data, &stored); err != nil {
		return ""
	}
	return stored.PermissionMode
}

// SetSessionPermissionMode 设置会话的授权模式（仅内存缓存）。
// 用于新创建或已加载的会话，以便权限回调中快速查询。
// Lzm 2026-07-21
func (sm *SessionManager) SetSessionPermissionMode(agentID, sessionID, mode string) {
	sm.permModeMu.Lock()
	defer sm.permModeMu.Unlock()
	if _, ok := sm.permissionModes[agentID]; !ok {
		sm.permissionModes[agentID] = make(map[string]string)
	}
	sm.permissionModes[agentID][sessionID] = mode
}

// UpdateSessionPermissionMode 更新会话的授权模式（内存 + 磁盘持久化）。
// Lzm 2026-07-21
func (sm *SessionManager) UpdateSessionPermissionMode(agentID, sessionID, mode string) {
	// 更新内存缓存
	sm.SetSessionPermissionMode(agentID, sessionID, mode)

	// 更新磁盘持久化
	sm.messageMu.Lock()
	defer sm.messageMu.Unlock()

	dir := filepath.Join(sm.storeDir, agentID, "sessions")
	path := filepath.Join(dir, safeSessionFileID(sessionID)+".json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		slog.Warn("读取会话文件失败，无法更新授权模式",
			"agent", agentID, "session", sessionID, "error", err,
		)
		return
	}
	var stored StoredSession
	if err := json.Unmarshal(data, &stored); err != nil {
		slog.Warn("解析会话文件失败，无法更新授权模式",
			"agent", agentID, "session", sessionID, "error", err,
		)
		return
	}
	stored.PermissionMode = mode
	if err := writeStoredSessionAtomically(path, stored); err != nil {
		slog.Warn("写入会话文件失败，无法更新授权模式",
			"agent", agentID, "session", sessionID, "error", err,
		)
	}
}

// AgentStorageStats 单个 Agent 的存储统计
// Lzm 2026-07-20
type AgentStorageStats struct {
	AgentID      string `json:"agent_id"`
	SessionCount int    `json:"session_count"`
	MessageCount int    `json:"message_count"`
}

// StorageInfo 存储状态信息
// Lzm 2026-07-20
type StorageInfo struct {
	StoreDir      string              `json:"store_dir"`
	AgentCount    int                 `json:"agent_count"`
	TotalSessions int                 `json:"total_sessions"`
	TotalMessages int                 `json:"total_messages"`
	Agents        []AgentStorageStats `json:"agents"`
}

// GetStorageInfo 获取存储状态统计信息
// 遍历所有 Agent 的存储目录，统计会话数和消息数
// Lzm 2026-07-20
func (sm *SessionManager) GetStorageInfo() StorageInfo {
	info := StorageInfo{
		StoreDir: sm.storeDir,
		Agents:   []AgentStorageStats{},
	}

	agentsDir, err := os.ReadDir(sm.storeDir)
	if err != nil {
		slog.Debug("读取存储目录失败", "path", sm.storeDir, "error", err)
		return info
	}

	for _, entry := range agentsDir {
		if !entry.IsDir() {
			continue
		}
		agentID := entry.Name()

		sessions := sm.msgStore.ListSessions(agentID, 0)
		var totalMessages int
		for _, session := range sessions {
			messages := sm.msgStore.LoadMessages(agentID, session.SessionID)
			totalMessages += len(messages)
		}

		stats := AgentStorageStats{
			AgentID:      agentID,
			SessionCount: len(sessions),
			MessageCount: totalMessages,
		}
		info.Agents = append(info.Agents, stats)
		info.TotalSessions += stats.SessionCount
		info.TotalMessages += stats.MessageCount
	}

	info.AgentCount = len(info.Agents)
	return info
}
