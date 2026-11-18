package launch

import (
	"context"
	"errors"
	"os/exec"
	"slices"

	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
)

// newShellCommand prepares the same shell environment for discovery and launch.
func newShellCommand(ctx context.Context, shell string, loginShell bool, script, dir string) (*exec.Cmd, error) {
	return newShellCommandWithEnvironment(ctx, shell, loginShell, script, dir, nil)
}

func newShellCommandWithEnvironment(ctx context.Context, shell string, loginShell bool, script, dir string, environment []string) (*exec.Cmd, error) {
	prepared := &exec.Cmd{Dir: dir, Env: slices.Clone(environment)}
	pathErr := RefreshCommandPath(prepared)
	program, lookupErr := resolvePreparedShell(prepared, shell)
	cmd := exec.CommandContext(ctx, program, terminal.CommandArgs(shell, loginShell, script)...)
	cmd.Dir = dir
	cmd.Env = prepared.Env
	if lookupErr != nil {
		cmd.Err = lookupErr
	}
	procutil.HideConsoleWindow(cmd)
	procutil.DetachFromTerminal(cmd)
	return cmd, errors.Join(pathErr, lookupErr)
}
