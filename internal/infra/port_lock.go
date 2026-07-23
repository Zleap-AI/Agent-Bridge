package infra

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// EnsureAddress verifies that the Local HTTP address is available. An existing
// listener is replaced only when its executable is positively identified as
// Agent-Bridge Local; unrelated processes are never terminated.
func EnsureAddress(host string, port int) error {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	for attempt := 1; attempt <= 3; attempt++ {
		listener, err := net.Listen("tcp", address)
		if err == nil {
			_ = listener.Close()
			return nil
		}

		pid := findPIDByPort(port)
		if pid <= 0 {
			return fmt.Errorf("地址 %s 已被占用，无法识别占用进程", address)
		}
		owner := processExecutable(pid)
		current, _ := os.Executable()
		if !canReplacePortOwner(owner, current) {
			if owner == "" {
				owner = "unknown"
			}
			return fmt.Errorf("地址 %s 已被其他程序占用（PID %d，%s），Agent-Bridge 不会终止该进程", address, pid, owner)
		}

		slog.Info("正在替换旧 Agent-Bridge Local 进程", "pid", pid)
		if err := killProcess(pid); err != nil {
			return fmt.Errorf("停止旧 Agent-Bridge Local（PID %d）失败: %w", pid, err)
		}
		time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
	}
	return fmt.Errorf("地址 %s 在停止旧 Agent-Bridge Local 后仍被占用", address)
}

func findPIDByPort(port int) int {
	portText := strconv.Itoa(port)
	if runtime.GOOS == "windows" {
		return findPIDByPortWindows(portText)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		return findPIDByPortUnix(portText)
	}
	return 0
}

func findPIDByPortWindows(port string) int {
	command := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("(Get-NetTCPConnection -State Listen -LocalPort %s -ErrorAction SilentlyContinue | Select-Object -First 1).OwningProcess", port),
	)
	output, err := command.Output()
	if err != nil {
		return 0
	}
	return firstPID(string(output))
}

func findPIDByPortUnix(port string) int {
	output, err := exec.Command("lsof", "-nP", "-t", "-iTCP:"+port, "-sTCP:LISTEN").Output()
	if err != nil {
		return 0
	}
	return firstPID(string(output))
}

func firstPID(output string) int {
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return 0
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0
	}
	return pid
}

func processExecutable(pid int) string {
	switch runtime.GOOS {
	case "windows":
		output, err := exec.Command("powershell", "-NoProfile", "-Command",
			fmt.Sprintf("(Get-Process -Id %d -ErrorAction SilentlyContinue).Path", pid),
		).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(output))
	case "linux":
		path, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil {
			return ""
		}
		return strings.TrimSuffix(path, " (deleted)")
	case "darwin":
		output, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "txt", "-Fn").Output()
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(output), "\n") {
			if strings.HasPrefix(line, "n/") {
				return strings.TrimSpace(strings.TrimPrefix(line, "n"))
			}
		}
	}
	return ""
}

func canReplacePortOwner(ownerExecutable, currentExecutable string) bool {
	ownerExecutable = strings.Trim(strings.TrimSpace(ownerExecutable), `"`)
	currentExecutable = strings.Trim(strings.TrimSpace(currentExecutable), `"`)
	if ownerExecutable == "" || currentExecutable == "" {
		return false
	}
	ownerPath, ownerOK := canonicalExecutablePath(ownerExecutable)
	currentPath, currentOK := canonicalExecutablePath(currentExecutable)
	if !ownerOK || !currentOK {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ownerPath, currentPath)
	}
	return ownerPath == currentPath
}

func canonicalExecutablePath(value string) (string, bool) {
	if !filepath.IsAbs(value) {
		return "", false
	}
	value = filepath.Clean(value)
	if resolved, err := filepath.EvalSymlinks(value); err == nil {
		value = filepath.Clean(resolved)
	}
	return value, true
}

func killProcess(pid int) error {
	pidText := strconv.Itoa(pid)
	if runtime.GOOS == "windows" {
		return exec.Command("taskkill", "/F", "/PID", pidText).Run()
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		return exec.Command("kill", "-TERM", pidText).Run()
	}
	return fmt.Errorf("当前平台不支持停止旧进程")
}
