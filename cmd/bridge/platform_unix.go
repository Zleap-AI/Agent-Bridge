//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"runtime"
)

func ensureUserAutostart() error { return nil }

func prepareBackgroundMode(bool) {}

func removeUserAutostart() error {
	return fmt.Errorf("此参数仅用于 Windows；macOS/Linux 请使用安装脚本卸载")
}

func openLocalConsole(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	default:
		return fmt.Errorf("当前平台不支持自动打开浏览器")
	}
}
