//go:build unix

package providerkit

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// fakeProgram writes a shell script and returns the locator that states it by
// its absolute path.
func fakeProgram(t *testing.T, body string) launch.Locator {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	return launch.Binaries(path)
}

func TestRunCLIReturnsWhatTheProgramPrinted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	output, err := RunCLI(context.Background(), CLIRun{
		Locator:    fakeProgram(t, `printf '%s|%s|%s\n' "$1" "$(pwd)" "$FAKE_VALUE"`),
		Label:      "Tool",
		Shell:      "/bin/sh",
		Args:       []string{"history"},
		WorkingDir: dir,
		Env:        func(env []string) []string { return append(env, "FAKE_VALUE=set") },
		Timeout:    30 * time.Second,
		MaxOutput:  1 << 10,
	})
	require.NoError(t, err)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	fields := strings.Split(strings.TrimSpace(string(output)), "|")
	require.Len(t, fields, 3)
	assert.Equal(t, "history", fields[0])
	resolvedPWD, err := filepath.EvalSymlinks(fields[1])
	require.NoError(t, err)
	assert.Equal(t, resolved, resolvedPWD)
	assert.Equal(t, "set", fields[2], "the environment hook reaches the program")
}

// A process that the CLI leaves behind inherits its stdout and keeps the pipe
// open after the CLI exits. The run's WaitDelay then ends the wait, and the
// CLI's own exit status and output still decide the result.
func TestRunCLIReturnsTheOutputOfACLIThatLeavesAProcessBehind(t *testing.T) {
	t.Parallel()
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Cleanup(func() {
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	output, err := RunCLI(context.Background(), CLIRun{
		Locator:   fakeProgram(t, "sleep 60 &\necho $! > '"+pidFile+"'\necho done"),
		Label:     "Tool",
		Shell:     "/bin/sh",
		Timeout:   30 * time.Second,
		MaxOutput: 1 << 10,
	})
	require.NoError(t, err)
	assert.Equal(t, "done", strings.TrimSpace(string(output)))
}

func TestRunCLIRunsInTheShellsDirectoryWhenTheDirectoryIsGone(t *testing.T) {
	t.Parallel()
	output, err := RunCLI(context.Background(), CLIRun{
		Locator: fakeProgram(t, `echo ok`), Label: "Tool", Shell: "/bin/sh",
		WorkingDir: filepath.Join(t.TempDir(), "gone"), Timeout: 30 * time.Second, MaxOutput: 1 << 10,
	})
	require.NoError(t, err)
	assert.Equal(t, "ok\n", string(output))
}

func TestRunCLIReportsAFailureWithItsStderr(t *testing.T) {
	t.Parallel()
	_, err := RunCLI(context.Background(), CLIRun{
		Locator: fakeProgram(t, `echo "no such session" >&2; exit 3`), Label: "Tool", Shell: "/bin/sh",
		Timeout: 30 * time.Second, MaxOutput: 1 << 10,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such session")
}

func TestRunCLIRefusesTooMuchOutput(t *testing.T) {
	t.Parallel()
	_, err := RunCLI(context.Background(), CLIRun{
		Locator: fakeProgram(t, `printf '0123456789'`), Label: "Tool", Shell: "/bin/sh",
		Timeout: 30 * time.Second, MaxOutput: 4,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds 4 bytes", "a cut record cannot be read")
}

func TestRunCLIStopsAProgramThatDoesNotAnswer(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RunCLI(ctx, CLIRun{
		Locator: fakeProgram(t, `echo late`), Label: "Tool", Shell: "/bin/sh",
		Args: []string{"history"}, Timeout: 30 * time.Second, MaxOutput: 1 << 10,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "`Tool history` did not answer", "the error states the command")
}

func TestRunCLIReportsAProgramItCannotFind(t *testing.T) {
	t.Parallel()
	_, err := RunCLI(context.Background(), CLIRun{
		Locator: launch.Binaries(filepath.Join(t.TempDir(), "absent")), Label: "Tool", Shell: "/bin/sh",
		Timeout: 30 * time.Second, MaxOutput: 1 << 10,
	})
	require.Error(t, err)
}

// A login shell runs the user's profile after the worker hands it cmd.Env, so
// a profile export replaces a value that Env sets. A value in SetEnv is set
// after the profile, so it reaches the program unchanged. The shell reads the
// profile of the test's own HOME, never the user's.
func TestRunCLISetEnvWinsOverAProfileExport(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".profile"),
		[]byte("export FAKE_SET=profile\nexport FAKE_ENV=profile\n"), 0o600))
	output, err := RunCLI(context.Background(), CLIRun{
		Locator:    fakeProgram(t, `printf '%s|%s\n' "$FAKE_SET" "$FAKE_ENV"`),
		Label:      "Tool",
		Shell:      "/bin/sh",
		LoginShell: true,
		WorkingDir: home,
		SetEnv:     []string{"FAKE_SET=private"},
		Env: func(env []string) []string {
			return envutil.PinEnv(env, "HOME="+home, "ENV=", "FAKE_ENV=private")
		},
		Timeout:   30 * time.Second,
		MaxOutput: 1 << 10,
	})
	require.NoError(t, err)
	fields := strings.Split(strings.TrimSpace(string(output)), "|")
	require.Len(t, fields, 2, "output: %q", output)
	assert.Equal(t, "private", fields[0], "a SetEnv value wins over the profile export")
	assert.Equal(t, "profile", fields[1], "the profile runs, and its export replaces a value that Env sets")
}

// The shell wrapper removes StripEnvKeys after the profile runs, so a key
// that the environment hook sets does not reach the program.
func TestRunCLIStripsTheStripEnvKeys(t *testing.T) {
	t.Parallel()
	output, err := RunCLI(context.Background(), CLIRun{
		Locator:      fakeProgram(t, `printf '%s|%s\n' "${FAKE_STRIPPED-unset}" "$FAKE_KEPT"`),
		Label:        "Tool",
		Shell:        "/bin/sh",
		StripEnvKeys: []string{"FAKE_STRIPPED"},
		Env: func(env []string) []string {
			return append(env, "FAKE_STRIPPED=leaked", "FAKE_KEPT=kept")
		},
		Timeout:   30 * time.Second,
		MaxOutput: 1 << 10,
	})
	require.NoError(t, err)
	assert.Equal(t, "unset|kept\n", string(output))
}

// A command of the CLI runs with the environment of an agent: the identity of
// a harness that started the worker and an inherited provider-helper variable
// do not reach it. The hook sees the environment after that scrub. t.Setenv
// keeps the test serial.
func TestRunCLIRunsWithTheScrubbedEnvironment(t *testing.T) {
	t.Setenv("GROK_SESSION_ID", "the-parent-session")
	t.Setenv(contracts.EnvAgentHelper, "/elsewhere/helper.json")
	var hookSaw []string
	output, err := RunCLI(context.Background(), CLIRun{
		Locator: fakeProgram(t, `printf '%s|%s|%s\n' "${GROK_SESSION_ID-unset}" "${`+contracts.EnvAgentHelper+`-unset}" "$LEAPMUX_WORKER"`),
		Label:   "Tool",
		Shell:   "/bin/sh",
		Env: func(env []string) []string {
			hookSaw = env
			return env
		},
		Timeout:   30 * time.Second,
		MaxOutput: 1 << 10,
	})
	require.NoError(t, err)
	assert.Equal(t, "unset|unset|1\n", string(output))
	assert.False(t, envutil.HasKey(hookSaw, "GROK_SESSION_ID"), "the hook sees the scrubbed environment")
	assert.False(t, envutil.HasKey(hookSaw, contracts.EnvAgentHelper))
}
