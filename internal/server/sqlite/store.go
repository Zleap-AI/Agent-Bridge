package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/server/device"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/model"
	_ "modernc.org/sqlite"
)

const SchemaVersion = 1

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("database path is required")
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(path)), "file:") {
		return nil, fmt.Errorf("SQLite file URI is not supported; use a filesystem path or :memory:")
	}
	filePath := databaseFilePath(path)
	if filePath != "" {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.configure(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if filePath != "" {
		if err := secureDatabaseFiles(filePath); err != nil {
			db.Close()
			return nil, err
		}
	}
	return store, nil
}

func databaseFilePath(path string) string {
	if path == ":memory:" {
		return ""
	}
	return path
}

func secureDatabaseFiles(path string) error {
	for index, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(candidate, 0o600); err != nil {
			if index != 0 && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("secure database permissions for %s: %w", candidate, err)
		}
	}
	return nil
}

func (s *Store) configure(ctx context.Context) error {
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}
	return nil
}

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	var current int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current); err != nil {
		return err
	}
	if current > SchemaVersion {
		return fmt.Errorf("database schema %d is newer than supported schema %d", current, SchemaVersion)
	}
	if current < 1 {
		if err := migrateV1(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES(1, ?)", time.Now().UTC().UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func migrateV1(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE server_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE owner_credentials (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			password_hash TEXT NOT NULL,
			auth_version INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE owner_sessions (
			id TEXT PRIMARY KEY,
			token_hash TEXT NOT NULL UNIQUE,
			expires_at INTEGER NOT NULL,
			auth_version INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX owner_sessions_token_idx ON owner_sessions(token_hash)`,
		`CREATE TABLE devices (
			bridge_id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			created_at INTEGER NOT NULL,
			last_seen_at INTEGER
		)`,
		`CREATE TABLE device_agents (
			bridge_id TEXT NOT NULL REFERENCES devices(bridge_id) ON DELETE CASCADE,
			agent_id TEXT NOT NULL,
			display_name TEXT NOT NULL,
			status TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (bridge_id, agent_id)
		)`,
		`CREATE TABLE pairing_codes (
			id TEXT PRIMARY KEY,
			code_hash TEXT NOT NULL UNIQUE,
			expires_at INTEGER NOT NULL,
			consumed_at INTEGER,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX pairing_codes_hash_idx ON pairing_codes(code_hash)`,
		`CREATE TABLE api_keys (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			key_hash TEXT NOT NULL UNIQUE,
			prefix TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			last_used_at INTEGER
		)`,
		`CREATE INDEX api_keys_hash_idx ON api_keys(key_hash)`,
		`CREATE TABLE call_metadata (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			device_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			status TEXT NOT NULL,
			duration_ms INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX call_metadata_created_idx ON call_metadata(created_at DESC, id DESC)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply schema migration v1: %w", err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CurrentSchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	return version, err
}

func (s *Store) Backup(ctx context.Context, destination string) error {
	if destination == "" {
		return fmt.Errorf("backup destination is required")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", destination); err != nil {
		return fmt.Errorf("backup sqlite database: %w", err)
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return fmt.Errorf("secure backup permissions: %w", err)
	}
	return nil
}

func (s *Store) Initialized(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM owner_credentials").Scan(&count)
	return count == 1, err
}

func (s *Store) ReplaceSetupTokenHash(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO server_settings(key, value) VALUES('setup_token_hash', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, hash)
	return err
}

func (s *Store) ValidateSetupTokenHash(ctx context.Context, hash string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM server_settings
		WHERE key = 'setup_token_hash' AND value = ?
		AND NOT EXISTS (SELECT 1 FROM owner_credentials)`, hash).Scan(&count)
	return count == 1, err
}

func (s *Store) InitializeOwner(ctx context.Context, setupHash, passwordHash string, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var expected string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM server_settings WHERE key = 'setup_token_hash'").Scan(&expected); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if expected != setupHash {
		return false, nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM owner_credentials").Scan(&count); err != nil {
		return false, err
	}
	if count != 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO owner_credentials(id, password_hash, auth_version, updated_at)
		VALUES(1, ?, 1, ?)`, passwordHash, millis(now)); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM server_settings WHERE key = 'setup_token_hash'"); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) OwnerCredential(ctx context.Context) (model.OwnerCredential, error) {
	var item model.OwnerCredential
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT password_hash, auth_version, updated_at
		FROM owner_credentials WHERE id = 1`).Scan(&item.PasswordHash, &item.AuthVersion, &updated)
	item.UpdatedAt = fromMillis(updated)
	return item, err
}

func (s *Store) CreateOwnerSession(ctx context.Context, item model.OwnerSession) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM owner_sessions WHERE expires_at <= ?", millis(item.CreatedAt)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO owner_sessions(id, token_hash, expires_at, auth_version, created_at)
		VALUES(?, ?, ?, ?, ?)`, item.ID, item.TokenHash, millis(item.ExpiresAt), item.AuthVersion, millis(item.CreatedAt)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ValidateOwnerSession(ctx context.Context, hash string, now time.Time) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM owner_sessions s
		JOIN owner_credentials c ON c.id = 1 AND c.auth_version = s.auth_version
		WHERE s.token_hash = ? AND s.expires_at > ?`, hash, millis(now)).Scan(&count)
	return count == 1, err
}

func (s *Store) DeleteOwnerSession(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM owner_sessions WHERE token_hash = ?", hash)
	return err
}

func (s *Store) ResetOwnerPassword(ctx context.Context, hash string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE owner_credentials SET password_hash = ?,
		auth_version = auth_version + 1, updated_at = ? WHERE id = 1`, hash, millis(now))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("owner is not initialized")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM owner_sessions"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateAPIKey(ctx context.Context, item model.APIKey) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_keys(id, name, key_hash, prefix, created_at)
		VALUES(?, ?, ?, ?, ?)`, item.ID, item.Name, item.KeyHash, item.Prefix, millis(item.CreatedAt))
	return err
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]model.APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, key_hash, prefix, created_at, last_used_at
		FROM api_keys ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.APIKey, 0)
	for rows.Next() {
		var item model.APIKey
		var created int64
		var last sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Name, &item.KeyHash, &item.Prefix, &created, &last); err != nil {
			return nil, err
		}
		item.CreatedAt = fromMillis(created)
		item.LastUsedAt = nullableTime(last)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeleteAPIKey(ctx context.Context, id string) (bool, error) {
	result, err := s.db.ExecContext(ctx, "DELETE FROM api_keys WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) AuthenticateAPIKey(ctx context.Context, hash string, now time.Time) (model.APIKey, bool, error) {
	var item model.APIKey
	var created int64
	var last sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, key_hash, prefix, created_at, last_used_at
		FROM api_keys WHERE key_hash = ?`, hash).Scan(&item.ID, &item.Name, &item.KeyHash, &item.Prefix, &created, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return model.APIKey{}, false, nil
	}
	if err != nil {
		return model.APIKey{}, false, err
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE api_keys SET last_used_at = ? WHERE id = ?", millis(now), item.ID); err != nil {
		return model.APIKey{}, false, err
	}
	item.CreatedAt = fromMillis(created)
	item.LastUsedAt = timePointer(now)
	return item, true, nil
}

func (s *Store) ReplacePairingCode(ctx context.Context, item model.PairingCode) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM pairing_codes"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pairing_codes(id, code_hash, expires_at, created_at)
		VALUES(?, ?, ?, ?)`, item.ID, item.CodeHash, millis(item.ExpiresAt), millis(item.CreatedAt)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ClaimPairingCode(ctx context.Context, codeHash string, now time.Time, item model.Device) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	var expires int64
	var consumed sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT id, expires_at, consumed_at FROM pairing_codes WHERE code_hash = ?`, codeHash).Scan(&id, &expires, &consumed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return device.ErrPairingInvalid
		}
		return err
	}
	if consumed.Valid {
		return device.ErrPairingConsumed
	}
	if fromMillis(expires).Before(now) || fromMillis(expires).Equal(now) {
		return device.ErrPairingExpired
	}
	result, err := tx.ExecContext(ctx, `UPDATE pairing_codes SET consumed_at = ?
		WHERE id = ? AND consumed_at IS NULL`, millis(now), id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return device.ErrPairingConsumed
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO devices(bridge_id, name, token_hash, created_at)
		VALUES(?, ?, ?, ?)`, item.BridgeID, item.Name, item.TokenHash, millis(item.CreatedAt)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListDevices(ctx context.Context) ([]model.Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bridge_id, name, token_hash, created_at, last_seen_at
		FROM devices ORDER BY created_at DESC, bridge_id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Device, 0)
	for rows.Next() {
		item, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) Device(ctx context.Context, id string) (model.Device, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT bridge_id, name, token_hash, created_at, last_seen_at
		FROM devices WHERE bridge_id = ?`, id)
	item, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Device{}, false, nil
	}
	return item, err == nil, err
}

type scanner interface{ Scan(...any) error }

func scanDevice(row scanner) (model.Device, error) {
	var item model.Device
	var created int64
	var last sql.NullInt64
	err := row.Scan(&item.BridgeID, &item.Name, &item.TokenHash, &created, &last)
	item.CreatedAt = fromMillis(created)
	item.LastSeenAt = nullableTime(last)
	return item, err
}

func (s *Store) RenameDevice(ctx context.Context, id, name string) (bool, error) {
	result, err := s.db.ExecContext(ctx, "UPDATE devices SET name = ? WHERE bridge_id = ?", name, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) DeleteDevice(ctx context.Context, id string) (bool, error) {
	result, err := s.db.ExecContext(ctx, "DELETE FROM devices WHERE bridge_id = ?", id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) AuthenticateDevice(ctx context.Context, id, tokenHash string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE bridge_id = ? AND token_hash = ?`, id, tokenHash).Scan(&count)
	return count == 1, err
}

func (s *Store) TouchDevice(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE devices SET last_seen_at = ? WHERE bridge_id = ?", millis(at), id)
	return err
}

func (s *Store) ReplaceDeviceAgents(ctx context.Context, bridgeID string, agents []model.Agent, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM device_agents WHERE bridge_id = ?", bridgeID); err != nil {
		return err
	}
	for _, item := range agents {
		if _, err := tx.ExecContext(ctx, `INSERT INTO device_agents(bridge_id, agent_id, display_name, status, updated_at)
			VALUES(?, ?, ?, ?, ?)`, bridgeID, item.AgentID, item.DisplayName, item.Status, millis(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListDeviceAgents(ctx context.Context, bridgeID string) ([]model.Agent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bridge_id, agent_id, display_name, status, updated_at
		FROM device_agents WHERE bridge_id = ? ORDER BY display_name, agent_id`, bridgeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Agent, 0)
	for rows.Next() {
		var item model.Agent
		var updated int64
		if err := rows.Scan(&item.BridgeID, &item.AgentID, &item.DisplayName, &item.Status, &updated); err != nil {
			return nil, err
		}
		item.UpdatedAt = fromMillis(updated)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) InsertCallRecord(ctx context.Context, item model.CallRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO call_metadata(device_id, agent_id, status, duration_ms, created_at)
		VALUES(?, ?, ?, ?, ?)`, item.DeviceID, item.AgentID, item.Status, item.DurationMS, millis(item.CreatedAt)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM call_metadata WHERE id NOT IN
		(SELECT id FROM call_metadata ORDER BY id DESC LIMIT 1000)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListCallRecords(ctx context.Context, limit int) ([]model.CallRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, device_id, agent_id, status, duration_ms, created_at
		FROM call_metadata ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.CallRecord, 0)
	for rows.Next() {
		var item model.CallRecord
		var created int64
		if err := rows.Scan(&item.ID, &item.DeviceID, &item.AgentID, &item.Status, &item.DurationMS, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = fromMillis(created)
		items = append(items, item)
	}
	return items, rows.Err()
}

func millis(value time.Time) int64 { return value.UTC().UnixMilli() }

func fromMillis(value int64) time.Time { return time.UnixMilli(value).UTC() }

func nullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	result := fromMillis(value.Int64)
	return &result
}

func timePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}
