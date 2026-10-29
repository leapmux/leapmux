//go:build unix

package copilot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// The launch tests put a fake `copilot` on PATH that re-runs this test binary as
// TestHelperCopilotNativeConnection. The helper answers `--version`, and
// `--server --stdio --sync-session=off` starts the same fake native runtime the
// other tests use.
//
// agenttest.InstallFakeCLI and RequireLaunchPins set PATH and environment with
// t.Setenv, which the testing package refuses in a parallel test, so these tests
// run serially.

// installFakeCopilot installs the fake runtime and returns the file that records
// the arguments of every launch.
func installFakeCopilot(t *testing.T, env ...string) string {
	t.Helper()
	argsFile := filepath.Join(t.TempDir(), "args")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: copilotBinaryName, HelperRun: "TestHelperCopilotNativeConnection", ForwardArgs: true,
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE", Env: env, ArgsFile: argsFile,
	})
	return argsFile
}

// startCopilotForTest starts the provider against the fake runtime, with a
// generous startup budget that no assertion reads.
func startCopilotForTest(t *testing.T, opts agent.Options) (agent.Agent, error) {
	t.Helper()
	if opts.AgentID == "" {
		opts.AgentID = "copilot-launch"
	}
	if opts.WorkingDir == "" {
		opts.WorkingDir = t.TempDir()
	}
	opts.Shell = testutil.TestShell()
	if opts.APITimeout == 0 {
		opts.APITimeout = 30 * time.Second
	}
	if opts.StartupTimeout == 0 {
		opts.StartupTimeout = 60 * time.Second
	}
	started, err := Start(context.Background(), opts, agent.NewProviderServices(&agenttest.ControlSink{}))
	if err == nil {
		t.Cleanup(func() {
			started.Stop()
			_ = started.Wait()
		})
	}
	return started, err
}

// Copilot CLI 1.0.87 starts its updater one second after any start that is not a
// subcommand, `--server --stdio` included. The updater downloads the newest
// package and then replaces the installed executable itself. It stops only for
// COPILOT_AUTO_UPDATE=false (case-insensitive; `0` and `off` do not count), the
// flag `--no-auto-update`, `--prefer-version` or COPILOT_OFFLINE. The variable is
// the one that children inherit, and both the loader and the app read it.
//
// The launch runs the CLI twice now, for the version probe and for the server,
// so both runs must carry the pin after the user's profile.
func TestStartPinsTheUpdaterOff(t *testing.T) {
	agenttest.RequireLaunchPins(t, agenttest.LaunchProbe{
		Binary: copilotBinaryName, Start: Start, Pins: []string{"COPILOT_AUTO_UPDATE=false"},
		Answers: map[string]string{"--version": "GitHub Copilot CLI 1.0.103."},
		Runs:    2,
	})
}

func TestStartRefusesAnOlderCopilotCLI(t *testing.T) {
	argsFile := installFakeCopilot(t, "LEAPMUX_TEST_COPILOT_VERSION=GitHub Copilot CLI 1.0.102.")
	_, err := startCopilotForTest(t, agent.Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1.0.102")
	assert.Contains(t, err.Error(), "GitHub Copilot CLI 1.0.103 or later")
	args, readErr := os.ReadFile(argsFile)
	require.NoError(t, readErr)
	assert.Equal(t, "--version", strings.TrimSpace(string(args)), "the server never launches")
}

func TestStartReportsAVersionProbeThatFails(t *testing.T) {
	installFakeCopilot(t, "LEAPMUX_TEST_COPILOT_VERSION_SCENARIO=fails")
	_, err := startCopilotForTest(t, agent.Options{})
	require.ErrorContains(t, err, "run `copilot --version`")
	assert.Contains(t, err.Error(), "copilot: the installation is broken", "the error quotes what the program printed on stderr")
	assert.NotContains(t, err.Error(), "second line", "the error quotes only the first line")
}

func TestStartReportsAVersionProbeThatNeverAnswers(t *testing.T) {
	installFakeCopilot(t, "LEAPMUX_TEST_COPILOT_VERSION_SCENARIO=hangs")
	// The fake blocks until the probe kills it, so only the probe's own limit
	// ends the wait. The startup timeout sets that limit.
	_, err := startCopilotForTest(t, agent.Options{StartupTimeout: 2 * time.Second})
	require.ErrorContains(t, err, "`copilot --version` did not answer within 2s")
}

func TestVersionProbeReportsCallerCancellation(t *testing.T) {
	installFakeCopilot(t, "LEAPMUX_TEST_COPILOT_VERSION_SCENARIO=hangs")
	spec, err := copilotLocator.Resolve(context.Background(), testutil.TestShell(), false, "GitHub Copilot")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = probeCopilotVersion(ctx, agent.Options{
		WorkingDir: t.TempDir(), Shell: testutil.TestShell(), StartupTimeout: 60 * time.Second,
	}, spec)
	require.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), "did not answer within")
}

// A user who stops the agent during startup cancels the probe while its process
// runs. The error must report that cancellation, not a probe that timed out.
func TestVersionProbeReportsCallerCancellationWhileItRuns(t *testing.T) {
	startedFile := filepath.Join(t.TempDir(), "version.started")
	installFakeCopilot(t, "LEAPMUX_TEST_COPILOT_VERSION_SCENARIO=hangs",
		"LEAPMUX_TEST_COPILOT_VERSION_STARTED_FILE="+startedFile)
	spec, err := copilotLocator.Resolve(context.Background(), testutil.TestShell(), false, "GitHub Copilot")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probed := make(chan error, 1)
	go func() {
		_, err := probeCopilotVersion(ctx, agent.Options{
			WorkingDir: t.TempDir(), Shell: testutil.TestShell(), StartupTimeout: 60 * time.Second,
		}, spec)
		probed <- err
	}()
	require.Eventually(t, func() bool {
		_, err := os.Stat(startedFile)
		return err == nil
	}, 30*time.Second, 10*time.Millisecond, "the fake copilot starts to answer `--version`")
	cancel()
	err = <-probed
	require.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), "did not answer within")
}

// A caller deadline that ends before the probe's own limit is the caller's
// deadline. The error must not claim that the probe waited its whole limit.
func TestVersionProbeReportsAnEarlierCallerDeadline(t *testing.T) {
	installFakeCopilot(t, "LEAPMUX_TEST_COPILOT_VERSION_SCENARIO=hangs")
	spec, err := copilotLocator.Resolve(context.Background(), testutil.TestShell(), false, "GitHub Copilot")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = probeCopilotVersion(ctx, agent.Options{
		WorkingDir: t.TempDir(), Shell: testutil.TestShell(), StartupTimeout: 60 * time.Second,
	}, spec)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.NotContains(t, err.Error(), "did not answer within")
}

// A background job that the login shell's profile starts inherits the shell's
// stdout, and it keeps the pipe open after `copilot --version` exits. The probe
// must not wait for that job, which can run for as long as the user's session.
func TestStartIsNotHeldByAProcessTheVersionProbeLeftBehind(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	installFakeCopilot(t,
		"LEAPMUX_TEST_COPILOT_VERSION_SCENARIO=leaves-child",
		"LEAPMUX_TEST_COPILOT_CHILD_PID_FILE="+pidFile)
	opts := agent.Options{
		AgentID: "copilot-launch", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: 30 * time.Second, StartupTimeout: 60 * time.Second,
	}

	type startResult struct {
		agent agent.Agent
		err   error
	}
	done := make(chan startResult, 1)
	go func() {
		started, err := Start(context.Background(), opts, agent.NewProviderServices(&agenttest.ControlSink{}))
		done <- startResult{agent: started, err: err}
	}()
	settle := func(result startResult) {
		if result.err == nil {
			result.agent.Stop()
			_ = result.agent.Wait()
		}
	}
	var result startResult
	select {
	case result = <-done:
	case <-time.After(45 * time.Second):
		// End the process that holds the probe, so the start returns and its
		// server does not outlive the test.
		_ = syscall.Kill(readCopilotHelperChildPID(t, pidFile), syscall.SIGKILL)
		settle(<-done)
		t.Fatal("the start waits for a process that the version probe left behind")
	}
	childPID := readCopilotHelperChildPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })
	require.NoError(t, result.err, "the probe read the version the program printed before it exited")
	t.Cleanup(func() { settle(result) })
	require.NoError(t, syscall.Kill(childPID, 0), "the process that holds the pipe still runs, so the start did not wait for it")
}

// readCopilotHelperChildPID reads the PID that the helper wrote.
func readCopilotHelperChildPID(t *testing.T, pidFile string) int {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	var pid int
	_, err = fmt.Sscan(string(data), &pid)
	require.NoError(t, err)
	require.Positive(t, pid)
	return pid
}
