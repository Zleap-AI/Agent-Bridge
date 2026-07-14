//go:build windows

package infra

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsProcessWrapperEnv  = "_AGENT_BRIDGE_INTERNAL_PROCESS_WRAPPER_V1"
	windowsProcessSpecEnv     = "_AGENT_BRIDGE_INTERNAL_PROCESS_SPEC_V1"
	windowsProcessGateEnv     = "_AGENT_BRIDGE_INTERNAL_PROCESS_GATE_V1"
	windowsProcessWrapperExit = 125
)

type windowsProcessSpec struct {
	Command     string   `json:"command"`
	Args        []string `json:"args"`
	CommandLine string   `json:"command_line,omitempty"`
}

// CommandContext starts native Windows executables directly and routes batch
// shims through cmd.exe. npm publishes its Windows entry points as .cmd files,
// which CreateProcess cannot execute on its own.
func CommandContext(ctx context.Context, command string, args ...string) *exec.Cmd {
	spec := prepareWindowsCommand(command, args, os.Getenv("ComSpec"))
	cmd := exec.CommandContext(ctx, spec.command, spec.args...)
	if spec.commandLine != "" {
		// cmd.exe does not follow CommandLineToArgvW quoting. Supplying the
		// already escaped line verbatim keeps arguments inside the /c boundary.
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: spec.commandLine}
	}
	return cmd
}

type windowsProcessTree struct {
	mu                 sync.Mutex
	job                windows.Handle
	gate               windows.Handle
	attached           bool
	terminateRequested bool
	terminated         bool
	terminateErr       error
}

// A Windows child must be inside the Job Object before it can create any
// descendants. The small copy of this executable launched here blocks on an
// inherited event; attach assigns that blocked process to the Job, then opens
// the gate so it can start the real Agent. This removes the Start->Assign race
// that would otherwise let fast .cmd launchers escape the Job.
func configureProcessTree(cmd *exec.Cmd) (processTree, error) {
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	spec := windowsProcessSpec{
		Command: cmd.Path,
		Args:    append([]string(nil), cmd.Args[1:]...),
	}
	if cmd.SysProcAttr != nil {
		spec.CommandLine = cmd.SysProcAttr.CmdLine
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("编码 Agent 启动参数: %w", err)
	}

	security := &windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1,
	}
	gate, err := windows.CreateEvent(security, 1, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("创建 Agent 启动门: %w", err)
	}
	wrapper, err := os.Executable()
	if err != nil {
		_ = windows.CloseHandle(gate)
		return nil, fmt.Errorf("定位 Agent-Bridge 可执行文件: %w", err)
	}

	tree := &windowsProcessTree{gate: gate}
	cmd.Path = wrapper
	cmd.Args = []string{wrapper}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(gate)},
	}
	cmd.Env = mergeEnvironment(cmd.Environ(), map[string]string{
		windowsProcessWrapperEnv: "1",
		windowsProcessSpecEnv:    base64.RawStdEncoding.EncodeToString(specJSON),
		windowsProcessGateEnv:    strconv.FormatUint(uint64(gate), 10),
	})
	cmd.Cancel = tree.terminate
	return tree, nil
}

func (tree *windowsProcessTree) attach(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}

	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.terminated {
		return os.ErrProcessDone
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("创建 Windows Job Object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("配置 Windows Job Object: %w", err)
	}

	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("打开 Agent 进程: %w", err)
	}
	err = windows.AssignProcessToJobObject(job, process)
	_ = windows.CloseHandle(process)
	if err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("将 Agent 加入 Windows Job Object: %w", err)
	}

	tree.job = job
	tree.attached = true
	if tree.terminateRequested {
		return tree.terminateLocked()
	}
	if err := windows.SetEvent(tree.gate); err != nil {
		cleanupErr := tree.terminateLocked()
		return errors.Join(fmt.Errorf("打开 Agent 启动门: %w", err), cleanupErr)
	}
	tree.closeGateLocked()
	return nil
}

func (tree *windowsProcessTree) terminate() error {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	tree.terminateRequested = true
	return tree.terminateLocked()
}

func (tree *windowsProcessTree) terminateLocked() error {
	if tree.terminated {
		return tree.terminateErr
	}
	if !tree.attached {
		tree.closeGateLocked()
		return nil
	}
	tree.terminated = true
	tree.closeGateLocked()
	terminateErr := windows.TerminateJobObject(tree.job, 1)
	closeErr := windows.CloseHandle(tree.job)
	tree.job = 0
	tree.terminateErr = errors.Join(terminateErr, closeErr)
	return tree.terminateErr
}

func (tree *windowsProcessTree) closeGateLocked() {
	if tree.gate == 0 {
		return
	}
	_ = windows.CloseHandle(tree.gate)
	tree.gate = 0
}

func init() {
	if os.Getenv(windowsProcessWrapperEnv) != "1" {
		return
	}
	os.Exit(runWindowsProcessWrapper())
}

func runWindowsProcessWrapper() int {
	gateValue, err := strconv.ParseUint(os.Getenv(windowsProcessGateEnv), 10, 64)
	if err != nil || gateValue == 0 {
		fmt.Fprintln(os.Stderr, "invalid Agent process gate")
		return windowsProcessWrapperExit
	}
	gate := windows.Handle(gateValue)
	state, waitErr := windows.WaitForSingleObject(gate, windows.INFINITE)
	_ = windows.CloseHandle(gate)
	if waitErr != nil || state != windows.WAIT_OBJECT_0 {
		fmt.Fprintln(os.Stderr, "waiting for Agent process gate failed")
		return windowsProcessWrapperExit
	}

	specJSON, err := base64.RawStdEncoding.DecodeString(os.Getenv(windowsProcessSpecEnv))
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid Agent process specification")
		return windowsProcessWrapperExit
	}
	var spec windowsProcessSpec
	if err := json.Unmarshal(specJSON, &spec); err != nil || spec.Command == "" {
		fmt.Fprintln(os.Stderr, "invalid Agent process specification")
		return windowsProcessWrapperExit
	}

	target := exec.Command(spec.Command, spec.Args...)
	if spec.CommandLine != "" {
		target.SysProcAttr = &syscall.SysProcAttr{CmdLine: spec.CommandLine}
	}
	target.Stdin = os.Stdin
	target.Stdout = os.Stdout
	target.Stderr = os.Stderr
	target.Env = windowsProcessTargetEnvironment(os.Environ())
	if err := target.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return windowsProcessWrapperExit
	}
	return 0
}

func windowsProcessTargetEnvironment(entries []string) []string {
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (strings.EqualFold(key, windowsProcessWrapperEnv) ||
			strings.EqualFold(key, windowsProcessSpecEnv) ||
			strings.EqualFold(key, windowsProcessGateEnv)) {
			continue
		}
		result = append(result, entry)
	}
	return result
}
