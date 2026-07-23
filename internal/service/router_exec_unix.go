//go:build darwin || linux

package service

import (
	"context"
	"os/exec"
)

func shellCommandContext(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", command)
}
