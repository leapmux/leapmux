//go:build windows

package acp

import (
	"os"
	"os/exec"

	"github.com/leapmux/leapmux/util/procutil"
)

// configureACPTerminalCmd suppresses the console window.
// The prepared owner attaches the Windows Job Object before cancellation can run.
func configureACPTerminalCmd(cmd *exec.Cmd) {
	procutil.HideConsoleWindow(cmd)
}

func exitStatusFromWaitStatus(*os.ProcessState) (exitCode *int, signal *string, ok bool) {
	return nil, nil, false
}
