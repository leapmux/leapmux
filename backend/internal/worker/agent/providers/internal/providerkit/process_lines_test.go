package providerkit

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"testing"
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
	stdin, stdout, stderr, err := SetupProcessPipes(cmd, cancel)
	require.NoError(t, err)
	process := NewProcess(agent.Options{AgentID: "server-lines"}, "probe", cmd, stdin, ctx, cancel, preambleDelimiter, "")
	require.NoError(t, process.StartCmd(cmd, cancel))
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
		address, err := waiter.Wait(t.Context(), process.ProcessDone(), 30*time.Second)
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
		stdin, stdout, stderr, err := SetupProcessPipes(cmd, cancel)
		require.NoError(t, err)
		process := NewProcess(agent.Options{AgentID: "server-lines"}, "probe", cmd, stdin, ctx, cancel, "", "")
		process.DiscardOutput()
		require.NoError(t, process.StartCmd(cmd, cancel))
		process.DrainStderr(stderr)
		go process.ReadLines(bufio.NewScanner(stdout), func(line []byte) { lines <- string(line) })
		<-process.ProcessDone()
		close(lines)
		assert.Empty(t, lines)
	})
}

// TestHelperServerLines is the child process of TestReadLines. It prints what a
// local agent server prints, then exits.
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
	_ = os.Stdout.Sync()
	os.Exit(0)
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
