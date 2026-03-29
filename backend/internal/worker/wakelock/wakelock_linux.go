//go:build linux

package wakelock

import (
	"log/slog"
	"os/exec"
	"syscall"
)

type linuxWakeLock struct {
	cmd *exec.Cmd
}

func newPlatformWakeLock() WakeLock {
	return &linuxWakeLock{}
}

func (w *linuxWakeLock) Acquire() error {
	w.cmd = exec.Command(
		"systemd-inhibit",
		"--what=idle",
		"--who=leapmux",
		"--why=Worker active",
		"--mode=block",
		"cat",
	)
	// Ask the kernel to send SIGTERM to the child when the worker process
	// dies (e.g. SIGKILL), preventing an orphaned inhibitor.
	w.cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGTERM,
	}
	if err := w.cmd.Start(); err != nil {
		return err
	}
	slog.Debug("wakelock acquired (systemd-inhibit)", "pid", w.cmd.Process.Pid)
	return nil
}

func (w *linuxWakeLock) Release() {
	if w.cmd != nil && w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
		_ = w.cmd.Wait()
		slog.Debug("wakelock released (systemd-inhibit killed)")
		w.cmd = nil
	}
}
