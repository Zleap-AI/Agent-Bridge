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
	"errors"
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
			errors.As(err, &retryable)
	},
}

// --- 统一错误码系统 ---
// 将各 Agent 的错误信息格式转换为统一的 SaaS 错误码
// Lzm 2026-07-20

// AgentErrorCode 表示 Agent 错误的统一分类编码
type AgentErrorCode string

const (
	ErrCodeStartFailed       AgentErrorCode = "AGENT_START_FAILED"        // Agent 进程启动失败
	ErrCodeAuthFailed        AgentErrorCode = "AGENT_AUTH_FAILED"         // API Key 认证失败
	ErrCodeTimeout           AgentErrorCode = "AGENT_TIMEOUT"             // 请求超时
	ErrCodeProcessExited     AgentErrorCode = "AGENT_PROCESS_EXITED"      // 进程意外退出
	ErrCodeHandshakeFailed   AgentErrorCode = "AGENT_HANDSHAKE_FAILED"    // ACP 握手失败
	ErrCodePermissionDenied  AgentErrorCode = "AGENT_PERMISSION_DENIED"   // 权限请求被拒绝 (Codex)
	ErrCodeEPERM             AgentErrorCode = "AGENT_EPERM"               // Windows EPERM 错误
	ErrCodeNotReady          AgentErrorCode = "AGENT_NOT_READY"           // Agent 未就绪（状态不对）
	ErrCodeSessionNotFound   AgentErrorCode = "AGENT_SESSION_NOT_FOUND"  // 会话不存在
)

// AgentCodedError 携带统一错误码的 Agent 错误
type AgentCodedError struct {
	Code       AgentErrorCode
	Message    string
	WrappedErr error
}

func (e AgentCodedError) Error() string {
	if e.WrappedErr != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.WrappedErr)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e AgentCodedError) Unwrap() error {
	return e.WrappedErr
}

// MapError 将任意错误映射为统一 AgentCodedError
// 按类型优先匹配，再按错误文本关键词匹配
// Lzm 2026-07-20
func MapError(err error) AgentCodedError {
	if err == nil {
		return AgentCodedError{}
	}

	// --- 类型匹配 ---
	var startErr *AgentStartError
	if errors.As(err, &startErr) {
		return AgentCodedError{
			Code:       ErrCodeStartFailed,
			Message:    startErr.Error(),
			WrappedErr: err,
		}
	}

	// --- 文本关键词匹配 ---
	errStr := err.Error()
	errLower := strings.ToLower(errStr)

	switch {
	case strings.Contains(errStr, "EPERM") || strings.Contains(errStr, "Access is denied") ||
		strings.Contains(errLower, "permission"):
		return AgentCodedError{
			Code:       ErrCodeEPERM,
			Message:    errStr,
			WrappedErr: err,
		}

	case strings.Contains(errLower, "timeout") || strings.Contains(errLower, "deadline"):
		return AgentCodedError{
			Code:       ErrCodeTimeout,
			Message:    errStr,
			WrappedErr: err,
		}

	case strings.Contains(errLower, "handshake") || strings.Contains(errLower, "initialize"):
		return AgentCodedError{
			Code:       ErrCodeHandshakeFailed,
			Message:    errStr,
			WrappedErr: err,
		}

	case strings.Contains(errStr, "API_KEY") || strings.Contains(errLower, "api_key") ||
		strings.Contains(errLower, "auth") || strings.Contains(errLower, "login"):
		return AgentCodedError{
			Code:       ErrCodeAuthFailed,
			Message:    errStr,
			WrappedErr: err,
		}

	case strings.Contains(errLower, "session") && strings.Contains(errLower, "not found"):
		return AgentCodedError{
			Code:       ErrCodeSessionNotFound,
			Message:    errStr,
			WrappedErr: err,
		}

	case strings.Contains(errStr, "未就绪") || strings.Contains(errLower, "not ready") ||
		strings.Contains(errLower, "disconnected"):
		return AgentCodedError{
			Code:       ErrCodeNotReady,
			Message:    errStr,
			WrappedErr: err,
		}

	case strings.Contains(errStr, "过早退出") || strings.Contains(errLower, "process exited") ||
		strings.Contains(errLower, "进程退出"):
		return AgentCodedError{
			Code:       ErrCodeProcessExited,
			Message:    errStr,
			WrappedErr: err,
		}
	}

	// 默认：保留原错误（Code 为空字符串）
	return AgentCodedError{
		Code:       "",
		Message:    errStr,
		WrappedErr: err,
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
