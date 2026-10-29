package providerkit

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
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
	os.Exit(1)
}

// gatedReader holds each read until the test opens the gate, so the test
// decides whether the drain reads before or after Wait closes the pipe.
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
	t.Cleanup(func() { process.Stop(); _ = process.Wait() })
	drain(&process, pipes.Stderr())
	go process.ReadLines(bufio.NewScanner(pipes.Stdout()), func([]byte) {})
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

// Wait closes the stderr pipe once the process exits, and a read after that
// close loses what the pipe still held. The drain must read the line of an
// exiting agent first, or its startup error loses the reason. That race made
// TestStartReportsAnOmpThatExitsBeforeItIsReady fail on a macOS runner.
func TestFinishOutputLetsTheStderrDrainReadBeforeWaitClosesThePipe(t *testing.T) {
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
	// reader waits for the drain, or, without that wait, once Wait has already
	// closed the pipe.
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
}
