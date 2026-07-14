// -*- coding: utf-8 -*-
// Go 1.25+
//
// session_replay.go
// 会话回放器 — 利用 ACP session/load 协议重放历史消息
// 借鉴 OpenViking 的 SessionReplayer 机制：
//   - 通过 ACP session/load 从 Agent 进程获取历史消息
//   - 消息持久化到 MessageStore（文件存储）
//   - 用于远程服务查询历史会话的完整对话记录
//
// Lzm 2026-07-10

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

// SessionLoadReplayer 会话回放器
// 通过 ACP session/load 从 Agent 进程回放历史会话消息
// 对应 OpenViking 的 SessionReplayer.reconcile() + append_batch()
// Lzm 2026-07-10
type SessionLoadReplayer struct {
	agentRegistry *agent.AgentRegistry
	msgStore      *MessageStore
}

// NewSessionLoadReplayer 创建会话回放器
func NewSessionLoadReplayer(reg *agent.AgentRegistry, store *MessageStore) *SessionLoadReplayer {
	return &SessionLoadReplayer{
		agentRegistry: reg,
		msgStore:      store,
	}
}

// LoadAndSaveSession 通过 ACP session/load 加载会话历史并保存到 MessageStore
// 返回加载到的消息数量
// Lzm 2026-07-10
func (sr *SessionLoadReplayer) LoadAndSaveSession(ctx context.Context, agentID, sessionID string) (int, error) {
	a := sr.agentRegistry.Get(agentID)
	if a == nil {
		return 0, fmt.Errorf("Agent 未注册: %s", agentID)
	}

	// 检查 Agent 是否在线
	if a.Status() != agent.AgentIdle && a.Status() != agent.AgentBusy {
		slog.Debug("Agent 不在线，跳过 ACP session/load",
			"agent", agentID,
			"status", a.Status().String(),
		)
		return sr.tryLoadFromFile(agentID, sessionID)
	}

	// 构建 ACP session/load 请求
	// 必须传递 cwd 和 mcpServers 参数，部分 Agent（如 Kimi）校验参数完整性
	// Lzm 2026-07-10
	acpReq := &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      fmt.Sprintf("load_%s", truncateString(sessionID, 8)),
		Method:  "session/load",
		Params: func() json.RawMessage {
			data, _ := json.Marshal(map[string]interface{}{
				"sessionId":  sessionID,
				"cwd":        "", // 调用方不持有 AgentMeta，传空串由 Agent 端处理
				"mcpServers": []interface{}{},
			})
			return data
		}(),
	}

	// 通过 Stream 获取回放消息
	chunkCh, err := a.Stream(ctx, acpReq)
	if err != nil {
		slog.Warn("ACP session/load 失败，回退文件读取",
			"agent", agentID,
			"error", err,
		)
		return sr.tryLoadFromFile(agentID, sessionID)
	}

	// 收集回放的消息
	var messages []StoredMessage
	for chunk := range chunkCh {
		if chunk.Type == internal.StreamChunkFinal {
			continue // 跳过 final 响应
		}

		// 使用简化规则：通过 RawUpdate 判断消息类型
		msg := rawUpdateToStored(chunk.RawUpdate, chunk.Text)
		if msg != nil {
			messages = append(messages, *msg)
		}
	}

	// 持久化到 MessageStore
	if len(messages) > 0 {
		sr.msgStore.SaveReplayedMessages(agentID, sessionID, messages)
		slog.Info("ACP session/load 完成",
			"agent", agentID,
			"messages", len(messages),
		)
	}

	return len(messages), nil
}

// tryLoadFromFile 尝试从文件加载（回退方案）
// Lzm 2026-07-10
func (sr *SessionLoadReplayer) tryLoadFromFile(agentID, sessionID string) (int, error) {
	messages := sr.msgStore.LoadMessages(agentID, sessionID)
	return len(messages), nil
}

// rawUpdateToStored 将 ACP stream update 转为 StoredMessage
// Lzm 2026-07-10
func rawUpdateToStored(rawType, text string) *StoredMessage {
	if text == "" {
		return nil
	}
	switch {
	case strings.Contains(rawType, "thought") || strings.Contains(rawType, "thinking"):
		return &StoredMessage{Role: "thought", Text: text}
	case strings.Contains(rawType, "user"):
		return &StoredMessage{Role: "user", Text: text}
	default:
		return &StoredMessage{Role: "assistant", Text: text}
	}
}

// BatchReplayHistories 批量回放 Agent 的历史会话
// 使用 LogScanner 发现会话 → 对每个会话调用 LoadAndSaveSession
// 对应 OpenViking 的 IngestOrchestrator.backfill()
// Lzm 2026-07-10
func (sr *SessionLoadReplayer) BatchReplayHistories(ctx context.Context, agentID string, maxSessions int) (int, error) {
	// 1. 通过 LogScanner 发现历史会话
	sessions, err := agent.DiscoverHistoricalSessions(agentID, maxSessions)
	if err != nil {
		return 0, fmt.Errorf("发现历史会话失败: %w", err)
	}

	if len(sessions) == 0 {
		slog.Debug("未发现历史会话", "agent", agentID)
		return 0, nil
	}

	// 2. 对每个会话尝试通过 ACP 加载
	total := 0
	for _, ref := range sessions {
		n, err := sr.LoadAndSaveSession(ctx, agentID, ref.NativeID)
		if err != nil {
			slog.Warn("回放会话失败",
				"agent", agentID,
				"session", ref.NativeID,
				"error", err,
			)
			continue
		}
		total += n

		// 限流：每个会话之间等待一小段时间
		time.Sleep(100 * time.Millisecond)
	}

	return total, nil
}
