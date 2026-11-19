//go:build unix

package cline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// C-M5 end to end: a start finds the agent directory of a crashed worker,
// whose daemon still runs. The new directory, and with it the new daemon,
// exist only after the old daemon ended, so the two never write the session's
// files at the same time. The fake `cline` of start_unix_test.go is the new
// daemon, so this test cannot run in parallel.
func TestClineStartWaitsUntilTheDaemonOfACrashedWorkerEnded(t *testing.T) {
	installFakeCline(t)
	base := t.TempDir()
	orphan := orphanAgentDir(t, base)
	old := newFakeDaemon(t)
	oldDaemon := old.proc.identity(t)
	old.hub.mu.Lock()
	// Cline's daemon ends after an accepted shutdown.
	old.hub.onShutdown = func() { _ = old.proc.stdin.Close() }
	old.hub.mu.Unlock()
	writeRecord(t, filepath.Join(orphan, discoveryFileName), old.record)
	dirs, err := agentdir.Start(context.Background(), agentdir.Config{Specs: []agentdir.Spec{agentDirSpec()}, Bases: []string{base}})
	require.NoError(t, err)

	sink := &agenttest.ControlSink{}
	opts := agent.Options{
		AgentID:        "cline-after-crash",
		WorkingDir:     t.TempDir(),
		Shell:          testutil.TestShell(),
		APITimeout:     30 * time.Second,
		StartupTimeout: 60 * time.Second,
		AgentDirs:      dirs,
	}
	oldRan := true
	started, err := start(context.Background(), opts, agent.NewProviderServices(sink), startDeps{
		getenv: os.Getenv,
		clock:  quartz.NewReal(),
		newDir: func(ctx context.Context, d *agentdir.Dirs) (*agentdir.Dir, error) {
			dir, err := newClineAgentDir(ctx, d)
			oldRan = oldDaemon.Runs()
			return dir, err
		},
	})
	require.NoError(t, err)
	a := started.(*Agent)
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
	})
	assert.False(t, oldRan, "the old daemon ended before the new directory existed")
	assert.NoDirExists(t, orphan, "the sweep removed the old directory")
	assert.Equal(t, 1, old.hub.shutdownCount())
	assert.NotEmpty(t, sink.LastSessionID(), "the new daemon opened the session")
	old.proc.waitExit(t)
	assert.True(t, old.proc.cmd.ProcessState.Success(), "the old daemon ended through its shutdown, with no kill")
}

func TestClineReportsTheSelectedCommandExitStatus(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	const candidate = "leapmux-cline-exit-peer"
	testutil.WriteExecutable(t, filepath.Join(dir, candidate), "#!/bin/sh\nprintf 'selected command ran\\n' >&2\nexit 127\n")

	started, err := startDaemonProcess(t.Context(), agent.Options{Shell: "/bin/sh", WorkingDir: dir, StartupTimeout: 30 * time.Second},
		launch.ShellCommands(candidate), dir, quartz.NewReal())
	require.Nil(t, started)
	require.ErrorContains(t, err, "stderr: selected command ran")
	require.ErrorIs(t, err, errDaemonExited)
	require.NotErrorIs(t, err, launch.ErrNoCandidate)
	var native *exec.ExitError
	require.ErrorAs(t, err, &native)
	assert.Equal(t, 127, native.ExitCode())
}

func TestClineStartupExhaustionPeer(*testing.T) { agenttest.RunInheritedWriterPeer() }

func TestClinePreservesFinalLaunchFailureAfterCancellation(t *testing.T) {
	t.Run("candidate exhaustion", func(t *testing.T) {
		err := cancelClineWhileDraining(t, "leapmux_cline_provider", "")
		require.ErrorIs(t, err, launch.ErrNoCandidate)
		assert.ErrorContains(t, err, "(leapmux_cline_provider)")
		assert.True(t, strings.HasSuffix(err.Error(), "stderr: provider absent diagnostic"), "%v", err)
		var native *exec.ExitError
		require.ErrorAs(t, err, &native)
		assert.Equal(t, 127, native.ExitCode())
	})
	t.Run("selected provider exit", func(t *testing.T) {
		err := cancelClineWhileDraining(t, "leapmux_cline_provider",
			"leapmux_cline_provider() { printf '\\nEADDRINUSE from selected provider\\n' >&2; return 37; }\n")
		require.ErrorIs(t, err, context.Canceled)
		assert.NotErrorIs(t, err, launch.ErrNoCandidate)
		assert.ErrorContains(t, err, "EADDRINUSE from selected provider")
		var native *exec.ExitError
		require.ErrorAs(t, err, &native)
		assert.Equal(t, 37, native.ExitCode())
	})
}

func cancelClineWhileDraining(t *testing.T, candidate, wrapper string) error {
	t.Helper()
	profile := agenttest.InstallStartupExhaustionProfile(t, "TestClineStartupExhaustionPeer", false)
	if wrapper != "" {
		path := os.Getenv("BASH_ENV")
		startup, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(startup, wrapper...), 0o600))
	}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	deadline := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	drain := clock.Trap().NewTimer("wrapped-process-drain")
	t.Cleanup(drain.Close)
	stop := clock.Trap().NewTimer("process-stop-grace")
	t.Cleanup(stop.Close)
	outcome := make(chan error, 1)
	attempts := 0
	go func() {
		opts := agent.Options{Shell: profile.Shell, WorkingDir: dir, StartupTimeout: 30 * time.Second}
		_, err := retryDaemonPort(ctx, opts.AgentID, func(port int) (*daemonStart, error) {
			attempts++
			return startDaemonOnPort(ctx, opts, launch.ShellCommands(candidate), dir, port, clock)
		})
		outcome <- err
	}()
	writer := profile.FinishSelection(t)
	testutil.WaitForTimer(t, deadline, drain)
	cancel()
	testutil.WaitForTimer(t, deadline, stop)
	require.NoError(t, writer.CloseWrite())

	select {
	case err := <-outcome:
		assert.NotErrorIs(t, err, errAddressInUse)
		assert.Equal(t, 1, attempts)
		return err
	case <-deadline.Done():
		t.Fatal("Cline did not return after the writer closed stdout")
		return nil
	}
}
