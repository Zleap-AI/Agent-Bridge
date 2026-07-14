//go:build darwin || linux

package infra

import (
	"bufio"
	"context"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStopTerminatesAgentProcessTree(t *testing.T) {
	pm, err := StartProcess(context.Background(), StartProcessConfig{
		Command: "/bin/sh",
		Args:    []string{"-c", `trap '' TERM; sleep 60 & child=$!; echo "$child"; while :; do sleep 1; done`},
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	parentPID := pm.PID()
	line, err := bufio.NewReader(pm.Stdout()).ReadString('\n')
	if err != nil {
		_ = pm.Stop()
		t.Fatalf("read child PID: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		_ = pm.Stop()
		t.Fatalf("parse child PID %q: %v", line, err)
	}

	if err := pm.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitForProcessExit(t, parentPID)
	waitForProcessExit(t, childPID)
}

func TestWaitTerminatesDescendantsAfterParentExit(t *testing.T) {
	tests := []struct {
		name      string
		exitCode  string
		wantError bool
	}{
		{name: "natural exit", exitCode: "0"},
		{name: "abnormal exit", exitCode: "7", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pm, err := StartProcess(context.Background(), StartProcessConfig{
				Command: "/bin/sh",
				Args: []string{"-c",
					`sleep 60 </dev/null >/dev/null 2>&1 & child=$!; echo "$child"; exit ` + test.exitCode},
			})
			if err != nil {
				t.Fatalf("StartProcess: %v", err)
			}
			t.Cleanup(func() { _ = pm.Stop() })

			line, err := bufio.NewReader(pm.Stdout()).ReadString('\n')
			if err != nil {
				t.Fatalf("read child PID: %v", err)
			}
			childPID, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil {
				t.Fatalf("parse child PID %q: %v", line, err)
			}

			waitErr := pm.Wait()
			if (waitErr != nil) != test.wantError {
				t.Fatalf("Wait error = %v, wantError %v", waitErr, test.wantError)
			}
			waitForProcessExit(t, childPID)
			if err := pm.Stop(); err != nil {
				t.Fatalf("repeated Stop after Wait: %v", err)
			}
		})
	}
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("process %d is still alive after ProcessManager cleanup", pid)
}
