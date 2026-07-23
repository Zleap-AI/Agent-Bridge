// -*- coding: utf-8 -*-
// Go 1.25+
//
// native_writer.go
// NativeSessionWriter 可选接口 — LogScanner 可实现此接口以支持写入 Agent 原生格式
// 提供会话一致性：Bridge 创建的会话自动同步到 Agent 原生存储
//
// Lzm 2026-07-22

package agent

import "time"

// NativeMessage 原生存储消息格式（与 service.StoredMessage 解耦以避免循环导入）
// Lzm 2026-07-22
type NativeMessage struct {
	Role string `json:"role"` // user | assistant | thought
	Text string `json:"text"`
}

// NativeSessionWriter 可选接口：LogScanner 可实现此接口以支持写入 Agent 原生格式
// 调用时机：
//   - WriteSessionMeta: session/new ACP 成功后（在 persistSession 之后调用）
//   - WriteMessages: SaveMessages 时调用（追加写入）
//
// Lzm 2026-07-22
type NativeSessionWriter interface {
	// WriteSessionMeta 将会话元数据写入 Agent 原生存储
	WriteSessionMeta(sessionID, cwd string, createdAt time.Time) error

	// WriteMessages 将消息写入 Agent 原生存储
	// 在会话已存在（WriteSessionMeta 已调用）时追加写入
	WriteMessages(sessionID string, msgs []NativeMessage, createdAt time.Time) error
}

// GetNativeWriter 获取 Agent 的 NativeSessionWriter（如果支持）
// Lzm 2026-07-22
func GetNativeWriter(agentID string) NativeSessionWriter {
	scanner := GetScanner(agentID)
	if scanner == nil {
		return nil
	}
	writer, ok := scanner.(NativeSessionWriter)
	if !ok {
		return nil
	}
	return writer
}
