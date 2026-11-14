//go:build !windows

package mcphost

import (
	"errors"
	"syscall"
	"testing"
)

func assertProcessReaped(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("helper %d not reaped: %v", pid, err)
	}
}
