//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

const (
	userRunKey     = `Software\Microsoft\Windows\CurrentVersion\Run`
	autostartValue = "Agent-Bridge"
)

var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	user32                = syscall.NewLazyDLL("user32.dll")
	getConsoleWindow      = kernel32.NewProc("GetConsoleWindow")
	getConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	showWindow            = user32.NewProc("ShowWindow")
)

// prepareBackgroundMode hides only a console owned exclusively by this
// process. A manually launched --background process must not hide the user's
// existing PowerShell or Command Prompt window.
func prepareBackgroundMode(background bool) {
	if !background {
		return
	}
	processIDs := [2]uint32{}
	count, _, _ := getConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&processIDs[0])),
		uintptr(len(processIDs)),
	)
	if count != 1 {
		return
	}
	window, _, _ := getConsoleWindow.Call()
	if window != 0 {
		showWindow.Call(window, 0) // SW_HIDE
	}
}

func ensureUserAutostart() error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取程序路径失败: %w", err)
	}
	command := fmt.Sprintf("\"%s\" --background", executable)
	key, _, err := registry.CreateKey(registry.CURRENT_USER, userRunKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开用户启动项失败: %w", err)
	}
	defer key.Close()
	if err := key.SetStringValue(autostartValue, command); err != nil {
		return fmt.Errorf("写入用户启动项失败: %w", err)
	}
	return nil
}

func removeUserAutostart() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, userRunKey, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("打开用户启动项失败: %w", err)
	}
	defer key.Close()
	if err := key.DeleteValue(autostartValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("删除用户启动项失败: %w", err)
	}
	return nil
}

func openLocalConsole(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
