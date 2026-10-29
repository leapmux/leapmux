package providerkit

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/procutil"
)

const exitWithStderrEnv = "LEAPMUX_TEST_EXIT_WITH_STDERR"

// TestHelperExitWithStderr is the child of the stderr drain tests. It states
// why it failed on stderr and exits, as an agent that cannot start does.
func TestHelperExitWithStderr(t *testing.T) {
	if os.Getenv(exitWithStderrEnv) != "1" {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "Error: Model not found: mock/missing")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(1)
}

// gatedReader holds each read until the test opens the gate, so the test
// decides when the drain reads relative to the native wait and pipe cleanup.
type gatedReader struct {
	gate   <-chan struct{}
	reader io.Reader
}

func (r gatedReader) Read(buffer []byte) (int, error) {
	<-r.gate
	return r.reader.Read(buffer)
}

// startExitWithStderr runs TestHelperExitWithStderr as a child on clock, with
// the drain that drain starts, and reads its stdout until the exit.
func startExitWithStderr(t *testing.T, clock quartz.Clock, drain func(*Process, io.Reader)) *Process {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestHelperExitWithStderr$")
	procutil.DetachFromTerminal(cmd)
	cmd.Env = append(os.Environ(), exitWithStderrEnv+"=1")
	pipes, err := SetupProcessPipes(cmd, cancel)
	require.NoError(t, err)
	process := NewProcessFrom(ProcessConfig{AgentID: "stderr-drain", ProviderName: "probe", Ctx: ctx, Cancel: cancel, pipes: pipes, Clock: clock})
	require.NoError(t, process.StartCmd())
	_, err = cmd.Stderr.(*os.File).Stat()
	require.ErrorIs(t, err, os.ErrClosed, "the parent must release its child stderr descriptor after start")
	t.Cleanup(func() { process.Stop(); _ = process.Wait() })
	drain(&process, pipes.Stderr())
	go process.ReadLines(bufio.NewScanner(pipes.Stdout()), func([]byte) {})
	require.NoError(t, process.stdin.Close())
	return &process
}

func awaitProcessDone(t *testing.T, ctx context.Context, process *Process) {
	t.Helper()
	select {
	case <-process.ProcessDone():
	case <-ctx.Done():
		require.FailNow(t, "the output reader never recorded the exit")
	}
}

// The stderr reader must retain diagnostics after the native wait, until the
// drain reads them. Closing it with the native wait loses a startup failure's
// reason when the reader has not run yet.
func TestFinishOutputRetainsStderrUntilTheDrainReadsIt(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	drainWait := clock.Trap().NewTimer("stderr-drain")
	t.Cleanup(drainWait.Close)
	gate := make(chan struct{})
	process := startExitWithStderr(t, clock, func(process *Process, stderr io.Reader) {
		process.DrainStderr(gatedReader{gate: gate, reader: stderr})
	})

	deadline := testutil.DeadlineContext(t)
	waiting := make(chan *quartz.Call, 1)
	go func() {
		if call, err := drainWait.Wait(deadline); err == nil {
			waiting <- call
		}
	}()
	// The drain reads only once the gate opens. The gate opens when the output
	// reader waits for the drain, or, without that wait, once cleanup has
	// already closed the pipe.
	select {
	case call := <-waiting:
		close(gate)
		call.MustRelease(deadline)
	case <-process.ProcessDone():
		close(gate)
	case <-deadline.Done():
		require.FailNow(t, "the child never exited")
	}
	awaitProcessDone(t, deadline, process)
	assert.Contains(t, process.Stderr(), "Model not found: mock/missing")
	assert.Contains(t, process.FormatStartupError("ready", process.ProcessExitError()).Error(), "stderr: Error: Model not found: mock/missing")
}

// A descendant that keeps stderr open holds the drain for ever, so the exit
// waits no longer than stderrDrainWait for it.
func TestFinishOutputWaitsForTheStderrDrainNoLongerThanItsLimit(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	drainWait, _ := testutil.NewTimerTraps(t, clock, "stderr-drain")
	held := make(chan struct{})
	process := startExitWithStderr(t, clock, func(process *Process, stderr io.Reader) {
		process.DrainStderr(gatedReader{gate: held, reader: stderr})
	})
	// The pipe is closed by then, so the released drain ends.
	t.Cleanup(func() { close(held) })
	deadline := testutil.DeadlineContext(t)
	assert.Equal(t, stderrDrainWait, testutil.WaitForTimer(t, deadline, drainWait))
	select {
	case <-process.ProcessDone():
		require.FailNow(t, "the exit did not wait for the drain")
	default:
	}
	clock.Advance(stderrDrainWait).MustWait(deadline)
	awaitProcessDone(t, deadline, process)
	_, err := process.pipes.Stderr().Read(make([]byte, 1))
	assert.ErrorIs(t, err, os.ErrClosed)
}

// A process without a stderr drain has nothing to wait for at its exit. The
// trap parks a drain timer until the test releases it, so an exit that armed
// one would never complete.
func TestFinishOutputWaitsForNoStderrDrainThatNeverStarted(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	testutil.NewTimerTraps(t, clock, "stderr-drain")
	process := startExitWithStderr(t, clock, func(*Process, io.Reader) {})
	awaitProcessDone(t, testutil.DeadlineContext(t), process)
	_, err := process.pipes.Stderr().Read(make([]byte, 1))
	assert.ErrorIs(t, err, os.ErrClosed)
}

func TestFinishOutputRecordsNaturalExitBeforeWaitingForStderr(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTimer("stderr-drain")
	t.Cleanup(trap.Close)
	gate := make(chan struct{})
	process := startExitWithStderr(t, clock, func(process *Process, stderr io.Reader) {
		process.DrainStderr(gatedReader{gate: gate, reader: stderr})
	})
	deadline := testutil.DeadlineContext(t)
	call, err := trap.Wait(deadline)
	require.NoError(t, err)
	state := process.owner.ProcessState()
	if assert.NotNil(t, state, "the native wait must finish before the optional drain wait") {
		assert.Equal(t, 1, state.ExitCode())
	}
	process.Mu.Lock()
	assert.True(t, process.ProcessExitedLocked())
	process.Mu.Unlock()
	process.NoteIntentionalStop()
	close(gate)
	call.MustRelease(deadline)
	awaitProcessDone(t, deadline, process)
	assert.Equal(t, agent.MessageCompletionError, process.ProcessExitCompletion())
	assert.Error(t, process.Wait())
	assert.Contains(t, process.Stderr(), "Model not found: mock/missing")
}

func TestFinishOutputKeepsAnIntentionalStopBeforeNativeExit(t *testing.T) {
	t.Parallel()
	process := startExitWithStderr(t, testutil.NewQuartzMock(t), func(process *Process, stderr io.Reader) {
		process.DrainStderr(stderr)
		process.NoteIntentionalStop()
	})
	awaitProcessDone(t, testutil.DeadlineContext(t), process)
	assert.Equal(t, agent.MessageCompletionInterrupted, process.ProcessExitCompletion())
}

func TestPreparedStderrSurvivesANativeWaitBeforeItsFirstRead(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestHelperExitWithStderr$")
	cmd.Env = append(os.Environ(), exitWithStderrEnv+"=1")
	pipes, err := SetupProcessPipes(cmd, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pipes.Close() })
	process := NewProcessFrom(ProcessConfig{pipes: pipes})
	require.NoError(t, process.StartCmd())
	require.NoError(t, process.stdin.Close())
	require.Error(t, pipes.owner.Wait())
	data, err := io.ReadAll(pipes.Stderr())
	require.NoError(t, err)
	assert.Equal(t, "Error: Model not found: mock/missing\n", string(data))
}

func TestStderrUsesTheProcessClockForAnExternalDrain(t *testing.T) {
	t.Parallel()
	for _, expires := range []bool{false, true} {
		t.Run(fmt.Sprint(expires), func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			trap, _ := testutil.NewTimerTraps(t, clock, "stderr-drain")
			done := make(chan struct{})
			var closeOnce sync.Once
			finish := func() { closeOnce.Do(func() { close(done) }) }
			t.Cleanup(finish)
			process := NewProcessFrom(ProcessConfig{Clock: clock, StderrDone: done})
			_, err := io.WriteString(process.StderrWriterForTest(), "captured diagnostic")
			require.NoError(t, err)
			result := make(chan string, 1)
			go func() { result <- process.Stderr() }()
			deadline := testutil.DeadlineContext(t)
			assert.Equal(t, stderrDrainWait, testutil.WaitForTimer(t, deadline, trap))
			if expires {
				clock.Advance(stderrDrainWait).MustWait(deadline)
			} else {
				finish()
			}
			select {
			case text := <-result:
				assert.Equal(t, "captured diagnostic", text)
			case <-deadline.Done():
				t.Fatal("stderr did not finish after its drain or timer ended")
			}
		})
	}
}

func TestStderrSkipsATimerForAnAlreadyCompletedDrain(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	testutil.NewTimerTraps(t, clock, "stderr-drain")
	process := NewProcessFrom(ProcessConfig{Clock: clock})
	process.SkipStderr()
	assert.Empty(t, process.Stderr())
}
