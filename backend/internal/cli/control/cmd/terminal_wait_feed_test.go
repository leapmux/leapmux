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
	}, "t-1", afterOffset, out)
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
	assert.Equal(t, afterOffset, entry.GetAfterOffset())
}

func TestScreenFeed_WatchesTheBytesAfterTheRecordThatTheWaitRead(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	tr := newFakeWatchTransport()
	items := startTestScreenFeed(t, ctx, tr, testutil.NewQuartzMock(t), 42)
	receive(t, ctx, tr.opened)
	requireTerminalEntry(t, tr.lastRequest(), 42)

	tr.push(terminalFrame("t-1", dataEvent("abc", 45, false)))
	item := receive(t, ctx, items)
	assert.Equal(t, screenFeedEvent, item.kind)
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
	tr.push(terminalFrame("t-1", dataEvent("abc", 45, false)))
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
	tr.push(terminalFrame("t-1", dataEvent("$ ", 12, true)))
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
	assert.Equal(t, screenFeedRejected, item.kind)
	e := cli.FromError(item.err)
	assert.Equal(t, cli.CodeNotFound, e.Code)
	assert.Equal(t, cli.ExitWaitEnded, e.ExitCode())
}

func TestScreenFeed_FailsWhenTheFirstSubscriptionFails(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	tr := newFakeWatchTransport()
	cause := errors.New("the worker is offline")
	tr.openErrs = []error{cause}
	items := startTestScreenFeed(t, ctx, tr, testutil.NewQuartzMock(t), 0)
	item := receive(t, ctx, items)
	assert.Equal(t, screenFeedFailed, item.kind)
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
	assert.Equal(t, screenFeedFailed, item.kind)
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
