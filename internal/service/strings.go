package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func truncateString(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func safeSessionFileID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "session_" + hex.EncodeToString(sum[:])
}

// legacySessionFileID preserves the v0.3 path mapping for read/delete fallback.
// New writes use safeSessionFileID so opaque ACP IDs cannot collide or create
// invalid Windows paths.
func legacySessionFileID(value string) string {
	value = strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"\x00", "_",
	).Replace(value)
	if value == "" {
		return "session"
	}
	return value
}
