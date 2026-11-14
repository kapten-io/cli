package fileutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dir", "refresh.lock")
	unlock, err := Lock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Lock(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	again, err := Lock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	again()

	// A lock left by a dead process expires.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * staleLock)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	stale, err := Lock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	stale()
}
