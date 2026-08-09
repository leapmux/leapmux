//go:build unix

package agent

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// configureACPTerminalCmd puts the child in its own process group so later
// kill/Stop can reap grandchildren that inherit the pipes (Setpgid + group
// SIGTERM, then WaitDelay force-kill). Without this, Process.Kill only hits
// /bin/sh and a still-running child holds stdout/stderr open forever.
func configureACPTerminalCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second
}

func exitStatusFromWaitStatus(ps *os.ProcessState) (exitCode *int, signal *string, ok bool) {
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok {
		return nil, nil, false
	}
	if ws.Signaled() {
		sig := ws.Signal().String()
		return nil, &sig, true
	}
	if ws.Exited() {
		c := ws.ExitStatus()
		return &c, nil, true
	}
	return nil, nil, false
}
