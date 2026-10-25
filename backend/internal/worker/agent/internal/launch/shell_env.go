package launch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/leapmux/leapmux/internal/util/envprobe"
	"github.com/leapmux/leapmux/util/procutil"
)

// ShellEnvOptions specifies the shell and the environment that it receives.
type ShellEnvOptions struct {
	Shell      string
	LoginShell bool
	WorkingDir string
	// Env is ordered, with the last duplicate winning. nil inherits the Worker environment.
	Env       []string
	Variables []string
	// PathVariables maps a variable to the characters its consumer trims.
	PathVariables map[string]string
}

// ShellEnv reads a native child's environment and directory after its shell
// profile runs. Executing the worker probe also captures native path conversion
// performed by shells such as Git Bash when they start a Windows executable.
// Requested values travel only through stdout, never arguments or a temporary file.
func ShellEnv(ctx context.Context, opts ShellEnvOptions) (envprobe.Snapshot, ProbeResult) {
	request := envprobe.Request{Variables: opts.Variables, PathVariables: opts.PathVariables}
	args, err := request.Arguments()
	if err != nil {
		panic(err)
	}
	executable, err := os.Executable()
	if err != nil {
		return envprobe.Snapshot{}, ProbeUnknown
	}
	workingDir := opts.WorkingDir
	if workingDir == "" {
		workingDir = os.TempDir()
	}
	cmd, _, _ := Wrap(ctx, WrapSpec{
		Shell: opts.Shell, LoginShell: opts.LoginShell, WorkingDir: workingDir,
		Launch: Spec{Program: executable}, BaseArgs: args,
	})
	if opts.Env != nil {
		cmd.Env = append([]string{}, opts.Env...)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// A profile can leave a descendant holding stdout after the probe exits.
	// The owner limits that pipe wait and owns cancellation of live children.
	owner := procutil.PrepareProcess(cmd)
	runErr := owner.Start()
	if runErr == nil {
		runErr = owner.Wait()
	}
	cleanupErr := owner.Close()
	if errors.Is(runErr, exec.ErrWaitDelay) {
		runErr = nil
	}
	if ctx.Err() != nil || errors.Join(runErr, cleanupErr) != nil {
		return envprobe.Snapshot{}, ProbeUnknown
	}
	snapshot, err := envprobe.Decode(stdout.Bytes())
	if err != nil {
		return envprobe.Snapshot{}, ProbeUnknown
	}
	for _, name := range opts.Variables {
		if _, present := snapshot.Values[name]; !present {
			return envprobe.Snapshot{}, ProbeUnknown
		}
	}
	return snapshot, ProbeYes
}
