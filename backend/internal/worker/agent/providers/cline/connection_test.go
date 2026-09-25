package cline

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir/agentdirtest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Each record states the core of Cline 3.0.64, so each case checks the
// protocol range alone. TestCheckProtocolRequiresTheCoreOfCline3064 checks the
// core.
func TestCheckProtocol(t *testing.T) {
	t.Parallel()
	const core = "0.0.85"
	for _, tc := range []struct {
		name   string
		record discoveryRecord
		ok     bool
	}{
		{"v1 only", discoveryRecord{ProtocolVersion: "v1", MinClientProtocolVersion: "v1", MaxClientProtocolVersion: "v1", CoreVersion: core}, true},
		{"a range around v1", discoveryRecord{ProtocolVersion: "v3", MinClientProtocolVersion: "v1", MaxClientProtocolVersion: "v3", CoreVersion: core}, true},
		{"no limits take the daemon's own version", discoveryRecord{ProtocolVersion: "v1", CoreVersion: core}, true},
		{"a newer daemon only", discoveryRecord{ProtocolVersion: "v2", MinClientProtocolVersion: "v2", MaxClientProtocolVersion: "v2", CoreVersion: core}, false},
		{"no limits and a newer daemon", discoveryRecord{ProtocolVersion: "v2", CoreVersion: core}, false},
		{"no version at all", discoveryRecord{CoreVersion: core}, false},
		{"a version that is not vN", discoveryRecord{ProtocolVersion: "1.0", CoreVersion: core}, false},
		{"a version in capitals", discoveryRecord{ProtocolVersion: "V1", CoreVersion: core}, false},
		{"a version with spaces around it", discoveryRecord{ProtocolVersion: " v1 ", MinClientProtocolVersion: " v1", MaxClientProtocolVersion: "v1 ", CoreVersion: core}, true},
		{"a limit that is not vN takes the daemon's own version", discoveryRecord{ProtocolVersion: "v1", MinClientProtocolVersion: "one", MaxClientProtocolVersion: "latest", CoreVersion: core}, true},
		{"an older daemon only", discoveryRecord{ProtocolVersion: "v1", MinClientProtocolVersion: "v0", MaxClientProtocolVersion: "v0", CoreVersion: core}, false},
		{"a range whose limits are reversed", discoveryRecord{ProtocolVersion: "v1", MinClientProtocolVersion: "v2", MaxClientProtocolVersion: "v0", CoreVersion: core}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkProtocol(tc.record)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, errProtocolMismatch)
			assert.Contains(t, err.Error(), "Cline 3.0.64", "the error says what to install")
		})
	}
}

func TestDaemonArgsAndEnv(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"--cwd", "/w", "--host", daemonListen.host, "--port", "4242", "--pathname", "/hub", "--no-connectors"}, daemonArgs("/w", 4242))
	assert.NotContains(t, daemonArgs("/w", 1), "--cline-hub-daemon", "cline doctor fix kills a process with that flag")
	env := daemonSetEnv("/d", 4242)
	assert.Equal(t, []string{
		envRunAsHubDaemon + "=1",
		envHubDiscoveryPath + "=" + filepath.Join("/d", discoveryFileName),
		envHubPort + "=4242",
		envTasksDBPath + "=" + filepath.Join("/d", tasksDBFileName),
		envSessionBackendMode + "=" + sessionBackendLocal,
		envNoAutoUpdate + "=1",
	}, env)
}

func TestFreePortIsALoopbackPort(t *testing.T) {
	t.Parallel()
	port, err := freePort()
	require.NoError(t, err)
	assert.Positive(t, port)
	// The port is free at the daemon's own address, which the daemon binds a
	// moment later.
	listener, err := net.Listen("tcp", net.JoinHostPort(daemonListen.address, strconv.Itoa(port)))
	require.NoError(t, err, "the daemon can bind the port on its loopback address")
	require.NoError(t, listener.Close())
}

func TestReadDiscoveryNeedsACompleteRecord(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, discoveryFileName)
	_, ok := readDiscovery(path)
	assert.False(t, ok, "an absent record")
	require.NoError(t, os.WriteFile(path, []byte(`{"authToken":"t"`), 0o600))
	_, ok = readDiscovery(path)
	assert.False(t, ok, "half a record")
	require.NoError(t, os.WriteFile(path, []byte(`{"authToken":"t","url":"ws://127.0.0.1:1/hub"}`), 0o600))
	_, ok = readDiscovery(path)
	assert.False(t, ok, "a record with no process id")
	for name, record := range map[string]discoveryRecord{
		"a negative process id": {AuthToken: "t", URL: "ws://127.0.0.1:1/hub", PID: -1},
		"no token":              {URL: "ws://127.0.0.1:1/hub", PID: 7},
		"no address":            {AuthToken: "t", PID: 7},
	} {
		writeRecord(t, path, record)
		_, ok = readDiscovery(path)
		assert.False(t, ok, name)
	}
	require.NoError(t, os.WriteFile(path, []byte(`not json`), 0o600))
	_, ok = readDiscovery(path)
	assert.False(t, ok, "a record that is not JSON")
	writeRecord(t, path, discoveryRecord{AuthToken: "t", URL: "ws://127.0.0.1:1/hub", PID: 7})
	record, ok := readDiscovery(path)
	require.True(t, ok)
	assert.Equal(t, 7, record.PID)
}

func writeRecord(t *testing.T, path string, record discoveryRecord) {
	t.Helper()
	data, err := json.Marshal(record)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func TestWaitForDiscoveryReturnsTheRecord(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), discoveryFileName)
	clock := quartz.NewMock(t)
	trap := clock.Trap().NewTicker("cline", "discovery-poll")
	defer trap.Close()
	done := make(chan struct{})
	result := make(chan discoveryRecord, 1)
	go func() {
		record, err := waitForDiscovery(context.Background(), path, done, time.Minute, clock)
		assert.NoError(t, err)
		result <- record
	}()
	ctx := testutil.DeadlineContext(t)
	trap.MustWait(ctx).MustRelease(ctx)
	writeRecord(t, path, discoveryRecord{AuthToken: "t", URL: "ws://127.0.0.1:1/hub", PID: 9})
	clock.Advance(discoveryPollInterval).MustWait(ctx)
	select {
	case record := <-result:
		assert.Equal(t, 9, record.PID)
	case <-ctx.Done():
		t.Fatal("the record was not read")
	}
}

func TestWaitForDiscoveryReportsAnEarlyExit(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	close(done)
	_, err := waitForDiscovery(context.Background(), filepath.Join(t.TempDir(), discoveryFileName), done, time.Minute, quartz.NewMock(t))
	require.ErrorIs(t, err, errDaemonExited)
}

func TestWaitForDiscoveryGivesUpAtTheDeadline(t *testing.T) {
	t.Parallel()
	clock := quartz.NewMock(t)
	timerTrap := clock.Trap().NewTimer("cline", "discovery-deadline")
	defer timerTrap.Close()
	tickerTrap := clock.Trap().NewTicker("cline", "discovery-poll")
	defer tickerTrap.Close()
	errs := make(chan error, 1)
	// The deadline and the first poll come due together, and whichever the
	// wait reads first, the next read finds the deadline.
	go func() {
		_, err := waitForDiscovery(context.Background(), filepath.Join(t.TempDir(), discoveryFileName), make(chan struct{}), discoveryPollInterval, clock)
		errs <- err
	}()
	ctx := testutil.DeadlineContext(t)
	timerTrap.MustWait(ctx).MustRelease(ctx)
	tickerTrap.MustWait(ctx).MustRelease(ctx)
	clock.Advance(discoveryPollInterval).MustWait(ctx)
	select {
	case err := <-errs:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "did not publish its address")
	case <-ctx.Done():
		t.Fatal("the wait did not end")
	}
}

// The caller of the start ends the wait, as it ends the start.
func TestWaitForDiscoveryStopsWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// No timer of the mock clock fires, so only the context can end the wait.
	_, err := waitForDiscovery(ctx, filepath.Join(t.TempDir(), discoveryFileName), make(chan struct{}), time.Minute, quartz.NewMock(t))
	require.ErrorIs(t, err, context.Canceled)
}

func TestHubEndpointTakesALoopbackAddressAlone(t *testing.T) {
	t.Parallel()
	endpoint, path, err := hubEndpoint(discoveryRecord{AuthToken: "t", URL: "ws://127.0.0.1:4242/hub"})
	require.NoError(t, err)
	defer endpoint.Close()
	assert.Equal(t, "/hub", path)
	_, _, err = hubEndpoint(discoveryRecord{AuthToken: "t", URL: "ws://example.com:4242/hub"})
	require.Error(t, err, "the worker never sends its token off the machine")
	endpoint, path, err = hubEndpoint(discoveryRecord{AuthToken: "t", URL: "ws://127.0.0.1:4242"})
	require.NoError(t, err)
	defer endpoint.Close()
	assert.Equal(t, hubPathname, path)
	for _, url := range []string{"", "wss://127.0.0.1:4242/hub", "ws://127.0.0.1/hub", "ws://user@127.0.0.1:4242/hub"} {
		_, _, err = hubEndpoint(discoveryRecord{AuthToken: "t", URL: url})
		assert.Error(t, err, "%q is no address that the worker sends its token to", url)
	}
}

// The helper processes of the daemon tests run this test binary.
const (
	// clineWaitEnv makes the helper wait until its stdin closes, and then exit
	// with status 0. It stands in for the process of a daemon.
	clineWaitEnv = "LEAPMUX_TEST_CLINE_WAIT"
	// clineOrphanBaseEnv makes the helper create a Cline agent directory under
	// the base that it states, print the directory's path, and exit without
	// removing it, as a worker that crashed leaves it.
	clineOrphanBaseEnv = "LEAPMUX_TEST_CLINE_ORPHAN_BASE"
)

// TestHelperProcessClineDaemon is a helper process of the daemon tests. It
// does nothing in the test process itself.
func TestHelperProcessClineDaemon(t *testing.T) {
	switch {
	case os.Getenv(clineWaitEnv) == "1":
		fmt.Println("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case os.Getenv(clineOrphanBaseEnv) != "":
		spec := agentdir.Spec{Prefix: agentDirSpec().Prefix}
		dirs, err := agentdir.Start(context.Background(), agentdir.Config{Specs: []agentdir.Spec{spec}, Bases: []string{os.Getenv(clineOrphanBaseEnv)}})
		if err == nil {
			var dir *agentdir.Dir
			if dir, err = dirs.New(context.Background(), spec); err == nil {
				fmt.Println(dir.Path())
				os.Exit(0)
			}
		}
		fmt.Println("failed:", err)
		os.Exit(1)
	}
}

// standInDaemon is a helper process that stands in for the process of a
// daemon. A goroutine reaps it the moment it exits, so an ended one no longer
// holds its pid.
type standInDaemon struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	exited chan struct{}
}

func startStandInDaemon(t *testing.T) *standInDaemon {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessClineDaemon$")
	cmd.Env = append(os.Environ(), clineHelperEnv+"=1", clineWaitEnv+"=1")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready", strings.TrimSpace(line))
	d := &standInDaemon{cmd: cmd, stdin: stdin, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(d.exited)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		<-d.exited
	})
	return d
}

// identity returns the identity of the running process.
func (d *standInDaemon) identity(t *testing.T) providerkit.ProcessIdentity {
	t.Helper()
	identity, ok := providerkit.IdentifyProcess(d.cmd.Process.Pid)
	require.True(t, ok)
	return identity
}

// waitExit waits until the process exited and was reaped.
func (d *standInDaemon) waitExit(t *testing.T) {
	t.Helper()
	select {
	case <-d.exited:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the stand-in daemon did not exit")
	}
}

// end closes the stdin of the process, which ends it with status 0, and waits
// until it was reaped.
func (d *standInDaemon) end(t *testing.T) {
	t.Helper()
	_ = d.stdin.Close()
	d.waitExit(t)
}

// endedByItself ends a process that still runs and reports whether it then
// exited with status 0. A process that something killed first ended with
// another status.
func (d *standInDaemon) endedByItself(t *testing.T) bool {
	t.Helper()
	d.end(t)
	return d.cmd.ProcessState.Success()
}

// fakeDaemon is a daemon as the worker sees it: a fake hub in this process,
// whose `/status` states the pid of a stand-in process, and the discovery
// record of both.
type fakeDaemon struct {
	hub    *fakeHub
	server *httptest.Server
	proc   *standInDaemon
	record discoveryRecord
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	hub, server := newFakeHubServer(t)
	proc := startStandInDaemon(t)
	hub.mu.Lock()
	hub.statusPID = proc.cmd.Process.Pid
	hub.mu.Unlock()
	record := fakeRecord(server.URL)
	record.PID = proc.cmd.Process.Pid
	return &fakeDaemon{hub: hub, server: server, proc: proc, record: record}
}

// staleDir returns a directory that holds the daemon's record, as the agent
// directory of an ended worker holds it.
func (d *fakeDaemon) staleDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeRecord(t, filepath.Join(dir, discoveryFileName), d.record)
	return dir
}

// awaitExitTraps catches the deadline and the poll ticker of awaitDaemonExit.
func awaitExitTraps(t *testing.T, clock *quartz.Mock) (deadline, poll *quartz.Trap) {
	t.Helper()
	deadline = clock.Trap().NewTimer("cline", "daemon-exit")
	poll = clock.Trap().NewTicker("cline", "daemon-exit-poll")
	t.Cleanup(func() {
		deadline.Close()
		poll.Close()
	})
	return deadline, poll
}

// armedWait waits until awaitDaemonExit armed its deadline and its ticker, and
// checks the deadline.
func armedWait(t *testing.T, ctx context.Context, deadline, poll *quartz.Trap) {
	t.Helper()
	assert.Equal(t, daemonExitWait, testutil.WaitForTimer(t, ctx, deadline))
	poll.MustWait(ctx).MustRelease(ctx)
}

// advancePoll moves the clock to the next poll of awaitDaemonExit.
func advancePoll(t *testing.T, ctx context.Context, clock *quartz.Mock) {
	t.Helper()
	d, w := clock.AdvanceNext()
	w.MustWait(ctx)
	require.Equal(t, daemonExitPoll, d)
}

// advanceToDeadline moves the clock past each poll until the deadline of
// awaitDaemonExit fires.
func advanceToDeadline(t *testing.T, ctx context.Context, clock *quartz.Mock) {
	t.Helper()
	for elapsed := time.Duration(0); elapsed < daemonExitWait; {
		d, w := clock.AdvanceNext()
		w.MustWait(ctx)
		elapsed += d
	}
}

// result waits for the error that done carries.
func result(t *testing.T, ctx context.Context, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		t.Fatal("the call did not return")
		return nil
	}
}

func TestStopDaemonAtSendsTheShutdownWithTheToken(t *testing.T) {
	t.Parallel()
	hub, server := newFakeHubServer(t)
	daemon := stopDaemonAt(fakeRecord(server.URL))
	assert.Equal(t, 1, hub.shutdownCount())
	assert.True(t, daemon.IsZero(), "a record with no pid verifies no process")
	record := fakeRecord(server.URL)
	record.AuthToken = "wrong"
	daemon = stopDaemonAt(record)
	assert.True(t, daemon.IsZero())
	hub.mu.Lock()
	defer hub.mu.Unlock()
	assert.Equal(t, 1, hub.badShutdownTokens)
}

// C-L1: the worker verifies the daemon's process while the daemon still
// answers, before the shutdown closes its listener.
func TestStopDaemonAtVerifiesTheDaemonBeforeItsShutdown(t *testing.T) {
	t.Parallel()
	d := newFakeDaemon(t)
	daemon := stopDaemonAt(d.record)
	assert.Equal(t, d.proc.identity(t), daemon)
	assert.Equal(t, []string{"/status", "/shutdown"}, d.hub.requestRoutes())
}

func TestStopDaemonAtVerifiesNoProcessThatTheHubDoesNotState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		adjust func(t *testing.T, d *fakeDaemon)
		status bool
	}{
		{"the hub states another pid", statesStatus(os.Getppid(), ""), true},
		{"the hub states another hub", statesStatus(0, "hub_other"), true},
		{"the record states no hub", func(_ *testing.T, d *fakeDaemon) { d.record.HubID = "" }, false},
		{"the record's pid runs no process", func(t *testing.T, d *fakeDaemon) { d.record.PID = endedProcessPID(t) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDaemon(t)
			tc.adjust(t, d)
			assert.True(t, stopDaemonAt(d.record).IsZero())
			assert.Equal(t, 1, d.hub.shutdownCount(), "the shutdown goes out, since the token reaches the daemon alone")
			assert.Equal(t, tc.status, slices.Contains(d.hub.requestRoutes(), "/status"))
			assert.True(t, d.proc.endedByItself(t))
		})
	}
	t.Run("the hub does not answer", func(t *testing.T) {
		t.Parallel()
		d := newFakeDaemon(t)
		d.server.Close()
		assert.True(t, stopDaemonAt(d.record).IsZero())
		assert.True(t, d.proc.endedByItself(t))
	})
}

// statesStatus makes the fake hub's `/status` state pid and hubID. A zero
// value keeps what the hub states.
func statesStatus(pid int, hubID string) func(*testing.T, *fakeDaemon) {
	return func(_ *testing.T, d *fakeDaemon) {
		d.hub.mu.Lock()
		defer d.hub.mu.Unlock()
		if pid != 0 {
			d.hub.statusPID = pid
		}
		if hubID != "" {
			d.hub.statusHubID = hubID
		}
	}
}

// endedProcessPID returns the pid of a process that ended and was reaped.
func endedProcessPID(t *testing.T) int {
	t.Helper()
	proc := startStandInDaemon(t)
	proc.end(t)
	return proc.cmd.Process.Pid
}

func TestAwaitDaemonExitWaitsForTheVerifiedDaemon(t *testing.T) {
	t.Parallel()
	proc := startStandInDaemon(t)
	clock := testutil.NewQuartzMock(t)
	deadline, poll := awaitExitTraps(t, clock)
	ctx := testutil.DeadlineContext(t)
	done := make(chan error, 1)
	go func() { done <- awaitDaemonExit(context.Background(), proc.identity(t), clock) }()
	armedWait(t, ctx, deadline, poll)
	advancePoll(t, ctx, clock)
	select {
	case <-done:
		t.Fatal("the wait ended while the daemon ran")
	default:
	}
	proc.end(t)
	advancePoll(t, ctx, clock)
	require.NoError(t, result(t, ctx, done))
	assert.True(t, proc.cmd.ProcessState.Success(), "the daemon ended by itself")
}

func TestAwaitDaemonExitKillsTheVerifiedDaemonAtTheDeadline(t *testing.T) {
	t.Parallel()
	proc := startStandInDaemon(t)
	clock := testutil.NewQuartzMock(t)
	deadline, poll := awaitExitTraps(t, clock)
	ctx := testutil.DeadlineContext(t)
	done := make(chan error, 1)
	go func() { done <- awaitDaemonExit(context.Background(), proc.identity(t), clock) }()
	armedWait(t, ctx, deadline, poll)
	advanceToDeadline(t, ctx, clock)
	require.NoError(t, result(t, ctx, done))
	proc.waitExit(t)
	assert.False(t, proc.cmd.ProcessState.Success(), "the wait killed the daemon that it verified")
}

func TestAwaitDaemonExitKillsTheVerifiedDaemonWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	proc := startStandInDaemon(t)
	clock := testutil.NewQuartzMock(t)
	deadline, poll := awaitExitTraps(t, clock)
	ctx := testutil.DeadlineContext(t)
	waitCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- awaitDaemonExit(waitCtx, proc.identity(t), clock) }()
	armedWait(t, ctx, deadline, poll)
	cancel()
	require.NoError(t, result(t, ctx, done))
	proc.waitExit(t)
	assert.False(t, proc.cmd.ProcessState.Success())
}

// C-L1: a pid whose process runs, but with another start time than the one
// that the worker verified, belongs to another process by now. The wait
// neither waits for it nor kills it.
func TestAwaitDaemonExitKillsNoUnverifiedProcess(t *testing.T) {
	t.Parallel()
	proc := startStandInDaemon(t)
	current := proc.identity(t)
	ctx := testutil.DeadlineContext(t)
	for _, daemon := range []providerkit.ProcessIdentity{
		{PID: current.PID, StartTime: current.StartTime - 1},
		{},
	} {
		done := make(chan error, 1)
		// No timer of the mock clock ever fires, so a wait that armed one
		// would not return.
		go func() { done <- awaitDaemonExit(context.Background(), daemon, testutil.NewQuartzMock(t)) }()
		require.NoError(t, result(t, ctx, done))
	}
	assert.True(t, proc.endedByItself(t), "nothing killed the process that holds the pid")
}

// C-M5: the hook of a stale directory returns only once the daemon that the
// directory records ended, so the sweep removes the directory, and a new
// daemon opens the session, only after that.
func TestTheStaleHookWaitsForTheDaemonToEnd(t *testing.T) {
	t.Parallel()
	d := newFakeDaemon(t)
	dir := d.staleDir(t)
	clock := testutil.NewQuartzMock(t)
	deadline, poll := awaitExitTraps(t, clock)
	ctx := testutil.DeadlineContext(t)
	done := make(chan error, 1)
	go func() { done <- staleDaemonStopper{clock: clock}.stop(context.Background(), dir) }()
	armedWait(t, ctx, deadline, poll)
	assert.Equal(t, []string{"/status", "/shutdown"}, d.hub.requestRoutes(), "the hook verifies the daemon, then asks it to shut down")
	advancePoll(t, ctx, clock)
	select {
	case <-done:
		t.Fatal("the hook returned while the daemon ran")
	default:
	}
	d.proc.end(t)
	advancePoll(t, ctx, clock)
	require.NoError(t, result(t, ctx, done))
	assert.True(t, d.proc.cmd.ProcessState.Success(), "the daemon ended by itself after its shutdown")
}

func TestTheStaleHookKillsAVerifiedDaemonThatDoesNotEnd(t *testing.T) {
	t.Parallel()
	d := newFakeDaemon(t)
	dir := d.staleDir(t)
	clock := testutil.NewQuartzMock(t)
	deadline, poll := awaitExitTraps(t, clock)
	ctx := testutil.DeadlineContext(t)
	done := make(chan error, 1)
	go func() { done <- staleDaemonStopper{clock: clock}.stop(context.Background(), dir) }()
	armedWait(t, ctx, deadline, poll)
	advanceToDeadline(t, ctx, clock)
	require.NoError(t, result(t, ctx, done))
	d.proc.waitExit(t)
	assert.False(t, d.proc.cmd.ProcessState.Success(), "the hook killed the daemon that it verified")
}

// The sweep ends the hook's context at its deadline, and the hook kills the
// daemon that it verified then.
func TestTheStaleHookKillsAVerifiedDaemonWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	d := newFakeDaemon(t)
	dir := d.staleDir(t)
	clock := testutil.NewQuartzMock(t)
	deadline, poll := awaitExitTraps(t, clock)
	ctx := testutil.DeadlineContext(t)
	hookCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- staleDaemonStopper{clock: clock}.stop(hookCtx, dir) }()
	armedWait(t, ctx, deadline, poll)
	cancel()
	require.NoError(t, result(t, ctx, done))
	d.proc.waitExit(t)
	assert.False(t, d.proc.cmd.ProcessState.Success())
}

// C-L1: the pid of a stale record can belong to any process by now. The hook
// kills and waits for none that the daemon's own answer did not verify.
func TestTheStaleHookNeverKillsAnUnverifiedPid(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		adjust func(t *testing.T, d *fakeDaemon)
	}{
		{"the hub states another pid", statesStatus(os.Getppid(), "")},
		{"the hub states another hub", statesStatus(0, "hub_other")},
		{"the hub does not answer", func(_ *testing.T, d *fakeDaemon) { d.server.Close() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDaemon(t)
			tc.adjust(t, d)
			dir := d.staleDir(t)
			ctx := testutil.DeadlineContext(t)
			done := make(chan error, 1)
			// No timer of the mock clock ever fires, so a hook that waited would
			// not return.
			go func() { done <- staleDaemonStopper{clock: testutil.NewQuartzMock(t)}.stop(context.Background(), dir) }()
			require.NoError(t, result(t, ctx, done))
			assert.True(t, d.proc.endedByItself(t), "nothing killed the process that holds the pid")
		})
	}
}

// A directory whose record is absent or not complete states no daemon that the
// hook could end: the daemon died before it published the record, or it never
// started. The hook contacts nothing, not even the address that a half record
// states.
func TestTheStaleHookIgnoresADirectoryWithNoRecord(t *testing.T) {
	t.Parallel()
	hub, server := newFakeHubServer(t)
	clock := testutil.NewQuartzMock(t)
	require.NoError(t, staleDaemonStopper{clock: clock}.stop(context.Background(), t.TempDir()), "no record")

	half := fakeRecord(server.URL)
	half.PID = 0
	dir := t.TempDir()
	writeRecord(t, filepath.Join(dir, discoveryFileName), half)
	require.NoError(t, staleDaemonStopper{clock: clock}.stop(context.Background(), dir), "a record with no process")
	assert.Empty(t, hub.requestRoutes(), "the hook contacts no address of a record that is not complete")
	assert.Zero(t, hub.shutdownCount())

	// The same record with its process is complete, and the hook asks it to
	// shut down: the assertions above are not true by accident.
	complete := half
	complete.PID = endedProcessPID(t)
	writeRecord(t, filepath.Join(dir, discoveryFileName), complete)
	require.NoError(t, staleDaemonStopper{clock: clock}.stop(context.Background(), dir))
	assert.Equal(t, 1, hub.shutdownCount())
}

// A record whose address is not a loopback address gets no request: the worker
// never sends the daemon's token off the machine.
func TestStopDaemonAtSkipsARecordThatIsNotLoopback(t *testing.T) {
	t.Parallel()
	record := fakeRecord("http://192.0.2.1:4242")
	record.PID = os.Getpid()
	assert.True(t, stopDaemonAt(record).IsZero())
}

// orphanAgentDir makes a Cline agent directory under base as a worker that
// crashed leaves it: a child process creates it, takes its lock, and exits
// without removing it.
func orphanAgentDir(t *testing.T, base string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessClineDaemon$")
	cmd.Env = append(os.Environ(), clineHelperEnv+"=1", clineOrphanBaseEnv+"="+base)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	require.NoError(t, err, "the child printed %q", out)
	dir := strings.TrimSpace(string(out))
	require.DirExists(t, dir)
	return dir
}

// C-M5 through the agent directories: the sweep ends the daemon that the
// directory of a crashed worker records, and a new directory waits until that
// daemon ended and the old directory went.
func TestANewAgentDirectoryWaitsForTheDaemonThatACrashedWorkerLeft(t *testing.T) {
	t.Parallel()
	base := agentdirtest.ShortBase(t)
	orphan := orphanAgentDir(t, base)
	d := newFakeDaemon(t)
	writeRecord(t, filepath.Join(orphan, discoveryFileName), d.record)
	clock := testutil.NewQuartzMock(t)
	deadline, poll := awaitExitTraps(t, clock)
	spec := agentDirSpec()
	spec.OnStale = staleDaemonStopper{clock: clock}.stop
	dirs, err := agentdir.Start(context.Background(), agentdir.Config{Specs: []agentdir.Spec{spec}, Bases: []string{base}, Clock: clock})
	require.NoError(t, err)
	ctx := testutil.DeadlineContext(t)
	armedWait(t, ctx, deadline, poll)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = newClineAgentDir(cancelled, dirs)
	require.ErrorIs(t, err, context.Canceled, "the new directory waits while the old daemon runs")
	assert.DirExists(t, orphan)

	d.proc.end(t)
	advancePoll(t, ctx, clock)
	select {
	case <-dirs.Swept():
	case <-ctx.Done():
		t.Fatal("the sweep did not end")
	}
	assert.NoDirExists(t, orphan, "the sweep removed the directory after its daemon ended")
	dir, err := newClineAgentDir(ctx, dirs)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dir.Close() })
	assert.Equal(t, 1, d.hub.shutdownCount())
	assert.True(t, d.proc.cmd.ProcessState.Success(), "the old daemon ended by itself after its shutdown")
}
