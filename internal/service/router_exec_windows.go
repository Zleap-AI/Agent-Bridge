//go:build windows

package service

import (
	"context"
	"os/exec"
)

func shellCommandContext(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command)
}
