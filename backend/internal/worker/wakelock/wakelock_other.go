//go:build !darwin && !linux && !windows

package wakelock

type noopWakeLock struct{}

func newPlatformWakeLock() WakeLock {
	return &noopWakeLock{}
}

func (w *noopWakeLock) Acquire() error { return nil }
func (w *noopWakeLock) Release()       {}
