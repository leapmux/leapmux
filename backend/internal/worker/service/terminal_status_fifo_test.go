package service

import (
	"context"
	"fmt"
	"go/ast"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// terminalStatusOf returns the terminal status that one frame states, and false
// for any other frame.
func terminalStatusOf(frame *leapmuxv1.InnerStreamMessage) (leapmuxv1.TerminalStatus, bool) {
	var resp leapmuxv1.WatchEventsResponse
	if err := proto.Unmarshal(frame.GetPayload(), &resp); err != nil {
		return leapmuxv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED, false
	}
	change := resp.GetTerminalEvent().GetStatusChange()
	if change == nil {
		return leapmuxv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED, false
	}
	return change.GetStatus(), true
}

// wireTerminalStatuses lists the terminal statuses that reached w, in wire order.
func wireTerminalStatuses(w *testResponseWriter) []leapmuxv1.TerminalStatus {
	var statuses []leapmuxv1.TerminalStatus
	for _, frame := range w.streamsSnapshot() {
		if status, ok := terminalStatusOf(frame); ok {
			statuses = append(statuses, status)
		}
	}
	return statuses
}

// waitForWireTerminalStatuses waits until n statuses reached w and returns them.
func waitForWireTerminalStatuses(t *testing.T, w *testResponseWriter, n int) []leapmuxv1.TerminalStatus {
	t.Helper()
	require.Eventually(t, func() bool { return len(wireTerminalStatuses(w)) >= n },
		30*time.Second, time.Millisecond, "expected %d terminal statuses on the wire", n)
	return wireTerminalStatuses(w)
}

// statusHookWriter runs before ahead of the first terminal status frame, in the
// window between the replay's derive and its frame reaching the wire. A hook that
// emits a live status there is the race that the status queue exists to close.
//
// The claim is a compare-and-swap, not a sync.Once: the live status passes through
// this writer while the hook still runs, and a sync.Once would park it.
type statusHookWriter struct {
	*testResponseWriter
	claimed atomic.Bool
	before  func()
}

func (w *statusHookWriter) SendStream(frame *leapmuxv1.InnerStreamMessage) error {
	if _, isStatus := terminalStatusOf(frame); isStatus && w.claimed.CompareAndSwap(false, true) {
		w.before()
	}
	return w.testResponseWriter.SendStream(frame)
}

// stallingWriter parks the first terminal status frame until the test releases
// it, as a send parks on the connection writer's byte budget. A set sendErr then
// fails every send, as a connection that ended does.
type stallingWriter struct {
	*testResponseWriter
	claimed     atomic.Bool
	parked      chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	sendErr     error
}

func newStallingWriter(t *testing.T, svc *Service, channelID string) *stallingWriter {
	t.Helper()
	base := newTestWriter()
	base.channelID = channelID
	writer := &stallingWriter{testResponseWriter: base, parked: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		writer.releaseSend()
		testutil.AssertEventually(t, func() bool { return terminalStatusQueues(svc) == 0 }, "the stalled writer must finish its status drain")
	})
	return writer
}

func runStalledTestWork(t *testing.T, writer *stallingWriter, work func()) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	t.Cleanup(func() { writer.releaseSend(); <-done })
	go func() { defer close(done); work() }()
	return done
}

func (w *stallingWriter) SendStream(frame *leapmuxv1.InnerStreamMessage) error {
	if _, isStatus := terminalStatusOf(frame); isStatus && w.claimed.CompareAndSwap(false, true) {
		close(w.parked)
		<-w.release
	}
	if w.sendErr != nil {
		return w.sendErr
	}
	return w.testResponseWriter.SendStream(frame)
}

// openTerminalWatch subscribes writer to a terminal through the real WatchEvents
// handler, which registers it for live events and then runs the replay.
func openTerminalWatch(t *testing.T, d testRPCDispatcher, terminalID string, writer channel.ResponseWriter, base *testResponseWriter) {
	t.Helper()
	payload, err := proto.Marshal(&leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: terminalID, Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	})
	require.NoError(t, err)
	dispatchTestRequest(d, t.Context(), channel.LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{
		Method: leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, Payload: payload,
	}, writer)
	t.Cleanup(func() { base.deliverStreamRequest(nil, true); waitStreamEnded(t, base) })
}

// watchTerminal registers writer for a terminal's live events, with no replay.
func watchTerminal(svc *Service, terminalID string, writer channel.ResponseWriter) {
	svc.Watchers.terminals.setWatches(writer.ChannelID(),
		[]watchEntry{{id: terminalID, mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, writer)
}

// terminalStatusEvent builds a status change that states status alone.
func terminalStatusEvent(id string, status leapmuxv1.TerminalStatus) *leapmuxv1.TerminalEvent {
	return &leapmuxv1.TerminalEvent{
		TerminalId: id,
		Event:      &leapmuxv1.TerminalEvent_StatusChange{StatusChange: &leapmuxv1.TerminalStatusChange{Status: status}},
	}
}

func terminalReadyEvent(id string) *leapmuxv1.TerminalEvent {
	return terminalStatusEvent(id, leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY)
}

func terminalExitedEvent(id string) *leapmuxv1.TerminalEvent {
	return terminalStatusEvent(id, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED)
}

// publishTerminalReady sends READY for a terminal that has no shell. The tests that
// use it exercise the queue's order, not the derive that runs after a startup.
func publishTerminalReady(svc *Service, id string) {
	svc.Watchers.PublishTerminalStatus(id, terminalReadyEvent(id))
}

// runOnAnotherGoroutine runs fn on its own goroutine and returns when fn returned.
// The caller keeps its place in the replay while fn emits.
func runOnAnotherGoroutine(fn func()) {
	var wg sync.WaitGroup
	wg.Go(fn)
	wg.Wait()
}

// A restart that lands between the replay's derive and its send must not be
// overtaken by the older status. The client would hold EXITED, drop the READY
// that follows, and show a dead tab over a live shell.
func TestTerminalReplayStatusReachesTheWireBeforeARestartThatFollowsIt(t *testing.T) {
	t.Parallel()

	const (
		id    = "term-restart"
		shell = "/bin/sh"
	)
	svc, d, _ := setupTestService(t)
	seedTerminal(t, svc, id)
	w := &statusHookWriter{testResponseWriter: newTestWriter()}
	w.before = func() {
		runOnAnotherGoroutine(func() {
			_, handle := svc.beginTerminalStartup(id, shell)
			trackStartupForTest(t, &svc.TerminalStartup.startupCore, handle)
			assert.NotNil(t, handle)
			svc.announceTerminalStarting(id, shell)
		})
	}

	openTerminalWatch(t, d, id, w, w.testResponseWriter)

	assert.Equal(t, []leapmuxv1.TerminalStatus{
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED,
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING,
	}, waitForWireTerminalStatuses(t, w.testResponseWriter, 2))
}

// The variant: the replay derives STARTING, and the startup then fails. A stale
// STARTING after STARTUP_FAILED leaves the spinner up until a reload.
func TestTerminalReplayStatusSendsNoStaleStartingAfterAFailedStartup(t *testing.T) {
	t.Parallel()

	const id = "term-failed"
	svc, d, _ := setupTestService(t)
	seedTerminal(t, svc, id)
	handle := beginForTest(t, &svc.TerminalStartup.startupCore, id)
	require.NotNil(t, handle)
	svc.TerminalStartup.setMessage(id, "Starting sh…")
	w := &statusHookWriter{testResponseWriter: newTestWriter()}
	w.before = func() {
		runOnAnotherGoroutine(func() {
			svc.TerminalStartup.fail(handle, "spawn failed")
			svc.broadcastTerminalFailed(id, "spawn failed")
		})
	}

	openTerminalWatch(t, d, id, w, w.testResponseWriter)

	assert.Equal(t, []leapmuxv1.TerminalStatus{
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING,
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED,
	}, waitForWireTerminalStatuses(t, w.testResponseWriter, 2))
}

// A send that parks on one terminal must not hold another terminal's status.
func TestTerminalStatusOfOneTerminalPassesAStalledSenderOfAnother(t *testing.T) {
	t.Parallel()

	testCtx := testutil.DeadlineContext(t)
	svc, _, _ := setupTestService(t)
	stalled := newStallingWriter(t, svc, "stalled-channel")
	watchTerminal(svc, "term-stalled", stalled)
	healthy := newTestWriter()
	healthy.channelID = "healthy-channel"
	watchTerminal(svc, "term-healthy", healthy)

	emitter := runStalledTestWork(t, stalled, func() { publishTerminalReady(svc, "term-stalled") })
	t.Cleanup(func() { <-emitter })
	t.Cleanup(func() { stalled.releaseSend() })
	select {
	case <-stalled.parked:
	case <-testCtx.Done():
		require.FailNow(t, "the first status never reached the stalled sender")
	}

	publishTerminalReady(svc, "term-healthy")

	assert.Equal(t, []leapmuxv1.TerminalStatus{leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY},
		wireTerminalStatuses(healthy), "the other terminal's status must reach the wire during the stall")
}

// An emitter that finds another goroutine draining its terminal only enqueues, so
// it does not wait for a stalled client, and the statuses still arrive in the order
// of their emission. The first emitter is that drainer, and it does wait.
func TestTerminalStatusEmitterDoesNotWaitForAnotherDrainer(t *testing.T) {
	t.Parallel()

	const id = "term-stalled"
	testCtx := testutil.DeadlineContext(t)
	svc, _, _ := setupTestService(t)
	stalled := newStallingWriter(t, svc, "stalled-channel")
	watchTerminal(svc, id, stalled)

	first := runStalledTestWork(t, stalled, func() { svc.broadcastTerminalStarting(id, "Starting sh…", nil) })
	select {
	case <-stalled.parked:
	case <-testCtx.Done():
		require.FailNow(t, "the first status never reached the stalled sender")
	}

	emitted := runStalledTestWork(t, stalled, func() {
		publishTerminalReady(svc, id)
		svc.broadcastTerminalFailed(id, "spawn failed")
	})
	select {
	case <-emitted:
	case <-testCtx.Done():
		stalled.releaseSend()
		require.FailNow(t, "an emitter blocked on the stalled sender")
	}
	assert.Empty(t, wireTerminalStatuses(stalled.testResponseWriter), "the sender is still stalled")

	stalled.releaseSend()
	<-first

	assert.Equal(t, []leapmuxv1.TerminalStatus{
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING,
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY,
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED,
	}, wireTerminalStatuses(stalled.testResponseWriter))
}

// A replay hands its status to the queue too, so a stalled sender on one terminal
// does not hold up the watch session that replays the next one.
func TestTerminalReplayNeverBlocksOnAStalledSender(t *testing.T) {
	t.Parallel()

	const (
		id    = "term-stalled"
		other = "term-other"
	)
	testCtx := testutil.DeadlineContext(t)
	svc, _, _ := setupTestService(t)
	seedTerminal(t, svc, id)
	seedTerminal(t, svc, other)
	stalled := newStallingWriter(t, svc, "stalled-channel")
	watchTerminal(svc, id, stalled)

	first := runStalledTestWork(t, stalled, func() { publishTerminalReady(svc, id) })
	select {
	case <-stalled.parked:
	case <-testCtx.Done():
		require.FailNow(t, "the first status never reached the stalled sender")
	}

	replaying := newTestWriter()
	replaying.channelID = "replaying-channel"
	sink := newReplaySink(replaying, 0, nil, nil)
	replayed := runStalledTestWork(t, stalled, func() {
		for _, terminalID := range []string{id, other} {
			row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
			assert.NoError(t, err)
			svc.replayTerminalCatchUp(sink, terminalID, 0, row)
		}
	})
	select {
	case <-replayed:
	case <-testCtx.Done():
		stalled.releaseSend()
		require.FailNow(t, "a replay blocked on the stalled sender")
	}
	assert.Equal(t, []leapmuxv1.TerminalStatus{leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED},
		waitForWireTerminalStatuses(t, replaying, 1), "the other terminal's replay must not wait for the stall")

	stalled.releaseSend()
	<-first

	assert.Equal(t, []leapmuxv1.TerminalStatus{
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED,
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED,
	}, wireTerminalStatuses(replaying), "the drainer must deliver the queued replay after the stall")
}

// replayIntoStalledDrain runs a replay of id with a live status queued during it,
// and returns the writer of that replay once the replay returned. The goroutine
// that drains the replay's own status takes the live status too, and its delivery
// parks on stalled.
func replayIntoStalledDrain(t *testing.T, svc *Service, id string, stalled *stallingWriter) *testResponseWriter {
	t.Helper()
	testCtx := testutil.DeadlineContext(t)
	seedTerminal(t, svc, id)
	row, err := svc.Queries.GetTerminal(context.Background(), id)
	require.NoError(t, err)
	watchTerminal(svc, id, stalled)

	// The replay's own status is on its way out, so the live status finds a
	// drainer at work and leaves itself to that drainer.
	replaying := &statusHookWriter{testResponseWriter: newTestWriter()}
	replaying.before = func() { runOnAnotherGoroutine(func() { publishTerminalReady(svc, id) }) }

	replayed := runStalledTestWork(t, stalled, func() {
		svc.replayTerminalCatchUp(newReplaySink(replaying, 0, nil, nil), id, 0, row)
	})
	select {
	case <-stalled.parked:
	case <-testCtx.Done():
		require.FailNow(t, "the live status never reached the stalled watcher")
	}
	select {
	case <-replayed:
	case <-testCtx.Done():
		require.FailNow(t, "a stalled watcher held the replay of another session")
	}
	return replaying.testResponseWriter
}

// The drain of a replay's status can reach a live status and park on ANOTHER
// watcher's stalled writer. That drain must not run on the watch session's
// goroutine, or the stall holds the rest of this session's replay.
func TestTerminalReplayReturnsWhileAnotherWatcherStalls(t *testing.T) {
	t.Parallel()

	const id = "term-shared"
	svc, _, _ := setupTestService(t)
	stalled := newStallingWriter(t, svc, "stalled-channel")
	t.Cleanup(func() { stalled.releaseSend() })

	replaying := replayIntoStalledDrain(t, svc, id, stalled)

	assert.Equal(t, []leapmuxv1.TerminalStatus{leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED},
		waitForWireTerminalStatuses(t, replaying, 1))
	assert.Empty(t, wireTerminalStatuses(stalled.testResponseWriter), "the other watcher is still stalled")
}

// The goroutine of a replay's drain ends when the connection that holds it ends.
// The parked send fails, the broadcast retires that watcher, and the queue empties.
func TestTerminalReplayDrainEndsWhenTheStalledConnectionEnds(t *testing.T) {
	t.Parallel()

	const id = "term-shared"
	svc, _, _ := setupTestService(t)
	stalled := newStallingWriter(t, svc, "stalled-channel")
	stalled.sendErr = channel.ErrTransportGone

	replayIntoStalledDrain(t, svc, id, stalled)
	require.Equal(t, 1, terminalStatusQueues(svc), "precondition: the drain holds the queue")

	stalled.releaseSend()

	require.Eventually(t, func() bool { return terminalStatusQueues(svc) == 0 }, 30*time.Second, time.Millisecond,
		"the drain must end with the connection")
	assert.Empty(t, svc.Watchers.terminals.snapshot(id), "the failed send must retire that watcher")
}

// Sequential statuses of each terminal reach every watcher in order while replays
// and other terminals run beside them. Run it under -race: the replay sink is
// shared between the watch session and whichever goroutine drains.
func TestTerminalStatusKeepsItsOrderUnderConcurrentEmittersAndReplays(t *testing.T) {
	t.Parallel()

	const (
		terminals = 8
		rounds    = 50
	)
	svc, _, _ := setupTestService(t)
	writers := make([]*testResponseWriter, terminals)
	var emitters sync.WaitGroup
	for i := range terminals {
		id := fmt.Sprintf("term-%d", i)
		seedTerminal(t, svc, id)
		row, err := svc.Queries.GetTerminal(context.Background(), id)
		require.NoError(t, err)
		writers[i] = newTestWriter()
		writers[i].channelID = id + "-channel"
		watchTerminal(svc, id, writers[i])
		emitters.Go(func() {
			for range rounds {
				svc.broadcastTerminalStarting(id, "Starting sh…", nil)
				publishTerminalReady(svc, id)
			}
		})
		emitters.Go(func() {
			sink := newReplaySink(newTestWriter(), 0, nil, nil)
			for range rounds {
				svc.replayTerminalCatchUp(sink, id, 0, row)
			}
		})
	}
	emitters.Wait()

	for _, w := range writers {
		statuses := waitForWireTerminalStatuses(t, w, 2*rounds)
		require.Len(t, statuses, 2*rounds)
		for i, status := range statuses {
			want := leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING
			if i%2 == 1 {
				want = leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY
			}
			require.Equal(t, want.String(), status.String(), "status %d", i)
		}
	}
}

// terminalStatusQueues reports how many terminals hold a status queue.
func terminalStatusQueues(svc *Service) int {
	svc.Watchers.terminalStatuses.mu.Lock()
	defer svc.Watchers.terminalStatuses.mu.Unlock()
	return len(svc.Watchers.terminalStatuses.queues)
}

// wireTerminalClosedEvents counts the closed events that reached w.
func wireTerminalClosedEvents(t *testing.T, w *testResponseWriter) int {
	t.Helper()
	closed := 0
	for _, frame := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		require.NoError(t, proto.Unmarshal(frame.GetPayload(), &resp))
		if resp.GetTerminalEvent().GetClosed() != nil {
			closed++
		}
	}
	return closed
}

// A terminal's queue is released by the drain that delivers its last event, which
// is the closed event of the exit handler. While a sender stalls, the queue holds
// the events that wait for it, and an exit handler that finds that drainer returns.
func TestTerminalStatusQueueIsReleasedWhenTheTerminalEnds(t *testing.T) {
	t.Parallel()

	const id = "term-ends"
	testCtx := testutil.DeadlineContext(t)
	svc, _, _ := setupTestService(t)
	seedTerminal(t, svc, id)
	stalled := newStallingWriter(t, svc, "stalled-channel")
	watchTerminal(svc, id, stalled)

	first := runStalledTestWork(t, stalled, func() { publishTerminalReady(svc, id) })
	select {
	case <-stalled.parked:
	case <-testCtx.Done():
		require.FailNow(t, "the first status never reached the stalled sender")
	}

	exited := runStalledTestWork(t, stalled, func() {
		svc.makeTerminalExitFn()(id, 0)
	})
	select {
	case <-exited:
	case <-testCtx.Done():
		stalled.releaseSend()
		require.FailNow(t, "the exit handler blocked on the stalled sender")
	}
	assert.Equal(t, 1, terminalStatusQueues(svc), "the closed event waits in the queue during the stall")

	stalled.releaseSend()
	<-first

	assert.Equal(t, []leapmuxv1.TerminalStatus{leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY},
		wireTerminalStatuses(stalled.testResponseWriter))
	assert.Equal(t, 1, wireTerminalClosedEvents(t, stalled.testResponseWriter))
	assert.Zero(t, terminalStatusQueues(svc), "the terminal's queue must be gone once its last event is delivered")
}

// The map must not grow with the terminals a worker has served, whether anybody
// watched them or not.
func TestTerminalStatusQueuesDoNotGrowWithTheTerminalsServed(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	for i := range 1000 {
		id := fmt.Sprintf("term-%d", i)
		svc.broadcastTerminalStarting(id, "Starting sh…", nil)
		publishTerminalReady(svc, id)
		svc.Watchers.PublishTerminalStatus(id, &leapmuxv1.TerminalEvent{
			TerminalId: id,
			Event:      &leapmuxv1.TerminalEvent_Closed{Closed: &leapmuxv1.TerminalClosed{}},
		})
		require.Zero(t, terminalStatusQueues(svc))
	}
}

// The derive must run inside the queue's lock, or a live status could enter the
// queue between the derive and the replay's own entry.
func TestReplayTerminalStatusDerivesInsideTheQueueLock(t *testing.T) {
	t.Parallel()

	manager := NewWatcherManager()
	w := newTestWriter()
	manager.ReplayTerminalStatus("term", newReplaySink(w, 0, nil, nil), func() *leapmuxv1.TerminalEvent {
		assertLockHeld(t, &manager.terminalStatuses.mu, "the derive must hold the queue's lock")
		return terminalExitedEvent("term")
	})
	assert.Equal(t, []leapmuxv1.TerminalStatus{leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED},
		waitForWireTerminalStatuses(t, w, 1))
}

// A replayed status reaches its subscriber alone. Every other watcher of the
// terminal already holds the live status.
func TestReplayTerminalStatusReachesOnlyItsSubscriber(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	seedTerminal(t, svc, "term")
	row, err := svc.Queries.GetTerminal(context.Background(), "term")
	require.NoError(t, err)
	watcher := newTestWriter()
	watcher.channelID = "other-watcher"
	watchTerminal(svc, "term", watcher)

	subscriber := newTestWriter()
	svc.replayTerminalCatchUp(newReplaySink(subscriber, 0, nil, nil), "term", 0, row)

	assert.Equal(t, []leapmuxv1.TerminalStatus{leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED},
		waitForWireTerminalStatuses(t, subscriber, 1))
	assert.Eventually(t, func() bool { return terminalStatusQueues(svc) == 0 }, 30*time.Second, time.Millisecond,
		"the drain must finish before the other watcher is checked")
	assert.Empty(t, wireTerminalStatuses(watcher))
}

// A replay can end while its status waits in the queue. The drainer then drops
// that status, and the queue still empties.
func TestTerminalReplayStatusOfARetiredSinkIsDropped(t *testing.T) {
	t.Parallel()

	const id = "term-retired"
	testCtx := testutil.DeadlineContext(t)
	svc, _, _ := setupTestService(t)
	stalled := newStallingWriter(t, svc, "stalled-channel")
	watchTerminal(svc, id, stalled)

	first := runStalledTestWork(t, stalled, func() { publishTerminalReady(svc, id) })
	select {
	case <-stalled.parked:
	case <-testCtx.Done():
		require.FailNow(t, "the first status never reached the stalled sender")
	}

	subscriber := newTestWriter()
	sink := newReplaySink(subscriber, 0, nil, nil)
	svc.Watchers.ReplayTerminalStatus(id, sink, func() *leapmuxv1.TerminalEvent {
		return &leapmuxv1.TerminalEvent{TerminalId: id}
	})
	sink.retire(channel.ErrTransportGone)

	stalled.releaseSend()
	<-first

	assert.Empty(t, subscriber.streamsSnapshot(), "a retired sink sends nothing")
	assert.Zero(t, terminalStatusQueues(svc))
}

// The drainer of a terminal's status queue shares the sink with the watch session.
// Run it under -race. The sink retires exactly once, whichever goroutine latches
// the failure first.
func TestReplaySinkRetiresOnceAcrossGoroutines(t *testing.T) {
	t.Parallel()

	w := newTestWatcher("failing-sink")
	w.failSends(channel.ErrTransportGone)
	var retired atomic.Int32
	sink := newReplaySink(w, 0, func() { retired.Add(1) }, nil)

	var senders sync.WaitGroup
	for range 8 {
		senders.Go(func() {
			for range 100 {
				sink.send(&leapmuxv1.WatchEventsResponse{})
				sink.fail(channel.ErrTransportGone)
				_ = sink.alive()
			}
		})
	}
	senders.Wait()

	assert.Equal(t, int32(1), retired.Load())
	assert.False(t, sink.alive())
}

// Every terminal status goes through the status queue. A status sent around it
// could overtake a queued replay, and the failure shows only as a rare stuck tab,
// so the rule is checked on the source.
func TestTerminalStatusesEnterOnlyThroughTheStatusQueue(t *testing.T) {
	t.Parallel()

	_, files := parseServiceSources(t)
	broadcasts := 0
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "BroadcastTerminalEvent" {
				return true
			}
			broadcasts++
			ast.Inspect(call, func(arg ast.Node) bool {
				if id, ok := arg.(*ast.Ident); ok {
					assert.NotContains(t, []string{"TerminalEvent_StatusChange", "TerminalEvent_Closed"}, id.Name,
						"send a terminal status with PublishTerminalStatus")
				}
				return true
			})
			return true
		})
	}
	require.Positive(t, broadcasts, "the scan must find the other terminal events")
}

// startShell starts a shell whose exit handler is the production one.
func startShell(ctx context.Context, t *testing.T, svc *Service, id string) {
	t.Helper()
	require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
		ID: id, Shell: testutil.TestShell(), WorkingDir: t.TempDir(), Cols: 80, Rows: 24,
	}, svc.makeTerminalOutputFn(id), svc.makeTerminalExitFn()))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, id)
}

// startWatchedShell starts a shell as startShell does, and returns a writer that
// watches the terminal.
func startWatchedShell(ctx context.Context, t *testing.T, svc *Service, id string) *testResponseWriter {
	t.Helper()
	seedTerminal(t, svc, id)
	w := newTestWriter()
	watchTerminal(svc, id, w)
	startShell(ctx, t, svc, id)
	return w
}

// READY states that the shell runs, and a shell can exit between its spawn and
// READY. Its closed event then enters the queue first, and a READY behind that
// event states something false. The queue entry states EXITED instead.
func TestBroadcastTerminalStatusAfterStartupStatesExitedAfterTheShellExited(t *testing.T) {
	t.Parallel()

	ctx := testutil.DeadlineContext(t)
	svc, _, _ := setupTestService(t)
	const id = "terminal-exited-before-ready"
	w := startWatchedShell(ctx, t, svc, id)

	svc.Terminals.StopTerminal(id)
	svc.Terminals.WaitForExit(id)
	require.Equal(t, 1, wireTerminalClosedEvents(t, w), "precondition: the exit handler sent closed")
	require.Empty(t, wireTerminalStatuses(w), "precondition: closed is the only frame on the wire")

	svc.broadcastTerminalStatusAfterStartup(id)

	assert.Equal(t, []leapmuxv1.TerminalStatus{leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED},
		wireTerminalStatuses(w), "the entry behind closed must state EXITED, never READY")
	assert.Zero(t, terminalStatusQueues(svc), "a delivered status must leave no queue")
}

// A close removes the terminal from the manager, and its exit handler may already
// have run. No shell runs in that state either.
func TestBroadcastTerminalStatusAfterStartupStatesExitedForAnUnknownTerminal(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	const id = "terminal-unknown"
	w := newTestWriter()
	watchTerminal(svc, id, w)

	svc.broadcastTerminalStatusAfterStartup(id)

	assert.Equal(t, []leapmuxv1.TerminalStatus{leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED},
		wireTerminalStatuses(w))
	assert.Zero(t, terminalStatusQueues(svc), "a delivered status must leave no queue")
}

// A shell exits within milliseconds of its spawn. The watch session registers
// after the exit handler published closed, so closed never reaches it, and its
// replay reads a registry that still says STARTING. The status that the success
// queues is then the only one that can correct this session, or its tab shows
// the spinner until a reload.
func TestTerminalReplayedStartingEndsInExitedWhenTheShellExitedFirst(t *testing.T) {
	t.Parallel()

	const id = "term-short-lived"
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	seedTerminal(t, svc, id)
	handle := beginForTest(t, &svc.TerminalStartup.startupCore, id)
	require.NotNil(t, handle)
	svc.TerminalStartup.setMessage(id, "Starting sh…")
	startShell(ctx, t, svc, id)
	svc.Terminals.StopTerminal(id)
	svc.Terminals.WaitForExit(id)

	w := newTestWriter()
	openTerminalWatch(t, d, id, w, w)
	require.Equal(t, []leapmuxv1.TerminalStatus{leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING},
		waitForWireTerminalStatuses(t, w, 1), "the replay reads the registry, which still says STARTING")

	svc.succeedTerminalStartup(id, handle)

	assert.Equal(t, []leapmuxv1.TerminalStatus{
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING,
		leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED,
	}, waitForWireTerminalStatuses(t, w, 2))
	assert.Zero(t, wireTerminalClosedEvents(t, w), "this session registered after closed")
}

// A restart can claim the terminal between the registry step of the success and
// its status. The registry then owns the status. An EXITED here would land behind
// the restart's STARTING, the client would drop the READY that follows, and the
// tab would show a dead shell over a live one.
func TestBroadcastTerminalStatusAfterStartupStatesTheRestartThatClaimedTheTerminal(t *testing.T) {
	t.Parallel()

	ctx := testutil.DeadlineContext(t)
	svc, _, _ := setupTestService(t)
	const id = "terminal-restarted-before-ready"
	w := startWatchedShell(ctx, t, svc, id)
	svc.Terminals.StopTerminal(id)
	svc.Terminals.WaitForExit(id)
	_, handle := svc.beginTerminalStartup(id, testutil.TestShell())
	trackStartupForTest(t, &svc.TerminalStartup.startupCore, handle)
	require.NotNil(t, handle)

	svc.broadcastTerminalStatusAfterStartup(id)

	assert.Equal(t, []leapmuxv1.TerminalStatus{leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING},
		wireTerminalStatuses(w))
}

func TestBroadcastTerminalStatusAfterStartupStatesReadyWhileTheShellRuns(t *testing.T) {
	t.Parallel()

	ctx := testutil.DeadlineContext(t)
	svc, _, _ := setupTestService(t)
	const id = "terminal-running"
	w := startWatchedShell(ctx, t, svc, id)

	svc.broadcastTerminalStatusAfterStartup(id)

	assert.Equal(t, []leapmuxv1.TerminalStatus{leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY},
		wireTerminalStatuses(w))
}

// The derive must run inside the queue's lock, or the shell's closed event could
// enter the queue between the derive and the status's own entry.
func TestPublishDerivedTerminalStatusDerivesInsideTheQueueLock(t *testing.T) {
	t.Parallel()

	manager := NewWatcherManager()
	derived := false
	manager.PublishDerivedTerminalStatus("term", func() *leapmuxv1.TerminalEvent {
		derived = true
		assertLockHeld(t, &manager.terminalStatuses.mu, "the derive must hold the queue's lock")
		return &leapmuxv1.TerminalEvent{TerminalId: "term"}
	})
	assert.True(t, derived)
}

// A live status reaches every watcher, unlike a replayed one.
func TestPublishDerivedTerminalStatusReachesEveryWatcher(t *testing.T) {
	t.Parallel()

	const id = "term"
	manager := NewWatcherManager()
	first, second := newTestWriter(), newTestWriter()
	first.channelID = "first-channel"
	second.channelID = "second-channel"
	for _, w := range []*testResponseWriter{first, second} {
		manager.terminals.setWatches(w.ChannelID(),
			[]watchEntry{{id: id, mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, w)
	}

	manager.PublishDerivedTerminalStatus(id, func() *leapmuxv1.TerminalEvent { return terminalReadyEvent(id) })

	ready := []leapmuxv1.TerminalStatus{leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY}
	assert.Equal(t, ready, wireTerminalStatuses(first))
	assert.Equal(t, ready, wireTerminalStatuses(second))
}

// frameStatesReady reports whether one frame states READY.
func frameStatesReady(frame *leapmuxv1.InnerStreamMessage) bool {
	status, isStatus := terminalStatusOf(frame)
	return isStatus && status == leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY
}

// The shell's exit races the status of a finished startup on two goroutines. As in
// production, the exit changes the state that the derive reads before it publishes
// closed. Whichever goroutine wins, READY never reaches the wire behind closed.
func TestPublishDerivedTerminalStatusKeepsReadyAheadOfARacingExit(t *testing.T) {
	t.Parallel()

	const id = "term"
	closed := &leapmuxv1.TerminalEvent{
		TerminalId: id,
		Event:      &leapmuxv1.TerminalEvent_Closed{Closed: &leapmuxv1.TerminalClosed{}},
	}
	for range 500 {
		manager := NewWatcherManager()
		w := newTestWriter()
		manager.terminals.setWatches(w.ChannelID(),
			[]watchEntry{{id: id, mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, w)
		var running atomic.Bool
		running.Store(true)

		var emitters sync.WaitGroup
		emitters.Go(func() {
			manager.PublishDerivedTerminalStatus(id, func() *leapmuxv1.TerminalEvent {
				if running.Load() {
					return terminalReadyEvent(id)
				}
				return terminalExitedEvent(id)
			})
		})
		emitters.Go(func() {
			running.Store(false)
			manager.PublishTerminalStatus(id, closed)
		})
		emitters.Wait()

		frames := w.streamsSnapshot()
		require.Len(t, frames, 2, "the wire holds closed and the derived status")
		_, firstIsStatus := terminalStatusOf(frames[0])
		closedCameFirst := !firstIsStatus
		require.False(t, closedCameFirst && frameStatesReady(frames[1]), "READY must never follow closed")
	}
}

func (w *stallingWriter) releaseSend() { w.releaseOnce.Do(func() { close(w.release) }) }

func TestStalledPublicationIsJoinedWhenItsSubtestEnds(t *testing.T) {
	var svc *Service
	var stalled *stallingWriter
	var finished <-chan struct{}
	defer func() {
		if stalled != nil {
			stalled.releaseSend()
		}
		if finished != nil {
			<-finished
		}
		if svc != nil {
			testutil.AssertEventually(t, func() bool { return terminalStatusQueues(svc) == 0 }, "failure cleanup must drain the queued status")
		}
	}()
	t.Run("owned publication", func(t *testing.T) {
		svc, _, _ = setupTestService(t)
		const id = "owned-stalled-publication"
		seedTerminal(t, svc, id)
		stalled = newStallingWriter(t, svc, "owned-stalled-channel")
		watchTerminal(svc, id, stalled)
		finished = runStalledTestWork(t, stalled, func() { publishTerminalReady(svc, id) })
		<-stalled.parked
	})
	select {
	case <-finished:
	default:
		t.Error("the subtest returned before its emitter finished")
	}
	assert.Zero(t, terminalStatusQueues(svc), "the subtest must join its stalled drainer")
}
