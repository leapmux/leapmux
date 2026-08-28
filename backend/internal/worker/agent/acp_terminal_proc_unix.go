//go:build unix

package agent

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/leapmux/leapmux/util/procutil"
)

// configureACPTerminalCmd starts the child in a new session so it cannot
// steal leapmux solo's foreground tty (the SIGTTIN class DetachFromTerminal
// documents). Setsid also makes the child a process-group leader, so later
// kill/Stop can reap grandchildren that inherit the pipes (group SIGTERM,
// then WaitDelay force-kill). Without this, Process.Kill only hits /bin/sh
// and a still-running child holds stdout/stderr open forever. Do not set
// Setpgid as well: Go runs setsid then setpgid, and setpgid on a session
// leader fails with EPERM.
func configureACPTerminalCmd(cmd *exec.Cmd) {
	procutil.DetachFromTerminal(cmd)
	procutil.GracefulGroupCancel(cmd)
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
