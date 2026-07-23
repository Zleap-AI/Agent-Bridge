package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	bridgeinternal "github.com/Zleap-AI/Agent-Bridge/internal"
	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/protocol"
)

type sessionTestAgent struct {
	mu            sync.Mutex
	agentID       string
	newSessions   []string
	newCalls      int
	newStarted    chan<- struct{}
	newRelease    <-chan struct{}
	loadedSession string
	loadErr       error
	streamCalls   int
	streamLoaded  string
	stream        <-chan bridgeinternal.StreamChunk
	streamErr     error
	streamStarted chan struct{}
	streamRelease <-chan struct{}
	disconnected  bool
	startCalls    int
	startErr      error
}

func (a *sessionTestAgent) ID() string {
	if a.agentID != "" {
		return a.agentID
	}
	return "test-agent"
}
func (a *sessionTestAgent) DisplayName() string { return "Test Agent" }
func (a *sessionTestAgent) Status() agent.AgentStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.disconnected {
		return agent.AgentDisconnected
	}
	return agent.AgentIdle
}
func (a *sessionTestAgent) Start(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.startCalls++
	if a.startErr == nil {
		a.disconnected = false
	}
	return a.startErr
}
func (a *sessionTestAgent) Stop(context.Context) error   { return nil }
func (a *sessionTestAgent) Health(context.Context) error { return nil }
func (a *sessionTestAgent) Send(context.Context, *protocol.ACPMessage) (*protocol.ACPMessage, error) {
	return nil, fmt.Errorf("not implemented")
}
func (a *sessionTestAgent) Stream(_ context.Context, _ *protocol.ACPMessage) (<-chan bridgeinternal.StreamChunk, error) {
	a.mu.Lock()
	a.streamCalls++
	a.streamLoaded = a.loadedSession
	a.mu.Unlock()
	if a.streamStarted != nil {
		select {
		case a.streamStarted <- struct{}{}:
		default:
		}
	}
	if a.streamRelease != nil {
		<-a.streamRelease
	}
	if a.stream != nil || a.streamErr != nil {
		return a.stream, a.streamErr
	}
	return nil, fmt.Errorf("not implemented")
}
func (a *sessionTestAgent) NewSession(ctx context.Context, cwd string) (string, error) {
	a.mu.Lock()
	a.newCalls++
	var sessionID string
	if len(a.newSessions) == 0 {
		sessionID = fmt.Sprintf("session-%d", a.newCalls)
	} else {
		idx := a.newCalls - 1
		if idx >= len(a.newSessions) {
			idx = len(a.newSessions) - 1
		}
		sessionID = a.newSessions[idx]
	}
	started, release := a.newStarted, a.newRelease
	a.mu.Unlock()

	if started != nil {
		select {
		case started <- struct{}{}:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return sessionID, nil
}
func (a *sessionTestAgent) Priority() int                 { return 0 }
func (a *sessionTestAgent) Capabilities() agent.CapabilityInfo { return agent.CapabilityInfo{} }
func (a *sessionTestAgent) Cancel(_ context.Context, _ string) error {
	return nil
}
func (a *sessionTestAgent) LoadSession(_ context.Context, sessionID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.loadedSession = sessionID
	return a.loadErr
}
func (a *sessionTestAgent) ResumeSession(_ context.Context, sessionID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.loadedSession = sessionID
	return a.loadErr
}
func (a *sessionTestAgent) CloseSession(_ context.Context, _ string) error {
	return nil
}
func (a *sessionTestAgent) DeleteSession(_ context.Context, _ string) error {
	return nil
}
func (a *sessionTestAgent) Logout(_ context.Context) error {
	return nil
}
func (a *sessionTestAgent) SetMode(_ context.Context, _, _ string) error {
	return nil
}
func (a *sessionTestAgent) GetConfig(_ context.Context, _ string) (interface{}, error) {
	return nil, nil
}
func (a *sessionTestAgent) SetConfig(_ context.Context, _ string, _ map[string]interface{}) error {
	return nil
}
func (a *sessionTestAgent) SetTitle(_ context.Context, _, _ string) error {
	return nil
}

func newSessionTestRegistry(a agent.Agent) *agent.AgentRegistry {
	reg := agent.NewAgentRegistry(agent.DefaultAgentRegistryConfig())
	reg.Register(a)
	return reg
}

func TestCreateNewSessionAlwaysCreatesANewAgentSession(t *testing.T) {
	a := &sessionTestAgent{newSessions: []string{"session-1", "session-2"}}
	sm := newSessionManagerWithStoreDir(newSessionTestRegistry(a), t.TempDir())

	first, err := sm.CreateNewSession(context.Background(), a.ID(), "", "")
	if err != nil {
		t.Fatalf("first CreateNewSession: %v", err)
	}
	second, err := sm.CreateNewSession(context.Background(), a.ID(), "", "")
	if err != nil {
		t.Fatalf("second CreateNewSession: %v", err)
	}
	if first != "session-1" || second != "session-2" {
		t.Fatalf("sessions = %q then %q, want session-1 then session-2", first, second)
	}
	if got := sm.GetSession(a.ID()); got != second {
		t.Fatalf("current session = %q, want %q", got, second)
	}
}

func TestGetOrCreateSessionRestoresMostRecentStoredSession(t *testing.T) {
	storeDir := t.TempDir()
	creator := &sessionTestAgent{newSessions: []string{"stored-session"}}
	firstManager := newSessionManagerWithStoreDir(newSessionTestRegistry(creator), storeDir)
	if _, err := firstManager.CreateNewSession(context.Background(), creator.ID(), "", ""); err != nil {
		t.Fatalf("create stored session: %v", err)
	}

	restorer := &sessionTestAgent{}
	secondManager := newSessionManagerWithStoreDir(newSessionTestRegistry(restorer), storeDir)
	got, err := secondManager.GetOrCreateSession(context.Background(), restorer.ID())
	if err != nil {
		t.Fatalf("restore session: %v", err)
	}
	if got != "stored-session" {
		t.Fatalf("restored session = %q, want stored-session", got)
	}
	if restorer.loadedSession != "stored-session" {
		t.Fatalf("Agent.LoadSession called with %q, want stored-session", restorer.loadedSession)
	}
	if restorer.newCalls != 0 {
		t.Fatalf("Agent.NewSession called %d times while restoring", restorer.newCalls)
	}
}

func TestConcurrentGetOrCreateSessionCreatesOnlyOncePerAgent(t *testing.T) {
	const callers = 64
	started := make(chan struct{}, callers)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAgent := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseAgent)

	a := &sessionTestAgent{
		newSessions: []string{"shared-session"},
		newStarted:  started,
		newRelease:  release,
	}
	sm := newSessionManagerWithStoreDir(newSessionTestRegistry(a), t.TempDir())
	begin := make(chan struct{})
	results := make(chan string, callers)
	errors := make(chan error, callers)
	var workers sync.WaitGroup
	workers.Add(callers)
	for range callers {
		go func() {
			defer workers.Done()
			<-begin
			sessionID, err := sm.GetOrCreateSession(context.Background(), a.ID())
			if err != nil {
				errors <- err
				return
			}
			results <- sessionID
		}()
	}
	close(begin)

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("no concurrent caller reached Agent.NewSession")
	}
	// Keep the first Agent call blocked long enough for every competing caller
	// to reach GetOrCreateSession's per-Agent critical section.
	time.Sleep(100 * time.Millisecond)
	releaseAgent()
	workers.Wait()
	close(results)
	close(errors)

	for err := range errors {
		t.Errorf("GetOrCreateSession: %v", err)
	}
	resultCount := 0
	for sessionID := range results {
		resultCount++
		if sessionID != "shared-session" {
			t.Errorf("Session ID = %q, want shared-session", sessionID)
		}
	}
	if resultCount != callers {
		t.Fatalf("successful callers = %d, want %d", resultCount, callers)
	}
	a.mu.Lock()
	newCalls := a.newCalls
	a.mu.Unlock()
	if newCalls != 1 {
		t.Fatalf("Agent.NewSession calls = %d, want 1", newCalls)
	}
}

func TestGetOrCreateSessionDoesNotBlockDifferentAgents(t *testing.T) {
	slowStarted := make(chan struct{}, 1)
	slowRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseSlow := func() { releaseOnce.Do(func() { close(slowRelease) }) }
	t.Cleanup(releaseSlow)

	slow := &sessionTestAgent{
		agentID:     "slow-agent",
		newSessions: []string{"slow-session"},
		newStarted:  slowStarted,
		newRelease:  slowRelease,
	}
	fast := &sessionTestAgent{
		agentID:     "fast-agent",
		newSessions: []string{"fast-session"},
	}
	registry := agent.NewAgentRegistry(agent.DefaultAgentRegistryConfig())
	registry.Register(slow)
	registry.Register(fast)
	sm := newSessionManagerWithStoreDir(registry, t.TempDir())

	slowDone := make(chan error, 1)
	go func() {
		_, err := sm.GetOrCreateSession(context.Background(), slow.ID())
		slowDone <- err
	}()
	select {
	case <-slowStarted:
	case <-time.After(time.Second):
		t.Fatal("slow Agent did not start Session creation")
	}

	type sessionResult struct {
		id  string
		err error
	}
	fastDone := make(chan sessionResult, 1)
	go func() {
		id, err := sm.GetOrCreateSession(context.Background(), fast.ID())
		fastDone <- sessionResult{id: id, err: err}
	}()
	select {
	case result := <-fastDone:
		if result.err != nil || result.id != "fast-session" {
			t.Fatalf("fast Agent result = id:%q err:%v", result.id, result.err)
		}
	case <-time.After(time.Second):
		releaseSlow()
		t.Fatal("fast Agent was blocked by another Agent's Session creation")
	}

	releaseSlow()
	if err := <-slowDone; err != nil {
		t.Fatalf("slow Agent Session creation: %v", err)
	}
}

func TestConcurrentMessageSavesDoNotLoseEitherWriter(t *testing.T) {
	a := &sessionTestAgent{}
	sm := newSessionManagerWithStoreDir(newSessionTestRegistry(a), t.TempDir())
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	for _, message := range []StoredMessage{
		{Role: "user", Text: "first"},
		{Role: "assistant", Text: "second"},
	} {
		message := message
		go func() {
			<-start
			sm.SaveMessages(a.ID(), "session-1", []StoredMessage{message})
			done <- struct{}{}
		}()
	}
	close(start)
	<-done
	<-done

	messages := sm.LoadMessages(a.ID(), "session-1")
	if len(messages) != 2 {
		t.Fatalf("saved messages = %+v, want both concurrent writes", messages)
	}
}

func TestSavingMessagesCreatesMissingSessionMetadata(t *testing.T) {
	a := &sessionTestAgent{}
	sm := newSessionManagerWithStoreDir(newSessionTestRegistry(a), t.TempDir())
	sm.SaveMessages(a.ID(), "loaded-elsewhere", []StoredMessage{{Role: "user", Text: "hello"}})

	sessions := sm.ListSessions(a.ID(), 0)
	if len(sessions) != 1 || sessions[0].SessionID != "loaded-elsewhere" || sessions[0].MessageCount != 1 {
		t.Fatalf("sessions = %+v, want saved Session metadata and message count", sessions)
	}
}

func TestSavingMessagesMovesSessionToMostRecentlyUpdated(t *testing.T) {
	a := &sessionTestAgent{newSessions: []string{"session-1", "session-2"}}
	sm := newSessionManagerWithStoreDir(newSessionTestRegistry(a), t.TempDir())
	for _, sessionID := range []string{"session-1", "session-2"} {
		if _, err := sm.CreateNewSession(context.Background(), a.ID(), "", ""); err != nil {
			t.Fatalf("create %s: %v", sessionID, err)
		}
	}

	now := time.Now()
	for sessionID, updatedAt := range map[string]time.Time{
		"session-1": now.Add(-2 * time.Hour),
		"session-2": now.Add(-1 * time.Hour),
	} {
		path := filepath.Join(sm.storeDir, a.ID(), "sessions", safeSessionFileID(sessionID)+".json")
		if err := writeStoredSessionAtomically(path, StoredSession{
			AgentID: a.ID(), SessionID: sessionID, CreatedAt: updatedAt, UpdatedAt: updatedAt,
		}); err != nil {
			t.Fatalf("seed %s metadata: %v", sessionID, err)
		}
	}

	sm.SaveMessages(a.ID(), "session-1", []StoredMessage{{Role: "user", Text: "new activity"}})
	sessions := sm.ListSessions(a.ID(), 0)
	if len(sessions) != 2 {
		t.Fatalf("sessions = %+v, want two sessions", sessions)
	}
	if sessions[0].SessionID != "session-1" {
		t.Fatalf("most recent session = %q, want session-1 after saving a message", sessions[0].SessionID)
	}
	if sessions[0].UpdatedAt <= sessions[1].UpdatedAt {
		t.Fatalf("updated times = %d then %d, want session-1 newer", sessions[0].UpdatedAt, sessions[1].UpdatedAt)
	}
}

func TestSessionPersistenceKeepsAgentSessionIDInsideStoreDirectory(t *testing.T) {
	storeDir := t.TempDir()
	a := &sessionTestAgent{newSessions: []string{"../outside/session:1"}}
	sm := newSessionManagerWithStoreDir(newSessionTestRegistry(a), storeDir)

	sessionID, err := sm.CreateNewSession(context.Background(), a.ID(), "", "")
	if err != nil {
		t.Fatalf("CreateNewSession: %v", err)
	}
	if sessionID != "../outside/session:1" {
		t.Fatalf("session ID changed: %q", sessionID)
	}

	path := filepath.Join(storeDir, a.ID(), "sessions", safeSessionFileID(sessionID)+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("safe session metadata path was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(storeDir, "outside")); !os.IsNotExist(err) {
		t.Fatalf("session ID escaped store directory: %v", err)
	}
}

func TestSessionAndMessagePersistenceUsesOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows permissions are enforced by the user profile ACL")
	}
	storeDir := t.TempDir()
	a := &sessionTestAgent{newSessions: []string{"private-session"}}
	sm := newSessionManagerWithStoreDir(newSessionTestRegistry(a), storeDir)
	if _, err := sm.CreateNewSession(context.Background(), a.ID(), "", ""); err != nil {
		t.Fatal(err)
	}
	sm.SaveMessages(a.ID(), "private-session", []StoredMessage{{Role: "user", Text: "private"}})

	paths := []struct {
		path string
		mode os.FileMode
	}{
		{filepath.Join(storeDir, a.ID(), "sessions"), 0o700},
		{filepath.Join(storeDir, a.ID(), "messages"), 0o700},
		{sm.msgStore.getSessionFile(a.ID(), "private-session"), 0o600},
		{sm.msgStore.getMessageFile(a.ID(), "private-session"), 0o600},
	}
	for _, item := range paths {
		info, err := os.Stat(item.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != item.mode {
			t.Fatalf("%s permissions = %04o, want %04o", item.path, got, item.mode)
		}
	}
}
