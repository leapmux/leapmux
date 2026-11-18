package cmd

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// fakeWatchTransport is a streamevents.Transport that records each
// subscription and lets a test push frames into the newest one.
type fakeWatchTransport struct {
	mu sync.Mutex
	// openErrs fails the next opens, one error each.
	openErrs []error
	requests []*leapmuxv1.WatchEventsRequest
	onFrame  func(*leapmuxv1.WatchEventsResponse)
	end      context.CancelFunc
	// done closes when the newest subscription ended.
	done chan struct{}
	// opened receives one value for each open that succeeded.
	opened chan struct{}
}

func newFakeWatchTransport() *fakeWatchTransport {
	return &fakeWatchTransport{opened: make(chan struct{}, 8)}
}

func (t *fakeWatchTransport) OpenWatchEvents(ctx context.Context, req *leapmuxv1.WatchEventsRequest,
	onFrame func(*leapmuxv1.WatchEventsResponse),
) (streamevents.Handle, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.openErrs) > 0 {
		err := t.openErrs[0]
		t.openErrs = t.openErrs[1:]
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(done)
	}()
	t.requests = append(t.requests, req)
	t.onFrame = onFrame
	t.end = cancel
	t.done = done
	t.opened <- struct{}{}
	return &fakeWatchHandle{cancel: cancel, done: done}, nil
}

type fakeWatchHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (h *fakeWatchHandle) Update(*leapmuxv1.WatchEventsRequest) error { return nil }
func (h *fakeWatchHandle) Cancel()                                    { h.cancel() }
func (h *fakeWatchHandle) Done() <-chan struct{}                      { return h.done }

// push delivers resp on the newest subscription.
func (t *fakeWatchTransport) push(resp *leapmuxv1.WatchEventsResponse) {
	t.mu.Lock()
	onFrame := t.onFrame
	t.mu.Unlock()
	onFrame(resp)
}

// ack acknowledges the newest subscription, with the rejections given.
func (t *fakeWatchTransport) ack(rejected ...*leapmuxv1.WatchRejection) {
	t.mu.Lock()
	id := t.requests[len(t.requests)-1].GetUpdateId()
	t.mu.Unlock()
	t.push(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_UpdateAck{
		UpdateAck: &leapmuxv1.WatchUpdateAck{UpdateId: id, RejectedAgents: rejected},
	}})
}

// endStream ends the newest subscription, as a dropped connection does.
func (t *fakeWatchTransport) endStream() {
	t.mu.Lock()
	end := t.end
	t.mu.Unlock()
	end()
}

func (t *fakeWatchTransport) lastRequest() *leapmuxv1.WatchEventsRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.requests[len(t.requests)-1]
}

func agentFrame(agentID string, event *leapmuxv1.AgentEvent) *leapmuxv1.WatchEventsResponse {
	event.AgentId = agentID
	return &leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: event}}
}

func activityChangedFrame(agentID string, state leapmuxv1.AgentActivityState) *leapmuxv1.WatchEventsResponse {
	return agentFrame(agentID, &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ActivityChanged{
		ActivityChanged: &leapmuxv1.AgentActivityChanged{State: state},
	}})
}

func turnEndFrame(agentID string) *leapmuxv1.WatchEventsResponse {
	return agentFrame(agentID, &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: &leapmuxv1.AgentTurnEnd{}}})
}

// levelReads is a readActivity that answers each call from the channel
// that the test feeds, and reports each call as it starts.
type levelReads struct {
	started chan []string
	answers chan activityLevelRead
}

func newLevelReads() *levelReads {
	return &levelReads{started: make(chan []string, 8), answers: make(chan activityLevelRead, 8)}
}

func (r *levelReads) read(ctx context.Context, agentIDs []string) (map[string]leapmuxv1.AgentActivityState, error) {
	r.started <- agentIDs
	select {
	case answer := <-r.answers:
		return answer.states, answer.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *levelReads) answer(states map[string]leapmuxv1.AgentActivityState) {
	r.answers <- activityLevelRead{states: states}
}

const (
	idle    = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE
	working = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING
	waiting = leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER
)

// startTestFeed starts a feed over tr and reads, with clock for its
// reconnects, and returns the channel of its events.
func startTestFeed(t *testing.T, ctx context.Context, tr *fakeWatchTransport, reads *levelReads, clock quartz.Clock, agentIDs ...string) <-chan activityEvent {
	t.Helper()
	out := make(chan activityEvent, 16)
	startActivityFeed(ctx, activityFeedSource{
		openTransport: func(context.Context) (streamevents.Transport, func(), error) { return tr, func() {}, nil },
		readActivity:  reads.read,
		clock:         clock,
	}, agentIDs, out)
	return out
}

func receive[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatal("timed out waiting for the next value")
		var zero T
		return zero
	}
}

// requireNoEvent fails when the feed sent an event that the test did not
// expect. The feed sends from goroutines, so this checks once after the
// steps that would send it returned.
func requireNoEvent(t *testing.T, events <-chan activityEvent) {
	t.Helper()
	select {
	case ev := <-events:
		t.Fatalf("unexpected event %+v", ev)
	default:
	}
}

func TestActivityFeed_SendsTheLevelOfEachAgentAfterTheAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	tr, reads := newFakeWatchTransport(), newLevelReads()
	events := startTestFeed(t, ctx, tr, reads, testutil.NewQuartzMock(t), "a-1", "a-2")

	receive(t, ctx, tr.opened)
	req := tr.lastRequest()
	require.Len(t, req.GetAgents(), 2)
	for i, id := range []string{"a-1", "a-2"} {
		assert.Equal(t, id, req.GetAgents()[i].GetAgentId())
		assert.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_NOTIFY, req.GetAgents()[i].GetMode(), "a wait needs no content")
	}
	requireNoEvent(t, events)

	tr.ack()
	assert.Equal(t, []string{"a-1", "a-2"}, receive(t, ctx, reads.started), "the level read starts only after the acknowledgement")
	reads.answer(map[string]leapmuxv1.AgentActivityState{"a-1": idle})

	assert.Equal(t, activityEvent{kind: activityLevel, agentID: "a-1", state: idle}, receive(t, ctx, events))
	assert.Equal(t, activityEvent{kind: activityClosed, agentID: "a-2"}, receive(t, ctx, events),
		"an agent that the read leaves out is closed")
}

// A change that arrives during the level read can be newer than the level,
// so the feed sends it after the level. Sent before it, the stale level
// would replace the newer state.
func TestActivityFeed_HoldsTheChangesThatArriveDuringTheLevelRead(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	tr, reads := newFakeWatchTransport(), newLevelReads()
	events := startTestFeed(t, ctx, tr, reads, testutil.NewQuartzMock(t), "a-1")
	receive(t, ctx, tr.opened)

	tr.push(activityChangedFrame("a-1", working))
	assert.Equal(t, activityEvent{kind: activityChange, agentID: "a-1", state: working}, receive(t, ctx, events),
		"a change before the acknowledgement passes at once")

	tr.ack()
	receive(t, ctx, reads.started)
	tr.push(activityChangedFrame("a-1", idle))
	tr.push(turnEndFrame("a-1"))
	requireNoEvent(t, events)
	reads.answer(map[string]leapmuxv1.AgentActivityState{"a-1": working})

	assert.Equal(t, activityEvent{kind: activityLevel, agentID: "a-1", state: working}, receive(t, ctx, events))
	assert.Equal(t, activityEvent{kind: activityChange, agentID: "a-1", state: idle}, receive(t, ctx, events))
	assert.Equal(t, activityEvent{kind: activityTurnEnd, agentID: "a-1"}, receive(t, ctx, events))
}

func TestActivityFeed_ReadsTheLevelAgainForAnAcknowledgementDuringARead(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	tr, reads := newFakeWatchTransport(), newLevelReads()
	events := startTestFeed(t, ctx, tr, reads, testutil.NewQuartzMock(t), "a-1")
	receive(t, ctx, tr.opened)

	tr.ack()
	receive(t, ctx, reads.started)
	tr.ack()
	tr.ack()
	reads.answer(map[string]leapmuxv1.AgentActivityState{"a-1": working})
	assert.Equal(t, activityEvent{kind: activityLevel, agentID: "a-1", state: working}, receive(t, ctx, events))

	receive(t, ctx, reads.started)
	reads.answer(map[string]leapmuxv1.AgentActivityState{"a-1": idle})
	assert.Equal(t, activityEvent{kind: activityLevel, agentID: "a-1", state: idle}, receive(t, ctx, events))
	requireNoEvent(t, events)
	select {
	case <-reads.started:
		t.Fatal("two acknowledgements during one read need one more read, not two")
	default:
	}
}

func TestActivityFeed_ReportsAClosedAgentThatTheWorkerRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	tr, reads := newFakeWatchTransport(), newLevelReads()
	events := startTestFeed(t, ctx, tr, reads, testutil.NewQuartzMock(t), "a-1", "a-2")
	receive(t, ctx, tr.opened)

	tr.ack(&leapmuxv1.WatchRejection{EntityId: "a-2", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND})
	assert.Equal(t, activityEvent{kind: activityClosed, agentID: "a-2"}, receive(t, ctx, events))
	receive(t, ctx, reads.started)
	reads.answer(map[string]leapmuxv1.AgentActivityState{"a-1": idle})
	assert.Equal(t, activityEvent{kind: activityLevel, agentID: "a-1", state: idle}, receive(t, ctx, events))
	assert.Equal(t, activityEvent{kind: activityClosed, agentID: "a-2"}, receive(t, ctx, events))
}

func TestActivityRejection(t *testing.T) {
	_, retried := activityRejection(&leapmuxv1.WatchRejection{EntityId: "a-1", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED})
	assert.False(t, retried, "the subscription retries a failed lookup by itself")

	forbidden, ok := activityRejection(&leapmuxv1.WatchRejection{EntityId: "a-1", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_FORBIDDEN})
	require.True(t, ok)
	assert.Equal(t, activityFailed, forbidden.kind)
	assert.Equal(t, cli.CodePermissionDenied, cli.FromError(forbidden.err).Code)

	unknown, ok := activityRejection(&leapmuxv1.WatchRejection{EntityId: "a-1", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_UNSPECIFIED})
	require.True(t, ok)
	assert.Equal(t, activityFailed, unknown.kind, "the protocol reads an unknown reason as durable")
	assert.Contains(t, unknown.err.Error(), "a-1")
}

func TestActivityFeed_EndsWhenTheWorkerForbidsTheWatch(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	tr, reads := newFakeWatchTransport(), newLevelReads()
	events := startTestFeed(t, ctx, tr, reads, testutil.NewQuartzMock(t), "a-1")
	receive(t, ctx, tr.opened)

	tr.ack(&leapmuxv1.WatchRejection{EntityId: "a-1", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_FORBIDDEN})
	ev := receive(t, ctx, events)
	assert.Equal(t, activityFailed, ev.kind)
	assert.Equal(t, cli.CodePermissionDenied, cli.FromError(ev.err).Code)
	select {
	case <-reads.started:
		t.Fatal("a feed that failed reads no level")
	default:
	}
}

// A failure ends the feed, so it passes a level read in flight instead of
// waiting behind it.
func TestActivityFeed_SendsAFailureDuringALevelReadAtOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	tr, reads := newFakeWatchTransport(), newLevelReads()
	events := startTestFeed(t, ctx, tr, reads, testutil.NewQuartzMock(t), "a-1")
	receive(t, ctx, tr.opened)

	tr.ack()
	receive(t, ctx, reads.started)
	tr.ack(&leapmuxv1.WatchRejection{EntityId: "a-1", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_FORBIDDEN})
	ev := receive(t, ctx, events)
	assert.Equal(t, activityFailed, ev.kind, "the failure arrives while the level read still runs")
	assert.Equal(t, cli.CodePermissionDenied, cli.FromError(ev.err).Code)
}

func TestActivityFeed_ForwardsTurnEndsAndStatusChangesButNoMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	tr, reads := newFakeWatchTransport(), newLevelReads()
	events := startTestFeed(t, ctx, tr, reads, testutil.NewQuartzMock(t), "a-1")
	receive(t, ctx, tr.opened)

	tr.push(agentFrame("a-1", &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: 4}}}))
	tr.push(turnEndFrame("a-1"))
	tr.push(agentFrame("a-1", &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{}}}))

	assert.Equal(t, activityEvent{kind: activityTurnEnd, agentID: "a-1"}, receive(t, ctx, events))
	assert.Equal(t, activityEvent{kind: activityStatusChange, agentID: "a-1"}, receive(t, ctx, events))
	requireNoEvent(t, events)
}

func TestActivityFeed_FailsWhenTheFirstSubscriptionFails(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	refused := errors.New("the worker is offline")
	tr, reads := newFakeWatchTransport(), newLevelReads()
	tr.openErrs = []error{refused}
	events := startTestFeed(t, ctx, tr, reads, testutil.NewQuartzMock(t), "a-1")

	ev := receive(t, ctx, events)
	assert.Equal(t, activityFailed, ev.kind)
	assert.ErrorIs(t, ev.err, refused)
}

func TestActivityFeed_FailsWhenTheLevelReadFails(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	refused := errors.New("the worker cannot read its agents")
	tr, reads := newFakeWatchTransport(), newLevelReads()
	events := startTestFeed(t, ctx, tr, reads, testutil.NewQuartzMock(t), "a-1")
	receive(t, ctx, tr.opened)

	tr.ack()
	receive(t, ctx, reads.started)
	reads.answers <- activityLevelRead{err: refused}
	ev := receive(t, ctx, events)
	assert.Equal(t, activityFailed, ev.kind)
	assert.ErrorIs(t, ev.err, refused)
}

// A stream that ends subscribes again, and the level of the new subscription
// covers the gap. A failed subscription after the first one is a gap too:
// it waits a longer backoff instead of ending the feed.
func TestActivityFeed_SubscribesAgainAfterTheStreamEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(activityReconnectTimerTag)
	defer newTimer.Close()
	tr, reads := newFakeWatchTransport(), newLevelReads()
	events := startTestFeed(t, ctx, tr, reads, clock, "a-1")
	receive(t, ctx, tr.opened)
	tr.ack()
	receive(t, ctx, reads.started)
	reads.answer(map[string]leapmuxv1.AgentActivityState{"a-1": working})
	assert.Equal(t, activityEvent{kind: activityLevel, agentID: "a-1", state: working}, receive(t, ctx, events))

	tr.mu.Lock()
	tr.openErrs = []error{errors.New("the connection dropped")}
	tr.mu.Unlock()
	tr.endStream()
	first := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, activityReconnectInitial, first, "a stream that delivered something waits the shortest backoff")
	clock.Advance(first).MustWait(ctx)

	second := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, 2*activityReconnectInitial, second, "a failed subscription grows the backoff")
	requireNoEvent(t, events)
	clock.Advance(second).MustWait(ctx)

	receive(t, ctx, tr.opened)
	tr.ack()
	receive(t, ctx, reads.started)
	reads.answer(map[string]leapmuxv1.AgentActivityState{"a-1": idle})
	assert.Equal(t, activityEvent{kind: activityLevel, agentID: "a-1", state: idle}, receive(t, ctx, events))
}

// The feed ends its subscription with its context.
func TestActivityFeed_StopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	tr, reads := newFakeWatchTransport(), newLevelReads()
	startTestFeed(t, ctx, tr, reads, testutil.NewQuartzMock(t), "a-1")
	receive(t, ctx, tr.opened)
	tr.mu.Lock()
	done := tr.done
	tr.mu.Unlock()

	cancel()
	receive(t, testutil.DeadlineContext(t), done)
}
