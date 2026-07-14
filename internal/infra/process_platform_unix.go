//go:build darwin || linux

package infra

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// CommandContext creates a child command using the platform's native launch
// boundary. Unix executables and shebang scripts can be started directly.
func CommandContext(ctx context.Context, command string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, command, args...)
}

type unixProcessTree struct {
	mu                 sync.Mutex
	processGroupID     int
	terminateRequested bool
	terminated         bool
	terminateErr       error
}

func configureProcessTree(cmd *exec.Cmd) (processTree, error) {
	tree := &unixProcessTree{}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = tree.terminate
	return tree, nil
}

func (tree *unixProcessTree) attach(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}

	tree.mu.Lock()
	defer tree.mu.Unlock()
	tree.processGroupID = cmd.Process.Pid
	if tree.terminateRequested {
		return tree.terminateLocked()
	}
	return nil
}

func (tree *unixProcessTree) terminate() error {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	tree.terminateRequested = true
	return tree.terminateLocked()
}

func (tree *unixProcessTree) terminateLocked() error {
	if tree.terminated {
		return tree.terminateErr
	}
	if tree.processGroupID == 0 {
		return nil
	}
	tree.terminated = true
	tree.terminateErr = syscall.Kill(-tree.processGroupID, syscall.SIGKILL)
	if errors.Is(tree.terminateErr, syscall.ESRCH) {
		tree.terminateErr = nil
	}
	return tree.terminateErr
}
