//go:build windows

package wakelock

import (
	"log/slog"
	"syscall"
	"unsafe"
)

var (
	kernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
)

const (
	esSystemRequired = 0x00000001
	esContinuous     = 0x80000000
)

type windowsWakeLock struct{}

func newPlatformWakeLock() WakeLock {
	return &windowsWakeLock{}
}

func (w *windowsWakeLock) Acquire() error {
	ret, _, _ := procSetThreadExecutionState.Call(uintptr(esSystemRequired | esContinuous))
	if ret == 0 {
		return syscall.GetLastError()
	}
	slog.Debug("wakelock acquired (SetThreadExecutionState)")
	return nil
}

func (w *windowsWakeLock) Release() {
	_, _, _ = procSetThreadExecutionState.Call(uintptr(esContinuous))
	slog.Debug("wakelock released (SetThreadExecutionState)")
}

// Ensure unsafe is referenced for the syscall.
var _ unsafe.Pointer
