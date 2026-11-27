package cmd

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// ackTerminals acknowledges the newest subscription of tr, with the terminal
// rejections given.
func ackTerminals(tr *fakeWatchTransport, rejected ...*leapmuxv1.WatchRejection) {
	tr.push(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_UpdateAck{
		UpdateAck: &leapmuxv1.WatchUpdateAck{UpdateId: tr.lastRequest().GetUpdateId(), RejectedTerminals: rejected},
	}})
}

// startTestScreenFeed starts a screen feed of t-1 over tr from afterOffset,
// with clock for its reconnects, and returns its items.
func startTestScreenFeed(t *testing.T, ctx context.Context, tr *fakeWatchTransport, clock quartz.Clock, afterOffset int64) <-chan screenFeedItem {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	out := make(chan screenFeedItem, 16)
	startScreenFeed(ctx, &wg, screenFeedSource{
		openTransport: func(context.Context) (streamevents.Transport, func(), error) { return tr, func() {}, nil },
		clock:         clock,
	}, "t-1", terminalScreenPosition{offset: afterOffset}, out)
	return out
}

// requireNoScreenItem fails when the feed sent an item that the test did not
// expect.
func requireNoScreenItem(t *testing.T, items <-chan screenFeedItem) {
	t.Helper()
	select {
	case item := <-items:
		t.Fatalf("unexpected item: %+v", item)
	default:
	}
}

// requireTerminalEntry asserts that req watches t-1 alone, in content mode,
// from afterOffset.
func requireTerminalEntry(t *testing.T, req *leapmuxv1.WatchEventsRequest, afterOffset int64) {
	t.Helper()
	assert.Empty(t, req.GetAgents())
	require.Len(t, req.GetTerminals(), 1)
	entry := req.GetTerminals()[0]
	assert.Equal(t, "t-1", entry.GetTerminalId())
	assert.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_FULL, entry.GetMode())
	assert.True(t, entry.GetScreenGeometry())
	assert.Equal(t, afterOffset, entry.GetAfterOffset())
}

func replayDataEvent(output string, endOffset int64, snapshot bool) *leapmuxv1.TerminalEvent {
	event := dataEvent(output, endOffset, snapshot)
	event.GetData().ScreenTimeline = &leapmuxv1.TerminalScreenTimeline{}
	return event
}

func outputReplayDataEvent(output string, endOffset int64, snapshot bool) *leapmuxv1.TerminalEvent {
	event := dataEvent(output, endOffset, snapshot)
	event.GetData().ReplayComplete = true
	return event
}

func startTestOutputFeed(t *testing.T, ctx context.Context, tr *fakeWatchTransport, clock quartz.Clock, afterOffset int64) <-chan screenFeedItem {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	out := make(chan screenFeedItem, 16)
	startTerminalOutputFeed(ctx, &wg, screenFeedSource{
		openTransport: func(context.Context) (streamevents.Transport, func(), error) { return tr, func() {}, nil },
		clock:         clock,
	}, "t-1", afterOffset, out)
	return out
}

func requireOutputTerminalEntry(t *testing.T, req *leapmuxv1.WatchEventsRequest, afterOffset int64) {
	t.Helper()
	assert.Empty(t, req.GetAgents())
	require.Len(t, req.GetTerminals(), 1)
	entry := req.GetTerminals()[0]
	assert.Equal(t, "t-1", entry.GetTerminalId())
	assert.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_FULL, entry.GetMode())
	assert.False(t, entry.GetScreenGeometry())
	assert.Empty(t, entry.GetScreenEpoch())
	assert.Zero(t, entry.GetAfterGeometryRevision())
	assert.Equal(t, afterOffset, entry.GetAfterOffset())
}

func TestScreenFeed_WatchesTheBytesAfterTheRecordThatTheWaitRead(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	tr := newFakeWatchTransport()
	items := startTestScreenFeed(t, ctx, tr, testutil.NewQuartzMock(t), 42)
	receive(t, ctx, tr.opened)
	requireTerminalEntry(t, tr.lastRequest(), 42)

	tr.push(terminalFrame("t-1", replayDataEvent("abc", 45, false)))
	item := receive(t, ctx, items)
	require.NotNil(t, item.event)
	assert.Equal(t, "abc", string(item.event.GetData().GetData()))
}

func TestScreenFeed_ResumesAfterTheLastChunkWhenTheStreamEnds(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(screenFeedReconnectTimerTag)
	defer newTimer.Close()
	tr := newFakeWatchTransport()
	items := startTestScreenFeed(t, ctx, tr, clock, 42)
	receive(t, ctx, tr.opened)
	tr.push(terminalFrame("t-1", replayDataEvent("abc", 45, false)))
	receive(t, ctx, items)

	tr.endStream()
	delay := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, activityReconnectInitial, delay, "a stream that delivered something waits the shortest backoff")
	clock.Advance(delay).MustWait(ctx)
	receive(t, ctx, tr.opened)
	requireTerminalEntry(t, tr.lastRequest(), 45)
}

// A restarted shell counts its offsets from 0 again. The snapshot that
// replaces the state moves the cursor back to the new offsets.
func TestScreenFeed_ASnapshotMovesTheCursorBack(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(screenFeedReconnectTimerTag)
	defer newTimer.Close()
	tr := newFakeWatchTransport()
	items := startTestScreenFeed(t, ctx, tr, clock, 900)
	receive(t, ctx, tr.opened)
	tr.push(terminalFrame("t-1", replayDataEvent("$ ", 12, true)))
	assert.True(t, receive(t, ctx, items).event.GetData().GetIsSnapshot())

	tr.endStream()
	clock.Advance(testutil.WaitForTimer(t, ctx, newTimer)).MustWait(ctx)
	receive(t, ctx, tr.opened)
	requireTerminalEntry(t, tr.lastRequest(), 12)
}

func TestScreenFeed_IgnoresTheEventsOfAnotherTerminal(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	tr := newFakeWatchTransport()
	items := startTestScreenFeed(t, ctx, tr, testutil.NewQuartzMock(t), 0)
	receive(t, ctx, tr.opened)
	tr.push(terminalFrame("t-2", dataEvent("x", 1, false)))
	tr.push(terminalFrame("t-1", replayDataEvent("", 0, true)))
	require.NotNil(t, receive(t, ctx, items).event.GetData().GetScreenTimeline())
	tr.push(terminalFrame("t-1", closedEvent(0)))
	assert.NotNil(t, receive(t, ctx, items).event.GetClosed(), "the event of t-2 never reached the wait")
}

func TestScreenFeed_ReportsATerminalThatTheWorkerNoLongerHolds(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	tr := newFakeWatchTransport()
	items := startTestScreenFeed(t, ctx, tr, testutil.NewQuartzMock(t), 0)
	receive(t, ctx, tr.opened)
	ackTerminals(tr, &leapmuxv1.WatchRejection{EntityId: "t-1", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED})
	requireNoScreenItem(t, items)
	ackTerminals(tr, &leapmuxv1.WatchRejection{EntityId: "t-1", Reason: leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND})
	item := receive(t, ctx, items)
	assert.Nil(t, item.event)
	assert.Nil(t, item.err)
	assert.False(t, item.overflow)
	require.NotNil(t, item.rejected)
	assert.Equal(t, "t-1", item.rejected.GetEntityId())
	assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND, item.rejected.GetReason())
}

func TestScreenFeed_FailsWhenTheFirstSubscriptionFails(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	tr := newFakeWatchTransport()
	cause := errors.New("the worker is offline")
	tr.openErrs = []error{cause}
	items := startTestScreenFeed(t, ctx, tr, testutil.NewQuartzMock(t), 0)
	item := receive(t, ctx, items)
	assert.Nil(t, item.event)
	assert.Nil(t, item.rejected)
	assert.ErrorIs(t, item.err, cause)
}

// failNextOpen makes the next open of tr fail with err.
func failNextOpen(tr *fakeWatchTransport, err error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.openErrs = append(tr.openErrs, err)
}

// Once a subscription succeeded, a reopen that fails is a gap that the next
// reopen covers: the feed waits and subscribes again, and the wait sees no
// failure.
func TestScreenFeed_RetriesAReopenThatFailsAfterTheFirstSubscription(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(screenFeedReconnectTimerTag)
	defer newTimer.Close()
	tr := newFakeWatchTransport()
	items := startTestScreenFeed(t, ctx, tr, clock, 42)
	receive(t, ctx, tr.opened)

	failNextOpen(tr, errors.New("the worker restarts"))
	tr.endStream()
	clock.Advance(testutil.WaitForTimer(t, ctx, newTimer)).MustWait(ctx)
	// The failed reopen arms the next reconnect instead of ending the feed.
	clock.Advance(testutil.WaitForTimer(t, ctx, newTimer)).MustWait(ctx)
	receive(t, ctx, tr.opened)
	requireTerminalEntry(t, tr.lastRequest(), 42)
	requireNoScreenItem(t, items)
}

// A reopen that the Worker refuses for good ends the feed, even after a
// subscription succeeded: no later reopen can succeed.
func TestScreenFeed_FailsWhenTheWorkerRefusesAReopenForGood(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(screenFeedReconnectTimerTag)
	defer newTimer.Close()
	tr := newFakeWatchTransport()
	items := startTestScreenFeed(t, ctx, tr, clock, 0)
	receive(t, ctx, tr.opened)

	cause := cli.Newf(cli.CodePermissionDenied, "the credential may not watch terminals")
	failNextOpen(tr, cause)
	tr.endStream()
	clock.Advance(testutil.WaitForTimer(t, ctx, newTimer)).MustWait(ctx)
	item := receive(t, ctx, items)
	assert.Nil(t, item.event)
	assert.Nil(t, item.rejected)
	assert.ErrorIs(t, item.err, cause)
}

func TestTerminalRejection_MapsEachReason(t *testing.T) {
	reject := func(reason leapmuxv1.WatchRejectionReason) error {
		return terminalRejection(&leapmuxv1.WatchRejection{EntityId: "t-1", Reason: reason})
	}
	require.NoError(t, reject(leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED), "the subscription retries a failed lookup by itself")
	assert.Equal(t, cli.CodeNotFound, cli.FromError(reject(leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND)).Code)
	assert.Equal(t, cli.CodePermissionDenied, cli.FromError(reject(leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_FORBIDDEN)).Code)
	assert.Equal(t, cli.CodeInternal, cli.FromError(reject(leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_UNSPECIFIED)).Code, "an unknown reason is durable")
}

func TestScreenFeedResumesAfterBytesDeliveredToTheWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		tr := newFakeWatchTransport()
		clock := testutil.NewQuartzMock(t)
		newTimer := clock.Trap().NewTimer(screenFeedReconnectTimerTag)
		defer newTimer.Close()
		var wg sync.WaitGroup
		defer func() {
			cancel()
			wg.Wait()
		}()
		out := make(chan screenFeedItem)
		startScreenFeed(ctx, &wg, screenFeedSource{
			openTransport: func(context.Context) (streamevents.Transport, func(), error) { return tr, func() {}, nil },
			clock:         clock,
		}, "t-1", terminalScreenPosition{offset: 42}, out)
		<-tr.opened
		requireTerminalEntry(t, tr.lastRequest(), 42)

		oldCallbackDone := make(chan struct{})
		go func() {
			defer close(oldCallbackDone)
			tr.push(terminalFrame("t-1", replayDataEvent("abc", 45, false)))
		}()
		synctest.Wait()
		tr.endStream()
		delay := testutil.WaitForTimer(t, ctx, newTimer)
		clock.Advance(delay).MustWait(ctx)
		<-tr.opened
		requireTerminalEntry(t, tr.lastRequest(), 42)
		synctest.Wait()

		// The old callback cannot send to the waiter after reconnect.
		var retired bool
		select {
		case <-oldCallbackDone:
			retired = true
		default:
		}
		assert.True(t, retired, "the ended stream must release its blocked callback")
		if retired {
			go tr.push(terminalFrame("t-1", replayDataEvent("abc", 45, false)))
			item := <-out
			assert.Equal(t, "abc", string(item.event.GetData().GetData()))
		}
	})
}

func TestScreenFeedReplaysBeforeClippingLiveOutputAndResumesGeometry(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	timer := clock.Trap().NewTimer(screenFeedReconnectTimerTag)
	defer timer.Close()
	tr := newFakeWatchTransport()
	items := startTestScreenFeed(t, ctx, tr, clock, 10)
	receive(t, ctx, tr.opened)
	const epoch = "screen"
	live := dataEvent("abcdef", 16, false)
	live.GetData().ScreenEpoch, live.GetData().GeometryRevision = epoch, 1
	tr.push(terminalFrame("t-1", live))
	base := &leapmuxv1.TerminalScreenGeometry{Epoch: epoch, Revision: 1, Cols: 6, Rows: 2}
	resize := &leapmuxv1.TerminalScreenGeometry{Epoch: epoch, Revision: 2, Offset: 16, Cols: 12, Rows: 2}
	tr.push(terminalFrame("t-1", &leapmuxv1.TerminalEvent{
		Event: &leapmuxv1.TerminalEvent_ScreenGeometry{ScreenGeometry: resize},
	}))
	replay := dataEvent("abc", 13, true)
	replay.GetData().ScreenTimeline = &leapmuxv1.TerminalScreenTimeline{Base: base, StartOffset: 10}
	tr.push(terminalFrame("t-1", replay))
	assert.Equal(t, "abc", string(receive(t, ctx, items).event.GetData().GetData()))
	assert.Equal(t, "def", string(receive(t, ctx, items).event.GetData().GetData()))
	assert.Equal(t, uint64(2), receive(t, ctx, items).event.GetScreenGeometry().GetRevision())

	tr.endStream()
	clock.Advance(testutil.WaitForTimer(t, ctx, timer)).MustWait(ctx)
	receive(t, ctx, tr.opened)
	request := tr.lastRequest()
	requireTerminalEntry(t, request, 16)
	assert.Equal(t, epoch, request.GetTerminals()[0].GetScreenEpoch())
	assert.Equal(t, uint64(2), request.GetTerminals()[0].GetAfterGeometryRevision())
}

func TestScreenFeedReplaysAgainWhenLiveOutputSkippedAResize(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	timer := clock.Trap().NewTimer(screenFeedReconnectTimerTag)
	defer timer.Close()
	tr := newFakeWatchTransport()
	items := startTestScreenFeed(t, ctx, tr, clock, 0)
	receive(t, ctx, tr.opened)
	base := &leapmuxv1.TerminalScreenGeometry{Epoch: "screen", Revision: 1, Cols: 6, Rows: 2}
	replay := dataEvent("abc", 3, true)
	replay.GetData().ScreenTimeline = &leapmuxv1.TerminalScreenTimeline{Base: base}
	tr.push(terminalFrame("t-1", replay))
	require.NotNil(t, receive(t, ctx, items).event.GetData().GetScreenTimeline())
	missed := dataEvent("def", 6, false)
	missed.GetData().ScreenEpoch, missed.GetData().GeometryRevision = "screen", 2
	tr.push(terminalFrame("t-1", missed))
	clock.Advance(testutil.WaitForTimer(t, ctx, timer)).MustWait(ctx)
	receive(t, ctx, tr.opened)
	request := tr.lastRequest()
	requireTerminalEntry(t, request, 3)
	assert.Equal(t, uint64(1), request.GetTerminals()[0].GetAfterGeometryRevision())
	requireNoScreenItem(t, items)
}

func TestScreenReplayPreservesAnExitThatArrivedBeforeCatchUp(t *testing.T) {
	out := make(chan screenFeedItem, 2)
	forward := screenReplayForwarder{feed: &screenFeed{terminalID: "t-1", out: out}}
	require.True(t, forward.receive(t.Context(), eventItem(closedEvent(17))))
	require.True(t, forward.receive(t.Context(), eventItem(replayDataEvent("", 0, true))))
	require.Len(t, out, 2)
	assert.NotNil(t, (<-out).event.GetData().GetScreenTimeline())
	closed := (<-out).event.GetClosed()
	require.NotNil(t, closed)
	assert.Equal(t, int32(17), closed.GetExitCode())
}

func TestScreenFeedValidatesEpochBeforeClippingOffsets(t *testing.T) {
	out := make(chan screenFeedItem, 1)
	previous := terminalScreenPosition{epoch: "old", offset: 100, revision: 2}
	forward := screenReplayForwarder{feed: &screenFeed{terminalID: "t-1", out: out}, position: previous, replayed: true}
	newEpoch := dataEvent("abc", 3, false)
	newEpoch.GetData().ScreenEpoch, newEpoch.GetData().GeometryRevision = "new", 1
	assert.False(t, forward.receive(t.Context(), eventItem(newEpoch)), "the new epoch needs replay")
	assert.Equal(t, previous, forward.position)
	assert.Empty(t, out)
}

func TestTerminalScreenPositionFromTimelineUsesLatestChange(t *testing.T) {
	t.Parallel()
	baseOnly := &leapmuxv1.TerminalScreenTimeline{
		Base: &leapmuxv1.TerminalScreenGeometry{Epoch: "ep-1", Revision: 1, Cols: 80, Rows: 24},
	}
	assert.Equal(t, terminalScreenPosition{offset: 100, epoch: "ep-1", revision: 1}, terminalScreenPositionFromTimeline(baseOnly, 100))

	withChanges := &leapmuxv1.TerminalScreenTimeline{
		Base: &leapmuxv1.TerminalScreenGeometry{Epoch: "ep-1", Revision: 1, Cols: 80, Rows: 24},
		Changes: []*leapmuxv1.TerminalScreenGeometry{
			{Epoch: "ep-1", Revision: 2, Offset: 120, Cols: 100, Rows: 30},
			{Epoch: "ep-1", Revision: 3, Offset: 200, Cols: 120, Rows: 40},
		},
	}
	assert.Equal(t, terminalScreenPosition{offset: 250, epoch: "ep-1", revision: 3}, terminalScreenPositionFromTimeline(withChanges, 250))
}

func TestScreenReplayForwarder_ReportsOverflowNeutrally(t *testing.T) {
	out := make(chan screenFeedItem, 1)
	forward := screenReplayForwarder{feed: &screenFeed{terminalID: "t-1", out: out}}
	huge := make([]byte, maxScreenBytesBeforeReplay+1)
	ev := &leapmuxv1.TerminalEvent{
		TerminalId: "t-1",
		Event:      &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{Data: huge, EndOffset: int64(len(huge))}},
	}
	assert.False(t, forward.receive(t.Context(), screenFeedItem{event: ev}))
	require.Len(t, out, 1)
	item := <-out
	assert.True(t, item.overflow)
	assert.Nil(t, item.err)
	assert.Nil(t, item.event)
	assert.Nil(t, item.rejected)
}

func TestTerminalScreenPositionFromTimelineAndTransitions(t *testing.T) {
	t.Parallel()
	tl := &leapmuxv1.TerminalScreenTimeline{
		Base: &leapmuxv1.TerminalScreenGeometry{Epoch: "ep-1", Revision: 1, Cols: 80, Rows: 24},
		Changes: []*leapmuxv1.TerminalScreenGeometry{
			{Epoch: "ep-1", Revision: 2, Offset: 10, Cols: 100, Rows: 30},
		},
	}
	pos := terminalScreenPositionFromTimeline(tl, 42)
	assert.Equal(t, terminalScreenPosition{epoch: "ep-1", offset: 42, revision: 2}, pos)

	// Stale revision is ignored without advancing.
	next, stale, ok := pos.applyLiveGeometry(&leapmuxv1.TerminalScreenGeometry{Epoch: "ep-1", Revision: 2, Offset: 42})
	assert.True(t, ok)
	assert.True(t, stale)
	assert.Equal(t, pos, next)

	// Next revision at matching offset advances.
	next, stale, ok = pos.applyLiveGeometry(&leapmuxv1.TerminalScreenGeometry{Epoch: "ep-1", Revision: 3, Offset: 42})
	assert.True(t, ok)
	assert.False(t, stale)
	assert.Equal(t, terminalScreenPosition{epoch: "ep-1", offset: 42, revision: 3}, next)

	// New epoch with revision 1 resets to the new epoch.
	next, stale, ok = next.applyLiveGeometry(&leapmuxv1.TerminalScreenGeometry{Epoch: "ep-2", Revision: 1, Offset: 0})
	assert.True(t, ok)
	assert.False(t, stale)
	assert.Equal(t, terminalScreenPosition{epoch: "ep-2", offset: 0, revision: 1}, next)

	// Output advances offset when epoch, revision, and byte length match.
	outPos, ok := next.advanceOutput(&leapmuxv1.TerminalData{Data: []byte("abcd"), EndOffset: 4, ScreenEpoch: "ep-2", GeometryRevision: 1})
	assert.True(t, ok)
	assert.Equal(t, terminalScreenPosition{epoch: "ep-2", offset: 4, revision: 1}, outPos)

	// Output with mismatched revision fails.
	_, ok = outPos.advanceOutput(&leapmuxv1.TerminalData{Data: []byte("ef"), EndOffset: 6, ScreenEpoch: "ep-2", GeometryRevision: 2})
	assert.False(t, ok)
}

func TestOutputFeed_BuffersLiveChunkBeforeCatchUpDeltaAndClipsOverlap(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	timer := clock.Trap().NewTimer(screenFeedReconnectTimerTag)
	defer timer.Close()
	tr := newFakeWatchTransport()
	items := startTestOutputFeed(t, ctx, tr, clock, 10)
	receive(t, ctx, tr.opened)
	requireOutputTerminalEntry(t, tr.lastRequest(), 10)

	// Live chunk covering bytes [10..16) arrives BEFORE catch-up delta [10..13).
	tr.push(terminalFrame("t-1", dataEvent("abcdef", 16, false)))
	requireNoScreenItem(t, items)

	// Catch-up delta with ReplayComplete=true arrives: must deliver "abc" first,
	// then clip and drain the buffered live chunk to "def" with zero byte loss.
	tr.push(terminalFrame("t-1", outputReplayDataEvent("abc", 13, false)))
	first := receive(t, ctx, items)
	require.NotNil(t, first.event.GetData())
	assert.Equal(t, "abc", string(first.event.GetData().GetData()))
	assert.True(t, first.event.GetData().GetReplayComplete())

	second := receive(t, ctx, items)
	require.NotNil(t, second.event.GetData())
	assert.Equal(t, "def", string(second.event.GetData().GetData()))
	assert.Equal(t, int64(16), second.event.GetData().GetEndOffset())

	// Reconnect resumes strictly after the drained live chunk offset (16).
	tr.endStream()
	clock.Advance(testutil.WaitForTimer(t, ctx, timer)).MustWait(ctx)
	receive(t, ctx, tr.opened)
	requireOutputTerminalEntry(t, tr.lastRequest(), 16)
}

func TestOutputFeed_CaughtUpEmptyReplayReleasesBufferedLiveChunksImmediately(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	tr := newFakeWatchTransport()
	items := startTestOutputFeed(t, ctx, tr, clock, 10)
	receive(t, ctx, tr.opened)
	requireOutputTerminalEntry(t, tr.lastRequest(), 10)

	// Live chunk arrives while Worker is still emitting the empty caught-up replay frame.
	tr.push(terminalFrame("t-1", dataEvent("hello", 15, false)))
	requireNoScreenItem(t, items)

	// Caught-up empty ReplayComplete=true frame releases the buffered live chunk.
	tr.push(terminalFrame("t-1", outputReplayDataEvent("", 10, false)))
	barrier := receive(t, ctx, items)
	require.NotNil(t, barrier.event.GetData())
	assert.True(t, barrier.event.GetData().GetReplayComplete())
	assert.Empty(t, barrier.event.GetData().GetData())

	live := receive(t, ctx, items)
	require.NotNil(t, live.event.GetData())
	assert.Equal(t, "hello", string(live.event.GetData().GetData()))
	assert.Equal(t, int64(15), live.event.GetData().GetEndOffset())
}

func TestOutputFeed_SnapshotResetMovesCursorBackAndPreservesPreReplayExit(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	timer := clock.Trap().NewTimer(screenFeedReconnectTimerTag)
	defer timer.Close()
	tr := newFakeWatchTransport()
	items := startTestOutputFeed(t, ctx, tr, clock, 900)
	receive(t, ctx, tr.opened)
	requireOutputTerminalEntry(t, tr.lastRequest(), 900)

	tr.push(terminalFrame("t-1", closedEvent(17)))
	tr.push(terminalFrame("t-1", outputReplayDataEvent("$ ", 12, true)))
	snap := receive(t, ctx, items)
	assert.True(t, snap.event.GetData().GetIsSnapshot())
	assert.Equal(t, "$ ", string(snap.event.GetData().GetData()))
	closed := receive(t, ctx, items)
	require.NotNil(t, closed.event.GetClosed())
	assert.Equal(t, int32(17), closed.event.GetClosed().GetExitCode())

	tr.endStream()
	clock.Advance(testutil.WaitForTimer(t, ctx, timer)).MustWait(ctx)
	receive(t, ctx, tr.opened)
	requireOutputTerminalEntry(t, tr.lastRequest(), 12)
}

func TestOutputReplayForwarder_ReportsOverflowNeutrally(t *testing.T) {
	out := make(chan screenFeedItem, 1)
	forward := outputReplayForwarder{feed: &screenFeed{terminalID: "t-1", out: out}}
	huge := make([]byte, maxScreenBytesBeforeReplay+1)
	ev := &leapmuxv1.TerminalEvent{
		TerminalId: "t-1",
		Event:      &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{Data: huge, EndOffset: int64(len(huge))}},
	}
	assert.False(t, forward.receive(t.Context(), screenFeedItem{event: ev}))
	require.Len(t, out, 1)
	item := <-out
	assert.True(t, item.overflow)
	assert.Nil(t, item.err)
	assert.Nil(t, item.event)
	assert.Nil(t, item.rejected)
}

func TestOutputReplayForwarder_ClippedReplayAndNegativeOffset(t *testing.T) {
	t.Run("fully clipped non-snapshot replay emits synthetic barrier and drains pending live chunks", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		out := make(chan screenFeedItem, 4)
		forward := outputReplayForwarder{
			feed:   &screenFeed{terminalID: "t-1", out: out},
			offset: 20,
		}

		// Live chunk arrives before replay completes and must buffer.
		require.True(t, forward.receive(ctx, eventItem(dataEvent("live!", 25, false))))
		assert.Empty(t, out)

		// Non-snapshot ReplayComplete=true chunk whose bytes [12..15) are fully
		// clipped by offset=20 still emits a synthetic empty barrier while !replayed.
		require.True(t, forward.receive(ctx, eventItem(outputReplayDataEvent("old", 15, false))))
		assert.True(t, forward.replayed)
		assert.Equal(t, int64(25), forward.offset)
		require.Len(t, out, 2)

		barrier := (<-out).event.GetData()
		require.NotNil(t, barrier)
		assert.Empty(t, barrier.GetData())
		assert.Equal(t, int64(20), barrier.GetEndOffset())
		assert.True(t, barrier.GetReplayComplete())
		assert.False(t, barrier.GetIsSnapshot())

		live := (<-out).event.GetData()
		require.NotNil(t, live)
		assert.Equal(t, "live!", string(live.GetData()))
		assert.Equal(t, int64(25), live.GetEndOffset())

		// A later fully clipped non-snapshot replay once replayed==true emits nothing.
		require.True(t, forward.receive(ctx, eventItem(outputReplayDataEvent("stale", 18, false))))
		assert.Empty(t, out)
	})

	t.Run("negative EndOffset returns early without marking replayed", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		out := make(chan screenFeedItem, 4)
		forward := outputReplayForwarder{
			feed:   &screenFeed{terminalID: "t-1", out: out},
			offset: 10,
		}

		require.True(t, forward.receive(ctx, eventItem(dataEvent("buf", 13, false))))
		require.True(t, forward.receive(ctx, eventItem(outputReplayDataEvent("bad", -1, false))))
		assert.False(t, forward.replayed)
		assert.Equal(t, int64(10), forward.offset)
		assert.Empty(t, out)
		assert.Len(t, forward.pending, 1)
	})
}
