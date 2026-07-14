package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/server/model"
)

func TestOpenMigratesAndBackupCanBeReopened(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	version, err := store.CurrentSchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, SchemaVersion)
	}
	assertUnixMode(t, path, 0o600)
	assertUnixMode(t, path+"-wal", 0o600)
	assertUnixMode(t, path+"-shm", 0o600)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("idempotent migrate: %v", err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := store.Backup(ctx, backup); err != nil {
		t.Fatalf("backup: %v", err)
	}
	assertUnixMode(t, backup, 0o600)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, backup)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer reopened.Close()
	version, err = reopened.CurrentSchemaVersion(ctx)
	if err != nil || version != SchemaVersion {
		t.Fatalf("backup schema version = %d, err = %v", version, err)
	}
}

func TestOpenCreatesPrivateDirectoriesWithoutChangingExistingParents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX permission bits")
	}

	ctx := context.Background()
	existingParent := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(existingParent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existingParent, 0o755); err != nil {
		t.Fatal(err)
	}

	databaseDirectory := filepath.Join(existingParent, "database")
	databasePath := filepath.Join(databaseDirectory, "state.db")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	assertUnixMode(t, existingParent, 0o755)
	assertUnixMode(t, databaseDirectory, 0o700)
	assertUnixMode(t, databasePath, 0o600)
	assertUnixMode(t, databasePath+"-wal", 0o600)
	assertUnixMode(t, databasePath+"-shm", 0o600)

	backupDirectory := filepath.Join(existingParent, "backups")
	backupPath := filepath.Join(backupDirectory, "state.db")
	if err := store.Backup(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	assertUnixMode(t, existingParent, 0o755)
	assertUnixMode(t, backupDirectory, 0o700)
	assertUnixMode(t, backupPath, 0o600)
}

func TestOpenRejectsFileURIThatWouldBypassPermissionHardening(t *testing.T) {
	path := "file:" + filepath.Join(t.TempDir(), "state.db")
	store, err := Open(context.Background(), path)
	if store != nil {
		_ = store.Close()
		t.Fatal("Open returned a Store for a file URI")
	}
	if err == nil || !strings.Contains(err.Error(), "file URI") {
		t.Fatalf("Open error = %v, want explicit file URI rejection", err)
	}
}

func assertUnixMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s permissions = %o, want %o", path, got, want)
	}
}

func TestCallMetadataRetainsOnlyNewestThousandRows(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 1005; i++ {
		if err := store.InsertCallRecord(ctx, model.CallRecord{
			DeviceID: "dev_1", AgentID: "codex", Status: fmt.Sprintf("status-%04d", i),
			DurationMS: int64(i), CreatedAt: started.Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatalf("insert call %d: %v", i, err)
		}
	}
	items, err := store.ListCallRecords(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1000 {
		t.Fatalf("call count = %d, want 1000", len(items))
	}
	if items[0].Status != "status-1004" || items[len(items)-1].Status != "status-0005" {
		t.Fatalf("unexpected retained range: newest=%s oldest=%s", items[0].Status, items[len(items)-1].Status)
	}
}
