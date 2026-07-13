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
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

	// 控制台输出（始终开启）
	consoleHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level:     getLogLevel(debug),
		AddSource: debug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// 时间格式化
			if a.Key == slog.TimeKey {
				return slog.Attr{
					Key:   slog.TimeKey,
					Value: slog.StringValue(a.Value.Time().Format("2006-01-02 15:04:05")),
				}
			}
			return a
		},
	})
	handlers = append(handlers, consoleHandler)

	// 文件输出（debug 模式下也输出到文件）
	if debug {
		logPath := getLogFilePath()
		if err := os.MkdirAll(filepath.Dir(logPath), 0755); err == nil {
			f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err == nil {
				fileHandler := slog.NewJSONHandler(f, &slog.HandlerOptions{
					Level: slog.LevelDebug,
				})
				handlers = append(handlers, fileHandler)
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
		return filepath.Join(os.TempDir(), "Agent-Bridge.log")
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
	return true
}

func (h *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, handler := range h.handlers {
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

// nopCloser 用于包装 io.Writer 为 io.WriteCloser（兼容文件接口）
type nopCloser struct {
	io.Writer
}

func (nopCloser) Close() error { return nil }
