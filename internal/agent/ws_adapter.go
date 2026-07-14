// -*- coding: utf-8 -*-
// Go 1.25+
//
// ws_adapter.go
// WebSocket ACP 适配器 — 将 WebSocket ACP 服务端桥接到 io.ReadWriter
// 用于对接 macOS 上以 WebSocket 方式提供 ACP 协议的 Agent（如 opencode acp）
//
// Lzm 2026-07-14

package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/infra"
)

// wsACPAdapter 将 WebSocket ACP 连接适配为 io.ReadWriter
//   - ACPWriter.WriteMessage → adapter.Write(json+newline) → conn.SendText(json)
//   - conn onMessage(json) → pipe.Write(json+newline) → ACPReader.ReadMessage → parse
//
// 这样 baseAgent 的 ACP 消息处理逻辑完全复用，底层传输透明切换
// Lzm 2026-07-14
type wsACPAdapter struct {
	conn      *infra.WSClient     // WebSocket 连接
	cmd       *infra.ProcessManager // 子进程管理器
	readPipe  *io.PipeReader       // ACPReader 从此读取（来自 WebSocket）
	writePipe *io.PipeWriter       // WebSocket onMessage 写入此
	closeOnce sync.Once
	closed    bool
}

// newWSACPAdapter 创建并连接 WebSocket ACP 适配器
// 启动子进程后，连接其 WebSocket ACP 端口，建立桥接管道
func newWSACPAdapter(ctx context.Context, cmd *infra.ProcessManager, port int) (*wsACPAdapter, error) {
	pr, pw := io.Pipe()
	url := fmt.Sprintf("ws://127.0.0.1:%d", port)

	conn, err := infra.NewWSClient(ctx, infra.WSClientConfig{
		URL: url,
		OnMessage: func(data []byte) {
			// 每条 WebSocket 消息直接写入管道，ACPReader 按行解析
			if _, err := pw.Write(data); err != nil {
				slog.Warn("WS→Pipe 写入失败",
					"url", url,
					"error", err,
				)
			}
		},
		OnError: func(err error) {
			pw.CloseWithError(err)
		},
	})
	if err != nil {
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("连接 WebSocket ACP %s 失败: %w", url, err)
	}

	slog.Info("WebSocket ACP 已连接",
		"url", url,
		"pid", cmd.PID(),
	)

	return &wsACPAdapter{
		conn:      conn,
		cmd:       cmd,
		readPipe:  pr,
		writePipe: pw,
	}, nil
}

// Write 实现 io.Writer — ACPWriter 写入时调用
// ACPWriter.WriteMessage 会写入 "json-line\n"，需要去掉末尾换行符再发送 WebSocket 消息
func (a *wsACPAdapter) Write(p []byte) (int, error) {
	if a.closed {
		return 0, fmt.Errorf("WebSocket ACP 已关闭")
	}
	// 去掉末尾换行符（WebSocket 消息自带帧边界）
	data := bytes.TrimRight(p, "\n\r")
	if len(data) == 0 {
		return len(p), nil
	}
	if err := a.conn.SendText(data); err != nil {
		return 0, fmt.Errorf("ACP→WS 发送失败: %w", err)
	}
	return len(p), nil
}

// Read 实现 io.Reader — ACPReader 读取时调用（从管道读取 WebSocket 消息）
func (a *wsACPAdapter) Read(p []byte) (int, error) {
	if a.closed {
		return 0, fmt.Errorf("WebSocket ACP 已关闭")
	}
	return a.readPipe.Read(p)
}

// Close 关闭 WebSocket 连接并终止子进程
func (a *wsACPAdapter) Close() error {
	var err error
	a.closeOnce.Do(func() {
		a.closed = true
		// 关闭管道（通知 ACPReader 结束）
		a.writePipe.Close()
		a.readPipe.Close()
		// 关闭 WebSocket
		a.conn.Close()
		// 终止子进程
		a.cmd.Stop()
	})
	return err
}

// findFreePort 查找系统空闲 TCP 端口
// 用于 opencode acp --port 指定端口
func findFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	// 短暂等待确保端口释放
	time.Sleep(50 * time.Millisecond)
	return port, nil
}
