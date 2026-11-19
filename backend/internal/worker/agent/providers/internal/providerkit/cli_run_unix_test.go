//go:build unix

package providerkit

import (
	"context"
	"errors"
	"os"
	"os/exec"
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
	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// fakeProgram writes a shell script and returns the locator that states it by
// its absolute path.
func fakeProgram(t *testing.T, body string) launch.Locator {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	testutil.WriteExecutable(t, path, "#!/bin/sh\n"+body+"\n")
	return directCommandLocator(path)
}

func directCommandLocator(path string) launch.Locator {
	return launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		return launch.Spec{Program: path}, launch.Found
	})
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
		Locator: directCommandLocator(filepath.Join(t.TempDir(), "absent")), Label: "Tool", Shell: "/bin/sh",
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

func TestRunCLIRefusesAProfileThatExitsBeforeReadiness(t *testing.T) {
	t.Parallel()
	shell := filepath.Join(t.TempDir(), "early-exit-shell")
	testutil.WriteExecutable(t, shell, "#!/bin/sh\nprintf 'profile banner'\nexit 0\n")
	result, err := RunCLI(t.Context(), CLIRun{
		Locator: launch.Binaries("leapmux-tool"), Label: "Tool", Shell: shell,
		Timeout: 30 * time.Second, MaxOutput: 1024,
	})
	require.ErrorContains(t, err, "before provider readiness")
	require.Empty(t, result)
}

func TestRunCLISelectsAProfileCommandWithoutStartingAScan(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile")
	calls := filepath.Join(dir, "calls")
	shell, err := exec.LookPath("bash")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(profile, []byte("printf 'profile\\n' >> "+shellquote.POSIX.Quote(calls)+
		"\nleapmux_cli_second() { printf 'found %s\\n' \"$1\"; }\n"), 0o600))
	t.Setenv("BASH_ENV", profile)
	out, err := RunCLI(t.Context(), CLIRun{
		Locator: launch.Binaries("leapmux_cli_first", "leapmux_cli_second"), Label: "Tool", Shell: shell,
		Args: []string{"history"}, Timeout: 30 * time.Second, MaxOutput: 1024,
	})
	require.NoError(t, err)
	assert.Equal(t, "found history\n", string(out))
	data, err := os.ReadFile(calls)
	require.NoError(t, err)
	assert.Equal(t, "profile\n", string(data))
}

func TestRunCLIKeepsCandidateExhaustionWhenCanceledDuringDrain(t *testing.T) {
	t.Parallel()
	writerFixture := agenttest.NewInheritedWriter(t, "TestWrappedProcessWriter")
	releaseFixture := agenttest.NewInheritedWriter(t, "TestWrappedProcessWriter")
	bash, profile := wrappedBashProfile(t, writerFixture.ShellCommand+" &\n"+
		releaseFixture.ShellCommand+" >/dev/null 2>/dev/null\nprintf 'isolated shell diagnostic' >&2")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := RunCLI(ctx, CLIRun{
			Locator: launch.Binaries("launch_test_definitely_missing", "launch_test_also_missing"), Label: "Tool", Shell: bash,
			Env:     func(env []string) []string { return envutil.PinEnv(env, "BASH_ENV="+profile) },
			Timeout: 30 * time.Second, MaxOutput: 1024,
		})
		done <- result{output: output, err: err}
	}()
	writer, writerIdentity, _ := writerFixture.Accept(t)
	release, _, shell := releaseFixture.Accept(t)
	require.NoError(t, release.CloseWrite())
	require.Eventually(t, func() bool { return !shell.Runs() }, 10*time.Second, 10*time.Millisecond)
	select {
	case <-done:
		t.Fatal("the inherited writer closed before the caller canceled the drain")
	default:
	}
	require.True(t, writerIdentity.Runs())
	cancel()
	select {
	case result := <-done:
		require.Empty(t, result.output)
		require.ErrorIs(t, result.err, launch.ErrNoCandidate)
		assert.ErrorContains(t, result.err, "launch_test_definitely_missing, launch_test_also_missing")
		assert.ErrorContains(t, result.err, "isolated shell diagnostic")
		assert.NotContains(t, result.err.Error(), "__LEAPMUX_EXHAUSTED_")
		var exit *exec.ExitError
		require.ErrorAs(t, result.err, &exit)
		assert.Equal(t, 127, exit.ExitCode())
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the CLI did not return by the test deadline after cancellation during the output drain")
	}
	assert.True(t, writerIdentity.Runs(), "an unverified inherited writer remains outside the worker's process ownership")
	require.NoError(t, writer.CloseWrite())
}

func TestRunCLIReportsNativeFailureWhenCanceledDuringDrain(t *testing.T) {
	t.Parallel()
	writerFixture := agenttest.NewInheritedWriter(t, "TestWrappedProcessWriter")
	releaseFixture := agenttest.NewInheritedWriter(t, "TestWrappedProcessWriter")
	bash, profile := wrappedBashProfile(t, "launch_test_wrapper() {\n"+
		writerFixture.ShellCommand+" &\n"+
		releaseFixture.ShellCommand+" >/dev/null 2>/dev/null\n"+
		"printf 'native diagnostic' >&2\nreturn 37\n}")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := RunCLI(ctx, CLIRun{
			Locator: launch.Binaries("launch_test_wrapper"), Label: "Tool", Shell: bash,
			Env:     func(env []string) []string { return envutil.PinEnv(env, "BASH_ENV="+profile) },
			Timeout: 30 * time.Second, MaxOutput: 1024,
		})
		done <- err
	}()
	writer, writerIdentity, _ := writerFixture.Accept(t)
	release, _, root := releaseFixture.Accept(t)
	require.NoError(t, release.CloseWrite())
	require.Eventually(t, func() bool { return !root.Runs() }, 10*time.Second, 10*time.Millisecond)
	require.True(t, writerIdentity.Runs())
	select {
	case err := <-done:
		t.Fatalf("the drain finished while the inherited writer was still running: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit)
		assert.Equal(t, 37, exit.ExitCode())
		assert.ErrorContains(t, err, "native diagnostic")
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the CLI did not return by the test deadline after cancellation during the output drain")
	}
	assert.True(t, writerIdentity.Runs(), "the worker must leave an unverified writer alive")
	require.NoError(t, writer.CloseWrite())
}

func TestRunCLIPreservesCancellationWhenTheLiveCommandExitsDuringIt(t *testing.T) {
	t.Parallel()
	fixture := agenttest.NewInheritedWriter(t, "TestWrappedProcessWriter")
	tool := fakeProgram(t, "trap 'exit 37' TERM\n"+fixture.ShellCommand+" &\nwait")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := RunCLI(ctx, CLIRun{
			Locator: tool, Label: "Tool", Shell: "/bin/sh", Timeout: 30 * time.Second, MaxOutput: 1024,
		})
		done <- err
	}()
	conn, _, root := fixture.Accept(t)
	require.True(t, root.Runs())
	cancel()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "`Tool` did not answer")
		var exit *exec.ExitError
		assert.False(t, errors.As(err, &exit))
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the CLI did not finish after cancellation")
	}
	agenttest.RequireProcessPeerClosed(t, conn)
}
