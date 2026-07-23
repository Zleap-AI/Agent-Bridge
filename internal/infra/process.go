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
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// ProcessManager 管理单个子进程的生命周期
type ProcessManager struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser
	cancel  context.CancelFunc
	done    chan struct{}
	waitErr error // cmd.Wait 的返回结果
	mu      sync.Mutex
}

// processTree owns the platform-specific boundary used to terminate every
// descendant of one Agent process generation. terminate must be idempotent:
// context cancellation and cmd.Wait can race while cleaning up the same tree.
type processTree interface {
	attach(*exec.Cmd) error
	terminate() error
}

// StartProcessConfig 进程启动配置
type StartProcessConfig struct {
	Command  string            // 可执行文件路径
	Args     []string          // 命令行参数
	WorkDir  string            // 工作目录
	Env      map[string]string // 额外环境变量
	PathDirs []string          // 在父进程 PATH 之外补充的可执行文件目录

	// DisableProcessTree disables descendant cleanup for processes that cannot
	// run inside the platform process boundary, such as Codex on Windows.
	// Process-tree management is enabled by default.
	DisableProcessTree bool
}

// StartProcess 启动一个新子进程
// ctx 用于控制进程生命周期，取消 ctx 将终止进程
func StartProcess(ctx context.Context, cfg StartProcessConfig) (*ProcessManager, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd := CommandContext(ctx, cfg.Command, cfg.Args...)
	useTree := !cfg.DisableProcessTree
	var tree processTree
	if useTree {
		var err error
		tree, err = configureProcessTree(cmd)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("配置进程树边界失败: %w", err)
		}
	}
	cmd.Dir = cfg.WorkDir

	cmd.Env = processEnvironment(cmd.Environ(), cfg)

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
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	if err := cmd.Start(); err != nil {
		if tree != nil {
			_ = tree.terminate()
		}
		cancel()
		return nil, fmt.Errorf("启动进程 %s 失败: %w", cfg.Command, err)
	}
	if tree != nil {
		if err := tree.attach(cmd); err != nil {
			_ = tree.terminate()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			cancel()
			return nil, fmt.Errorf("建立进程树边界失败: %w", err)
		}
	}

	// 清理必须紧邻 Wait 且只执行一次。否则自然退出的父进程会遗留子进程，
	// 而很晚才调用 Stop 又可能把已经复用的 PID/进程组误认为旧 Agent。
	go func() {
		waitErr := cmd.Wait()
		if tree != nil {
			if cleanupErr := tree.terminate(); cleanupErr != nil {
				waitErr = errors.Join(waitErr, fmt.Errorf("清理 Agent 子进程树失败: %w", cleanupErr))
			}
		}
		pm.waitErr = waitErr
		close(pm.done)
	}()

	return pm, nil
}

func processEnvironment(base []string, cfg StartProcessConfig) []string {
	overrides := make(map[string]string, len(cfg.Env)+1)
	parentPath := environmentValue(base, "PATH")
	for key, value := range cfg.Env {
		overrides[key] = value
		if environmentKey(key) == environmentKey("PATH") {
			parentPath = value
		}
	}

	if processPath := buildProcessPath(cfg.Command, cfg.PathDirs, parentPath); processPath != "" {
		for key := range overrides {
			if environmentKey(key) == environmentKey("PATH") {
				delete(overrides, key)
			}
		}
		overrides["PATH"] = processPath
	}

	return mergeEnvironment(base, overrides)
}

func environmentValue(entries []string, name string) string {
	wanted := environmentKey(name)
	var result string
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok && environmentKey(key) == wanted {
			result = value
		}
	}
	return result
}

// buildProcessPath constructs a deterministic child PATH without invoking a
// login shell. Keeping the absolute command's directory first is important for
// npm shims whose #!/usr/bin/env node interpreter lives beside the shim.
func buildProcessPath(command string, extraDirs []string, parentPath string) string {
	dirs := make([]string, 0, len(extraDirs)+2)
	if filepath.IsAbs(command) {
		dirs = append(dirs, filepath.Dir(command))
	}
	dirs = append(dirs, extraDirs...)
	dirs = append(dirs, filepath.SplitList(parentPath)...)

	unique := make([]string, 0, len(dirs))
	seen := make(map[string]struct{}, len(dirs))
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		dir = filepath.Clean(dir)
		key := environmentKey(dir)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, dir)
	}
	return strings.Join(unique, string(os.PathListSeparator))
}

func mergeEnvironment(base []string, overrides map[string]string) []string {
	overrideKeys := make(map[string]struct{}, len(overrides))
	for key := range overrides {
		overrideKeys[environmentKey(key)] = struct{}{}
	}

	result := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if ok && key != "" {
			if _, replaced := overrideKeys[environmentKey(key)]; replaced {
				continue
			}
		}
		result = append(result, item)
	}

	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, fmt.Sprintf("%s=%s", key, overrides[key]))
	}
	return result
}

func environmentKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
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

// Stop 终止进程
//   - 先尝试关闭 stdin（让进程自行退出）
//   - 等待 3 秒后强制终止
//   - 清理资源
func (pm *ProcessManager) Stop() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pid := 0
	if pm.cmd != nil && pm.cmd.Process != nil {
		pid = pm.cmd.Process.Pid
	}

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
		return fmt.Errorf("进程 %d 强制终止超时", pid)
	}
}

// Wait 等待进程退出
func (pm *ProcessManager) Wait() error {
	<-pm.done
	return pm.waitErr
}
