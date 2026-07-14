// -*- coding: utf-8 -*-
// Go 1.25+
//
// process.go
// Agent 子进程管理：启动、监控、终止
//
// Lzm 2026-07-09

package infra

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// ProcessManager 管理单个子进程的生命周期
type ProcessManager struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    io.ReadCloser
	cancel    context.CancelFunc
	done      chan struct{}
	waitErr   error // cmd.Wait 的返回结果
	mu        sync.Mutex
	startedAt time.Time
}

// StartProcessConfig 进程启动配置
type StartProcessConfig struct {
	Command string            // 可执行文件路径
	Args    []string          // 命令行参数
	WorkDir string            // 工作目录
	Env     map[string]string // 额外环境变量
}

// StartProcess 启动一个新子进程
// ctx 用于控制进程生命周期，取消 ctx 将终止进程
func StartProcess(ctx context.Context, cfg StartProcessConfig) (*ProcessManager, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.Dir = cfg.WorkDir

	// 设置环境变量
	if cfg.Env != nil {
		cmd.Env = append(cmd.Environ(), mapToEnvSlice(cfg.Env)...)
	}

	// 获取管道
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("创建 stdin 管道失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("创建 stderr 管道失败: %w", err)
	}

	pm := &ProcessManager{
		cmd:       cmd,
		stdin:     stdin,
		stdout:    stdout,
		stderr:    stderr,
		cancel:    cancel,
		done:      make(chan struct{}),
		startedAt: time.Now(),
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("启动进程 %s 失败: %w", cfg.Command, err)
	}

	// 后台等待进程退出
	go func() {
		pm.waitErr = cmd.Wait()
		close(pm.done)
	}()

	return pm, nil
}

// Stdin 返回进程的 stdin 写入器
func (pm *ProcessManager) Stdin() io.WriteCloser {
	return pm.stdin
}

// Stdout 返回进程的 stdout 读取器
func (pm *ProcessManager) Stdout() io.Reader {
	return pm.stdout
}

// StderrReader 返回进程的 stderr 读取器
func (pm *ProcessManager) StderrReader() io.Reader {
	return pm.stderr
}

// PID 返回进程 ID
func (pm *ProcessManager) PID() int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.cmd == nil || pm.cmd.Process == nil {
		return 0
	}
	return pm.cmd.Process.Pid
}

// IsRunning 检查进程是否仍在运行
func (pm *ProcessManager) IsRunning() bool {
	select {
	case <-pm.done:
		return false
	default:
		return true
	}
}

// Uptime 返回进程已运行时间
func (pm *ProcessManager) Uptime() time.Duration {
	return time.Since(pm.startedAt)
}

// Stop 终止进程
//   - 先尝试关闭 stdin（让进程自行退出）
//   - 等待 3 秒后强制终止
//   - 清理资源
func (pm *ProcessManager) Stop() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	select {
	case <-pm.done:
		return nil // 已退出
	default:
	}

	// 先关闭 stdin（通知进程退出）
	if pm.stdin != nil {
		pm.stdin.Close()
	}

	// 等待进程自行退出
	select {
	case <-pm.done:
		return nil
	case <-time.After(3 * time.Second):
		// 超时，强制结束
	}

	// 强制终止
	pm.cancel()

	// 等待进程完全退出
	select {
	case <-pm.done:
		return nil
	case <-time.After(2 * time.Second):
		return fmt.Errorf("进程 %d 强制终止超时", pm.PID())
	}
}

// Wait 等待进程退出
func (pm *ProcessManager) Wait() error {
	<-pm.done
	return pm.waitErr
}

// ExitCode 返回进程退出码（进程尚未退出时返回 -1）
func (pm *ProcessManager) ExitCode() int {
	if pm.cmd == nil || pm.cmd.ProcessState == nil {
		return -1
	}
	return pm.cmd.ProcessState.ExitCode()
}

// mapToEnvSlice 将 map[string]string 转为 []string（格式：KEY=VALUE）
func mapToEnvSlice(env map[string]string) []string {
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, fmt.Sprintf("%s=%s", k, v))
	}
	return result
}
