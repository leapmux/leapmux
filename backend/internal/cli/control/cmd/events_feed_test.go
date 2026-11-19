package cmd

import (
	"context"
	"errors"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// feedHarness runs one workerFeed against a recordingWatch.
type feedHarness struct {
	t     *testing.T
	ctx   context.Context
	clock *quartz.Mock
	watch *recordingWatch
	reads *agentReads
	feed  *workerFeed
	out   chan feedOutput
	// openErrs fails the next opens of the transport, one error each.
	mu       sync.Mutex
	openErrs []error
}

func startFeedHarness(t *testing.T, interest watchInterest, beforeStart func(clock *quartz.Mock)) *feedHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	var wg sync.WaitGroup
	// Registered first, so it runs after the cleanups of the traps.
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	h := &feedHarness{t: t, ctx: ctx, clock: testutil.NewQuartzMock(t), watch: newRecordingWatch(), reads: newAgentReads(), out: make(chan feedOutput, 64)}
	if beforeStart != nil {
		beforeStart(h.clock)
	}
	h.feed = newWorkerFeed("worker-A", workerFeedSource{
		openTransport: func(context.Context) (streamevents.Transport, func(), error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.openErrs) > 0 {
				err := h.openErrs[0]
				h.openErrs = h.openErrs[1:]
				return nil, nil, err
			}
			return h.watch, func() {}, nil
		},
		readAgents: h.reads.read,
		clock:      h.clock,
	}, h.out)
	h.feed.setInterest(interest)
	h.feed.start(ctx, &wg)
	return h
}

// next waits for the next output of the feed.
func (h *feedHarness) next() feedOutput {
	h.t.Helper()
	return receive(h.t, h.ctx, h.out)
}

func (h *feedHarness) requireNoOutput() {
	h.t.Helper()
	select {
	case out := <-h.out:
		h.t.Fatalf("unexpected output %+v", out)
	default:
	}
}

func (h *feedHarness) requireNoRead() {
	h.t.Helper()
	select {
	case agents := <-h.reads.started:
		h.t.Fatalf("unexpected read of %v", agents)
	default:
	}
}

// A change that arrives during the read of the agents can be newer than the
// read, so the feed sends it after the rows. Sent before them, the stale row
// would make the stream drop the newer change as a repeat.
func TestWorkerFeed_HoldsTheEventsThatArriveDuringTheRead(t *testing.T) {
	h := startFeedHarness(t, watchInterest{agents: []string{"a-1"}, terminals: []string{"t-1"}}, nil)
	req := receive(t, h.ctx, h.watch.opened)
	assert.Equal(t, []string{"a-1"}, watchedAgents(t, req))
	assert.Equal(t, []string{"t-1"}, watchedTerminals(t, req))

	h.watch.push(turnEndFrame("a-1"))
	assert.Equal(t, feedAgentEvent, h.next().kind, "an event before the acknowledgement passes at once")

	h.watch.ack()
	assert.Equal(t, []string{"a-1"}, receive(t, h.ctx, h.reads.started))
	h.watch.push(activityChangedFrame("a-1", idle))
	h.watch.push(bellFrame("t-1"))
	h.requireNoOutput()
	h.reads.answer(agentLevelRow("a-1", statusActive, working))

	levels := h.next()
	assert.Equal(t, feedLevels, levels.kind)
	assert.True(t, levels.first)
	assert.Equal(t, 1, levels.session)
	assert.Equal(t, working, levels.levels["a-1"].GetPublishedActivityState())
	change := h.next()
	assert.Equal(t, feedAgentEvent, change.kind)
	assert.Equal(t, idle, change.agent.GetActivityChanged().GetState())
	bell := h.next()
	assert.Equal(t, feedTerminalEvent, bell.kind)
	assert.NotNil(t, bell.terminal.GetBell(), "the notify frames of a terminal reach the feed")
}

// A watch with no agent needs no read: its acknowledgement makes it live.
func TestWorkerFeed_AWatchOfTerminalsIsLiveAtItsAcknowledgement(t *testing.T) {
	h := startFeedHarness(t, watchInterest{terminals: []string{"t-1"}}, nil)
	receive(t, h.ctx, h.watch.opened)
	h.watch.ack()
	levels := h.next()
	assert.Equal(t, feedLevels, levels.kind)
	assert.True(t, levels.first)
	assert.Empty(t, levels.levels)
	h.requireNoRead()
}

// A revision travels on the same stream, and its acknowledgement reads only
// the agents that the session did not read. An acknowledgement during a read
// asks for one read more, however many arrive.
func TestWorkerFeed_ReadsOnlyTheAgentsThatJoined(t *testing.T) {
	h := startFeedHarness(t, watchInterest{agents: []string{"a-1"}}, nil)
	receive(t, h.ctx, h.watch.opened)
	h.watch.ack()
	assert.Equal(t, []string{"a-1"}, receive(t, h.ctx, h.reads.started))

	h.feed.setInterest(watchInterest{agents: []string{"a-1", "a-2"}})
	assert.Equal(t, []string{"a-1", "a-2"}, watchedAgents(t, receive(t, h.ctx, h.watch.revised)))
	h.watch.ack()
	h.feed.setInterest(watchInterest{agents: []string{"a-1", "a-2", "a-3"}})
	receive(t, h.ctx, h.watch.revised)
	h.watch.ack()
	h.requireNoRead()
	h.reads.answer(agentLevelRow("a-1", statusActive, idle))
	first := h.next()
	assert.True(t, first.first)
	assert.Contains(t, first.levels, "a-1")

	assert.Equal(t, []string{"a-2", "a-3"}, receive(t, h.ctx, h.reads.started), "one read covers both revisions")
	h.reads.answer(agentLevelRow("a-2", statusActive, idle), agentLevelRow("a-3", statusActive, idle))
	second := h.next()
	assert.False(t, second.first)
	assert.Len(t, second.levels, 2)

	h.feed.setInterest(watchInterest{agents: []string{"a-2", "a-3"}})
	receive(t, h.ctx, h.watch.revised)
	h.watch.ack()
	// The sequencer reads the interest when it takes the acknowledgement. An
	// event after the acknowledgement comes out after it was taken.
	h.watch.push(turnEndFrame("a-2"))
	assert.Equal(t, feedAgentEvent, h.next().kind)
	h.requireNoRead()
	h.feed.setInterest(watchInterest{agents: []string{"a-1", "a-2", "a-3"}})
	receive(t, h.ctx, h.watch.revised)
	h.watch.ack()
	assert.Equal(t, []string{"a-1"}, receive(t, h.ctx, h.reads.started), "an agent that left and returned is read again")
}

// A session that ends during a read sends the events that the read held,
// then the end, and drops the read: it can no longer make the session live.
func TestWorkerFeed_ASessionThatEndsDuringAReadDropsTheRead(t *testing.T) {
	var reconnect *quartz.Trap
	h := startFeedHarness(t, watchInterest{agents: []string{"a-1"}}, func(clock *quartz.Mock) {
		reconnect = trapNewTimer(t, clock, feedReconnectTimerTag)
	})
	receive(t, h.ctx, h.watch.opened)
	h.watch.ack()
	receive(t, h.ctx, h.reads.started)
	h.watch.push(turnEndFrame("a-1"))
	h.watch.endStream(connect.NewError(connect.CodeUnavailable, "worker disconnected"))

	held := h.next()
	assert.Equal(t, feedAgentEvent, held.kind, "a held event is real, so it still goes out")
	down := h.next()
	assert.Equal(t, feedDown, down.kind)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(down.err), "the end reason of the stream")
	h.reads.answer(agentLevelRow("a-1", statusActive, idle))
	h.requireNoOutput()

	h.clock.Advance(testutil.WaitForTimer(t, h.ctx, reconnect)).MustWait(h.ctx)
	receive(t, h.ctx, h.watch.opened)
	h.watch.ack()
	assert.Equal(t, []string{"a-1"}, receive(t, h.ctx, h.reads.started))
	h.reads.answer(agentLevelRow("a-1", statusActive, working))
	levels := h.next()
	assert.True(t, levels.first)
	assert.Equal(t, 2, levels.session)
	assert.Equal(t, working, levels.levels["a-1"].GetPublishedActivityState(), "the result of the stale read never went out")
}

// Each tab that an acknowledgement rejects reaches the stream, marked as an
// agent or a terminal, before the levels that the acknowledgement asks for.
func TestWorkerFeed_ReportsTheTabsThatAnAcknowledgementRejects(t *testing.T) {
	h := startFeedHarness(t, watchInterest{agents: []string{"a-1", "a-2"}, terminals: []string{"t-1"}}, nil)
	receive(t, h.ctx, h.watch.opened)
	h.watch.push(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_UpdateAck{UpdateAck: &leapmuxv1.WatchUpdateAck{
		UpdateId:          h.watch.lastRequest().GetUpdateId(),
		RejectedAgents:    []*leapmuxv1.WatchRejection{{EntityId: "a-2", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND}},
		RejectedTerminals: []*leapmuxv1.WatchRejection{{EntityId: "t-1", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_FORBIDDEN}},
	}}})

	agent := h.next()
	assert.Equal(t, feedRejected, agent.kind)
	assert.Equal(t, "a-2", agent.rejection.GetEntityId())
	assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND, agent.rejection.GetReason())
	assert.False(t, agent.rejectedTerminal)
	terminal := h.next()
	assert.Equal(t, feedRejected, terminal.kind)
	assert.Equal(t, "t-1", terminal.rejection.GetEntityId())
	assert.True(t, terminal.rejectedTerminal)

	assert.Equal(t, []string{"a-1", "a-2"}, receive(t, h.ctx, h.reads.started), "a rejection does not hold the acknowledgement back")
	h.reads.answer(agentLevelRow("a-1", statusActive, idle))
	levels := h.next()
	assert.Equal(t, feedLevels, levels.kind)
	assert.True(t, levels.first)
}

// An item that a session sends after the next session started belongs to a
// watch that ended, so the feed drops it.
func TestWorkerFeed_DropsTheLateItemsOfAnEndedSession(t *testing.T) {
	out := make(chan feedOutput, 8)
	q := newFeedSequencer(&workerFeed{workerID: "worker-A", source: workerFeedSource{clock: testutil.NewQuartzMock(t)}, out: out})
	t.Cleanup(q.stopAckTimer)
	ctx := testutil.DeadlineContext(t)
	require.True(t, q.input(ctx, feedInput{kind: inputSessionStarted, session: 2}))

	late := []feedInput{
		{kind: inputAgent, session: 1, agent: &leapmuxv1.AgentEvent{AgentId: "a-1", Event: &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: &leapmuxv1.AgentTurnEnd{}}}},
		{kind: inputTerminal, session: 1, terminal: &leapmuxv1.TerminalEvent{TerminalId: "t-1", Event: &leapmuxv1.TerminalEvent_Bell{Bell: &leapmuxv1.TerminalBell{}}}},
		{kind: inputDown, session: 1, err: errWatchEnded},
		{kind: inputAck, session: 1, ack: &leapmuxv1.WatchUpdateAck{}},
	}
	for _, in := range late {
		require.True(t, q.input(ctx, in))
	}
	require.Empty(t, out, "no late item of session 1 reaches the stream")
	assert.True(t, q.firstOwed, "a late end of session 1 does not end session 2")

	require.True(t, q.input(ctx, feedInput{kind: inputTerminal, session: 2, terminal: late[1].terminal}))
	assert.Equal(t, feedTerminalEvent, receive(t, ctx, out).kind, "an item of the current session passes")
}

// A refusal for good ends the feed without a reconnect.
func TestWorkerFeed_EndsWhenTheWorkerRefusesForGood(t *testing.T) {
	h := startFeedHarness(t, watchInterest{agents: []string{"a-1"}}, nil)
	h.mu.Lock()
	h.openErrs = []error{&codedRPCError{Code: "channel_open_failed", Cause: connect.NewError(connect.CodePermissionDenied, "no access to the worker")}}
	h.mu.Unlock()
	refused := h.next()
	assert.Equal(t, feedRefused, refused.kind)
	assert.Equal(t, cli.CodePermissionDenied, cli.FromError(refused.err).Code)
	h.requireNoOutput()
}

// An offline Worker is down, and wakeUp ends the wait for the next session.
func TestWorkerFeed_WakeUpEndsTheBackoff(t *testing.T) {
	var reconnect *quartz.Trap
	h := startFeedHarness(t, watchInterest{terminals: []string{"t-1"}}, func(clock *quartz.Mock) {
		reconnect = trapNewTimer(t, clock, feedReconnectTimerTag)
	})
	h.mu.Lock()
	h.openErrs = []error{&codedRPCError{Code: "channel_open_failed", Cause: errors.New("the worker is offline")}}
	h.mu.Unlock()
	down := h.next()
	assert.Equal(t, feedDown, down.kind)
	assert.Equal(t, cli.CodeWorkerOffline, cli.FromError(down.err).Code)
	assert.Equal(t, activityReconnectInitial, testutil.WaitForTimer(t, h.ctx, reconnect))

	h.feed.wakeUp()
	receive(t, h.ctx, h.watch.opened)
	h.watch.ack()
	assert.True(t, h.next().first)
}

// A wake that arrives while a session runs is no news for the wait after
// it: that wait lasts its backoff.
func TestWorkerFeed_AWakeDuringASessionDoesNotCutTheNextWait(t *testing.T) {
	var reconnect *quartz.Trap
	h := startFeedHarness(t, watchInterest{terminals: []string{"t-1"}}, func(clock *quartz.Mock) {
		reconnect = trapNewTimer(t, clock, feedReconnectTimerTag)
	})
	receive(t, h.ctx, h.watch.opened)
	h.watch.ack()
	h.next()
	h.feed.wakeUp()
	h.watch.endStream(nil)
	assert.Equal(t, feedDown, h.next().kind)
	wait := testutil.WaitForTimer(t, h.ctx, reconnect)
	select {
	case <-h.watch.opened:
		t.Fatal("a wake from before the drop ended the backoff")
	default:
	}
	h.clock.Advance(wait).MustWait(h.ctx)
	receive(t, h.ctx, h.watch.opened)
}

// The interest of a feed lists each tab once, as a notify watch.
func TestWatchInterest(t *testing.T) {
	w := watchInterest{agents: []string{"a-1", "a-2"}, terminals: []string{"t-1"}}
	assert.False(t, w.empty())
	assert.True(t, watchInterest{}.empty())
	assert.True(t, w.equal(watchInterest{agents: []string{"a-1", "a-2"}, terminals: []string{"t-1"}}))
	assert.False(t, w.equal(watchInterest{agents: []string{"a-1"}, terminals: []string{"t-1"}}))
	assert.False(t, w.equal(watchInterest{agents: []string{"a-1", "a-2"}}))
	assert.Equal(t, []string{"a-1", "a-2", "t-1"}, w.tabIDs())
	req := w.request()
	assert.Equal(t, []string{"a-1", "a-2"}, watchedAgents(t, req))
	assert.Equal(t, []string{"t-1"}, watchedTerminals(t, req))
	for _, entry := range req.GetAgents() {
		assert.Zero(t, entry.GetReplayId(), "a notify watch replays nothing")
		assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_UNSPECIFIED, entry.GetReplay())
	}
	assert.Empty(t, watchInterest{}.request().GetAgents())
}

func TestRefusedForGood(t *testing.T) {
	for _, tc := range []struct {
		err  error
		good bool
	}{
		{nil, false},
		{errors.New("connection reset"), false},
		{connect.NewError(connect.CodeUnavailable, "down"), false},
		{&codedRPCError{Code: "channel_open_failed", Cause: errors.New("offline")}, false},
		{errWatchEnded, false},
		{errNoWatchAck, false},
		{connect.NewError(connect.CodePermissionDenied, "no"), true},
		{connect.NewError(connect.CodeUnauthenticated, "expired"), true},
		{connect.NewError(connect.CodeUnimplemented, "old worker"), true},
		{connect.NewError(connect.CodeNotFound, "no worker"), true},
		{&codedRPCError{Code: "channel_open_failed", Cause: connect.NewError(connect.CodePermissionDenied, "no")}, true},
	} {
		assert.Equal(t, tc.good, refusedForGood(tc.err), "%v", tc.err)
	}
	// The transports record an in-band refusal as it arrived, with its gRPC
	// code: 7 is PermissionDenied, and 14 is Unavailable.
	require.True(t, refusedForGood(&channelwire.RPCError{Code: 7, Message: "refused"}), "an in-band refusal keeps its code")
	require.False(t, refusedForGood(&channelwire.RPCError{Code: 14, Message: "shutting down"}))
}
