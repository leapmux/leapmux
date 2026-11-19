//go:build unix

package providerkit

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

func TestWrappedProcessKeepsTheFinalLineWhileAnEarlierHandlerWaits(t *testing.T) {
	prepared := prepareWrappedWriter(t, launch.ShellCommands("launch_test_wrapper"),
		"launch_test_wrapper() { printf 'first line\\n'; IFS= read -r release; printf 'final line\\n'; return 0; }")
	clock := testutil.NewQuartzMock(t)
	started := clock.Trap().NewTimer(WrappedProcessDrainTimerTag)
	t.Cleanup(started.Close)
	stopped := clock.Trap().TimerStop(WrappedProcessDrainTimerTag)
	t.Cleanup(stopped.Close)
	process, pipes := startWrappedProcess(t, prepared, clock, func() {})
	process.DrainStderr(pipes.Stderr())
	deadline := testutil.DeadlineContext(t)
	first := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	output := make([]string, 0, 2)
	go process.ReadLines(process.NewOutputScanner(pipes.Stdout()), func(line []byte) {
		output = append(output, string(line))
		if len(output) == 1 {
			close(first)
			select {
			case <-release:
			case <-deadline.Done():
			}
		}
	})
	select {
	case <-first:
	case <-deadline.Done():
		t.Fatal("the first callback did not begin")
	}
	require.NoError(t, process.WriteStdin([]byte("write the final line and exit\n")))
	require.Equal(t, stderrDrainWait, testutil.WaitForTimer(t, deadline, started))
	testutil.AdvanceAndAwaitStop(t, deadline, clock, stderrDrainWait, stopped)
	close(release)
	awaitProcessDone(t, deadline, process)
	require.NoError(t, process.Wait())
	require.Equal(t, []string{"first line", "final line"}, output)
}

func TestWrappedProcessRejectsAnIncompleteLineOnForcedDrain(t *testing.T) {
	t.Parallel()
	writer := agenttest.NewInheritedWriter(t, "TestWrappedProcessWriter")
	prepared := prepareWrappedWriter(t, launch.ShellCommands("launch_test_wrapper"),
		writer.ShellCommand+" &\nIFS= read -r release\n"+
			"launch_test_wrapper() { printf '%s' '{\"final\":true}'; return 37; }")
	clock := testutil.NewQuartzMock(t)
	drain := clock.Trap().NewTimer(WrappedProcessDrainTimerTag)
	t.Cleanup(drain.Close)
	process, pipes := startWrappedProcess(t, prepared, clock, func() {})
	process.DrainStderr(pipes.Stderr())
	var lines []string
	go process.ReadLines(process.NewOutputScanner(pipes.Stdout()), func(line []byte) { lines = append(lines, string(line)) })
	connection, child, _ := writer.Accept(t)
	require.NoError(t, process.WriteStdin([]byte("exit\n")))

	deadline := testutil.DeadlineContext(t)
	require.Equal(t, stderrDrainWait, testutil.WaitForTimer(t, deadline, drain))
	clock.Advance(stderrDrainWait).MustWait(deadline)
	awaitProcessDone(t, deadline, process)
	var exit *exec.ExitError
	require.ErrorAs(t, process.Wait(), &exit)
	assert.Equal(t, 37, exit.ExitCode())
	assert.Empty(t, lines, "the reader must discard an incomplete record on pipe close")
	assert.True(t, child.Runs(), "the worker must leave an unverified writer alone")
	require.NoError(t, connection.CloseWrite())
}

func TestWrappedProcessRequiresAnExactCompleteReadinessRecord(t *testing.T) {
	for _, tc := range []struct {
		label, profile string
	}{
		{"missing newline", "printf '%s' \"$LEAPMUX_PRIVATE_READY\"; exit 0"},
		{"whitespace padding", "printf ' %s \\n' \"$LEAPMUX_PRIVATE_READY\"; exit 0"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			prepared := prepareWrappedWriter(t, launch.ShellCommands("launch_test_missing"), tc.profile)
			prepared.Cmd.Env = envutil.PinEnv(prepared.Cmd.Environ(), "LEAPMUX_PRIVATE_READY="+prepared.Delimiter)
			process, pipes := startWrappedProcess(t, prepared, realClock, func() {})
			process.DrainStderr(pipes.Stderr())
			go process.ReadLines(process.NewOutputScanner(pipes.Stdout()), func(line []byte) {
				t.Errorf("unexpected provider output %q", line)
			})
			awaitProcessDone(t, testutil.DeadlineContext(t), process)
			assert.False(t, process.Ready())
		})
	}
}
