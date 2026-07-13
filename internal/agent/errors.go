// -*- coding: utf-8 -*-
// Go 1.25+
//
// errors.go
// Agent 相关的错误类型与恢复策略定义
// 处理不同 Agent 的异常模式（EPERM、503、会话超时等）
//
// Lzm 2026-07-09

package agent

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// --- 错误类型定义 ---

// RetryableError 表示可重试的错误
type RetryableError struct {
	Err        error
	RetryAfter time.Duration // 重试前等待时间
}

func (e *RetryableError) Error() string {
	return fmt.Sprintf("retryable error (retry after %v): %v", e.RetryAfter, e.Err)
}

func (e *RetryableError) Unwrap() error {
	return e.Err
}

// SessionError 会话相关错误
type SessionError struct {
	SessionID string
	Err       error
}

func (e *SessionError) Error() string {
	return fmt.Sprintf("session %s: %v", e.SessionID, e.Err)
}

func (e *SessionError) Unwrap() error {
	return e.Err
}

// AgentStartError Agent 启动失败错误
type AgentStartError struct {
	AgentID string
	Err     error
}

func (e *AgentStartError) Error() string {
	return fmt.Sprintf("start agent %s: %v", e.AgentID, e.Err)
}

func (e *AgentStartError) Unwrap() error {
	return e.Err
}

// --- EPERM 检测与处理（Kimi/Codex on Windows） ---

var epermRegex = regexp.MustCompile(`mkdir '([^']+)'`)

// HandleEPERM 检测并尝试修复 EPERM 错误
// Windows 上某些 Agent（Kimi、Codex）会因目录权限问题报 EPERM
// 策略：提取路径 → 创建目录 → 返回可重试错误
// Lzm 2026-07-09
func HandleEPERM(err error) error {
	if err == nil {
		return nil
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "EPERM") && !strings.Contains(errStr, "Access is denied") {
		return nil // 不是 EPERM 错误
	}

	// 尝试提取路径
	if m := epermRegex.FindStringSubmatch(errStr); len(m) > 1 {
		// 返回可重试错误，调用方需创建目录后重试
		return &RetryableError{
			Err:        fmt.Errorf("EPERM on directory %s", m[1]),
			RetryAfter: 300 * time.Millisecond,
		}
	}
	return nil
}

// --- Codex 503 检测 ---

// IsCodex503 检测 Codex 的 503 Service Unavailable 错误
// Codex 的上游 API 经常返回 503，需要重试
// Lzm 2026-07-09
func IsCodex503(text string) bool {
	return strings.Contains(text, "503") &&
		(strings.Contains(text, "Service Unavailable") ||
			strings.Contains(text, "service unavailable") ||
			strings.Contains(text, "503 Service Temporarily Unavailable"))
}

// --- 重试工具 ---

// RetryConfig 重试配置
type RetryConfig struct {
	MaxRetries     int
	BaseBackoff    time.Duration
	MaxBackoff     time.Duration
	RetryableCheck func(error) bool // 判断错误是否可重试
}

// DefaultRetryConfig 默认重试配置
var DefaultRetryConfig = RetryConfig{
	MaxRetries:  3,
	BaseBackoff: 1 * time.Second,
	MaxBackoff:  10 * time.Second,
	RetryableCheck: func(err error) bool {
		if err == nil {
			return false
		}
		// 所有 RetryableError 都是可重试的
		var retryable *RetryableError
		return strings.Contains(err.Error(), "503") ||
			strings.Contains(err.Error(), "timeout") ||
			strings.Contains(err.Error(), "temporary") ||
			strings.Contains(err.Error(), "EPERM") ||
			strings.Contains(err.Error(), "connection refused") ||
			strings.Contains(err.Error(), "reset by peer") ||
			strings.Contains(err.Error(), "broken pipe") ||
			strings.Contains(err.Error(), "interrupted") ||
			strings.Contains(err.Error(), "closed network connection") ||
			strings.Contains(err.Error(), "port") ||
			strings.Contains(err.Error(), "permission denied") ||
			strings.Contains(err.Error(), "too many requests") ||
			strings.Contains(err.Error(), "rate limit") ||
			strings.Contains(err.Error(), "429") ||
			strings.Contains(err.Error(), "502") ||
			strings.Contains(err.Error(), "503") ||
			strings.Contains(err.Error(), "504") ||
			// 也可以直接匹配 RetryableError 类型
			errorsAs(err, &retryable)
	},
}

// errorsAs 检查 error 是否可以转换为目标类型
func errorsAs(err error, target interface{}) bool {
	if err == nil {
		return false
	}
	// 简单实现：递归 Unwrap
	for {
		if fmt.Sprintf("%T", err) == fmt.Sprintf("%T", target) {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
		if err == nil {
			return false
		}
	}
}

// RetryWithBackoff 带指数退避的重试执行
// Lzm 2026-07-09
func RetryWithBackoff(ctx interface{ Done() <-chan struct{} }, fn func() error, cfg RetryConfig) error {
	var lastErr error
	backoff := cfg.BaseBackoff

	for i := 0; i <= cfg.MaxRetries; i++ {
		if i > 0 {
			// 检查是否应该重试
			if !cfg.RetryableCheck(lastErr) {
				return lastErr
			}
			// 等待退避时间
			select {
			case <-ctx.Done():
				return fmt.Errorf("retry cancelled: %w", ctx.(interface{ Err() error }).Err())
			case <-time.After(backoff):
			}
			// 指数退避
			backoff *= 2
			if backoff > cfg.MaxBackoff {
				backoff = cfg.MaxBackoff
			}
		}

		if err := fn(); err != nil {
			lastErr = err
			continue
		}
		return nil // 成功
	}

	return fmt.Errorf("retry exhausted after %d attempts: %w", cfg.MaxRetries, lastErr)
}
