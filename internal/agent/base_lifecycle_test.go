package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

func TestConcurrentStartCreatesOneProcess(t *testing.T) {
	startsFile := t.TempDir() + "/starts"
	a := newHelperAgent(t, "normal", time.Second, startsFile)
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	const callers = 16
	ready := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			errs <- a.Start(context.Background())
		}()
	}
	close(ready)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Start failed: %v", err)
		}
	}

	if got := helperStartCount(t, startsFile); got != 1 {
		t.Fatalf("child process starts = %d, want 1", got)
	}
}

func TestSendAppliesReadTimeout(t *testing.T) {
	startsFile := t.TempDir() + "/starts"
	a := newHelperAgent(t, "silent", 60*time.Millisecond, startsFile)
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	started := time.Now()
	_, err := a.Send(context.Background(), testACPRequest("slow"))
	if err == nil || !strings.Contains(err.Error(), "60ms 内未收到消息") {
		t.Fatalf("Send error = %v, want configured read timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("read timeout took %s", elapsed)
	}
	waitForAgentStatus(t, a, AgentDisconnected)
	a.meta.Env["AGENT_BRIDGE_HELPER_MODE"] = "normal"
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("restart after timeout: %v", err)
	}
	resp, err := a.Send(context.Background(), testACPRequest("after-timeout"))
	if err != nil || !resp.IsSuccess() {
		t.Fatalf("request after timeout restart = %+v, %v", resp, err)
	}
	if got := helperStartCount(t, startsFile); got != 2 {
		t.Fatalf("child process starts = %d, want restart after timeout", got)
	}
}

func TestWaitingForRequestGateIsCancellable(t *testing.T) {
	a := newHelperAgent(t, "silent", 2*time.Second, "")
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	streamCtx, cancelStream := context.WithCancel(context.Background())
	chunks, err := a.Stream(streamCtx, testACPRequest("stream"))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancelWait()
	started := time.Now()
	_, err = a.Send(waitCtx, testACPRequest("blocked"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked Send error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("canceled lock wait took %s", elapsed)
	}

	cancelStream()
	for range chunks {
	}
	waitForAgentStatus(t, a, AgentDisconnected)
}

func TestStreamPreservesBurstChunks(t *testing.T) {
	a := newHelperAgent(t, "flood", time.Second, "")
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	chunks, err := a.Stream(context.Background(), testACPRequest("flood"))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var responseText strings.Builder
	for chunk := range chunks {
		if chunk.Type == internal.StreamChunkResponse {
			responseText.WriteString(chunk.Text)
		}
	}
	if got := responseText.Len(); got != 300 {
		t.Fatalf("streamed response length = %d, want every one of 300 chunks", got)
	}
}

func TestStreamFinalResultExposesText(t *testing.T) {
	a := newHelperAgent(t, "final-only", time.Second, "")
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	chunks, err := a.Stream(context.Background(), testACPRequest("final-only"))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var final internal.StreamChunk
	for chunk := range chunks {
		final = chunk
	}
	if final.Type != internal.StreamChunkFinal || final.Text != "only final" {
		t.Fatalf("final chunk = %+v, want text from result", final)
	}
}

func TestExitedProcessBecomesDisconnectedAndCanRestart(t *testing.T) {
	startsFile := t.TempDir() + "/starts"
	a := newHelperAgent(t, "exit-after-handshake", time.Second, startsFile)
	t.Cleanup(func() { _ = a.Stop(context.Background()) })

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	waitForAgentStatus(t, a, AgentDisconnected)
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	waitForAgentStatus(t, a, AgentDisconnected)
	if got := helperStartCount(t, startsFile); got != 2 {
		t.Fatalf("child process starts = %d, want 2", got)
	}
}

func newHelperAgent(t *testing.T, mode string, readTimeout time.Duration, startsFile string) *CodexAgent {
	t.Helper()
	return NewCodexAgent(AgentMeta{
		ID:             "helper",
		DisplayName:    "Helper",
		Cmd:            os.Args[0],
		Args:           []string{"-test.run=^TestAgentProcessHelper$"},
		WorkDir:        t.TempDir(),
		StartupTimeout: time.Second,
		ReadTimeout:    readTimeout,
		Env: map[string]string{
			"AGENT_BRIDGE_HELPER_PROCESS": "1",
			"AGENT_BRIDGE_HELPER_MODE":    mode,
			"AGENT_BRIDGE_HELPER_STARTS":  startsFile,
		},
	})
}

func testACPRequest(id string) *protocol.ACPMessage {
	return &protocol.ACPMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "session/prompt",
		Params:  json.RawMessage(`{"sessionId":"test","prompt":[]}`),
	}
}

func waitForAgentStatus(t *testing.T, a Agent, want AgentStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if a.Status() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("agent status = %s, want %s", a.Status(), want)
}

func helperStartCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read helper starts: %v", err)
	}
	return len(strings.Fields(string(data)))
}

// TestAgentProcessHelper runs in a subprocess started by the lifecycle tests.
func TestAgentProcessHelper(t *testing.T) {
	if os.Getenv("AGENT_BRIDGE_HELPER_PROCESS") != "1" {
		return
	}
	if startsFile := os.Getenv("AGENT_BRIDGE_HELPER_STARTS"); startsFile != "" {
		f, err := os.OpenFile(startsFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintln(f, os.Getpid())
		_ = f.Close()
	}

	mode := os.Getenv("AGENT_BRIDGE_HELPER_MODE")
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req protocol.ACPMessage
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			os.Exit(3)
		}
		if req.Method == "initialize" {
			_ = encoder.Encode(protocol.ACPMessage{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"protocolVersion":1,"serverInfo":{"name":"helper","version":"1"}}`),
			})
			if mode == "exit-after-handshake" {
				os.Exit(0)
			}
			continue
		}

		switch mode {
		case "silent":
			continue
		case "flood":
			if req.ID == "flood" {
				for i := 0; i < 300; i++ {
					params, _ := json.Marshal(map[string]any{
						"request_id": req.ID,
						"type":       "response",
						"content":    map[string]string{"text": "x"},
					})
					_ = encoder.Encode(protocol.ACPMessage{JSONRPC: "2.0", Method: "session/update", Params: params})
				}
			}
		}

		text := "ok"
		if mode == "final-only" {
			text = "only final"
		}
		result, _ := json.Marshal(map[string]string{"text": text})
		_ = encoder.Encode(protocol.ACPMessage{JSONRPC: "2.0", ID: req.ID, Result: result})
	}
	os.Exit(0)
}
