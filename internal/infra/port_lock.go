// -*- coding: utf-8 -*-
// Go 1.26+
//
// port_lock.go
// 单例运行保障 — 启动时检测端口占用，自动清理旧进程
// 确保同一台机器上只有一份 bridge 实例运行
//
// Lzm 2026-07-13

package infra

import (
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// EnsurePort 确保端口可用 — 如有旧进程占用则自动清理
// 使用指数退避重试（最多 3 次），每次间隔递增
// Lzm 2026-07-13
func EnsurePort(port int) error {
	addr := fmt.Sprintf(":%d", port)

	for i := 0; i < 3; i++ {
		// 尝试监听
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
			return nil
		}

		slog.Warn("端口被占用，尝试清理旧进程",
			"port", port,
			"attempt", i+1,
		)

		// 查找并清理旧进程
		if pid := findPIDByPort(port); pid > 0 {
			slog.Info("发现旧 bridge 进程", "pid", pid)
			killProcess(pid)
			// 等待端口释放（带指数退避）
			time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
		} else {
			slog.Warn("未找到占用端口的进程，端口可能被其他程序占用",
				"port", port,
			)
			return fmt.Errorf("端口 %d 被非 bridge 进程占用，请手动释放", port)
		}
	}

	// 最终检查
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		ln.Close()
		return nil
	}
	return fmt.Errorf("端口 %d 仍然被占用: %w", port, err)
}

// findPIDByPort 查找占用指定端口的进程 PID
// 使用平台特定命令：
//
//	Windows: (Get-NetTCPConnection -LocalPort N).OwningProcess
//	macOS:   lsof -ti :N
//
// Lzm 2026-07-13
func findPIDByPort(port int) int {
	portStr := strconv.Itoa(port)

	switch runtime.GOOS {
	case "windows":
		return findPIDByPortWindows(portStr)
	case "darwin", "linux":
		return findPIDByPortUnix(portStr)
	default:
		return 0
	}
}

// findPIDByPortWindows 在 Windows 上通过 PowerShell 查找进程 PID
// Lzm 2026-07-13
func findPIDByPortWindows(port string) int {
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("(Get-NetTCPConnection -LocalPort %s -ErrorAction SilentlyContinue).OwningProcess", port),
	)
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	pidStr := strings.TrimSpace(string(output))
	if pidStr == "" {
		return 0
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0
	}
	return pid
}

// findPIDByPortUnix 在 macOS/Linux 上通过 lsof 查找进程 PID
// Lzm 2026-07-13
func findPIDByPortUnix(port string) int {
	cmd := exec.Command("lsof", "-ti", fmt.Sprintf(":%s", port))
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	pidStr := strings.TrimSpace(string(output))
	if pidStr == "" {
		return 0
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0
	}
	return pid
}

// killProcess 强制终止指定 PID 的进程
// 使用平台特定命令
// Lzm 2026-07-13
func killProcess(pid int) {
	pidStr := strconv.Itoa(pid)
	slog.Info("正在终止旧进程", "pid", pid)

	switch runtime.GOOS {
	case "windows":
		exec.Command("taskkill", "/F", "/PID", pidStr).Run()
	case "darwin", "linux":
		exec.Command("kill", "-9", pidStr).Run()
	}
}
