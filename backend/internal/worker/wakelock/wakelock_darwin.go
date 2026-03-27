//go:build darwin

package wakelock

import (
	"log/slog"
	"os/exec"
)

type darwinWakeLock struct {
	cmd *exec.Cmd
}

func newPlatformWakeLock() WakeLock {
	return &darwinWakeLock{}
}

func (w *darwinWakeLock) Acquire() error {
	w.cmd = exec.Command("caffeinate", "-i")
	if err := w.cmd.Start(); err != nil {
		return err
	}
	slog.Debug("wakelock acquired (caffeinate -i)", "pid", w.cmd.Process.Pid)
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
