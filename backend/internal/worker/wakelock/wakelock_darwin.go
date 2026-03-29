//go:build darwin

package wakelock

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
)

type darwinWakeLock struct {
	cmd *exec.Cmd
}

func newPlatformWakeLock() WakeLock {
	return &darwinWakeLock{}
}

func (w *darwinWakeLock) Acquire() error {
	// Use -w to tie caffeinate's lifetime to the current process.
	// If the worker is killed (e.g. SIGKILL), caffeinate exits automatically
	// instead of remaining as an orphan.
	w.cmd = exec.Command("caffeinate", "-i", "-w", fmt.Sprintf("%d", os.Getpid()))
	if err := w.cmd.Start(); err != nil {
		return err
	}
	slog.Debug("wakelock acquired (caffeinate -i -w)", "pid", w.cmd.Process.Pid)
	return nil
}

func (w *darwinWakeLock) Release() {
	if w.cmd != nil && w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
		_ = w.cmd.Wait()
		slog.Debug("wakelock released (caffeinate killed)")
		w.cmd = nil
	}
}
