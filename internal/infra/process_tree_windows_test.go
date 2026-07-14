//go:build windows

package infra

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const windowsTreeHelperMode = "_AGENT_BRIDGE_TEST_PROCESS_TREE_MODE"

func TestWindowsProcessTreeHelper(t *testing.T) {
	mode := os.Getenv(windowsTreeHelperMode)
	if mode == "" {
		return
	}
	if mode == "child" {
		time.Sleep(5 * time.Minute)
		return
	}

	child := exec.Command(os.Args[0], "-test.run=^TestWindowsProcessTreeHelper$")
	child.Env = mergeEnvironment(os.Environ(), map[string]string{windowsTreeHelperMode: "child"})
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatalf("start helper child: %v", err)
	}
	childPID := child.Process.Pid
	_ = child.Process.Release()
	child.Process = nil
	fmt.Fprintln(os.Stdout, childPID)

	switch mode {
	case "parent-natural":
		return
	case "parent-abnormal":
		os.Exit(7)
	case "parent-stop":
		time.Sleep(5 * time.Minute)
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func TestWindowsWaitTerminatesDescendantsAfterParentExit(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		wantError bool
	}{
		{name: "natural exit", mode: "parent-natural"},
		{name: "abnormal exit", mode: "parent-abnormal", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pm, childPID := startWindowsTreeHelper(t, test.mode)
			waitErr := pm.Wait()
			if (waitErr != nil) != test.wantError {
				t.Fatalf("Wait error = %v, wantError %v", waitErr, test.wantError)
			}
			waitForWindowsProcessExit(t, childPID)
			if err := pm.Stop(); err != nil {
				t.Fatalf("repeated Stop after Wait: %v", err)
			}
		})
	}
}

func TestWindowsStopTerminatesAgentProcessTree(t *testing.T) {
	pm, childPID := startWindowsTreeHelper(t, "parent-stop")
	if !windowsProcessRunning(t, childPID) {
		t.Fatalf("helper child %d exited before Stop", childPID)
	}
	if err := pm.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitForWindowsProcessExit(t, childPID)
}

func startWindowsTreeHelper(t *testing.T, mode string) (*ProcessManager, int) {
	t.Helper()
	pm, err := StartProcess(context.Background(), StartProcessConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestWindowsProcessTreeHelper$"},
		Env:     map[string]string{windowsTreeHelperMode: mode},
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	t.Cleanup(func() { _ = pm.Stop() })

	line, err := bufio.NewReader(pm.Stdout()).ReadString('\n')
	if err != nil {
		t.Fatalf("read helper child PID: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("parse helper child PID %q: %v", line, err)
	}
	return pm, childPID
}

func waitForWindowsProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !windowsProcessRunning(t, pid) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("process %d is still alive after ProcessManager cleanup", pid)
}

func windowsProcessRunning(t *testing.T, pid int) bool {
	t.Helper()
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		t.Fatalf("query process %d: %v", pid, err)
	}
	return state != windows.WAIT_OBJECT_0
}
