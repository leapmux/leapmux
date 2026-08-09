//go:build windows

package agent

import (
	"os"
	"os/exec"
	"time"

	"github.com/leapmux/leapmux/util/procutil"
)

// configureACPTerminalCmd suppresses the console window and bounds
// CommandContext teardown. Tree kill on Windows is via the JobObject
// attached after Start (see attachACPTerminalJob).
func configureACPTerminalCmd(cmd *exec.Cmd) {
	procutil.HideConsoleWindow(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 5 * time.Second
}

func exitStatusFromWaitStatus(*os.ProcessState) (exitCode *int, signal *string, ok bool) {
	return nil, nil, false
}
