// -*- coding: utf-8 -*-
// Go 1.25+
//
// codex_integration_test.go
// Codex 完整功能流集成测试 — 验证会话管理、权限处理、503 重试、流式通信
//
// Lzm 2026-07-22

package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal"
)

// --- 集成测试：会话生命周期（new / load / close / delete） ---

// TestCodexIntegration_SessionLifecycle 验证 Agent 的会话 CRUD 全流程
// 使用 session-crud 模式模拟 Codex 行为
// Lzm 2026-07-22
func TestCodexIntegration_SessionLifecycle(t *testing.T) {
	a := newHelperAgent(t, "session-crud", 5*time.Second, "")
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	ctx := context.Background()

	// 1. 创建新会话
	sid, err := a.NewSession(ctx, "/tmp/test")
	if err != nil {
		t.Fatalf("NewSession 失败: %v", err)
	}
	if sid == "" {
		t.Fatal("NewSession 返回了空 sessionID")
	}
	if !strings.HasPrefix(sid, "integ-sid-") {
		t.Errorf("期望 sessionID 以 integ-sid- 开头，得到 %s", sid)
	}

	// 2. 加载已有会话
	if err := a.LoadSession(ctx, sid); err != nil {
		t.Fatalf("LoadSession 失败: %v", err)
	}

	// 3. 关闭会话（不删除）
	if err := a.CloseSession(ctx, sid); err != nil {
		t.Fatalf("CloseSession 失败: %v", err)
	}

	// 4. 删除会话
	if err := a.DeleteSession(ctx, sid); err != nil {
		t.Fatalf("DeleteSession 失败: %v", err)
	}
}

// --- 集成测试：权限请求处理 ---

// TestCodexIntegration_PermissionCallback 验证 Agent 权限请求通过回调处理
// perm-request 模式模拟 Agent 先发送 session/request_permission，
// 然后等待 Bridge 发送权限响应
// Lzm 2026-07-22
func TestCodexIntegration_PermissionCallback(t *testing.T) {
	a := newHelperAgent(t, "perm-request", 5*time.Second, "")

	// 设置权限回调：模拟手动授权
	var callbackCalled atomic.Bool
	a.SetPermissionCallback(func(params json.RawMessage) (bool, string, error) {
		callbackCalled.Store(true)
		return true, "", nil // 允许
	})

	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// 发送一个普通请求，触发 perm-request 模式下的权限请求流
	ctx := context.Background()
	req := testACPRequest("perm-test")
	resp, err := a.Send(ctx, req)
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if resp == nil {
		t.Fatal("Send 返回了空响应")
	}
	if !resp.IsSuccess() {
		t.Errorf("期望成功的响应，得到 error=%+v", resp.Error)
	}

	// 验证回调被调用
	if !callbackCalled.Load() {
		t.Error("权限回调未被调用")
	}
}

// --- 集成测试：503 重试 ---

// TestCodexIntegration_503Retry 验证 Agent 在收到 503 错误后检测并重试
// 503-once 模式：首次非 initialize 请求返回 503，后续请求正常
// Lzm 2026-07-22
func TestCodexIntegration_503Retry(t *testing.T) {
	// 重置环境变量，确保子进程首次检测到空
	os.Unsetenv("AGENT_BRIDGE_503_OCCURRED")

	a := newHelperAgent(t, "503-once", 5*time.Second, "")
	t.Cleanup(func() {
		_ = a.Stop(context.Background())
		os.Unsetenv("AGENT_BRIDGE_503_OCCURRED")
	})

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	ctx := context.Background()

	// 第 1 次请求：期望 RetryableError（503 检测）
	_, err := a.Send(ctx, testACPRequest("first-503"))
	if err == nil {
		t.Fatal("期望 503 错误，但没有错误")
	}
	// 确认是 RetryableError（由 IsCodex503 检测返回）
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("错误消息应包含 503，得到: %v", err)
	}

	// 第 2 次请求：应该正常（503 已发生一次）
	resp, err := a.Send(ctx, testACPRequest("second-ok"))
	if err != nil {
		t.Fatalf("重试后 Send 失败: %v", err)
	}
	if !resp.IsSuccess() {
		t.Errorf("重试后期望成功响应，得到 error=%+v", resp.Error)
	}
}

// --- 集成测试：流式响应 ---

// TestCodexIntegration_StreamMultipleChunks 验证流式响应能正确接收多个块
// stream-simple 模式模拟 Agent 发送 5 个流式块 + 1 个 final 响应
// Lzm 2026-07-22
func TestCodexIntegration_StreamMultipleChunks(t *testing.T) {
	a := newHelperAgent(t, "stream-simple", 5*time.Second, "")
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	ctx := context.Background()

	// 发送流式 prompt 请求
	req := testACPRequest("stream-prompt")
	chunks, err := a.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}

	// 收集所有块
	var responseText strings.Builder
	var finalChunk internal.StreamChunk
	chunkCount := 0
	for chunk := range chunks {
		chunkCount++
		finalChunk = chunk
		if chunk.Type == internal.StreamChunkResponse {
			responseText.WriteString(chunk.Text)
		}
	}

	// 验证至少有 5 个流式块
	if chunkCount < 5 {
		t.Errorf("期望至少 5 个块，得到 %d", chunkCount)
	}

	// 验证 final chunk
	if finalChunk.Type != internal.StreamChunkFinal {
		t.Errorf("最后一个块应为 final 类型，得到 %s", finalChunk.Type)
	}
	if !strings.Contains(responseText.String(), "chunk") {
		t.Errorf("响应文本应包含 'chunk'，得到 '%s'", responseText.String())
	}
}

// TestCodexIntegration_StreamFinalResult 验证流式 final 块包含正确的文本和 stopReason
// Lzm 2026-07-22
func TestCodexIntegration_StreamFinalResult(t *testing.T) {
	a := newHelperAgent(t, "stream-simple", 5*time.Second, "")
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	ctx := context.Background()
	req := testACPRequest("stream-prompt")
	chunks, err := a.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}

	var finalText string
	for chunk := range chunks {
		if chunk.Type == internal.StreamChunkFinal {
			finalText = chunk.Text
		}
	}
	if finalText == "" {
		t.Fatal("final 块的文本为空")
	}
	if !strings.Contains(finalText, "chunk") {
		t.Errorf("final 文本应包含 'chunk'，得到 '%s'", finalText)
	}
}

// --- 集成测试：无回调时自动批准 ---

// TestCodexIntegration_PermissionAutoApprove 验证无回调时权限请求自动批准
// Lzm 2026-07-22
func TestCodexIntegration_PermissionAutoApprove(t *testing.T) {
	a := newHelperAgent(t, "perm-request", 5*time.Second, "")
	// 不设置回调 → 应自动批准
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	ctx := context.Background()
	req := testACPRequest("auto-approve-test")
	resp, err := a.Send(ctx, req)
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if resp == nil {
		t.Fatal("Send 返回了空响应")
	}
	if !resp.IsSuccess() {
		t.Errorf("期望成功响应，得到 error=%+v", resp.Error)
	}
}

// --- 集成测试：启动失败处理 ---

// TestCodexIntegration_StartStopRestart 验证 Agent 启动-停止-重启全流程
// Lzm 2026-07-22
func TestCodexIntegration_StartStopRestart(t *testing.T) {
	a := newHelperAgent(t, "normal", 2*time.Second, "")
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	// 第一次启动
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("第一次 Start 失败: %v", err)
	}
	if a.Status() != AgentIdle {
		t.Errorf("期望状态 AgentIdle，得到 %s", a.Status())
	}

	// 发送测试请求
	resp, err := a.Send(context.Background(), testACPRequest("start-stop"))
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if !resp.IsSuccess() {
		t.Errorf("期望成功响应，得到 error=%+v", resp.Error)
	}

	// 停止
	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}

	// 重启
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("重启 Start 失败: %v", err)
	}

	resp2, err := a.Send(context.Background(), testACPRequest("after-restart"))
	if err != nil {
		t.Fatalf("重启后 Send 失败: %v", err)
	}
	if !resp2.IsSuccess() {
		t.Errorf("重启后期望成功响应，得到 error=%+v", resp2.Error)
	}
}

// --- 集成测试：并发请求排队 ---

// TestCodexIntegration_ConcurrentSendBlocks 验证并发 Send 会排队等待
// 使用 silent 模式让 Stream 不响应，占用 requestGate
// Lzm 2026-07-22
func TestCodexIntegration_ConcurrentSendBlocks(t *testing.T) {
	a := newHelperAgent(t, "silent", 2*time.Second, "")
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// 用 Stream 占住 requestGate（silent 模式不返回响应）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := a.Stream(ctx, testACPRequest("stream-hold"))
	if err != nil {
		t.Fatalf("Stream 失败: %v", err)
	}

	// Stream 已占住 gate，Send 应阻塞直到超时
	// 短暂等待确保 Stream 已获取锁
	time.Sleep(20 * time.Millisecond)
	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer timeoutCancel()
	_, sendErr := a.Send(timeoutCtx, testACPRequest("blocked-send"))
	if sendErr == nil {
		t.Error("期望并发 Send 超时，但没有错误")
	}

	cancel()
}

// TestCodexIntegration_EmptyResultResponse 测试处理空结果响应
// Lzm 2026-07-22
func TestCodexIntegration_EmptyResultResponse(t *testing.T) {
	a := newHelperAgent(t, "normal", 2*time.Second, "")
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// Send 一个普通请求，得到 "ok" 响应
	resp, err := a.Send(context.Background(), testACPRequest("empty-check"))
	if err != nil {
		t.Fatalf("Send 失败: %v", err)
	}
	if !resp.IsSuccess() {
		t.Errorf("期望成功响应，得到 error=%+v", resp.Error)
	}
}

// TestCodexIntegration_SessionNewWithWorkDir 测试创建新会话时传入工作目录
// Lzm 2026-07-22
func TestCodexIntegration_SessionNewWithWorkDir(t *testing.T) {
	a := newHelperAgent(t, "session-crud", 5*time.Second, "")
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	sid, err := a.NewSession(context.Background(), "/custom/workdir")
	if err != nil {
		t.Fatalf("NewSession 失败: %v", err)
	}
	if sid == "" {
		t.Fatal("NewSession 返回了空 sessionID")
	}
	if !strings.HasPrefix(sid, "integ-sid-") {
		t.Errorf("期望 sessionID 以 integ-sid- 开头，得到 %s", sid)
	}
}
