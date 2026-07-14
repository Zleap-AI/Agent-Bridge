package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSaveReplayedMessagesOnlyRemovesBoundaryOverlap(t *testing.T) {
	store := NewMessageStore(t.TempDir())
	existing := []StoredMessage{
		{Role: "user", Text: "first"},
		{Role: "assistant", Text: "same"},
		{Role: "user", Text: "second"},
	}
	store.SaveMessages("agent", "session", existing)
	store.SaveReplayedMessages("agent", "session", []StoredMessage{
		{Role: "assistant", Text: "same"},
		{Role: "user", Text: "second"},
		{Role: "assistant", Text: "done"},
	})

	want := append(append([]StoredMessage{}, existing...), StoredMessage{Role: "assistant", Text: "done"})
	if got := store.LoadMessages("agent", "session"); !reflect.DeepEqual(got, want) {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
}

func TestSaveMessagesPreservesRealDuplicates(t *testing.T) {
	store := NewMessageStore(t.TempDir())
	messages := []StoredMessage{
		{Role: "user", Text: "repeat"},
		{Role: "assistant", Text: "answer"},
		{Role: "user", Text: "repeat"},
		{Role: "assistant", Text: "answer"},
	}
	store.SaveMessages("agent", "session", messages)

	if got := store.LoadMessages("agent", "session"); !reflect.DeepEqual(got, messages) {
		t.Fatalf("messages = %#v, want duplicate messages preserved as %#v", got, messages)
	}
}

func TestSaveMessagesPreservesConsecutiveDuplicateAcrossWrites(t *testing.T) {
	store := NewMessageStore(t.TempDir())
	repeated := StoredMessage{Role: "user", Text: "可以"}
	store.SaveMessages("agent", "session", []StoredMessage{repeated})
	store.SaveMessages("agent", "session", []StoredMessage{repeated})

	want := []StoredMessage{repeated, repeated}
	if got := store.LoadMessages("agent", "session"); !reflect.DeepEqual(got, want) {
		t.Fatalf("messages = %#v, want consecutive duplicate preserved as %#v", got, want)
	}
}

func TestOpaqueSessionIDsUseDistinctPortableMessageFiles(t *testing.T) {
	store := NewMessageStore(t.TempDir())
	ids := []string{
		"a/b",
		"a:b",
		"CON",
		`question?star*quote"angle<>pipe|back\\slash`,
		strings.Repeat("very-long-session-id", 40),
	}
	paths := make(map[string]string, len(ids))
	for _, id := range ids {
		store.SaveMessages("agent", id, []StoredMessage{{Role: "user", Text: id}})
		name := filepath.Base(store.getMessageFile("agent", id))
		if previous, exists := paths[name]; exists {
			t.Fatalf("session IDs %q and %q map to the same file %q", previous, id, name)
		}
		paths[name] = id
		if strings.IndexFunc(name, func(r rune) bool {
			return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '.')
		}) >= 0 {
			t.Fatalf("session file %q contains a non-portable character", name)
		}
		got := store.LoadMessages("agent", id)
		if len(got) != 1 || got[0].Text != id {
			t.Fatalf("messages for %q = %+v", id, got)
		}
	}
}

func TestLegacyMessageFileIsReadAndMigratedOnWrite(t *testing.T) {
	store := NewMessageStore(t.TempDir())
	sessionID := "legacy/session:1"
	legacy := []StoredMessage{{Role: "user", Text: "before upgrade"}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := store.getLegacyMessageFile("agent", sessionID)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	if got := store.LoadMessages("agent", sessionID); !reflect.DeepEqual(got, legacy) {
		t.Fatalf("legacy messages = %+v, want %+v", got, legacy)
	}
	store.SaveMessages("agent", sessionID, []StoredMessage{{Role: "assistant", Text: "after upgrade"}})
	if _, err := os.Stat(store.getMessageFile("agent", sessionID)); err != nil {
		t.Fatalf("new collision-resistant message file was not written: %v", err)
	}
	if got := store.LoadMessages("agent", sessionID); len(got) != 2 || got[0].Text != "before upgrade" || got[1].Text != "after upgrade" {
		t.Fatalf("migrated messages = %+v", got)
	}
}

func TestListSessionsDeduplicatesLegacyAndNewMetadata(t *testing.T) {
	store := NewMessageStore(t.TempDir())
	sessionID := "legacy/session:1"
	now := time.Now()
	if err := os.MkdirAll(filepath.Dir(store.getSessionFile("agent", sessionID)), 0755); err != nil {
		t.Fatal(err)
	}
	for path, updatedAt := range map[string]time.Time{
		store.getLegacySessionFile("agent", sessionID): now.Add(-time.Hour),
		store.getSessionFile("agent", sessionID):       now,
	} {
		if err := writeStoredSessionAtomically(path, StoredSession{
			AgentID: "agent", SessionID: sessionID, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: updatedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	sessions := store.ListSessions("agent", 0)
	if len(sessions) != 1 || sessions[0].SessionID != sessionID || sessions[0].UpdatedAt != now.Unix() {
		t.Fatalf("sessions = %+v, want one newest record", sessions)
	}
}

func TestListSessionsUsesStableOrderAndLimitForEqualTimestamps(t *testing.T) {
	store := NewMessageStore(t.TempDir())
	updatedAt := time.Unix(1_700_000_000, 0).UTC()
	for _, sessionID := range []string{"charlie", "alpha", "bravo"} {
		path := store.getSessionFile("agent", sessionID)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := writeStoredSessionAtomically(path, StoredSession{
			AgentID: "agent", SessionID: sessionID, CreatedAt: updatedAt, UpdatedAt: updatedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{"alpha", "bravo"}
	for attempt := 0; attempt < 20; attempt++ {
		sessions := store.ListSessions("agent", 2)
		if len(sessions) != len(want) {
			t.Fatalf("attempt %d: sessions = %+v", attempt, sessions)
		}
		for i, sessionID := range want {
			if sessions[i].SessionID != sessionID {
				t.Fatalf("attempt %d: sessions = %+v, want %v", attempt, sessions, want)
			}
		}
	}
}

func TestListSessionsDoesNotExposeLegacyFilenameAsGhostSession(t *testing.T) {
	store := NewMessageStore(t.TempDir())
	sessionID := "a/b"
	now := time.Now().UTC()
	metadataPath := store.getSessionFile("agent", sessionID)
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeStoredSessionAtomically(metadataPath, StoredSession{
		AgentID: "agent", SessionID: sessionID, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	legacyMessages := []StoredMessage{
		{Role: "user", Text: "before migration"},
		{Role: "assistant", Text: "still visible"},
	}
	legacyData, err := json.Marshal(legacyMessages)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.getLegacySessionFile("agent", sessionID), legacyData, 0644); err != nil {
		t.Fatal(err)
	}

	sessions := store.ListSessions("agent", 0)
	if len(sessions) != 1 || sessions[0].SessionID != sessionID || sessions[0].MessageCount != len(legacyMessages) {
		t.Fatalf("sessions = %+v, want only canonical %q with legacy message count", sessions, sessionID)
	}
	if got := store.LoadMessages("agent", sessionID); !reflect.DeepEqual(got, legacyMessages) {
		t.Fatalf("legacy fallback messages = %+v, want %+v", got, legacyMessages)
	}
}
