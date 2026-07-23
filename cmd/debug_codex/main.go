// -*- coding: utf-8 -*-
// Go 1.25+
//
// main.go
// 调试：测试 Job Object 对 codex-acp 的影响
//
// Lzm 2026-07-22

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func main() {
	codexAcpCmd := `C:\Users\PC\.trae-cn\binaries\node\versions\24.18.0\codex-acp.cmd`
	codexHome := os.ExpandEnv("${USERPROFILE}/.codex")

	// TEST 1: 直接启动 codex-acp (NO Job Object)
	fmt.Println("====== TEST 1: 直接启动（无 Job Object）======")
	testCodexAcp(codexAcpCmd, codexHome, false)

	// 等待 2 秒
	time.Sleep(2 * time.Second)

	// TEST 2: 通过 Job Object 启动
	fmt.Println("\n====== TEST 2: 通过 Job Object 启动 ======")
	testCodexAcp(codexAcpCmd, codexHome, true)
}

func testCodexAcp(cmdPath, codexHome string, useJob bool) {
	// 启动 codex-acp.cmd
	cmd := exec.Command("cmd.exe", "/d", "/s", "/c", cmdPath)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	env := os.Environ()
	env = append(env, "CODEX_HOME="+codexHome)
	cmd.Env = env

	if err := cmd.Start(); err != nil {
		log.Printf("Start failed: %v", err)
		return
	}
	log.Printf("PID: %d", cmd.Process.Pid)

	if useJob {
		// 创建 Job Object 并附加
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			log.Printf("CreateJobObject failed: %v", err)
			cmd.Process.Kill()
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		windows.SetInformationJobObject(
			job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)),
		)

		process, err := windows.OpenProcess(
			windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
			false, uint32(cmd.Process.Pid),
		)
		if err != nil {
			log.Printf("OpenProcess failed: %v", err)
			cmd.Process.Kill()
			windows.CloseHandle(job)
			return
		}
		if err := windows.AssignProcessToJobObject(job, process); err != nil {
			log.Printf("AssignProcessToJobObject failed: %v", err)
		} else {
			log.Println("Process assigned to Job Object")
		}
		windows.CloseHandle(process)
		windows.CloseHandle(job)
	}

	// 发送 initialize 请求
	initReq := map[string]interface{}{
		"jsonrpc": "2.0", "id": "msg-001", "method": "initialize",
		"params": map[string]interface{}{
			"protocolVersion": 1,
			"clientCapabilities": map[string]interface{}{
				"fs":       map[string]bool{"readTextFile": true, "writeTextFile": true},
				"terminal": true,
				"session":  map[string]interface{}{"configOptions": map[string]interface{}{"boolean": map[string]interface{}{}}},
			},
			"clientInfo": map[string]string{"name": "zleap-bridge", "title": "Zleap Bridge", "version": "1.0.0"},
		},
	}
	initJSON, _ := json.Marshal(initReq)
	stdin.Write(initJSON)
	stdin.Write([]byte("\n"))
	log.Printf("Sent initialize (%d bytes)", len(initJSON))

	// 读取响应
	done := make(chan struct{}, 2)
	go func() {
		buf := make([]byte, 65536)
		n, err := stdout.Read(buf)
		if err != nil {
			log.Printf("stdout error: %v", err)
		} else {
			log.Printf("STDOUT (%d bytes): %s", n, string(buf[:n]))
		}
		done <- struct{}{}
	}()
	go func() {
		buf := make([]byte, 65536)
		n, err := stderr.Read(buf)
		if err != nil {
			log.Printf("stderr error: %v", err)
		} else {
			log.Printf("STDERR (%d bytes): %s", n, string(buf[:n]))
		}
		done <- struct{}{}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Println("TIMEOUT - killing process")
		cmd.Process.Kill()
	}

	// 等待进程退出
	state, err := cmd.Process.Wait()
	if err != nil {
		log.Printf("Process wait error: %v", err)
	} else {
		log.Printf("Process exited: code=%d", state.ExitCode())
	}

	stdin.Close()
}
