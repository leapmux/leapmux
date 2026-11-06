package providerkit

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const serverLinesEnv = "LEAPMUX_TEST_SERVER_LINES"

// startLineServer runs this test binary as a child that prints the lines of
// TestHelperServerLines, and reads its stdout with ReadLines.
func startLineServer(t *testing.T, preambleDelimiter string, handle func([]byte)) *Process {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestHelperServerLines$")
	procutil.DetachFromTerminal(cmd)
	cmd.Env = append(os.Environ(), serverLinesEnv+"=1", "LEAPMUX_TEST_PREAMBLE="+preambleDelimiter)
	pipes, err := SetupProcessPipes(cmd, cancel)
	require.NoError(t, err)
	stdout, stderr := pipes.Stdout(), pipes.Stderr()
	process := NewProcess(agent.Options{AgentID: "server-lines"}, ProcessLaunch{ProviderName: "probe", ShutdownGrace: 0, PreambleDelimiter: preambleDelimiter, PreambleMetaPrefix: ""}, pipes, ctx, cancel)
	require.NoError(t, process.StartCmd())
	process.DrainStderr(stderr)
	go process.ReadLines(bufio.NewScanner(stdout), handle)
	t.Cleanup(func() { process.Stop(); _ = process.Wait() })
	return &process
}

func TestReadLines(t *testing.T) {
	t.Parallel()

	t.Run("hands every non-empty text line on, and records the exit", func(t *testing.T) {
		t.Parallel()
		lines := make(chan string, 16)
		process := startLineServer(t, "", func(line []byte) { lines <- string(line) })
		select {
		case <-process.ProcessDone():
		case <-time.After(30 * time.Second):
			t.Fatal("the reader never recorded the exit of the child")
		}
		require.NoError(t, process.Wait())
		close(lines)
		var got []string
		for line := range lines {
			got = append(got, line)
		}
		assert.Equal(t, []string{
			"starting the server",
			"mimocode server listening on http://127.0.0.1:4096",
			"{not json either",
		}, got)
		assert.Equal(t, agent.MessageCompletionError, process.ProcessExitCompletion(),
			"a child that exits by itself is not an intentional stop")
	})

	t.Run("skips the shell preamble before the delimiter", func(t *testing.T) {
		t.Parallel()
		lines := make(chan string, 16)
		process := startLineServer(t, "--LEAPMUX-DELIMITER--", func(line []byte) { lines <- string(line) })
		<-process.ProcessDone()
		close(lines)
		var got []string
		for line := range lines {
			got = append(got, line)
		}
		assert.Equal(t, []string{
			"mimocode server listening on http://127.0.0.1:4096",
			"{not json either",
		}, got)
		assert.Equal(t, "starting the server", process.PreambleOutput())
	})

	t.Run("feeds a listen waiter", func(t *testing.T) {
		t.Parallel()
		waiter := NewListenWaiter(regexp.MustCompile(`server listening on (\S+)`))
		process := startLineServer(t, "", func(line []byte) { waiter.Observe(line) })
		address, err := waiter.Wait(t.Context(), process.Clock(), process.ProcessDone(), 30*time.Second)
		require.NoError(t, err)
		assert.Equal(t, "http://127.0.0.1:4096", address)
	})

	t.Run("drops the lines after DiscardOutput", func(t *testing.T) {
		t.Parallel()
		lines := make(chan string, 16)
		executable, err := os.Executable()
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestHelperServerLines$")
		procutil.DetachFromTerminal(cmd)
		cmd.Env = append(os.Environ(), serverLinesEnv+"=1")
		pipes, err := SetupProcessPipes(cmd, cancel)
		require.NoError(t, err)
		stdout, stderr := pipes.Stdout(), pipes.Stderr()
		process := NewProcess(agent.Options{AgentID: "server-lines"}, ProcessLaunch{ProviderName: "probe", ShutdownGrace: 0, PreambleDelimiter: "", PreambleMetaPrefix: ""}, pipes, ctx, cancel)
		process.DiscardOutput()
		require.NoError(t, process.StartCmd())
		process.DrainStderr(stderr)
		go process.ReadLines(bufio.NewScanner(stdout), func(line []byte) { lines <- string(line) })
		<-process.ProcessDone()
		close(lines)
		assert.Empty(t, lines)
	})
}

// TestHelperServerLines prints the lines of a local agent server, then exits.
func TestHelperServerLines(t *testing.T) {
	if os.Getenv(serverLinesEnv) != "1" {
		return
	}
	fmt.Println("starting the server")
	if delimiter := os.Getenv("LEAPMUX_TEST_PREAMBLE"); delimiter != "" {
		fmt.Println(delimiter)
	}
	fmt.Println("mimocode server listening on http://127.0.0.1:4096")
	fmt.Println("")
	fmt.Println("{not json either")
	// Stdout is an unbuffered *os.File. Sync on a Windows pipe waits for
	// the reader, which a lifecycle test can deliberately block.
	os.Exit(0)
}

func TestHelperServerLinesExitsBeforeStdoutIsRead(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestHelperServerLines$")
	procutil.DetachFromTerminal(cmd)
	cmd.Env = append(os.Environ(), serverLinesEnv+"=1", "LEAPMUX_TEST_PREAMBLE=")
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
	t.Cleanup(func() { _ = writer.Close() })
	// Own the reader independently: StdoutPipe would close it in cmd.Wait
	// before this test can verify the output retained after the child exits.
	cmd.Stdout = writer
	require.NoError(t, cmd.Start())
	require.NoError(t, writer.Close())
	err = cmd.Wait()
	require.NoError(t, ctx.Err(), "the child must exit without cancellation")
	require.NoError(t, err, "the child must exit while its stdout remains unread")
	output, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "starting the server\nmimocode server listening on http://127.0.0.1:4096\n\n{not json either\n", string(output))
}

// Work that a provider binds to the process context -- an event stream, a
// reconnect loop, a poller -- must end when the process ends, whether the
// process exits by itself or crashes, and not only when Stop runs out of grace.
func TestProcessContextEndsAtTheExit(t *testing.T) {
	t.Parallel()
	process := startLineServer(t, "", func([]byte) {})
	<-process.ProcessDone()

	// finishOutput cancels the context just AFTER processDone closes, so a wait
	// that sees both reports the exit (see AwaitResponse). This goroutine can see
	// the closed channel before that cancel runs, so it waits for the context.
	select {
	case <-process.Context().Done():
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the process context outlived the process")
	}
}

// A response wait after the exit reports the exit, which states why no answer
// comes. The context is done by then too, and a random select between the two
// would report a bare "context canceled" instead.
func TestAwaitResponseAfterTheExitReportsTheExit(t *testing.T) {
	t.Parallel()
	process := startLineServer(t, "", func([]byte) {})
	<-process.ProcessDone()
	want := process.ProcessExitError()
	require.Error(t, want)

	for range 64 {
		_, err := process.AwaitResponse(make(chan json.RawMessage), "probe", 0)
		require.Equal(t, want.Error(), err.Error())
		_, err = process.AwaitResponse(make(chan json.RawMessage), "probe", time.Minute)
		require.Equal(t, want.Error(), err.Error())
	}
}

// A context that Stop ended while the process still runs states only that it
// ended: no exit is there to report yet. A response that already arrived wins
// over a wait that has no limit.
func TestAwaitResponseReportsTheContextWhileTheProcessRuns(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	process := NewProcessFrom(ProcessConfig{AgentID: "await", Ctx: ctx, Cancel: cancel})

	answered := make(chan json.RawMessage, 1)
	answered <- json.RawMessage(`{"ok":true}`)
	raw, err := process.AwaitResponse(answered, "probe", 0)
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(raw))

	cancel()
	for _, timeout := range []time.Duration{0, time.Minute} {
		_, err := process.AwaitResponse(make(chan json.RawMessage), "probe", timeout)
		require.ErrorIs(t, err, context.Canceled, "timeout %s", timeout)
	}
}

// The timeout runs on the process clock, so a test that states a mock ends the
// wait, and nothing ends it one nanosecond before the limit.
func TestAwaitResponseTimesOutOnTheProcessClock(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTimer(AwaitResponseTimerTag, "probe")
	defer trap.Close()
	process := NewProcessFrom(ProcessConfig{AgentID: "await", Ctx: t.Context(), Cancel: func() {}, Clock: clock})

	result := make(chan error, 1)
	go func() {
		_, err := process.AwaitResponse(make(chan json.RawMessage), "probe", time.Minute)
		result <- err
	}()
	trap.MustWait(ctx).MustRelease(ctx)

	clock.Advance(time.Minute - time.Nanosecond).MustWait(ctx)
	select {
	case err := <-result:
		t.Fatalf("the wait ended before its limit: %v", err)
	default:
	}
	clock.Advance(time.Nanosecond).MustWait(ctx)
	require.EqualError(t, <-result, "timeout waiting for probe response")
}

// A process that states no clock takes the real clock, so a process that a test
// builds as a zero value never meets a nil clock. A stated clock is the one that
// the process returns.
func TestProcessClockIsTheStatedClockOrTheRealOne(t *testing.T) {
	t.Parallel()
	assert.NotNil(t, (&Process{}).Clock())
	unstated := NewProcessFrom(ProcessConfig{})
	assert.NotNil(t, unstated.Clock())

	clock := testutil.NewQuartzMock(t)
	stated := NewProcessFrom(ProcessConfig{Clock: clock})
	assert.Same(t, clock, stated.Clock())
}

// Wait and every stop path block on processDone, and the teardown after them
// reads what a line handler writes. So processDone must close only after the
// last handler returned, which spares each provider a join of its own reader.
func TestProcessCompletionFollowsTheLastHandledLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(process *Process)
	}{
		{name: "Wait", end: func(process *Process) { _ = process.Wait() }},
		{name: "Stop", end: func(process *Process) { process.Stop() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A syscall inside synctest prevents its fake clock from advancing.
			// Keep the failure deadline outside the bubble so a stuck child
			// fails this test instead of reaching the package timeout.
			deadline := time.NewTimer(30 * time.Second)
			defer deadline.Stop()
			synctest.Test(t, func(t *testing.T) {
				entered := make(chan struct{})
				announce := sync.OnceFunc(func() { close(entered) })
				gate := make(chan struct{})
				release := sync.OnceFunc(func() { close(gate) })
				process := startLineServer(t, "", func([]byte) {
					announce()
					<-gate
				})
				// Runs before the cleanup of startLineServer, whose Stop waits for
				// the same handler.
				t.Cleanup(release)
				select {
				case <-entered:
				case <-deadline.C:
					t.Fatal("the handler never received a line")
				}

				exited := make(chan error, 1)
				go func() { exited <- process.owner.Wait() }()
				select {
				case err := <-exited:
					require.NoError(t, err)
				case <-deadline.C:
					t.Fatal("the child never exited while its output handler was blocked")
				}
				select {
				case <-process.stderrDone:
				case <-deadline.C:
					t.Fatal("the stderr reader never finished after the child exited")
				}

				ended := make(chan struct{})
				go func() {
					defer close(ended)
					tc.end(process)
				}()
				synctest.Wait()
				select {
				case <-ended:
					t.Fatal("the process completed while a handler still ran")
				default:
				}

				release()
				select {
				case <-ended:
				case <-deadline.C:
					t.Fatal("the process never completed after the handler returned")
				}
			})
		})
	}
}
