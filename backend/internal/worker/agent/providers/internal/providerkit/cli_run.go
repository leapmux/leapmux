package providerkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
)

// CLIRun states one short command of a provider's CLI that the worker runs
// through the user's shell and reads the output of: a version probe, or the
// listing of the stored sessions.
//
// The command runs through the same login shell that starts the provider's
// agents, so it finds the same program, the same PATH and the same login.
type CLIRun struct {
	// Locator finds the CLI.
	Locator launch.Locator
	// Label identifies the CLI in an error message, for example "Cline".
	Label string
	// Shell and LoginShell are the shell the worker launches agents through.
	// An empty Shell selects the platform's default shell.
	Shell      string
	LoginShell bool
	// Args are the CLI's arguments.
	Args []string
	// WorkingDir is the command's directory. A directory that does not exist
	// is left out, and the command runs in the shell's own directory.
	WorkingDir string
	// StripEnvKeys are removed by the shell wrapper after the profile runs.
	StripEnvKeys []string
	// SetEnv are set by the shell wrapper after the profile runs and after
	// StripEnvKeys are removed, just before the program starts. Each entry is
	// `NAME=VALUE`, and launch.WrapSpec.SetEnv states the rules. Put here a
	// value that must reach the program unchanged: a profile export can replace
	// a value that Env sets, because the shell runs the profile after it.
	SetEnv []string
	// Env finishes the environment. It takes the inherited environment, already
	// scrubbed by FinalizeAgentEnv, and returns the one the command runs with.
	// Nil keeps the scrubbed environment.
	Env func(env []string) []string
	// Timeout limits the whole command, the shell's profile included.
	Timeout time.Duration
	// MaxOutput caps the bytes of stdout that the run keeps. Output past it
	// fails the run, because a truncated record cannot be read.
	MaxOutput int
}

// maxCLIStderr caps the bytes of stderr that a run keeps for its error message.
const maxCLIStderr = 64 << 10

// RunCLI runs one command and returns what it printed to stdout after the
// shell wrapper's preamble.
func RunCLI(ctx context.Context, run CLIRun) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, run.Timeout)
	defer cancel()

	shell := run.Shell
	if shell == "" {
		shell = terminal.ResolveDefaultShell()
	}
	spec, err := run.Locator.Resolve(ctx, shell, run.LoginShell, run.Label)
	if err != nil {
		return nil, err
	}
	workingDir := run.WorkingDir
	if info, err := os.Stat(workingDir); err != nil || !info.IsDir() {
		workingDir = ""
	}
	cmd, delimiter, _ := launch.Wrap(ctx, launch.WrapSpec{
		Shell:        shell,
		LoginShell:   run.LoginShell,
		Launch:       spec,
		StripEnvKeys: run.StripEnvKeys,
		SetEnv:       run.SetEnv,
		BaseArgs:     run.Args,
		WorkingDir:   workingDir,
	})
	env := FinalizeAgentEnv(cmd.Environ(), agent.Options{})
	if run.Env != nil {
		env = run.Env(env)
	}
	cmd.Env = env
	procutil.GracefulGroupCancel(cmd)
	var stdout, stderr LimitedBuffer
	stdout.Limit, stderr.Limit = run.MaxOutput, maxCLIStderr
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil
	// A process that the CLI leaves behind, or that the login shell's profile
	// started, inherits stdout and keeps the pipe open after the CLI exits. The
	// WaitDelay of GracefulGroupCancel then ends the wait with ErrWaitDelay,
	// which Wait returns only for a successful exit. The CLI's own output is
	// complete, so it is read as usual.
	if err := cmd.Run(); err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("`%s` did not answer within %s", strings.Join(append([]string{run.Label}, run.Args...), " "), run.Timeout)
		}
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Truncated {
		return nil, fmt.Errorf("the output exceeds %d bytes", run.MaxOutput)
	}
	return AfterPreamble(stdout.Bytes(), delimiter), nil
}

// AfterPreamble returns the output after the shell wrapper's delimiter line. A
// login shell's profile can print before it, and the program prints after it.
// An output with no delimiter comes back whole.
func AfterPreamble(output []byte, delimiter string) []byte {
	if delimiter == "" {
		return output
	}
	marker := []byte(delimiter)
	index := bytes.Index(output, marker)
	if index < 0 {
		return output
	}
	rest := output[index+len(marker):]
	if newline := bytes.IndexByte(rest, '\n'); newline >= 0 {
		return rest[newline+1:]
	}
	return nil
}

// LimitedBuffer keeps at most Limit bytes and records whether it dropped any.
// It keeps accepting writes past the limit, so a child never stalls on a full
// pipe.
//
// It holds its buffer in a field rather than embedding it. An embedded
// bytes.Buffer would promote ReadFrom, and io.Copy -- which exec uses to fill
// cmd.Stdout -- prefers ReadFrom to Write, so the limit would never apply.
type LimitedBuffer struct {
	buf       bytes.Buffer
	Limit     int
	Truncated bool
}

func (b *LimitedBuffer) Write(data []byte) (int, error) {
	room := b.Limit - b.buf.Len()
	if room <= 0 {
		b.Truncated = b.Truncated || len(data) > 0
		return len(data), nil
	}
	if len(data) > room {
		b.Truncated = true
		_, _ = b.buf.Write(data[:room])
		return len(data), nil
	}
	return b.buf.Write(data)
}

// Bytes returns what the buffer kept.
func (b *LimitedBuffer) Bytes() []byte { return b.buf.Bytes() }

// String returns what the buffer kept.
func (b *LimitedBuffer) String() string { return b.buf.String() }

var _ io.Writer = (*LimitedBuffer)(nil)
