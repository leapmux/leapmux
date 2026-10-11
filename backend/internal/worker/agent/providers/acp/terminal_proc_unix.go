//go:build unix

package acp

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/leapmux/leapmux/util/procutil"
)

// configureACPTerminalCmd separates the child's native session from the Worker.
// PrepareProcess then owns cancellation and verifies descendants before its first signal.
// Setsid and Setpgid cannot combine here because Go calls them in that order.
// Setpgid fails with EPERM after Setsid creates a session leader.
func configureACPTerminalCmd(cmd *exec.Cmd) {
	procutil.DetachFromTerminal(cmd)
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
