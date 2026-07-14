// -*- coding: utf-8 -*-
// Go 1.25+
//
// logger.go
// 日志初始化与配置，基于 log/slog
//
// Lzm 2026-07-09

package infra

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// LogDir 日志文件存放目录
	LogDir = ".agent-bridge/logs"
)

// InitLogger 初始化 slog 日志器
//   - 开发模式：输出到控制台，带颜色级别，格式友好
//   - 生产模式：同时输出到控制台和日志文件，JSON 格式
func InitLogger(debug bool) error {
	var handlers []slog.Handler
	options := &slog.HandlerOptions{
		Level:       getLogLevel(debug),
		AddSource:   debug,
		ReplaceAttr: replaceLogAttribute,
	}

	// 控制台输出（始终开启）
	consoleHandler := slog.NewTextHandler(os.Stderr, options)
	handlers = append(handlers, consoleHandler)

	// Local Console 的日志页在普通模式下也必须可用。
	logPath := getLogFilePath()
	if logPath != "" && ensurePrivateDirectory(filepath.Dir(logPath)) == nil {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			if err := f.Chmod(0o600); err == nil {
				fileHandler := slog.NewJSONHandler(f, options)
				handlers = append(handlers, fileHandler)
			} else {
				_ = f.Close()
			}
		}
	}

	// 组合多个 handler
	if len(handlers) == 1 {
		slog.SetDefault(slog.New(handlers[0]))
	} else {
		slog.SetDefault(slog.New(newTeeHandler(handlers...)))
	}

	return nil
}

func replaceLogAttribute(_ []string, attribute slog.Attr) slog.Attr {
	if attribute.Key == slog.TimeKey {
		return slog.Attr{
			Key:   slog.TimeKey,
			Value: slog.StringValue(attribute.Value.Time().Format("2006-01-02 15:04:05")),
		}
	}
	if isSensitiveLogKey(attribute.Key) {
		return slog.String(attribute.Key, "[REDACTED]")
	}
	return attribute
}

func isSensitiveLogKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	switch key {
	case "token", "authorization", "api_key", "bridge_token", "setup_token", "pairing_code",
		"content", "prompt", "message", "message_body", "request_body", "response_body":
		return true
	default:
		return false
	}
}

// getLogLevel 根据 debug 模式返回日志级别
func getLogLevel(debug bool) slog.Leveler {
	if debug {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// getLogFilePath 获取日志文件路径
func getLogFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, LogDir,
		time.Now().Format("2006-01-02")+".log")
}

// teeHandler 将日志同时写入多个 handler
type teeHandler struct {
	handlers []slog.Handler
}

func newTeeHandler(handlers ...slog.Handler) *teeHandler {
	return &teeHandler{handlers: handlers}
}

func (h *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, handler := range h.handlers {
		if !handler.Enabled(ctx, r.Level) {
			continue
		}
		if err := handler.Handle(ctx, r.Clone()); err != nil {
			return err
		}
	}
	return nil
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newHandlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		newHandlers[i] = handler.WithAttrs(attrs)
	}
	return &teeHandler{handlers: newHandlers}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	newHandlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		newHandlers[i] = handler.WithGroup(name)
	}
	return &teeHandler{handlers: newHandlers}
}

// 确保 teeHandler 实现 slog.Handler 接口
var _ slog.Handler = (*teeHandler)(nil)
