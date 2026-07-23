package service

import (
	"os"
	"path/filepath"
)

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func writeFileAtomically(path string, data []byte, mode os.FileMode) (err error) {
	// 优先在目标目录创建临时文件（同盘，os.Rename 原子操作）
	// 权限拒绝时降级到系统临时目录 + copy+delete（跨盘兼容）
	// Lzm 2026-07-20
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agent-bridge-*.tmp")
	if os.IsPermission(err) {
		// 降级：系统临时目录，需走 copy+delete 路径
		tmp, err = os.CreateTemp("", ".agent-bridge-*.tmp")
	}
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	err = os.Rename(tmpPath, path)
	if err != nil {
		// 跨盘（Windows os.Rename 不能跨驱动器）→ 直接写目标路径 + 清理临时文件
		if writeErr := os.WriteFile(path, data, mode); writeErr != nil {
			return writeErr
		}
		_ = os.Remove(tmpPath)
		return nil
	}
	return nil
}
