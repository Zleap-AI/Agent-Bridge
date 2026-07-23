// -*- coding: utf-8 -*-
// Go 1.25+
//
// codex_scanner_test.go
// CodexScanner 单元测试 — 验证原生会话发现、元数据提取和消息读取
//
// Lzm 2026-07-22

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// createTestCodexJSONL 创建 Codex 格式的测试 JSONL 文件
// 模拟 ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl 结构
// Lzm 2026-07-22
func createTestCodexJSONL(t *testing.T, dir, sessionID string, startedAt time.Time, userMsg string) string {
	t.Helper()

	// 按 Codex 格式创建日期子目录
	dateStr := startedAt.Format("2006/01/02")
	sessionDir := filepath.Join(dir, dateStr)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("创建测试目录失败: %v", err)
	}

	timeStr := startedAt.Format("2006-01-02T15-04-05")
	filename := filepath.Join(sessionDir, "rollout-"+timeStr+"-"+sessionID+".jsonl")

	// 创建 JSONL 内容
	ts := startedAt.UTC().Format(time.RFC3339Nano)
	lines := []string{
		`{"timestamp":"` + ts + `","type":"session_meta","payload":{"session_id":"` + sessionID + `","id":"` + sessionID + `","timestamp":"` + ts + `","cwd":"/tmp/test","originator":"Codex Desktop","cli_version":"0.142.5","source":"vscode","model_provider":"test-model"}}`,
		`{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-001","started_at":1234567890}}`,
		`{"timestamp":"` + ts + `","type":"turn_context","payload":{"turn_id":"turn-001","cwd":"/tmp/test"}}`,
	}

	// 用户消息（如果没有提供，则生成一个）
	if userMsg == "" {
		userMsg = "帮我写一个 Python 爬虫"
	}
	lines = append(lines, `{"timestamp":"`+ts+`","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"`+userMsg+`"}]}}`)
	lines = append(lines, `{"timestamp":"`+ts+`","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"input_text","text":"好的，我来帮你写一个 Python 爬虫。"}]}}`)

	// 第二条用户消息（非首条，不会影响标题）
	lines = append(lines, `{"timestamp":"`+ts+`","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"再加一个超时设置"}]}}`)
	lines = append(lines, `{"timestamp":"`+ts+`","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"input_text","text":"已添加超时设置。"}]}}`)

	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	return filename
}

// createTestCodexJSONLWithInjection 创建包含环境上下文注入的测试文件
// 模拟 Codex 在 VSCode 中注入 <environment_context> XML 的行为
// Lzm 2026-07-22
func createTestCodexJSONLWithInjection(t *testing.T, dir, sessionID string) string {
	t.Helper()

	startedAt := time.Now().Add(-1 * time.Hour)
	dateStr := startedAt.Format("2006/01/02")
	sessionDir := filepath.Join(dir, dateStr)
	os.MkdirAll(sessionDir, 0755)

	timeStr := startedAt.Format("2006-01-02T15-04-05")
	filename := filepath.Join(sessionDir, "rollout-"+timeStr+"-"+sessionID+".jsonl")

	ts := startedAt.UTC().Format(time.RFC3339Nano)
	lines := []string{
		`{"timestamp":"` + ts + `","type":"session_meta","payload":{"session_id":"` + sessionID + `","id":"` + sessionID + `","timestamp":"` + ts + `","cwd":"/tmp/test","model_provider":"custom"}}`,
	}

	// 注入环境上下文（应该被跳过）
	lines = append(lines, `{"timestamp":"`+ts+`","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n<cwd>/tmp</cwd>\n</environment_context>"}]}}`)
	// 真实用户消息（应该是标题）
	lines = append(lines, `{"timestamp":"`+ts+`","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"你好，广州塔多高？"}]}}`)
	lines = append(lines, `{"timestamp":"`+ts+`","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"input_text","text":"广州塔 600 米高。"}]}}`)

	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	return filename
}

// createTestCodexJSONLEmpty 创建不含消息的会话文件（模拟空会话）
// Lzm 2026-07-22
func createTestCodexJSONLEmpty(t *testing.T, dir, sessionID string) string {
	t.Helper()

	startedAt := time.Now().Add(-2 * time.Hour)
	dateStr := startedAt.Format("2006/01/02")
	sessionDir := filepath.Join(dir, dateStr)
	os.MkdirAll(sessionDir, 0755)

	timeStr := startedAt.Format("2006-01-02T15-04-05")
	filename := filepath.Join(sessionDir, "rollout-"+timeStr+"-"+sessionID+".jsonl")

	ts := startedAt.UTC().Format(time.RFC3339Nano)
	content := `{"timestamp":"` + ts + `","type":"session_meta","payload":{"session_id":"` + sessionID + `","timestamp":"` + ts + `"}}` + "\n"
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	return filename
}

// TestCodexScanner_DiscoverSessions 测试 DiscoverSessions 基本功能
// Lzm 2026-07-22
func TestCodexScanner_DiscoverSessions(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建 3 个测试会话，时间倒序
	t1 := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)

	createTestCodexJSONL(t, tmpDir, "sid-1111-2222-3333", t1, "帮我写一个 Go 程序")
	createTestCodexJSONL(t, tmpDir, "sid-4444-5555-6666", t2, "什么是 RESTful API")
	createTestCodexJSONL(t, tmpDir, "sid-7777-8888-9999", t3, "解释一下什么是微服务架构")

	scanner := NewCodexScanner(tmpDir)
	sessions, err := scanner.DiscoverSessions()
	if err != nil {
		t.Fatalf("DiscoverSessions 失败: %v", err)
	}

	if len(sessions) != 3 {
		t.Fatalf("期望 3 个会话，得到 %d", len(sessions))
	}

	// 验证时间降序
	if !sort.SliceIsSorted(sessions, func(i, j int) bool {
		return sessions[i].StartedAt >= sessions[j].StartedAt
	}) {
		t.Error("会话未按时间降序排列")
	}

	// 验证第一个会话（最新的）的详细信息
	latest := sessions[0]
	if latest.Harness != "codex" {
		t.Errorf("期望 harness=codex，得到 %s", latest.Harness)
	}
	if latest.NativeID != "sid-1111-2222-3333" {
		t.Errorf("期望 session_id=sid-1111-2222-3333，得到 %s", latest.NativeID)
	}
	if latest.Title != "帮我写一个 Go 程序" {
		t.Errorf("期望 title='帮我写一个 Go 程序'，得到 '%s'", latest.Title)
	}
	if latest.Meta["model_provider"] != "test-model" {
		t.Errorf("期望 model_provider=test-model，得到 %s", latest.Meta["model_provider"])
	}
	if latest.Meta["message_count"] != "4" {
		t.Errorf("期望 message_count=4，得到 %s", latest.Meta["message_count"])
	}
}

// TestCodexScanner_FilterInjectedContent 测试过滤环境上下文注入
// Lzm 2026-07-22
func TestCodexScanner_FilterInjectedContent(t *testing.T) {
	tmpDir := t.TempDir()
	createTestCodexJSONLWithInjection(t, tmpDir, "sid-injected-001")

	scanner := NewCodexScanner(tmpDir)
	sessions, err := scanner.DiscoverSessions()
	if err != nil {
		t.Fatalf("DiscoverSessions 失败: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("期望 1 个会话，得到 %d", len(sessions))
	}

	// 标题应该跳过 <environment_context>，取真实用户消息
	expected := "你好，广州塔多高？"
	if sessions[0].Title != expected {
		t.Errorf("期望 title='%s'，得到 '%s'", expected, sessions[0].Title)
	}
}

// TestCodexScanner_EmptySession 测试空会话（仅有 session_meta，无消息）
// Lzm 2026-07-22
func TestCodexScanner_EmptySession(t *testing.T) {
	tmpDir := t.TempDir()
	createTestCodexJSONLEmpty(t, tmpDir, "sid-empty-001")

	scanner := NewCodexScanner(tmpDir)
	sessions, err := scanner.DiscoverSessions()
	if err != nil {
		t.Fatalf("DiscoverSessions 失败: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("期望 1 个会话，得到 %d", len(sessions))
	}

	if sessions[0].Title != "" {
		t.Errorf("空会话不应有标题，得到 '%s'", sessions[0].Title)
	}
	if sessions[0].Meta["message_count"] != "0" {
		t.Errorf("空会话 message_count 应为 0，得到 %s", sessions[0].Meta["message_count"])
	}
}

// TestCodexScanner_NoSessionDir 测试会话目录不存在的情况
// Lzm 2026-07-22
func TestCodexScanner_NoSessionDir(t *testing.T) {
	scanner := NewCodexScanner("/tmp/nonexistent-codex-sessions-xxxx")
	_, err := scanner.DiscoverSessions()
	if err == nil {
		t.Error("期望目录不存在时返回错误，得到 nil")
	}
}

// TestCodexScanner_ReadMessages 测试消息读取
// Lzm 2026-07-22
func TestCodexScanner_ReadMessages(t *testing.T) {
	tmpDir := t.TempDir()
	t1 := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	path := createTestCodexJSONL(t, tmpDir, "sid-read-001", t1, "测试消息读取")

	scanner := NewCodexScanner(tmpDir)
	sessions, err := scanner.DiscoverSessions()
	if err != nil {
		t.Fatalf("DiscoverSessions 失败: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("没有找到会话")
	}
	// 使用确切的文件路径构造 SessionRef
	ref := SessionRef{
		Harness:  "codex",
		NativeID: "sid-read-001",
		Locator:  path,
	}

	msgs, total, err := scanner.ReadMessages(ref, 0, 0)
	if err != nil {
		t.Fatalf("ReadMessages 失败: %v", err)
	}
	if total != 4 {
		t.Errorf("期望 4 条消息，得到 %d", total)
	}
	if len(msgs) != 4 {
		t.Errorf("期望 4 个消息元素，得到 %d", len(msgs))
	}

	// 验证消息格式和内容
	if len(msgs) > 0 {
		if !strings.Contains(msgs[0], `"role":"user"`) {
			t.Errorf("第一条消息应为 user 角色: %s", msgs[0])
		}
		if !strings.Contains(msgs[0], "测试消息读取") {
			t.Errorf("第一条消息应包含用户文本: %s", msgs[0])
		}
	}
}

// TestCodexScanner_ReadMessagesFiltered 测试消息读取时过滤环境上下文
// Lzm 2026-07-22
func TestCodexScanner_ReadMessagesFiltered(t *testing.T) {
	tmpDir := t.TempDir()
	path := createTestCodexJSONLWithInjection(t, tmpDir, "sid-filter-001")

	scanner := NewCodexScanner(tmpDir)
	ref := SessionRef{
		Harness:  "codex",
		NativeID: "sid-filter-001",
		Locator:  path,
	}

	msgs, total, err := scanner.ReadMessages(ref, 0, 0)
	if err != nil {
		t.Fatalf("ReadMessages 失败: %v", err)
	}
	// 只有 2 条合法的消息（真实 user + assistant），不应包含环境上下文
	if total != 2 {
		t.Errorf("期望 2 条消息（过滤后），得到 %d", total)
	}
	_ = msgs
}

// TestCodexScanner_NewWithDefault 测试默认构造函数
// Lzm 2026-07-22
func TestCodexScanner_NewWithDefault(t *testing.T) {
	// 不传参数时，应使用 ~/.codex/sessions
	scanner := NewCodexScanner("")
	if scanner.sessionsDir == "" {
		t.Error("默认构造后 sessionsDir 不应为空")
	}
	if scanner.Name() != "codex" {
		t.Errorf("期望 Name()=codex，得到 %s", scanner.Name())
	}
}

// TestCodexScanner_WriteSessionMeta 测试原生会话写入
// 验证：WriteSessionMeta → DiscoverSessions 双向一致
// Lzm 2026-07-22
func TestCodexScanner_WriteSessionMeta(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewCodexScanner(tmpDir)

	sessionID := "write-sid-001-xxxx-yyyy"
	now := time.Date(2026, 7, 22, 14, 30, 0, 0, time.UTC)

	// 写入会话元数据
	if err := scanner.WriteSessionMeta(sessionID, "/tmp/test-cwd", now); err != nil {
		t.Fatalf("WriteSessionMeta 失败: %v", err)
	}

	// 验证文件已创建
	expectedPath := scanner.codexSessionFilePath(sessionID, now)
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Fatalf("会话文件未创建: %s", expectedPath)
	}

	// 验证可以通过 DiscoverSessions 发现
	sessions, err := scanner.DiscoverSessions()
	if err != nil {
		t.Fatalf("DiscoverSessions 失败: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("期望 1 个会话，得到 %d", len(sessions))
	}

	if sessions[0].NativeID != sessionID {
		t.Errorf("期望 session_id=%s，得到 %s", sessionID, sessions[0].NativeID)
	}
	if sessions[0].Meta["cwd"] != "/tmp/test-cwd" {
		t.Errorf("期望 cwd=/tmp/test-cwd，得到 %s", sessions[0].Meta["cwd"])
	}
	if sessions[0].Meta["originator"] != "zleap-bridge" {
		t.Errorf("期望 originator=zleap-bridge，得到 %s", sessions[0].Meta["originator"])
	}
}

// TestCodexScanner_WriteThenReadMessages 测试写入消息后读取一致
// 验证：WriteSessionMeta → WriteMessages → ReadMessages 双向一致
// Lzm 2026-07-22
func TestCodexScanner_WriteThenReadMessages(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewCodexScanner(tmpDir)

	sessionID := "write-msg-001-test"
	now := time.Date(2026, 7, 22, 15, 0, 0, 0, time.UTC)

	// 1. 写入会话元数据
	if err := scanner.WriteSessionMeta(sessionID, "/tmp/test", now); err != nil {
		t.Fatalf("WriteSessionMeta 失败: %v", err)
	}

	// 2. 写入消息
	msgs := []NativeMessage{
		{Role: "user", Text: "你好，请问什么是微服务？"},
		{Role: "assistant", Text: "微服务是一种架构风格，将应用拆分为多个小型服务。"},
		{Role: "user", Text: "那和单体架构有什么区别？"},
		{Role: "assistant", Text: "单体架构所有功能在同一个进程中，微服务则独立部署。"},
	}
	if err := scanner.WriteMessages(sessionID, msgs, now); err != nil {
		t.Fatalf("WriteMessages 失败: %v", err)
	}

	// 3. 读取并验证
	path := scanner.codexSessionFilePath(sessionID, now)
	ref := SessionRef{
		Harness:  "codex",
		NativeID: sessionID,
		Locator:  path,
	}
	readMsgs, total, err := scanner.ReadMessages(ref, 0, 0)
	if err != nil {
		t.Fatalf("ReadMessages 失败: %v", err)
	}
	// 应该读到 4 条消息（没有 thought 消息）
	if total != 4 {
		t.Errorf("期望 4 条消息，得到 %d", total)
	}
	if len(readMsgs) != 4 {
		t.Errorf("期望 4 个元素，得到 %d", len(readMsgs))
	}

	// 4. 验证消息内容
	if len(readMsgs) > 0 {
		if !strings.Contains(readMsgs[0], `"role":"user"`) {
			t.Errorf("第一条消息应为 user: %s", readMsgs[0])
		}
		if !strings.Contains(readMsgs[0], "微服务") {
			t.Errorf("第一条消息应包含'微服务': %s", readMsgs[0])
		}
	}

	// 5. 验证发现时的消息数统计
	sessions, err := scanner.DiscoverSessions()
	if err != nil {
		t.Fatalf("DiscoverSessions 失败: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("期望 1 个会话，得到 %d", len(sessions))
	}
	if sessions[0].Meta["message_count"] != "4" {
		t.Errorf("期望 message_count=4，得到 %s", sessions[0].Meta["message_count"])
	}
}

// TestCodexScanner_WriteMessagesBeforeMeta 测试先写消息（应失败）
// Lzm 2026-07-22
func TestCodexScanner_WriteMessagesBeforeMeta(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewCodexScanner(tmpDir)

	sessionID := "no-meta-001"
	now := time.Now()
	msgs := []NativeMessage{{Role: "user", Text: "hello"}}

	// 不先写 session_meta 直接写消息应该报错
	err := scanner.WriteMessages(sessionID, msgs, now)
	if err == nil {
		t.Error("先写消息应该报错（文件不存在），但得到 nil")
	}
}

// TestCodexScanner_WriteSessionMetaIndex 测试 WriteSessionMeta 同步写入 session_index.jsonl
// 验证：WriteSessionMeta → session_index.jsonl 索引存在且内容正确
// Lzm 2026-07-22
func TestCodexScanner_WriteSessionMetaIndex(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewCodexScanner(tmpDir)

	sessionID := "idx-sid-001-test"
	now := time.Date(2026, 7, 22, 16, 0, 0, 0, time.UTC)

	// 写入会话元数据
	if err := scanner.WriteSessionMeta(sessionID, "/tmp/test", now); err != nil {
		t.Fatalf("WriteSessionMeta 失败: %v", err)
	}

	// 验证 index 文件已创建
	indexPath := scanner.sessionIndexPath()
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		t.Fatalf("session_index.jsonl 未创建: %s", indexPath)
	}

	// 读取 index 文件内容并验证
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("读取 session_index.jsonl 失败: %v", err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		t.Fatal("session_index.jsonl 内容为空")
	}

	// 解析验证条目
	lines := strings.Split(content, "\n")
	if len(lines) != 1 {
		t.Fatalf("期望 1 行索引条目，得到 %d", len(lines))
	}

	var entry sessionIndexEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("解析索引条目失败: %v", err)
	}
	if entry.SessionID != sessionID {
		t.Errorf("期望 session_id=%s，得到 %s", sessionID, entry.SessionID)
	}
	if entry.Timestamp == "" {
		t.Error("索引条目缺少 timestamp")
	}
	// title 初始应为空
	if entry.Title != "" {
		t.Errorf("新会话 title 应为空，得到 '%s'", entry.Title)
	}
}

// TestCodexScanner_ParseSessionIndex 测试 ParseSessionIndex 正确读取索引并关联 rollout 文件
// Lzm 2026-07-22
func TestCodexScanner_ParseSessionIndex(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewCodexScanner(tmpDir)

	// 创建 2 个会话
	sid1 := "parse-idx-001"
	sid2 := "parse-idx-002"
	t1 := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)

	// 通过 WriteSessionMeta 创建（会自动写 index）
	if err := scanner.WriteSessionMeta(sid1, "/tmp/a", t1); err != nil {
		t.Fatal(err)
	}
	if err := scanner.WriteSessionMeta(sid2, "/tmp/b", t2); err != nil {
		t.Fatal(err)
	}

	// 直接解析索引
	sessions, err := scanner.ParseSessionIndex()
	if err != nil {
		t.Fatalf("ParseSessionIndex 失败: %v", err)
	}

	if len(sessions) != 2 {
		t.Fatalf("期望 2 个会话，得到 %d", len(sessions))
	}

	// 验证会话信息
	found := false
	for _, s := range sessions {
		if s.NativeID == sid1 {
			found = true
			if s.Harness != "codex" {
				t.Errorf("期望 harness=codex，得到 %s", s.Harness)
			}
			if s.Locator == "" {
				t.Error("Locator 不应为空")
			}
			if _, err := os.Stat(s.Locator); err != nil {
				t.Errorf("rollout 文件不存在: %s", s.Locator)
			}
		}
	}
	if !found {
		t.Errorf("未找到会话 %s", sid1)
	}
}

// TestCodexScanner_DiscoverSessionsFromIndex 测试 DiscoverSessions 优先使用索引
// 验证：索引存在且有数据时，不降级到目录扫描
// Lzm 2026-07-22
func TestCodexScanner_DiscoverSessionsFromIndex(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewCodexScanner(tmpDir)

	// 通过 WriteSessionMeta 创建 3 个会话（索引+rollout 同时写入）
	for i := 0; i < 3; i++ {
		sid := fmt.Sprintf("disc-idx-%03d", i)
		now := time.Date(2026, 7, 22, 10+i, 0, 0, 0, time.UTC)
		if err := scanner.WriteSessionMeta(sid, "/tmp/test", now); err != nil {
			t.Fatal(err)
		}
	}

	// DiscoverSessions 应优先使用索引
	sessions, err := scanner.DiscoverSessions()
	if err != nil {
		t.Fatalf("DiscoverSessions 失败: %v", err)
	}

	if len(sessions) != 3 {
		t.Fatalf("期望 3 个会话，得到 %d", len(sessions))
	}

	// 验证时间降序
	if !sort.SliceIsSorted(sessions, func(i, j int) bool {
		return sessions[i].StartedAt >= sessions[j].StartedAt
	}) {
		t.Error("会话未按时间降序排列")
	}

	// 验证索引补充的元数据存在
	if sessions[0].Meta["cwd"] != "/tmp/test" {
		t.Errorf("索引补充的 cwd 应为 /tmp/test，得到 %s", sessions[0].Meta["cwd"])
	}
	if sessions[0].Meta["originator"] != "zleap-bridge" {
		t.Errorf("索引补充的 originator 应为 zleap-bridge，得到 %s", sessions[0].Meta["originator"])
	}
}

// TestCodexScanner_IndexEmptyFallback 测试索引不存在时降级到目录扫描
// Lzm 2026-07-22
func TestCodexScanner_IndexEmptyFallback(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewCodexScanner(tmpDir)

	// 创建 rollout 文件但不要索引（直接用 createTestCodexJSONL）
	createTestCodexJSONL(t, tmpDir, "fallback-sid-001", time.Now(), "降级测试")

	// 验证索引文件不存在
	indexPath := scanner.sessionIndexPath()
	if _, err := os.Stat(indexPath); !os.IsNotExist(err) {
		t.Log("索引文件不应存在")
	}

	// DiscoverSessions 应降级为目录扫描
	sessions, err := scanner.DiscoverSessions()
	if err != nil {
		t.Fatalf("DiscoverSessions 降级失败: %v", err)
	}

	if len(sessions) != 1 {
		t.Fatalf("降级后期望 1 个会话，得到 %d", len(sessions))
	}
	if sessions[0].NativeID != "fallback-sid-001" {
		t.Errorf("降级后期望 session_id=fallback-sid-001，得到 %s", sessions[0].NativeID)
	}
}

// TestCodexScanner_AppendSessionIndex 测试多次 WriteSessionMeta 追加索引条目
// Lzm 2026-07-22
func TestCodexScanner_AppendSessionIndex(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewCodexScanner(tmpDir)

	// 连续写入多个会话
	sids := []string{"append-001", "append-002", "append-003"}
	for i, sid := range sids {
		now := time.Date(2026, 7, 22, 10+i, 0, 0, 0, time.UTC)
		if err := scanner.WriteSessionMeta(sid, "/tmp/test", now); err != nil {
			t.Fatal(err)
		}
	}

	// 读取 index 文件，验证有 3 行
	indexPath := scanner.sessionIndexPath()
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("读取索引文件失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("期望 3 行索引条目，得到 %d", len(lines))
	}

	// 验证每个会话 ID 都存在
	for i, sid := range sids {
		if !strings.Contains(lines[i], sid) {
			t.Errorf("第 %d 行应包含 session_id=%s，实际: %s", i, sid, lines[i])
		}
	}
}

// TestCodexScanner_WriteTxtFileThenDiscover 测试写入后能正确发现（验证文件路径格式）
// Lzm 2026-07-22
func TestCodexScanner_WriteTxtFileThenDiscover(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewCodexScanner(tmpDir)

	// 写入 2 个会话
	sid1 := "batch-sid-001"
	sid2 := "batch-sid-002"
	t1 := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)

	if err := scanner.WriteSessionMeta(sid1, "/tmp/a", t1); err != nil {
		t.Fatal(err)
	}
	if err := scanner.WriteSessionMeta(sid2, "/tmp/b", t2); err != nil {
		t.Fatal(err)
	}

	// 发现 - 应返回 2 个会话，时间降序
	sessions, err := scanner.DiscoverSessions()
	if err != nil {
		t.Fatalf("DiscoverSessions 失败: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("期望 2 个会话，得到 %d", len(sessions))
	}
	// 最新的在前
	if sessions[0].NativeID != sid2 {
		t.Errorf("期望最新会话=%s，得到 %s", sid2, sessions[0].NativeID)
	}
}
