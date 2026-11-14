package mcphost

import (
	"errors"
	"syscall"
	"testing"
)

func assertProcessReaped(t *testing.T, pid int) {
	t.Helper()
	handle, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	// ERROR_INVALID_PARAMETER means the PID is no longer present.
	if errors.Is(err, syscall.Errno(87)) {
		return
	}
	if err != nil {
		t.Fatalf("inspect helper %d: %v", pid, err)
	}
	defer syscall.CloseHandle(handle)
	event, err := syscall.WaitForSingleObject(handle, 0)
	if err != nil || event != syscall.WAIT_OBJECT_0 {
		t.Fatalf("helper %d has not exited: event=%d err=%v", pid, event, err)
	}
}
