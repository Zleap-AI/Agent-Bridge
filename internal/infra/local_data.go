package infra

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const LocalDataDir = ".agent-bridge"

// SecureLocalDataPermissions upgrades existing Local state to owner-only
// permissions before credentials, conversations, or logs are read.
func SecureLocalDataPermissions() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("locate home directory: %w", err)
	}
	root := filepath.Join(home, LocalDataDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create Local data directory: %w", err)
	}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		mode := os.FileMode(0o600)
		if entry.IsDir() {
			mode = 0o700
		} else {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			if info.Mode().Perm()&0o111 != 0 {
				mode = 0o700
			}
		}
		return os.Chmod(path, mode)
	}); err != nil {
		return fmt.Errorf("secure Local data permissions: %w", err)
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}
