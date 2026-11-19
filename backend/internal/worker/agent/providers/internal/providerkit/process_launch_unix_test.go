//go:build unix

package providerkit

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/util/procutil"
)

func TestWrappedProcessWriter(*testing.T) { agenttest.RunInheritedWriterPeer() }

// outputPipeFile is the file under one output pipe of a started process. Only
// the Unix tests read a pipe's file, so it lives here: on Windows it would be
// unused, which the linter refuses.
func outputPipeFile(reader io.Reader) *os.File {
	if pipe, ok := reader.(*processOutputPipe); ok {
		return pipe.file
	}
	return reader.(*os.File)
}

func wrappedBashProfile(t *testing.T, body string) (string, string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	profile := filepath.Join(t.TempDir(), "profile")
	require.NoError(t, os.WriteFile(profile, []byte(body+"\n"), 0o600))
	return bash, profile
}

func prepareWrappedWriter(t *testing.T, spec launch.Spec, body string) *launch.PreparedLaunch {
	t.Helper()
	bash, profile := wrappedBashProfile(t, body)
	prepared, err := launch.Wrap(t.Context(), launch.WrapSpec{Shell: bash, Launch: spec})
	require.NoError(t, err)
	prepared.Cmd.Env = envutil.PinEnv(prepared.Cmd.Environ(), "BASH_ENV="+profile)
	t.Cleanup(func() { require.NoError(t, prepared.Close()) })
	return prepared
}

func startWrappedProcess(t *testing.T, prepared *launch.PreparedLaunch, clock quartz.Clock, cancel func()) (*Process, *ProcessPipes) {
	t.Helper()
	return startWrappedProcessWithExitObserver(t, prepared, clock, cancel, nil)
}

func startWrappedProcessWithExitObserver(t *testing.T, prepared *launch.PreparedLaunch, clock quartz.Clock, cancel func(), observer procutil.ProcessExitObserver) (*Process, *ProcessPipes) {
	t.Helper()
	pipes, err := SetupLaunchPipesWithExitObserver(prepared, cancel, observer)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pipes.Close() })
	process := NewProcessFrom(ProcessConfig{
		pipes: pipes, Clock: clock, Ctx: t.Context(), Cancel: cancel,
		PreambleDelimiter: prepared.Delimiter, PreambleMetaPrefix: prepared.MetaPrefix,
	})
	require.NoError(t, process.StartCmd())
	requireClosedPipeEnd(t, prepared.Cmd.Stdin.(*os.File))
	requireClosedPipeEnd(t, prepared.Cmd.Stdout.(*os.File))
	requireClosedPipeEnd(t, prepared.Cmd.Stderr.(*os.File))
	return &process, pipes
}

type observedFixtureExits map[procutil.ProcessIdentity]error

func (exits observedFixtureExits) ObserveExit(_ context.Context, identity procutil.ProcessIdentity, nativeErr error) error {
	exits[identity] = nativeErr
	return nil
}

func (exits observedFixtureExits) requireExit(t *testing.T, identity procutil.ProcessIdentity) {
	t.Helper()
	// An exited orphan can retain its PID until the system reaps it.
	nativeErr, observed := exits[identity]
	require.True(t, observed, "teardown must observe exit of process %d", identity.PID)
	require.NoError(t, nativeErr)
}

func TestWrappedProcessLateStopKeepsTheNativeExitClassification(t *testing.T) {
	t.Parallel()
	writer := agenttest.NewInheritedWriter(t, "TestWrappedProcessWriter")
	prepared := prepareWrappedWriter(t, launch.ShellCommands("launch_test_wrapper"),
		"launch_test_wrapper() { "+writer.ShellCommand+
			" &\n  IFS= read -r release\n  printf 'last native line\\n'\n  IFS= read -r release\n  return 37\n}")
	clock := testutil.NewQuartzMock(t)
	drain := clock.Trap().NewTimer(WrappedProcessDrainTimerTag)
	t.Cleanup(drain.Close)
	stop := clock.Trap().NewTimer(ProcessStopGraceTimerTag)
	t.Cleanup(stop.Close)
	exits := make(observedFixtureExits)
	process, pipes := startWrappedProcessWithExitObserver(t, prepared, clock, func() {}, exits)
	output := captureWrappedOutput(process, pipes)
	_, child, root := writer.Accept(t)
	require.Equal(t, process.owner.PID(), root.PID)
	require.NoError(t, process.WriteStdin([]byte("send output\n")))

	deadline := testutil.DeadlineContext(t)
	select {
	case line := <-output:
		require.Equal(t, "last native line", line)
	case <-deadline.Done():
		t.Fatal("the output handler did not receive the last provider line")
	}
	require.NoError(t, process.WriteStdin([]byte("exit\n")))
	assert.Equal(t, stderrDrainWait, testutil.WaitForTimer(t, deadline, drain))
	assert.True(t, child.Runs())
	select {
	case <-process.ProcessDone():
		t.Fatal("the wrapper completed before its stdout and tracked child closed")
	default:
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		process.Stop()
	}()
	assert.Equal(t, stderrDrainWait, testutil.WaitForTimer(t, deadline, stop))
	clock.Advance(stderrDrainWait).MustWait(deadline)
	awaitProcessDone(t, deadline, process)
	select {
	case <-finished:
	case <-deadline.Done():
		t.Fatal("Stop did not complete")
	}

	assert.Equal(t, agent.MessageCompletionError, process.ProcessExitCompletion())
	assert.False(t, process.IntentionalStopRequested())
	var exit *exec.ExitError
	require.ErrorAs(t, process.Wait(), &exit)
	assert.Equal(t, 37, exit.ExitCode())
	exits.requireExit(t, child)
}

func TestWrappedProcessForcedDrainKeepsTheUnverifiedWriterAndReportsExitBeforeCancel(t *testing.T) {
	t.Parallel()
	writer := agenttest.NewInheritedWriter(t, "TestWrappedProcessWriter")
	prepared := prepareWrappedWriter(t, launch.ShellCommands("launch_test_definitely_missing"),
		writer.ShellCommand+" &\nIFS= read -r release\nprintf 'profile diagnostic' >&2")
	clock := testutil.NewQuartzMock(t)
	drain := clock.Trap().NewTimer(WrappedProcessDrainTimerTag)
	t.Cleanup(drain.Close)
	canceledAfterDone := make(chan bool, 2)
	var process *Process
	process, pipes := startWrappedProcess(t, prepared, clock, func() {
		select {
		case <-process.ProcessDone():
			canceledAfterDone <- true
		default:
			canceledAfterDone <- false
		}
	})
	process.DrainStderr(pipes.Stderr())
	go process.ReadLines(agent.NewStdoutScanner(pipes.Stdout()), func(line []byte) { t.Errorf("unexpected provider output %q", line) })
	conn, child, _ := writer.Accept(t)
	require.NoError(t, process.WriteStdin([]byte("exit\n")))

	deadline := testutil.DeadlineContext(t)
	assert.Equal(t, stderrDrainWait, testutil.WaitForTimer(t, deadline, drain))
	clock.Advance(stderrDrainWait).MustWait(deadline)
	awaitProcessDone(t, deadline, process)
	select {
	case afterDone := <-canceledAfterDone:
		assert.True(t, afterDone, "ending the stdout drain must publish the native exit before cancellation")
	case <-deadline.Done():
		t.Fatal("the process context did not end")
	}
	require.ErrorIs(t, process.Wait(), launch.ErrNoCandidate)
	var exit *exec.ExitError
	require.ErrorAs(t, process.ProcessExitError(), &exit)
	assert.Equal(t, 127, exit.ExitCode())
	assert.Equal(t, "profile diagnostic\n", process.Stderr())
	assert.False(t, process.Ready())
	assert.True(t, child.Runs(), "the worker cannot signal a writer it never verified")
	require.NoError(t, conn.CloseWrite())
}

func TestWrappedProcessStopBeforeReadinessClosesTheAttachedWriter(t *testing.T) {
	t.Parallel()
	writer := agenttest.NewInheritedWriter(t, "TestWrappedProcessWriter")
	prepared := prepareWrappedWriter(t, launch.ShellCommands("launch_test_definitely_missing"), writer.ShellCommand)
	clock := testutil.NewQuartzMock(t)
	stop := clock.Trap().NewTimer(ProcessStopGraceTimerTag)
	t.Cleanup(stop.Close)
	exits := make(observedFixtureExits)
	process, pipes := startWrappedProcessWithExitObserver(t, prepared, clock, func() {}, exits)
	process.DrainStderr(pipes.Stderr())
	go process.ReadLines(agent.NewStdoutScanner(pipes.Stdout()), func(line []byte) { t.Errorf("unexpected provider output %q", line) })
	_, child, root := writer.Accept(t)
	require.Equal(t, process.owner.PID(), root.PID)
	require.False(t, process.Ready())

	go process.Stop()
	deadline := testutil.DeadlineContext(t)
	assert.Equal(t, stderrDrainWait, testutil.WaitForTimer(t, deadline, stop))
	clock.Advance(stderrDrainWait).MustWait(deadline)
	awaitProcessDone(t, deadline, process)
	_ = process.Wait()
	assert.Equal(t, agent.MessageCompletionInterrupted, process.ProcessExitCompletion())
	exits.requireExit(t, child)
	assert.False(t, root.Runs())
	assert.False(t, process.Ready())
}

// The last read already holds the shell's bytes when the worker closes the
// descriptor. Keep those bytes in the caller's Read until the test releases it.
type stderrReadAtClose struct {
	io.ReadCloser
	closing  chan struct{}
	captured chan struct{}
	release  chan struct{}
	read     bool
}

func (r *stderrReadAtClose) Read(data []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	contents, err := io.ReadAll(r.ReadCloser)
	close(r.captured)
	<-r.release
	return copy(data, contents), err
}

func (r *stderrReadAtClose) Close() error {
	err := r.ReadCloser.Close()
	close(r.closing)
	return err
}

func TestWrappedProcessClassifiesTheFinalStderrReadAfterForcedClosure(t *testing.T) {
	t.Parallel()
	prepared := prepareWrappedWriter(t, launch.ShellCommands("launch_test_definitely_missing"), "printf 'profile diagnostic' >&2")
	clock := testutil.NewQuartzMock(t)
	wrappedDrain := clock.Trap().NewTimer(WrappedProcessDrainTimerTag)
	t.Cleanup(wrappedDrain.Close)
	stderrDrain := clock.Trap().NewTimer("stderr-drain")
	t.Cleanup(stderrDrain.Close)
	pipes, err := SetupLaunchPipes(prepared, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pipes.Close() })
	reader := &stderrReadAtClose{ReadCloser: pipes.Stderr(), closing: make(chan struct{}), captured: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(reader.release) }) })
	pipes.stderr = reader
	process := NewProcessFrom(ProcessConfig{pipes: pipes, Clock: clock, Ctx: t.Context(), Cancel: func() {},
		PreambleDelimiter: prepared.Delimiter, PreambleMetaPrefix: prepared.MetaPrefix})
	require.NoError(t, process.StartCmd())
	process.DrainStderr(reader)
	go process.ReadLines(agent.NewStdoutScanner(pipes.Stdout()), func([]byte) {})

	deadline := testutil.DeadlineContext(t)
	assert.Equal(t, stderrDrainWait, testutil.WaitForTimer(t, deadline, stderrDrain))
	select {
	case <-reader.captured:
	case <-deadline.Done():
		t.Fatal("the reader did not collect the shell's actual diagnostics")
	}
	clock.Advance(stderrDrainWait).MustWait(deadline)
	select {
	case <-reader.closing:
	case <-deadline.Done():
		t.Fatal("the drain timer did not close the stderr reader")
	}
	release.Do(func() { close(reader.release) })
	awaitProcessDone(t, deadline, &process)
	require.ErrorIs(t, process.Wait(), launch.ErrNoCandidate)
	var status *exec.ExitError
	require.ErrorAs(t, process.Wait(), &status)
	assert.Equal(t, 127, status.ExitCode())
	assert.Equal(t, "profile diagnostic\n", process.Stderr())
}

func TestProfileCommandPreambleAfterAnUnterminatedBanner(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	shell := filepath.Join(dir, "profile-shell")
	testutil.WriteExecutable(t, shell, "#!/bin/sh\nprintf 'profile message'\nexec /bin/sh \"$@\"\n")
	provider := filepath.Join(dir, "native-provider")
	testutil.WriteExecutable(t, provider, "#!/bin/sh\nprintf 'first 한글 line\r\nlast line without newline'\nprintf 'provider stderr\n' >&2\n")
	prepared, err := launch.Wrap(t.Context(), launch.WrapSpec{
		Shell: shell, Launch: launch.Spec{Program: provider},
		EnvGated: &launch.EnvGatedArgs{EnvVars: []string{"LEAPMUX_TEST_HIDE"}, MetaKey: "check"},
		SetEnv:   []string{"LEAPMUX_TEST_HIDE=1"},
	})
	require.NoError(t, err)
	process, pipes := startWrappedProcess(t, prepared, quartz.NewReal(), func() {})
	process.DrainStderr(pipes.Stderr())
	var output []string
	go process.ReadLines(agent.NewStdoutScanner(pipes.Stdout()), func(line []byte) { output = append(output, string(line)) })
	awaitProcessDone(t, testutil.DeadlineContext(t), process)
	require.NoError(t, process.Wait())
	assert.Equal(t, []string{"first 한글 line", "last line without newline"}, output)
	assert.Equal(t, "profile message", process.PreambleOutput())
	assert.Equal(t, "false", process.PreambleMetaValue("check"))
	assert.Equal(t, "provider stderr\n", process.Stderr())
	assert.True(t, process.Ready())
	requireClosedPipeEnd(t, pipes.Stdin().(*os.File))
	requireClosedPipeEnd(t, outputPipeFile(pipes.Stdout()))
	requireClosedPipeEnd(t, outputPipeFile(pipes.Stderr()))
}

func TestSetupLaunchPipesReleasesNushellInvocationFiles(t *testing.T) {
	nu, err := exec.LookPath("nu")
	if err != nil {
		t.Skip("Nushell is needed to check real invocation files")
	}
	for _, phase := range []string{"setup failure", "start failure", "normal exit", "stop"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			artifactRoot := t.TempDir()
			t.Setenv("TMPDIR", artifactRoot)
			t.Setenv("HOME", dir)
			t.Setenv("XDG_CONFIG_HOME", dir)
			provider := filepath.Join(dir, "launch-artifact-provider")
			body := "#!/bin/sh\nprintf 'live provider\n'\n"
			if phase == "stop" {
				body += "IFS= read -r release\nexit 0\n"
			}
			testutil.WriteExecutable(t, provider, body)
			prepared, err := launch.Wrap(t.Context(), launch.WrapSpec{
				Shell: nu, Launch: launch.ShellCommands("launch-artifact-provider"), SetEnv: []string{"PATH=" + dir},
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = prepared.Close() })
			entries, err := os.ReadDir(artifactRoot)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			artifact := filepath.Join(artifactRoot, entries[0].Name(), "launch-artifact-provider.nu")
			require.FileExists(t, artifact)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch phase {
			case "setup failure":
				prepared.Cmd.Stderr = io.Discard
				pipes, err := SetupLaunchPipes(prepared, cancel)
				require.Nil(t, pipes)
				require.ErrorContains(t, err, "stderr is already configured")
				assert.ErrorIs(t, ctx.Err(), context.Canceled)
				requireClosedPipeEnd(t, prepared.Cmd.Stdin.(*os.File))
				requireClosedPipeEnd(t, prepared.Cmd.Stdout.(*os.File))
			case "start failure":
				prepared.Cmd.Dir = filepath.Join(dir, "absent")
				pipes, err := SetupLaunchPipes(prepared, cancel)
				require.NoError(t, err)
				process := NewProcessFrom(ProcessConfig{pipes: pipes, Ctx: ctx, Cancel: cancel})
				require.ErrorIs(t, process.StartCmd(), fs.ErrNotExist)
				awaitProcessDone(t, testutil.DeadlineContext(t), &process)
				assert.Error(t, process.Wait())
				assert.ErrorIs(t, ctx.Err(), context.Canceled)
				requireClosedPipeEnd(t, outputPipeFile(pipes.Stdout()))
				requireClosedPipeEnd(t, outputPipeFile(pipes.Stderr()))
			default:
				process, pipes := startWrappedProcess(t, prepared, quartz.NewReal(), cancel)
				output := captureWrappedOutput(process, pipes)
				deadline := testutil.DeadlineContext(t)
				if phase == "stop" {
					select {
					case line := <-output:
						assert.Equal(t, "live provider", line)
					case <-deadline.Done():
						t.Fatal("the prepared provider did not start")
					}
					require.FileExists(t, artifact, "the owner must retain the invocation file while the provider runs")
					go process.Stop()
				}
				awaitProcessDone(t, deadline, process)
				require.NoError(t, process.Wait(), process.Stderr())
				assert.True(t, process.Ready())
				requireClosedPipeEnd(t, pipes.Stdin().(*os.File))
				requireClosedPipeEnd(t, outputPipeFile(pipes.Stdout()))
				requireClosedPipeEnd(t, outputPipeFile(pipes.Stderr()))
			}
			assert.NoDirExists(t, filepath.Dir(artifact))
		})
	}
}

func TestWrappedProcessCancelsAfterAGenuineFramingError(t *testing.T) {
	t.Parallel()
	prepared := prepareWrappedWriter(t, launch.ShellCommands("launch_test_wrapper"),
		"launch_test_wrapper() { printf '%200s\\n' too-long; IFS= read -r release; return 37; }")
	canceledAfterDone := make(chan bool, 2)
	var process *Process
	process, pipes := startWrappedProcess(t, prepared, quartz.NewReal(), func() {
		select {
		case <-process.ProcessDone():
			canceledAfterDone <- true
		default:
			canceledAfterDone <- false
			process.closeStdin()
		}
	})
	process.DrainStderr(pipes.Stderr())
	scanner := bufio.NewScanner(pipes.Stdout())
	scanner.Buffer(make([]byte, 128), 128)
	go process.ReadLines(scanner, func(line []byte) { t.Errorf("framed an invalid provider line %q", line) })

	deadline := testutil.DeadlineContext(t)
	select {
	case afterDone := <-canceledAfterDone:
		assert.False(t, afterDone, "the framing error must cancel a still-running provider")
	case <-deadline.Done():
		t.Fatal("the framing error did not cancel the process")
	}
	awaitProcessDone(t, deadline, process)
	assert.True(t, process.Ready())
	assert.Equal(t, agent.MessageCompletionError, process.ProcessExitCompletion())
	var exit *exec.ExitError
	require.ErrorAs(t, process.Wait(), &exit)
	assert.Equal(t, 37, exit.ExitCode())
	if cause := process.FinalizeStartupCause(io.ErrUnexpectedEOF); cause != io.ErrUnexpectedEOF {
		t.Errorf("the framing-triggered cancellation replaced the original cause with %v", cause)
	}
}

func TestWrappedProcessCapturesItsWriterBeforeDispatchingTheFirstLine(t *testing.T) {
	t.Parallel()
	for _, form := range []string{"candidate", "direct program"} {
		t.Run(form, func(t *testing.T) {
			t.Parallel()
			writer := agenttest.NewInheritedWriter(t, "TestWrappedProcessWriter")
			body := writer.ShellCommand + " &\nIFS= read -r release\nprintf 'first provider line\\n'\nIFS= read -r release\n"
			spec := launch.ShellCommands("launch_test_wrapper")
			profile := "launch_test_wrapper() {\n" + body + "return 37\n}"
			if form == "direct program" {
				program := filepath.Join(t.TempDir(), "native-provider")
				testutil.WriteExecutable(t, program, "#!/bin/sh\n"+body+"exit 37\n")
				spec = launch.Spec{Program: program}
				profile = ""
			}
			prepared := prepareWrappedWriter(t, spec, profile)
			clock := testutil.NewQuartzMock(t)
			drain := clock.Trap().NewTimer(WrappedProcessDrainTimerTag)
			t.Cleanup(drain.Close)
			exits := make(observedFixtureExits)
			process, pipes := startWrappedProcessWithExitObserver(t, prepared, clock, func() {}, exits)
			output := captureWrappedOutput(process, pipes)
			_, child, root := writer.Accept(t)
			require.Equal(t, process.owner.PID(), root.PID)
			require.NoError(t, process.WriteStdin([]byte("send output\n")))
			deadline := testutil.DeadlineContext(t)
			select {
			case line := <-output:
				require.Equal(t, "first provider line", line)
			case <-deadline.Done():
				t.Fatal("the output handler did not receive the first provider line")
			}
			require.True(t, root.Runs())
			require.True(t, child.Runs())
			require.NoError(t, process.WriteStdin([]byte("exit\n")))
			assert.Equal(t, stderrDrainWait, testutil.WaitForTimer(t, deadline, drain))
			clock.Advance(stderrDrainWait).MustWait(deadline)
			awaitProcessDone(t, deadline, process)
			var exit *exec.ExitError
			require.ErrorAs(t, process.Wait(), &exit)
			assert.Equal(t, 37, exit.ExitCode())
			assert.Equal(t, agent.MessageCompletionError, process.ProcessExitCompletion())
			exits.requireExit(t, child)
		})
	}
}

func TestWrappedProcessPublishesAnObservedExitBeforeCancelingAfterFramingFailure(t *testing.T) {
	t.Parallel()
	prepared := prepareWrappedWriter(t, launch.ShellCommands("launch_test_wrapper"),
		"launch_test_wrapper() { printf '%200s\\n' too-long; return 37; }")
	canceledAfterDone := make(chan bool, 2)
	var process *Process
	process, pipes := startWrappedProcess(t, prepared, testutil.NewQuartzMock(t), func() {
		select {
		case <-process.ProcessDone():
			canceledAfterDone <- true
		default:
			canceledAfterDone <- false
		}
	})
	process.DrainStderr(pipes.Stderr())
	var exit *exec.ExitError
	require.ErrorAs(t, pipes.waitForRecordedExit(), &exit)
	assert.Equal(t, 37, exit.ExitCode())
	scanner := bufio.NewScanner(pipes.Stdout())
	scanner.Buffer(make([]byte, 128), 128)
	go process.ReadLines(scanner, func(line []byte) { t.Errorf("framed an invalid provider line %q", line) })

	deadline := testutil.DeadlineContext(t)
	select {
	case afterDone := <-canceledAfterDone:
		assert.True(t, afterDone, "publish the observed native exit before canceling the process context")
	case <-deadline.Done():
		t.Fatal("the process context did not end")
	}
	awaitProcessDone(t, deadline, process)
	assert.True(t, process.Ready())
	assert.ErrorIs(t, process.Wait(), exit)
}

func TestProcessExitErrorPreservesCleanupFailureAfterNativeSuccess(t *testing.T) {
	t.Parallel()
	// The exit observer can return a typed status from its own work. It
	// must remain a cleanup cause and cannot become the provider's status.
	cleanupErr := exec.CommandContext(t.Context(), "/bin/sh", "-c", "exit 19").Run()
	var cleanupExit *exec.ExitError
	require.ErrorAs(t, cleanupErr, &cleanupExit)
	require.Equal(t, 19, cleanupExit.ExitCode())
	observer := fixtureExitObserver(func(_ context.Context, _ procutil.ProcessIdentity, nativeErr error) error {
		assert.NoError(t, nativeErr)
		return cleanupErr
	})
	prepared := prepareWrappedWriter(t, launch.ShellCommands("launch_test_wrapper"), "launch_test_wrapper() { return 0; }")
	pipes, err := SetupLaunchPipesWithExitObserver(prepared, nil, observer)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pipes.Close() })
	process := NewProcessFrom(ProcessConfig{pipes: pipes, Ctx: t.Context(), PreambleDelimiter: prepared.Delimiter})
	require.NoError(t, process.StartCmd())
	process.DrainStderr(pipes.Stderr())
	go process.ReadLines(agent.NewStdoutScanner(pipes.Stdout()), func([]byte) {})
	awaitProcessDone(t, testutil.DeadlineContext(t), &process)

	require.True(t, process.owner.ProcessState().Success())
	require.ErrorIs(t, process.Wait(), cleanupErr)
	exitErr := process.ProcessExitError()
	require.EqualError(t, exitErr, "agent process exited unexpectedly")
	assert.ErrorIs(t, exitErr, cleanupErr)
	assert.ErrorIs(t, process.FormatStartupError("initialize", exitErr), cleanupErr)
	if cause := process.FinalizeStartupCause(io.ErrUnexpectedEOF); cause != io.ErrUnexpectedEOF {
		t.Errorf("the observer's status replaced the original cause with %v", cause)
	}
}

func TestFinalizeStartupCauseKeepsEarlierFailuresAfterActualCleanup(t *testing.T) {
	t.Parallel()
	prepared := prepareWrappedWriter(t, launch.ShellCommands("launch_test_wrapper"),
		"launch_test_wrapper() { printf 'waiting for startup\\n'; IFS= read -r release; return 37; }")
	process, pipes := startWrappedProcess(t, prepared, quartz.NewReal(), func() {})
	output := captureWrappedOutput(process, pipes)
	select {
	case line := <-output:
		require.Equal(t, "waiting for startup", line)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the live provider did not report readiness")
	}
	require.True(t, process.Ready())
	require.False(t, process.ProcessExited())
	process.Stop()
	var exit *exec.ExitError
	require.ErrorAs(t, process.Wait(), &exit)
	require.Equal(t, 37, exit.ExitCode())
	require.Equal(t, agent.MessageCompletionInterrupted, process.ProcessExitCompletion())
	for _, cause := range []error{context.DeadlineExceeded, context.Canceled, errors.New("the provider refused to resume the session")} {
		if finalized := process.FinalizeStartupCause(cause); finalized != cause {
			t.Errorf("cleanup changed the original cause %v to %v", cause, finalized)
		}
		reported := process.FormatStartupError("initialize", cause)
		assert.ErrorIs(t, reported, cause)
		assert.False(t, errors.As(reported, &exit), "the cleanup-generated status must stay out of the startup error")
	}
}

func TestFinalizeStartupCausePreservesTheNativeStatusAfterServerOrTransportFailure(t *testing.T) {
	t.Parallel()
	prepared := prepareWrappedWriter(t, launch.ShellCommands("launch_test_wrapper"),
		"launch_test_wrapper() { printf 'native diagnostic\\n' >&2; return 37; }")
	process, pipes := startWrappedProcess(t, prepared, quartz.NewReal(), func() {})
	process.DrainStderr(pipes.Stderr())
	listen := NewListenWaiter(regexp.MustCompile("Listening on (.+)"))
	go process.ReadLines(agent.NewStdoutScanner(pipes.Stdout()), func(line []byte) { listen.Observe(line) })
	address, listenErr := listen.Wait(t.Context(), process.Clock(), process.ProcessDone(), 30*time.Second)
	require.Empty(t, address)
	require.ErrorIs(t, listenErr, ErrServerExited)
	process.Stop()

	for _, cause := range []error{listenErr, io.ErrUnexpectedEOF} {
		finalized := process.FinalizeStartupCause(cause)
		assert.ErrorIs(t, finalized, cause)
		var exit *exec.ExitError
		require.ErrorAs(t, finalized, &exit)
		assert.Equal(t, 37, exit.ExitCode())
		reported := process.FormatStartupError("server start", finalized)
		assert.ErrorIs(t, reported, cause)
		assert.ErrorIs(t, reported, exit)
		assert.EqualError(t, reported, "server start: "+cause.Error()+" (agent process exited with code 37); stderr: native diagnostic")
	}
}

func captureWrappedOutput(process *Process, pipes *ProcessPipes) <-chan string {
	process.DrainStderr(pipes.Stderr())
	lines := make(chan string, 1)
	go process.ReadLines(agent.NewStdoutScanner(pipes.Stdout()), func(line []byte) { lines <- string(line) })
	return lines
}
