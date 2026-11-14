// Package fileutil contains the CLI's owner-only file replacement primitive.
package fileutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile replaces path with data without exposing a partially written file.
// It creates new directories with mode 0700 and the replacement with mode 0600.
// Callers remain responsible for coordinating read-modify-write transactions.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	file, err := os.CreateTemp(dir, ".kapten-*.tmp")
	if err != nil {
		return fmt.Errorf("create replacement for %s: %w", path, err)
	}
	defer os.Remove(file.Name())
	defer file.Close()

	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("set permissions for %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write replacement for %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync replacement for %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close replacement for %s: %w", path, err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
