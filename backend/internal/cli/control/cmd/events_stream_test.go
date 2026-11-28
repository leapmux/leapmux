package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// watchedAgents lists the agents of a watch request, and requires that each
// is a notify watch: the stream needs no content and no replay.
func watchedAgents(t *testing.T, req *leapmuxv1.WatchEventsRequest) []string {
	t.Helper()
	var out []string
	for _, entry := range req.GetAgents() {
		assert.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_NOTIFY, entry.GetMode())
		out = append(out, entry.GetAgentId())
	}
	return out
}

// watchedTerminals lists the terminals of a watch request, each a notify
// watch.
func watchedTerminals(t *testing.T, req *leapmuxv1.WatchEventsRequest) []string {
	t.Helper()
	var out []string
	for _, entry := range req.GetTerminals() {
		assert.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_NOTIFY, entry.GetMode())
		out = append(out, entry.GetTerminalId())
	}
	return out
}

// bringLive acknowledges the newest watch of w and answers its read of the
// agents with rows.
func (h *streamHarness) bringLive(w *fakeWorkerSide, rows ...*leapmuxv1.AgentInfo) {
	h.t.Helper()
	w.watch.ack()
	if len(rows) == 0 {
		return
	}
	receive(h.t, h.ctx, w.reads.started)
	w.reads.answer(rows...)
}

// requireReady reads stream.ready and returns its scope.
func (h *streamHarness) requireReady() *scopeCounts {
	h.t.Helper()
	ev := h.next()
	require.Equal(h.t, kindStreamReady, ev.Kind, "summary: %s", ev.Summary)
	require.NotNil(h.t, ev.Scope)
	return ev.Scope
}

// The stream watches each Worker that hosts a tab of the scope, with a
// notify watch of exactly its tabs, and prints the events of every Worker
// in one stream after stream.ready.
func TestEventStream_MergesTheEventsOfEveryWorker(t *testing.T) {
	h := startStreamHarness(t, streamHarnessOptions{})
	hub := h.session()
	hub.send(snapshotEvent([]string{"ws-1"},
		agentTabRecord("ag-1", "ws-1", "worker-A"), terminalTabRecord("tm-1", "ws-1", "worker-B")))
	a, b := h.worker("worker-A"), h.worker("worker-B")

	reqA := receive(t, h.ctx, a.watch.opened)
	assert.Equal(t, []string{"ag-1"}, watchedAgents(t, reqA))
	assert.Empty(t, reqA.GetTerminals())
	reqB := receive(t, h.ctx, b.watch.opened)
	assert.Empty(t, reqB.GetAgents())
	assert.Equal(t, []string{"tm-1"}, watchedTerminals(t, reqB))

	h.bringLive(a, agentLevelRow("ag-1", statusActive, idle))
	h.requireQuiet()
	h.bringLive(b)
	assert.Equal(t, &scopeCounts{Workspaces: 1, Tabs: 2, Workers: 2, WorkersLive: 2, HubLive: true}, h.requireReady(),
		"stream.ready waits for every Worker, and the levels of the start print nothing")

	a.watch.push(turnEndFrame("ag-1"))
	turnEnd := h.next()
	assert.Equal(t, kindAgentTurnEnd, turnEnd.Kind)
	assert.Equal(t, "ag-1", turnEnd.TabID)
	assert.Equal(t, "ws-1", turnEnd.WorkspaceID)
	assert.Equal(t, "worker-A", turnEnd.WorkerID)
	assert.False(t, turnEnd.Time.IsZero())

	b.watch.push(bellFrame("tm-1"))
	bell := h.next()
	assert.Equal(t, kindTerminalBell, bell.Kind)
	assert.Equal(t, "tm-1", bell.TabID)
	assert.Equal(t, "worker-B", bell.WorkerID)

	a.watch.push(activityChangedFrame("ag-1", idle))
	a.watch.push(activityChangedFrame("ag-1", working))
	ev := h.next()
	assert.Equal(t, "working", ev.State, "the level of the start makes a repeated idle no news")
}

// A tab that opens joins the stream: on the watch of its Worker when the
// stream watches the Worker already, on a new watch otherwise. The stream
// prints the state of a new agent, which no reader of the state saw.
func TestEventStream_ATabThatOpensJoinsTheStream(t *testing.T) {
	h := startStreamHarness(t, streamHarnessOptions{})
	hub := h.session()
	hub.send(snapshotEvent([]string{"ws-1"}, agentTabRecord("ag-1", "ws-1", "worker-A")))
	a := h.worker("worker-A")
	receive(t, h.ctx, a.watch.opened)
	h.bringLive(a, agentLevelRow("ag-1", statusActive, idle))
	h.requireReady()

	hub.send(materializedTab(agentTabRecord("ag-2", "ws-1", "worker-A")))
	hub.send(batchEndEvent())
	opened := h.next()
	assert.Equal(t, kindTabOpened, opened.Kind)
	assert.Equal(t, "ag-2", opened.TabID)
	assert.Equal(t, "agent", opened.TabType)
	assert.Equal(t, "worker-A", opened.WorkerID)
	assert.False(t, opened.Reconciled)

	revision := receive(t, h.ctx, a.watch.updated)
	assert.Equal(t, []string{"ag-1", "ag-2"}, watchedAgents(t, revision), "the same stream carries the new tab")
	a.watch.ack()
	assert.Equal(t, []string{"ag-2"}, receive(t, h.ctx, a.reads.started), "only the new agent needs a read")
	a.reads.answer(agentLevelRow("ag-2", statusStarting, working))
	status, activity := h.next(), h.next()
	assert.Equal(t, kindAgentStatus, status.Kind)
	assert.Equal(t, "starting", status.Status)
	assert.Equal(t, kindAgentActivity, activity.Kind)
	assert.Equal(t, "working", activity.State)
	assert.False(t, activity.Reconciled, "the state of a new agent is no gap")

	hub.send(materializedTab(terminalTabRecord("tm-9", "ws-1", "worker-C")))
	hub.send(batchEndEvent())
	assert.Equal(t, "tm-9", h.next().TabID)
	c := h.worker("worker-C")
	assert.Equal(t, []string{"tm-9"}, watchedTerminals(t, receive(t, h.ctx, c.watch.opened)), "a new Worker gets a watch of its own")
	h.bringLive(c)
	c.watch.push(bellFrame("tm-9"))
	assert.Equal(t, kindTerminalBell, h.next().Kind, "the new watch delivers, and stream.ready does not print again")
}

// A tab that closes leaves the stream: the watch of its Worker drops it, an
// event of it prints nothing, and the watch of a Worker with no tab left
// stops.
func TestEventStream_ATabThatClosesLeavesTheStream(t *testing.T) {
	h := startStreamHarness(t, streamHarnessOptions{})
	hub := h.session()
	hub.send(snapshotEvent([]string{"ws-1"},
		agentTabRecord("ag-1", "ws-1", "worker-A"), agentTabRecord("ag-2", "ws-1", "worker-A"), terminalTabRecord("tm-1", "ws-1", "worker-B")))
	a, b := h.worker("worker-A"), h.worker("worker-B")
	receive(t, h.ctx, a.watch.opened)
	receive(t, h.ctx, b.watch.opened)
	h.bringLive(a, agentLevelRow("ag-1", statusActive, idle), agentLevelRow("ag-2", statusActive, idle))
	h.bringLive(b)
	h.requireReady()

	hub.send(batchEvent(closeTabOp(leapmuxv1.TabType_TAB_TYPE_AGENT, "ag-2", 20)))
	hub.send(batchEndEvent())
	closed := h.next()
	assert.Equal(t, kindTabClosed, closed.Kind)
	assert.Equal(t, "ag-2", closed.TabID)
	assert.Equal(t, "ws-1", closed.WorkspaceID)
	assert.Equal(t, []string{"ag-1"}, watchedAgents(t, receive(t, h.ctx, a.watch.updated)))
	a.watch.ack()

	a.watch.push(turnEndFrame("ag-2"))
	a.watch.push(turnEndFrame("ag-1"))
	assert.Equal(t, "ag-1", h.next().TabID, "an event of a closed tab prints nothing")

	hub.send(batchEvent(closeTabOp(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "tm-1", 21)))
	hub.send(batchEndEvent())
	assert.Equal(t, "tm-1", h.next().TabID)
	receive(t, h.ctx, b.watch.cancelled)
	h.requireQuiet()
}

// A tab that moves between two watched workspaces prints tab.moved with
// both. Under --workspace, a tab that leaves for a workspace outside the
// scope prints tab.moved without the destination, which the hub does not
// send.
func TestEventStream_ATabThatMoves(t *testing.T) {
	t.Run("between two watched workspaces", func(t *testing.T) {
		h := startStreamHarness(t, streamHarnessOptions{})
		hub := h.session()
		hub.send(snapshotEvent([]string{"ws-1", "ws-2"}, agentTabRecord("ag-1", "ws-1", "worker-A")))
		a := h.worker("worker-A")
		receive(t, h.ctx, a.watch.opened)
		h.bringLive(a, agentLevelRow("ag-1", statusActive, idle))
		assert.Equal(t, 2, h.requireReady().Workspaces)

		hub.send(batchEvent(moveTabOp(leapmuxv1.TabType_TAB_TYPE_AGENT, "ag-1", "ws-2", 20)))
		hub.send(batchEndEvent())
		moved := h.next()
		assert.Equal(t, kindTabMoved, moved.Kind)
		assert.Equal(t, "ws-1", moved.FromWorkspaceID)
		assert.Equal(t, "ws-2", moved.WorkspaceID)
		assert.Equal(t, "agent tab from workspace ws-1", moved.Summary)
		a.watch.push(turnEndFrame("ag-1"))
		assert.Equal(t, "ws-2", h.next().WorkspaceID, "the tab stays in the stream, in its new workspace")
	})
	t.Run("out of the watched workspace", func(t *testing.T) {
		h := startStreamHarness(t, streamHarnessOptions{workspace: "ws-1"})
		hub := h.session()
		hub.send(snapshotEvent([]string{"ws-1"}, agentTabRecord("ag-1", "ws-1", "worker-A")))
		a := h.worker("worker-A")
		receive(t, h.ctx, a.watch.opened)
		h.bringLive(a, agentLevelRow("ag-1", statusActive, idle))
		h.requireReady()

		hub.send(removedTab(leapmuxv1.TabType_TAB_TYPE_AGENT, "ag-1"))
		hub.send(batchEndEvent())
		moved := h.next()
		assert.Equal(t, kindTabMoved, moved.Kind)
		assert.Equal(t, "ws-1", moved.FromWorkspaceID)
		assert.Empty(t, moved.WorkspaceID)
		assert.Equal(t, "agent tab left the watched workspace", moved.Summary)
		receive(t, h.ctx, a.watch.cancelled)
	})
}

// A Worker's stream that drops prints stream.unavailable, subscribes again
// after the backoff, and prints stream.gap with what it may have lost, then
// each change of state that it missed, marked reconciled.
func TestEventStream_AWorkerStreamThatDropsPrintsAGap(t *testing.T) {
	var reconnect *quartz.Trap
	h := startStreamHarness(t, streamHarnessOptions{beforeRun: func(clock *quartz.Mock) {
		reconnect = trapNewTimer(t, clock, feedReconnectTimerTag)
	}})
	hub := h.session()
	hub.send(snapshotEvent([]string{"ws-1"}, agentTabRecord("ag-1", "ws-1", "worker-A"), agentTabRecord("ag-2", "ws-1", "worker-A")))
	a := h.worker("worker-A")
	receive(t, h.ctx, a.watch.opened)
	h.bringLive(a, agentLevelRow("ag-1", statusActive, idle), agentLevelRow("ag-2", statusActive, working))
	h.requireReady()
	dropped := h.clock.Now()

	a.watch.endStreamWithErr(connect.NewError(connect.CodeUnavailable, "worker disconnected"))
	down := h.next()
	assert.Equal(t, kindStreamUnavailable, down.Kind)
	assert.Equal(t, "worker", down.Source)
	assert.Equal(t, "worker-A", down.WorkerID)
	assert.Equal(t, string(cli.CodeUnavailable), down.Code)
	assert.Equal(t, "worker disconnected", down.Message, "the reason that the transport reported")
	require.NotNil(t, down.Retrying)
	assert.True(t, *down.Retrying)

	wait := testutil.WaitForTimer(t, h.ctx, reconnect)
	assert.Equal(t, activityReconnectInitial, wait, "a stream that delivered reconnects after the shortest backoff")
	h.clock.Advance(wait).MustWait(h.ctx)

	assert.Equal(t, []string{"ag-1", "ag-2"}, watchedAgents(t, receive(t, h.ctx, a.watch.opened)))
	a.watch.ack()
	assert.Equal(t, []string{"ag-1", "ag-2"}, receive(t, h.ctx, a.reads.started), "a new session reads every agent again")
	a.reads.answer(agentLevelRow("ag-1", statusActive, waiting), agentLevelRow("ag-2", statusActive, working))

	gap := h.next()
	assert.Equal(t, kindStreamGap, gap.Kind)
	assert.Equal(t, "worker-A", gap.WorkerID)
	require.NotNil(t, gap.Since)
	assert.True(t, gap.Since.Equal(dropped), "since: %s, dropped: %s", gap.Since, dropped)
	assert.Equal(t, lostKinds(sourceWorker, false), gap.Lost)
	assert.Equal(t, []string{"ag-1", "ag-2"}, gap.TabIDs)
	assert.Contains(t, gap.Summary, "after a gap of 250ms")

	reconciled := h.next()
	assert.Equal(t, kindAgentActivity, reconciled.Kind)
	assert.Equal(t, "ag-1", reconciled.TabID)
	assert.Equal(t, "waiting_for_user", reconciled.State)
	assert.True(t, reconciled.Reconciled)

	a.watch.push(activityChangedFrame("ag-2", idle))
	live := h.next()
	assert.Equal(t, "ag-2", live.TabID, "ag-2 did not change during the gap, so nothing reconciled it")
	assert.False(t, live.Reconciled)
}

// When the read of the agents fails after a gap, the stream cannot state
// what changed, so the gap lists the activity and the status as lost too.
func TestEventStream_AGapWhoseReadFailsLosesTheStateToo(t *testing.T) {
	var reconnect *quartz.Trap
	h := startStreamHarness(t, streamHarnessOptions{beforeRun: func(clock *quartz.Mock) {
		reconnect = trapNewTimer(t, clock, feedReconnectTimerTag)
	}})
	h.session().send(snapshotEvent([]string{"ws-1"}, agentTabRecord("ag-1", "ws-1", "worker-A")))
	a := h.worker("worker-A")
	receive(t, h.ctx, a.watch.opened)
	h.bringLive(a, agentLevelRow("ag-1", statusActive, idle))
	h.requireReady()

	a.watch.endStreamWithErr(nil)
	assert.Equal(t, "the worker ended the watch", h.next().Message, "a stream that ended without a reason")
	h.clock.Advance(testutil.WaitForTimer(t, h.ctx, reconnect)).MustWait(h.ctx)
	receive(t, h.ctx, a.watch.opened)
	a.watch.ack()
	receive(t, h.ctx, a.reads.started)
	a.reads.answers <- agentReadAnswer{err: errors.New("the worker cannot read its agents")}

	gap := h.next()
	assert.Equal(t, kindStreamGap, gap.Kind)
	assert.Equal(t, lostKinds(sourceWorker, true), gap.Lost)
	a.watch.push(activityChangedFrame("ag-1", working))
	assert.Equal(t, "working", h.next().State, "the watch delivers again")
}

// The hub's stream that drops prints stream.unavailable, subscribes again,
// and compares the new snapshot with the state that it held: stream.gap,
// then each workspace and tab that changed during the gap, reconciled.
func TestEventStream_TheHubStreamThatDropsPrintsAGap(t *testing.T) {
	var reconnect *quartz.Trap
	h := startStreamHarness(t, streamHarnessOptions{beforeRun: func(clock *quartz.Mock) {
		reconnect = trapNewTimer(t, clock, hubReconnectTimerTag)
	}})
	hub := h.session()
	hub.send(snapshotEvent([]string{"ws-1", "ws-2"},
		agentTabRecord("ag-1", "ws-1", "worker-A"), agentTabRecord("ag-2", "ws-1", "worker-A")))
	a := h.worker("worker-A")
	receive(t, h.ctx, a.watch.opened)
	h.bringLive(a, agentLevelRow("ag-1", statusActive, idle), agentLevelRow("ag-2", statusActive, idle))
	h.requireReady()

	hub.end(errors.New("connection reset by peer"))
	down := h.next()
	assert.Equal(t, kindStreamUnavailable, down.Kind)
	assert.Equal(t, "hub", down.Source)
	assert.Contains(t, down.Message, "connection reset by peer")
	h.clock.Advance(testutil.WaitForTimer(t, h.ctx, reconnect)).MustWait(h.ctx)

	again := h.session()
	again.send(snapshotEvent([]string{"ws-1", "ws-3"},
		agentTabRecord("ag-1", "ws-1", "worker-A"), agentTabRecord("ag-3", "ws-3", "worker-A")))
	gap := h.next()
	assert.Equal(t, kindStreamGap, gap.Kind)
	assert.Equal(t, "hub", gap.Source)
	assert.Equal(t, []string{kindWorkspaceRenamed}, gap.Lost)
	assert.Empty(t, gap.TabIDs)

	var got []string
	for range 4 {
		ev := h.next()
		assert.True(t, ev.Reconciled, "%s happened during the gap", ev.Kind)
		got = append(got, ev.Kind+" "+ev.WorkspaceID+" "+ev.TabID)
	}
	assert.Equal(t, []string{
		"workspace.created ws-3 ",
		"tab.closed ws-1 ag-2",
		"tab.opened ws-3 ag-3",
		"workspace.deleted ws-2 ",
	}, got, "a workspace opens before its tabs, and closes after them")

	assert.Equal(t, []string{"ag-1", "ag-3"}, watchedAgents(t, receive(t, h.ctx, a.watch.updated)))
	a.watch.ack()
	assert.Equal(t, []string{"ag-3"}, receive(t, h.ctx, a.reads.started))
	a.reads.answer(agentLevelRow("ag-3", statusActive, waiting))
	status, activity := h.next(), h.next()
	assert.Equal(t, []string{"ag-3", "ag-3"}, []string{status.TabID, activity.TabID})
	assert.Equal(t, "waiting_for_user", activity.State, "a tab that joined during the gap prints its state too")
}

// An outage that lasts several sessions of the hub prints one
// stream.unavailable, and its stream.gap counts from the first drop.
func TestEventStream_AHubOutageOfSeveralSessionsPrintsOneUnavailable(t *testing.T) {
	var reconnect *quartz.Trap
	h := startStreamHarness(t, streamHarnessOptions{beforeRun: func(clock *quartz.Mock) {
		reconnect = trapNewTimer(t, clock, hubReconnectTimerTag)
	}})
	hub := h.session()
	hub.send(snapshotEvent([]string{"ws-1"}))
	h.requireReady()
	dropped := h.clock.Now()

	hub.end(errors.New("connection reset by peer"))
	assert.Equal(t, kindStreamUnavailable, h.next().Kind)
	first := testutil.WaitForTimer(t, h.ctx, reconnect)
	h.clock.Advance(first).MustWait(h.ctx)
	h.session().end(errors.New("dial: connection refused"))
	second := testutil.WaitForTimer(t, h.ctx, reconnect)
	h.clock.Advance(second).MustWait(h.ctx)
	h.session().send(snapshotEvent([]string{"ws-1"}))

	gap := h.next()
	require.Equal(t, kindStreamGap, gap.Kind, "a session that fails during the outage prints nothing")
	require.NotNil(t, gap.Since)
	assert.True(t, gap.Since.Equal(dropped), "since: %s, dropped: %s", gap.Since, dropped)
	assert.Contains(t, gap.Summary, "after a gap of "+(first+second).String())
}

// An outage that lasts several sessions of a Worker's watch prints one
// stream.unavailable, and its stream.gap counts from the first drop.
func TestEventStream_AWorkerOutageOfSeveralSessionsPrintsOneUnavailable(t *testing.T) {
	var reconnect *quartz.Trap
	h := startStreamHarness(t, streamHarnessOptions{beforeRun: func(clock *quartz.Mock) {
		reconnect = trapNewTimer(t, clock, feedReconnectTimerTag)
	}})
	h.session().send(snapshotEvent([]string{"ws-1"}, terminalTabRecord("tm-1", "ws-1", "worker-A")))
	a := h.worker("worker-A")
	receive(t, h.ctx, a.watch.opened)
	h.bringLive(a)
	h.requireReady()
	dropped := h.clock.Now()

	a.watch.endStreamWithErr(connect.NewError(connect.CodeUnavailable, "worker disconnected"))
	assert.Equal(t, kindStreamUnavailable, h.next().Kind)
	a.mu.Lock()
	a.openErr = errors.New("dial unix: connection refused")
	a.mu.Unlock()
	first := testutil.WaitForTimer(t, h.ctx, reconnect)
	h.clock.Advance(first).MustWait(h.ctx)
	second := testutil.WaitForTimer(t, h.ctx, reconnect)
	a.mu.Lock()
	a.openErr = nil
	a.mu.Unlock()
	h.clock.Advance(second).MustWait(h.ctx)
	receive(t, h.ctx, a.watch.opened)
	h.bringLive(a)

	gap := h.next()
	require.Equal(t, kindStreamGap, gap.Kind, "a session that fails during the outage prints nothing")
	require.NotNil(t, gap.Since)
	assert.True(t, gap.Since.Equal(dropped), "since: %s, dropped: %s", gap.Since, dropped)
	assert.Contains(t, gap.Summary, "after a gap of "+(first+second).String())
}

// A Worker that refuses the watch for good is an event: the stream goes on
// with the other Workers, and stream.ready says that one cannot deliver.
func TestEventStream_AWorkerThatRefusesIsAnEventNotTheEnd(t *testing.T) {
	h := startStreamHarness(t, streamHarnessOptions{})
	b := h.worker("worker-B")
	b.openErr = connect.NewError(connect.CodePermissionDenied, "the credential may not use worker-B")
	hub := h.session()
	hub.send(snapshotEvent([]string{"ws-1"}, agentTabRecord("ag-1", "ws-1", "worker-A"), agentTabRecord("ag-2", "ws-1", "worker-B")))
	a := h.worker("worker-A")
	receive(t, h.ctx, a.watch.opened)
	h.bringLive(a, agentLevelRow("ag-1", statusActive, idle))

	refused := h.next()
	assert.Equal(t, kindStreamUnavailable, refused.Kind)
	assert.Equal(t, "worker-B", refused.WorkerID)
	assert.Equal(t, string(cli.CodePermissionDenied), refused.Code)
	require.NotNil(t, refused.Retrying)
	assert.False(t, *refused.Retrying)
	assert.Contains(t, refused.Summary, "the stream does not try again")
	ready := h.next()
	assert.Equal(t, kindStreamReady, ready.Kind)
	assert.Equal(t, 1, ready.Scope.WorkersLive)
	assert.Contains(t, ready.Summary, "1 of them cannot deliver now")

	a.watch.push(turnEndFrame("ag-1"))
	assert.Equal(t, kindAgentTurnEnd, h.next().Kind)
	hub.send(workersChangedEvent())
	hub.send(materializedTab(agentTabRecord("ag-3", "ws-1", "worker-A")))
	hub.send(batchEndEvent())
	assert.Equal(t, "ag-3", h.next().TabID, "a refused Worker is not woken, and the stream goes on")
}

// A Worker that is offline when the stream starts is an event, and the
// stream watches it once the hub says that a Worker came online.
func TestEventStream_AWorkerThatIsOfflineIsWatchedOnceItComesOnline(t *testing.T) {
	var reconnect *quartz.Trap
	h := startStreamHarness(t, streamHarnessOptions{beforeRun: func(clock *quartz.Mock) {
		reconnect = trapNewTimer(t, clock, feedReconnectTimerTag)
	}})
	b := h.worker("worker-B")
	b.openErr = errors.New("dial unix: connection refused")
	started := h.clock.Now()
	hub := h.session()
	hub.send(snapshotEvent([]string{"ws-1"}, terminalTabRecord("tm-1", "ws-1", "worker-B")))

	down := h.next()
	assert.Equal(t, kindStreamUnavailable, down.Kind)
	require.NotNil(t, down.Retrying)
	assert.True(t, *down.Retrying)
	assert.Equal(t, 0, h.requireReady().WorkersLive)
	testutil.WaitForTimer(t, h.ctx, reconnect)

	b.mu.Lock()
	b.openErr = nil
	b.mu.Unlock()
	hub.send(workersChangedEvent())
	assert.Equal(t, []string{"tm-1"}, watchedTerminals(t, receive(t, h.ctx, b.watch.opened)),
		"the hub's word that a Worker changed ends the backoff at once")
	h.bringLive(b)
	gap := h.next()
	assert.Equal(t, kindStreamGap, gap.Kind)
	require.NotNil(t, gap.Since)
	assert.True(t, gap.Since.Equal(started), "a Worker that never delivered lost everything since the stream watched it")
	assert.Equal(t, []string{"tm-1"}, gap.TabIDs)
}

// A Worker that does not acknowledge the watch is reported unavailable, so
// stream.ready does not wait for ever. A late acknowledgement is a gap.
func TestEventStream_AnAcknowledgementThatDoesNotArriveIsAnEvent(t *testing.T) {
	var ackTimer *quartz.Trap
	h := startStreamHarness(t, streamHarnessOptions{beforeRun: func(clock *quartz.Mock) {
		ackTimer = trapNewTimer(t, clock, feedAckTimerTag)
	}})
	h.session().send(snapshotEvent([]string{"ws-1"}, terminalTabRecord("tm-1", "ws-1", "worker-A")))
	a := h.worker("worker-A")
	assert.Equal(t, watchAckTimeout, testutil.WaitForTimer(t, h.ctx, ackTimer))
	receive(t, h.ctx, a.watch.opened)
	h.requireQuiet()
	h.clock.Advance(watchAckTimeout).MustWait(h.ctx)

	down := h.next()
	assert.Equal(t, kindStreamUnavailable, down.Kind)
	assert.Contains(t, down.Message, "did not acknowledge the watch")
	h.requireReady()

	a.watch.ack()
	assert.Equal(t, kindStreamGap, h.next().Kind)
	a.watch.push(bellFrame("tm-1"))
	assert.Equal(t, kindTerminalBell, h.next().Kind)
}

// The first failure of the hub ends the stream with its code, so a command
// that cannot reach the hub fails at once.
func TestEventStream_TheFirstHubFailureEndsTheStream(t *testing.T) {
	h := startStreamHarness(t, streamHarnessOptions{})
	h.session().end(connect.NewError(connect.CodeUnauthenticated, "the token expired"))
	err := h.result()
	require.Error(t, err)
	assert.Equal(t, cli.CodeNotLoggedIn, cli.FromError(err).Code)
	h.requireQuiet()
}

// --workspace ends the stream when its workspace is deleted, after the
// events of its tabs.
func TestEventStream_EndsWhenTheWatchedWorkspaceIsDeleted(t *testing.T) {
	h := startStreamHarness(t, streamHarnessOptions{workspace: "ws-1"})
	hub := h.session()
	hub.send(snapshotEvent([]string{"ws-1"}, agentTabRecord("ag-1", "ws-1", "worker-A")))
	a := h.worker("worker-A")
	receive(t, h.ctx, a.watch.opened)
	h.bringLive(a, agentLevelRow("ag-1", statusActive, idle))
	h.requireReady()

	hub.send(batchEvent(closeTabOp(leapmuxv1.TabType_TAB_TYPE_AGENT, "ag-1", 20)))
	hub.send(batchEndEvent())
	hub.send(workspaceDeletedEvent("ws-1"))
	assert.Equal(t, kindTabClosed, h.next().Kind)
	assert.Equal(t, kindWorkspaceDeleted, h.next().Kind)
	require.NoError(t, h.result(), "nothing can happen in a deleted workspace, so the stream ends cleanly")
}

// --workspace refuses a workspace that the first snapshot does not hold.
func TestEventStream_RefusesAWatchedWorkspaceThatTheHubDoesNotHold(t *testing.T) {
	h := startStreamHarness(t, streamHarnessOptions{workspace: "ws-9"})
	h.session().send(snapshotEvent([]string{"ws-1"}))
	err := h.result()
	assert.Equal(t, cli.CodeNotFound, cli.FromError(err).Code)
	assert.Contains(t, err.Error(), "ws-9")
}

// --limit ends the stream after the events that it allows on stdout.
func TestEventStream_EndsAfterTheLimit(t *testing.T) {
	h := startStreamHarness(t, streamHarnessOptions{kinds: []string{"agent.turn_end"}, limit: 2})
	h.session().send(snapshotEvent([]string{"ws-1"}, agentTabRecord("ag-1", "ws-1", "worker-A")))
	a := h.worker("worker-A")
	receive(t, h.ctx, a.watch.opened)
	h.bringLive(a, agentLevelRow("ag-1", statusActive, idle))
	assert.Contains(t, h.nextNotice(), " stream.ready ", "stream.ready goes to stderr, so it does not count")

	a.watch.push(turnEndFrame("ag-1"))
	a.watch.push(turnEndFrame("ag-1"))
	a.watch.push(turnEndFrame("ag-1"))
	assert.Equal(t, kindAgentTurnEnd, h.next().Kind)
	assert.Equal(t, kindAgentTurnEnd, h.next().Kind)
	require.NoError(t, h.result())
	h.requireQuiet()
}

// --kind selects what prints on stdout. A stream event that it does not
// select prints its text line on stderr, so a filter never hides a gap.
func TestEventStream_AFilterSendsTheStreamEventsThatItDropsToStderr(t *testing.T) {
	h := startStreamHarness(t, streamHarnessOptions{kinds: []string{"terminal.bell"}})
	hub := h.session()
	hub.send(snapshotEvent([]string{"ws-1"}, agentTabRecord("ag-1", "ws-1", "worker-A"), terminalTabRecord("tm-1", "ws-1", "worker-A")))
	a := h.worker("worker-A")
	req := receive(t, h.ctx, a.watch.opened)
	assert.Empty(t, req.GetAgents(), "terminal-only filter does not watch agents")
	assert.Equal(t, []string{"tm-1"}, watchedTerminals(t, req))
	h.bringLive(a)
	notice := h.nextNotice()
	assert.Contains(t, notice, " stream.ready watching 1 workspace and 2 tabs on 1 worker")

	a.watch.push(turnEndFrame("ag-1"))
	a.watch.push(bellFrame("tm-1"))
	assert.Equal(t, kindTerminalBell, h.next().Kind, "an agent event that --kind does not select prints nothing")
	hub.end(errors.New("connection reset by peer"))
	assert.Contains(t, h.nextNotice(), " stream.unavailable source=hub ")
	h.requireQuiet()
}

// --heartbeat prints stream.heartbeat at its interval, whatever --kind
// selects, and --limit does not count it.
func TestEventStream_PrintsAHeartbeat(t *testing.T) {
	var ticker *quartz.Trap
	h := startStreamHarness(t, streamHarnessOptions{kinds: []string{"agent"}, limit: 1, heartbeat: 30 * time.Second, beforeRun: func(clock *quartz.Mock) {
		ticker = clock.Trap().NewTicker(eventHeartbeatTimerTag)
		t.Cleanup(ticker.Close)
	}})
	assert.Equal(t, 30*time.Second, testutil.WaitForTimer(t, h.ctx, ticker))
	h.session().send(snapshotEvent([]string{"ws-1"}))
	assert.Contains(t, h.nextNotice(), "stream.ready")

	for range 2 {
		h.clock.Advance(30 * time.Second).MustWait(h.ctx)
		beat := h.next()
		assert.Equal(t, kindStreamHeartbeat, beat.Kind)
		assert.Equal(t, &scopeCounts{Workspaces: 1, HubLive: true}, beat.Scope)
		assert.Equal(t, "the hub delivers; 0 of 0 workers deliver", beat.Summary)
	}
	select {
	case err := <-h.done:
		t.Fatalf("a heartbeat counted toward --limit: %v", err)
	default:
	}
}

// Ctrl-C ends the stream without a failure, and the stream cancels every
// watch that it opened.
func TestEventStream_StopsWithItsContext(t *testing.T) {
	h := startStreamHarness(t, streamHarnessOptions{})
	h.session().send(snapshotEvent([]string{"ws-1"}, terminalTabRecord("tm-1", "ws-1", "worker-A")))
	a := h.worker("worker-A")
	receive(t, h.ctx, a.watch.opened)
	h.bringLive(a)
	h.requireReady()

	h.cancel()
	select {
	case err := <-h.done:
		require.NoError(t, err)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the stream did not end with its context")
	}
	receive(t, testutil.DeadlineContext(t), a.watch.cancelled)
}

// The text form prints one line per event, as TextLine states it.
func TestEventStream_TheTextFormPrintsOneLinePerEvent(t *testing.T) {
	d := newDirectStream(t)
	d.cliPrinter.Format = cli.FormatText
	d.agent(t, &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: &leapmuxv1.AgentTurnEnd{}}})
	line := strings.TrimSuffix(d.out.String(), "\n")
	assert.NotContains(t, line, "\n")
	fields := strings.Fields(line)
	require.GreaterOrEqual(t, len(fields), 5)
	assert.Equal(t, []string{"agent.turn_end", "workspace=ws-1", "tab=ag-1", "worker=worker-A"}, fields[1:5])
	assert.True(t, strings.HasSuffix(line, " the turn ended"))
}

// stream.ready waits for each Worker that hosts a tab of the scope. A watch
// that stops before it settled, because its last tab left, no longer holds
// stream.ready back.
func TestEventStream_AWatchThatStopsBeforeItSettledDoesNotHoldReadyBack(t *testing.T) {
	h := startStreamHarness(t, streamHarnessOptions{})
	hub := h.session()
	hub.send(snapshotEvent([]string{"ws-1"}, terminalTabRecord("tm-1", "ws-1", "worker-A"), terminalTabRecord("tm-2", "ws-1", "worker-B")))
	a, b := h.worker("worker-A"), h.worker("worker-B")
	receive(t, h.ctx, a.watch.opened)
	receive(t, h.ctx, b.watch.opened)
	h.bringLive(a)
	// An event prints before stream.ready too. The watch sends its first
	// levels before this bell, so the stream took them when the bell prints.
	a.watch.push(bellFrame("tm-1"))
	assert.Equal(t, kindTerminalBell, h.next().Kind)
	h.requireQuiet()

	hub.send(batchEvent(closeTabOp(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "tm-2", 20)))
	hub.send(batchEndEvent())
	assert.Equal(t, kindTabClosed, h.next().Kind)
	receive(t, h.ctx, b.watch.cancelled)
	scope := h.requireReady()
	assert.Equal(t, 1, scope.Workers)
	assert.Equal(t, 1, scope.WorkersLive)
}

func TestEventStream_HeartbeatDoesNotPrintBeforeReady(t *testing.T) {
	var ticker *quartz.Trap
	h := startStreamHarness(t, streamHarnessOptions{heartbeat: 30 * time.Second, beforeRun: func(clock *quartz.Mock) {
		ticker = clock.Trap().NewTicker(eventHeartbeatTimerTag)
		t.Cleanup(ticker.Close)
	}})
	assert.Equal(t, 30*time.Second, testutil.WaitForTimer(t, h.ctx, ticker))
	// Advance before snapshot/stream.ready: must not emit stream.heartbeat.
	h.clock.Advance(30 * time.Second).MustWait(h.ctx)
	h.session().send(snapshotEvent([]string{"ws-1"}))
	ready := h.requireReady()
	assert.Equal(t, 1, ready.Workspaces)

	h.clock.Advance(30 * time.Second).MustWait(h.ctx)
	assert.Equal(t, kindStreamHeartbeat, h.next().Kind)
}

func TestEventStream_NoReconnectReportsRetryingFalseOnWorkerDisconnect(t *testing.T) {
	h := startStreamHarness(t, streamHarnessOptions{noReconnect: true})
	h.session().send(snapshotEvent([]string{"ws-1"}, terminalTabRecord("tm-1", "ws-1", "worker-A")))
	a := h.worker("worker-A")
	receive(t, h.ctx, a.watch.opened)
	h.bringLive(a)
	h.requireReady()

	a.watch.endStreamWithErr(connect.NewError(connect.CodeUnavailable, "worker stopped"))
	down := h.next()
	assert.Equal(t, kindStreamUnavailable, down.Kind)
	assert.Equal(t, "worker-A", down.WorkerID)
	require.NotNil(t, down.Retrying)
	assert.False(t, *down.Retrying)
}

func TestEventStream_EarlyLiveAgentEventDoesNotSuppressOtherFieldInAgentLevel(t *testing.T) {
	t.Run("early activity does not suppress initial status", func(t *testing.T) {
		d := newDirectStream(t)
		d.tabs["ag-2"] = scopedTab{ID: "ag-2", Type: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkspaceID: "ws-1", WorkerID: "worker-A"}
		track := d.agentTrack("ag-2")
		track.announceStatus = true
		track.announceActivity = true

		d.agent(t, &leapmuxv1.AgentEvent{
			AgentId: "ag-2",
			Event:   &leapmuxv1.AgentEvent_ActivityChanged{ActivityChanged: &leapmuxv1.AgentActivityChanged{State: working}},
		})
		require.NoError(t, d.agentLevel("ag-2", agentLevelRow("ag-2", statusActive, idle), false))

		got := d.events(t)
		require.Equal(t, []string{kindAgentActivity, kindAgentStatus}, kindsOf(got))
		assert.Equal(t, "working", got[0].State)
		assert.Equal(t, "active", got[1].Status)
	})

	t.Run("early status does not suppress initial activity", func(t *testing.T) {
		d := newDirectStream(t)
		d.tabs["ag-2"] = scopedTab{ID: "ag-2", Type: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkspaceID: "ws-1", WorkerID: "worker-A"}
		track := d.agentTrack("ag-2")
		track.announceStatus = true
		track.announceActivity = true

		d.agent(t, &leapmuxv1.AgentEvent{
			AgentId: "ag-2",
			Event:   &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{Status: statusStarting}},
		})
		require.NoError(t, d.agentLevel("ag-2", agentLevelRow("ag-2", statusActive, working), false))

		got := d.events(t)
		require.Equal(t, []string{kindAgentStatus, kindAgentActivity}, kindsOf(got))
		assert.Equal(t, "starting", got[0].Status)
		assert.Equal(t, "working", got[1].State)
	})
}

func TestEventStream_KindFilterSkipsUnneededWorkerFeeds(t *testing.T) {
	t.Run("workspace-only opens no worker watch and emits ready immediately", func(t *testing.T) {
		h := startStreamHarness(t, streamHarnessOptions{kinds: []string{"workspace"}, withPrivate: true})
		h.session().send(snapshotEvent([]string{"ws-1"},
			agentTabRecord("ag-1", "ws-1", "worker-A"),
			terminalTabRecord("tm-1", "ws-1", "worker-B"),
		))
		notice := h.nextNotice()
		assert.Contains(t, notice, " stream.ready watching 1 workspace and 2 tabs on 0 workers")
		select {
		case <-h.worker("worker-A").watch.opened:
			t.Fatal("workspace-only filter opened a worker watch")
		case <-h.worker("worker-A").privateSessions:
			t.Fatal("workspace-only filter opened a private stream")
		default:
		}
	})

	t.Run("agent-only skips private feed", func(t *testing.T) {
		h := startStreamHarness(t, streamHarnessOptions{kinds: []string{"agent"}, withPrivate: true})
		h.session().send(snapshotEvent([]string{"ws-1"}, agentTabRecord("ag-1", "ws-1", "worker-A")))
		a := h.worker("worker-A")
		receive(t, h.ctx, a.watch.opened)
		h.bringLive(a, agentLevelRow("ag-1", statusActive, idle))
		assert.Contains(t, h.nextNotice(), " stream.ready ")
		select {
		case <-a.privateSessions:
			t.Fatal("agent-only filter opened a private stream")
		default:
		}
	})
}

func TestEventStream_PrivateFeedOnFileOnlyWorkerAndOutageRecovery(t *testing.T) {
	var reconnect *quartz.Trap
	h := startStreamHarness(t, streamHarnessOptions{
		withPrivate: true,
		beforeRun: func(clock *quartz.Mock) {
			reconnect = trapNewTimer(t, clock, privateReconnectTimerTag)
		},
	})
	hub := h.session()
	hub.send(snapshotEvent([]string{"ws-1"}, fileTabRecord("file-1", "ws-1", "worker-F")))
	f := h.worker("worker-F")
	priv1 := receive(t, h.ctx, f.privateSessions)
	scope := h.requireReady()
	assert.Equal(t, &scopeCounts{Workspaces: 1, Tabs: 1, Workers: 1, WorkersLive: 1, HubLive: true}, scope)
	select {
	case <-f.watch.opened:
		t.Fatal("file-only worker should not open WatchEvents")
	default:
	}

	priv1.send(&leapmuxv1.WorkerPrivateEvent{
		Event: &leapmuxv1.WorkerPrivateEvent_TabRenamed{
			TabRenamed: &leapmuxv1.TabRenamed{TabId: "file-1", Title: "README.md"},
		},
	})
	renamed := h.next()
	assert.Equal(t, kindTabRenamed, renamed.Kind)
	assert.Equal(t, "file-1", renamed.TabID)
	assert.Equal(t, "README.md", renamed.Title)

	// Private stream outage emits stream.unavailable, backs off, wakes on
	// WorkersChanged, and emits stream.gap on recovery.
	priv1.end(connect.NewError(connect.CodeUnavailable, "private stream lost"))
	down := h.next()
	assert.Equal(t, kindStreamUnavailable, down.Kind)
	assert.Equal(t, "worker-F", down.WorkerID)
	require.NotNil(t, down.Retrying)
	assert.True(t, *down.Retrying)

	testutil.WaitForTimer(t, h.ctx, reconnect)
	hub.send(workersChangedEvent())
	priv2 := receive(t, h.ctx, f.privateSessions)
	priv2.send(&leapmuxv1.WorkerPrivateEvent{
		Event: &leapmuxv1.WorkerPrivateEvent_TabRenamed{
			TabRenamed: &leapmuxv1.TabRenamed{TabId: "file-1", Title: "CHANGELOG.md"},
		},
	})
	gap := h.next()
	assert.Equal(t, kindStreamGap, gap.Kind)
	assert.Equal(t, "worker-F", gap.WorkerID)
	assert.Equal(t, privateLostKinds(), gap.Lost)
	assert.Equal(t, []string{"file-1"}, gap.TabIDs)
	assert.Equal(t, "CHANGELOG.md", h.next().Title)
}

func TestEventStream_LayoutOnlyBatchSkipsReprojection(t *testing.T) {
	h := startStreamHarness(t, streamHarnessOptions{})
	hub := h.session()
	hub.send(snapshotEvent([]string{"ws-1"}, terminalTabRecord("tm-1", "ws-1", "worker-A")))
	a := h.worker("worker-A")
	receive(t, h.ctx, a.watch.opened)
	h.bringLive(a)
	h.requireReady()

	ratioOp := &leapmuxv1.CrdtOp{
		OpId:         "ratio-1",
		CanonicalHlc: testHLC(20),
		Body: &leapmuxv1.CrdtOp_SetNodeRegister{
			SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				NodeId: rootOf("ws-1"),
				Field:  &leapmuxv1.SetNodeRegisterOp_Ratios{Ratios: &leapmuxv1.DoubleList{Values: []float64{0.4, 0.6}}},
			},
		},
	}
	hub.send(batchEvent(ratioOp))
	hub.send(batchEndEvent())
	a.watch.push(bellFrame("tm-1"))
	assert.Equal(t, kindTerminalBell, h.next().Kind)
	h.requireQuiet()
}
