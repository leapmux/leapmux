package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func staticDrain(fetch messagePager) func(context.Context, func(messagePager) error) error {
	return func(_ context.Context, run func(messagePager) error) error {
		return run(fetch)
	}
}

func noopDrain() func(context.Context, func(messagePager) error) error {
	return staticDrain(func(context.Context, int64) ([]*leapmuxv1.AgentChatMessage, bool, error) {
		return nil, false, nil
	})
}

func messageFrame(agentID string, msg *leapmuxv1.AgentChatMessage) *leapmuxv1.WatchEventsResponse {
	return agentFrame(agentID, &leapmuxv1.AgentEvent{
		Event: &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: msg},
	})
}

type followTailHarness struct {
	out    *lineSink
	errOut *lineSink
	cancel context.CancelFunc
	errCh  chan error
}

// startFollowTail runs the real tailAgentMessagesWith + lineEmitter + cli.Printer
// pipeline against tr and clock so follow tests exercise production rendering
// and cursor tracking end-to-end.
func startFollowTail(t *testing.T, parentCtx context.Context, agentID string, startSeq int64,
	format cli.Format, tr *fakeWatchTransport, clock quartz.Clock,
) *followTailHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(parentCtx)
	out := newLineSink()
	errOut := newLineSink()
	em := &lineEmitter{printer: &cli.Printer{Format: format, Out: out, Err: errOut}}
	openTransport := func(context.Context) (*workerWatchTransport, error) {
		return &workerWatchTransport{transport: tr, close: func() {}}, nil
	}
	errCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		errCh <- tailAgentMessagesWith(ctx, agentID, startSeq, openTransport, noopDrain(), em, clock)
	}()
	h := &followTailHarness{out: out, errOut: errOut, cancel: cancel, errCh: errCh}
	t.Cleanup(func() {
		cancel()
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			t.Error("tailAgentMessagesWith did not stop during cleanup")
		}
	})
	return h
}

// TestAgentMessages_StreamingTail_EmitsMessagesInOrder confirms the
// happy path of `agent messages --follow`: each frame surfaces as a
// JSON envelope line on stdout in sequence order.
func TestAgentMessages_StreamingTail_EmitsMessagesInOrder(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	tr := newFakeWatchTransport()
	h := startFollowTail(t, ctx, "a-1", 0, cli.FormatJSON, tr, testutil.NewQuartzMock(t))

	receive(t, ctx, tr.opened)
	tr.push(messageFrame("a-1", &leapmuxv1.AgentChatMessage{Seq: 1}))
	tr.push(messageFrame("a-1", &leapmuxv1.AgentChatMessage{Seq: 2}))
	tr.push(messageFrame("a-1", &leapmuxv1.AgentChatMessage{Seq: 3}))

	for expected := int64(1); expected <= 3; expected++ {
		line := receive(t, ctx, h.out.lines)
		var env struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &env))
		assert.EqualValues(t, expected, env.Data["seq"])
	}
}

// TestAgentMessages_ReconnectCarriesCursor exercises the resume-on-
// reconnect contract: after a message at seq N is delivered, a reconnect
// must resume AFTER_CURSOR from cursor_seq=N in the new WatchEventsRequest.
func TestAgentMessages_ReconnectCarriesCursor(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(agentMessagesReconnectTimerTag)
	defer newTimer.Close()

	tr := newFakeWatchTransport()
	h := startFollowTail(t, ctx, "a-1", 0, cli.FormatJSON, tr, clock)

	receive(t, ctx, tr.opened)
	first := tr.lastRequest()
	require.NotNil(t, first)
	require.Positive(t, first.GetAgents()[0].GetReplayId())
	assert.True(t, first.GetAgents()[0].GetIncludeReplyText(), "watch request must enable IncludeReplyText")

	tr.push(messageFrame("a-1", &leapmuxv1.AgentChatMessage{Seq: 5}))
	receive(t, ctx, h.out.lines)

	tr.endStream()
	delay := testutil.WaitForTimer(t, ctx, newTimer)
	clock.Advance(delay).MustWait(ctx)
	receive(t, ctx, tr.opened)

	last := tr.lastRequest()
	require.NotNil(t, last)
	require.Len(t, last.GetAgents(), 1)
	assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, last.GetAgents()[0].GetReplay())
	assert.Equal(t, int64(5), last.GetAgents()[0].GetCursorSeq(),
		"reconnect must carry the latest cursor forward")
	assert.Equal(t, last.GetUpdateId(), last.GetAgents()[0].GetReplayId())
	assert.True(t, last.GetAgents()[0].GetIncludeReplyText(), "reconnect watch request must keep IncludeReplyText")
}

// TestAgentMessages_StartSeqIsHonored: when `agent messages --follow`
// resumes from a non-zero `--anchor after --cursor-seq`, the streaming
// subscription's first request must reflect that cursor. Otherwise a
// caller resuming from an earlier checkpoint would re-receive
// messages they have already processed.
func TestAgentMessages_StartSeqIsHonored(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	tr := newFakeWatchTransport()
	startFollowTail(t, ctx, "a-1", 42, cli.FormatJSON, tr, testutil.NewQuartzMock(t))

	receive(t, ctx, tr.opened)
	last := tr.lastRequest()
	require.NotNil(t, last)
	require.Len(t, last.GetAgents(), 1)
	assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, last.GetAgents()[0].GetReplay())
	assert.Equal(t, int64(42), last.GetAgents()[0].GetCursorSeq())
	assert.True(t, last.GetAgents()[0].GetIncludeReplyText(), "initial resume watch request must set IncludeReplyText")
}

func TestDrainMessagesRequest_SetsIncludeReplyText(t *testing.T) {
	oldest := drainMessagesRequest("a-1", 0)
	assert.Equal(t, "a-1", oldest.GetAgentId())
	assert.Equal(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST, oldest.GetAnchor())
	assert.Equal(t, int64(0), oldest.GetCursorSeq())
	assert.True(t, oldest.GetIncludeReplyText())

	after := drainMessagesRequest("a-1", 42)
	assert.Equal(t, "a-1", after.GetAgentId())
	assert.Equal(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER, after.GetAnchor())
	assert.Equal(t, int64(42), after.GetCursorSeq())
	assert.True(t, after.GetIncludeReplyText())
}

// TestAgentMessages_OutOfOrderFramesDoNotRegressCursor: even if a
// frame with a lower seq arrives, the subscription drops it and the
// cursor must not regress on the next reconnect.
func TestAgentMessages_OutOfOrderFramesDoNotRegressCursor(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(agentMessagesReconnectTimerTag)
	defer newTimer.Close()

	tr := newFakeWatchTransport()
	h := startFollowTail(t, ctx, "a-1", 0, cli.FormatJSON, tr, clock)

	receive(t, ctx, tr.opened)
	tr.push(messageFrame("a-1", &leapmuxv1.AgentChatMessage{Seq: 10}))
	receive(t, ctx, h.out.lines)
	tr.push(messageFrame("a-1", &leapmuxv1.AgentChatMessage{Seq: 3}))

	tr.endStream()
	delay := testutil.WaitForTimer(t, ctx, newTimer)
	clock.Advance(delay).MustWait(ctx)
	receive(t, ctx, tr.opened)

	last := tr.lastRequest()
	require.NotNil(t, last)
	assert.Equal(t, int64(10), last.GetAgents()[0].GetCursorSeq(),
		"out-of-order seq=3 must not regress the cursor below 10")
	assert.Empty(t, h.out.lines, "out-of-order seq=3 must not be emitted")
}

// TestAgentMessages_CtxCancelStopsSubscription: cancelling the
// parent context (CTRL-C in production) must stop the subscription
// and allow tailAgentMessagesWith to return cleanly.
func TestAgentMessages_CtxCancelStopsSubscription(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	tr := newFakeWatchTransport()
	h := startFollowTail(t, ctx, "a-1", 0, cli.FormatJSON, tr, testutil.NewQuartzMock(t))

	receive(t, ctx, tr.opened)
	tr.mu.Lock()
	done := tr.done
	tr.mu.Unlock()

	h.cancel()
	receive(t, ctx, done)
	require.NoError(t, receive(t, ctx, h.errCh))
}

// TestAgentMessages_OutputIsByteForByteCompatibleWithPolling exercises the real
// tailAgentMessagesWith + lineEmitter + cli.Printer pipeline across JSON,
// NDJSON, text, and quiet formats.
func TestAgentMessages_OutputIsByteForByteCompatibleWithPolling(t *testing.T) {
	compressed, kind := msgcodec.Compress([]byte(`{"text":"hi"}`))
	reply := "hi"
	sample := &leapmuxv1.AgentChatMessage{
		Id:                 "m-11",
		Seq:                11,
		Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		ReplyText:          &reply,
		Content:            compressed,
		ContentCompression: kind,
	}

	t.Run("json emits data envelope with decompressed message", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		tr := newFakeWatchTransport()
		h := startFollowTail(t, ctx, "a-1", 0, cli.FormatJSON, tr, testutil.NewQuartzMock(t))

		receive(t, ctx, tr.opened)
		tr.push(messageFrame("a-1", sample))
		line := receive(t, ctx, h.out.lines)
		assert.JSONEq(t, `{"data":{"id":"m-11","seq":11,"source":"agent","reply_text":"hi","content":{"text":"hi"}}}`, line)
	})

	t.Run("ndjson emits unwrapped message object", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		tr := newFakeWatchTransport()
		h := startFollowTail(t, ctx, "a-1", 0, cli.FormatNDJSON, tr, testutil.NewQuartzMock(t))

		receive(t, ctx, tr.opened)
		tr.push(messageFrame("a-1", sample))
		line := receive(t, ctx, h.out.lines)
		assert.JSONEq(t, `{"id":"m-11","seq":11,"source":"agent","reply_text":"hi","content":{"text":"hi"}}`, line)
	})

	t.Run("text emits rendered TextLine", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		tr := newFakeWatchTransport()
		h := startFollowTail(t, ctx, "a-1", 0, cli.FormatText, tr, testutil.NewQuartzMock(t))

		receive(t, ctx, tr.opened)
		tr.push(messageFrame("a-1", sample))
		assert.Equal(t, "#11 agent: hi", receive(t, ctx, h.out.lines))
	})

	t.Run("quiet emits integer sequence number matching paged -q", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		tr := newFakeWatchTransport()
		h := startFollowTail(t, ctx, "a-1", 0, cli.FormatQuiet, tr, testutil.NewQuartzMock(t))

		receive(t, ctx, tr.opened)
		tr.push(messageFrame("a-1", sample))
		assert.Equal(t, "11", receive(t, ctx, h.out.lines))
	})
}

// TestAgentMessages_TwoUpdatesShareCursor verifies that cursor state survives
// multiple consecutive reconnect cycles in tailAgentMessagesWith.
func TestAgentMessages_TwoUpdatesShareCursor(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(agentMessagesReconnectTimerTag)
	defer newTimer.Close()

	tr := newFakeWatchTransport()
	h := startFollowTail(t, ctx, "a-1", 0, cli.FormatJSON, tr, clock)

	receive(t, ctx, tr.opened)
	tr.push(messageFrame("a-1", &leapmuxv1.AgentChatMessage{Seq: 8}))
	receive(t, ctx, h.out.lines)

	tr.endStream()
	clock.Advance(testutil.WaitForTimer(t, ctx, newTimer)).MustWait(ctx)
	receive(t, ctx, tr.opened)
	assert.Equal(t, int64(8), tr.lastRequest().GetAgents()[0].GetCursorSeq())

	tr.push(messageFrame("a-1", &leapmuxv1.AgentChatMessage{Seq: 15}))
	receive(t, ctx, h.out.lines)

	tr.endStream()
	clock.Advance(testutil.WaitForTimer(t, ctx, newTimer)).MustWait(ctx)
	receive(t, ctx, tr.opened)
	assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, tr.lastRequest().GetAgents()[0].GetReplay())
	assert.Equal(t, int64(15), tr.lastRequest().GetAgents()[0].GetCursorSeq())
}

// TestTailAgentMessages_ReturnsLatchedWriteErrorOnCancel guards TRAPS-8:
// a latched stdout write error must always surface as a non-nil error rather
// than exiting 0 when waitBackoff or ErrSubscriptionClosed unwinds the loop.
func TestTailAgentMessages_ReturnsLatchedWriteErrorOnCancel(t *testing.T) {
	t.Run("live emit failure cancels loop and returns write error", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		errWrite := errors.New("broken pipe")
		tr := newFakeWatchTransport()
		openTransport := func(context.Context) (*workerWatchTransport, error) {
			return &workerWatchTransport{transport: tr, close: func() {}}, nil
		}
		em := &lineEmitter{printer: &cli.Printer{Format: cli.FormatJSON, Out: failingJSONLineWriter{err: errWrite}, Err: &bytes.Buffer{}}}
		errCh := make(chan error, 1)
		go func() {
			errCh <- tailAgentMessagesWith(ctx, "a-1", 0, openTransport, noopDrain(), em, testutil.NewQuartzMock(t))
		}()

		receive(t, ctx, tr.opened)
		tr.push(messageFrame("a-1", &leapmuxv1.AgentChatMessage{Seq: 1}))

		require.ErrorIs(t, receive(t, ctx, errCh), errWrite)
	})

	t.Run("cancel during reconnect waitBackoff returns latched write error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
		defer cancel()
		clock := testutil.NewQuartzMock(t)
		newTimer := clock.Trap().NewTimer(agentMessagesReconnectTimerTag)
		defer newTimer.Close()

		errWrite := errors.New("stdout closed")
		tr := newFakeWatchTransport()
		openTransport := func(context.Context) (*workerWatchTransport, error) {
			return &workerWatchTransport{transport: tr, close: func() {}}, nil
		}
		em := &lineEmitter{printer: &cli.Printer{Format: cli.FormatJSON, Out: failingJSONLineWriter{err: errWrite}, Err: &bytes.Buffer{}}}
		errCh := make(chan error, 1)
		go func() {
			errCh <- tailAgentMessagesWith(ctx, "a-1", 0, openTransport, noopDrain(), em, clock)
		}()

		receive(t, ctx, tr.opened)
		tr.endStream()
		_ = testutil.WaitForTimer(t, ctx, newTimer)

		// Latch a write error before cancelling during waitBackoff.
		_ = em.emit(map[string]any{"seq": 99})
		cancel()

		require.ErrorIs(t, receive(t, testutil.DeadlineContext(t), errCh), errWrite)
	})

	t.Run("ErrSubscriptionClosed returns latched write error", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		errWrite := errors.New("stdout broken")
		tr := newFakeWatchTransport()
		tr.openErrs = []error{streamevents.ErrSubscriptionClosed}
		var opens atomic.Int32
		openTransport := func(context.Context) (*workerWatchTransport, error) {
			opens.Add(1)
			return &workerWatchTransport{transport: tr, close: func() {}}, nil
		}
		var errBuf bytes.Buffer
		em := &lineEmitter{printer: &cli.Printer{Format: cli.FormatJSON, Out: failingJSONLineWriter{err: errWrite}, Err: &errBuf}}
		_ = em.emit(map[string]any{"seq": 1})

		err := tailAgentMessagesWith(ctx, "a-1", 0, openTransport, noopDrain(), em, testutil.NewQuartzMock(t))
		require.ErrorIs(t, err, errWrite)
		assert.Equal(t, int32(1), opens.Load())
		assert.Empty(t, errBuf.String(), "ErrSubscriptionClosed must not log a retry notice")
	})

	t.Run("ErrSubscriptionClosed without latched write error exits cleanly", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		tr := newFakeWatchTransport()
		tr.openErrs = []error{streamevents.ErrSubscriptionClosed}
		openTransport := func(context.Context) (*workerWatchTransport, error) {
			return &workerWatchTransport{transport: tr, close: func() {}}, nil
		}
		var outBuf, errBuf bytes.Buffer
		em := &lineEmitter{printer: &cli.Printer{Format: cli.FormatJSON, Out: &outBuf, Err: &errBuf}}

		err := tailAgentMessagesWith(ctx, "a-1", 0, openTransport, noopDrain(), em, testutil.NewQuartzMock(t))
		require.NoError(t, err)
		assert.Empty(t, errBuf.String())
	})
}

func TestTailAgentMessages_DrainReusesSingleChannelScopePerBacklogRun(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	tr := newFakeWatchTransport()
	var drainScopes atomic.Int32
	var pagesFetched atomic.Int32
	fetch := pagingFetch(seqRange(120), contracts.MessagePageLimit)
	withDrain := func(_ context.Context, run func(messagePager) error) error {
		drainScopes.Add(1)
		return run(func(ctx context.Context, afterSeq int64) ([]*leapmuxv1.AgentChatMessage, bool, error) {
			pagesFetched.Add(1)
			return fetch(ctx, afterSeq)
		})
	}
	out := newLineSink()
	em := &lineEmitter{printer: &cli.Printer{Format: cli.FormatQuiet, Out: out, Err: &bytes.Buffer{}}}
	openTransport := func(context.Context) (*workerWatchTransport, error) {
		return &workerWatchTransport{transport: tr, close: func() {}}, nil
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- tailAgentMessagesWith(ctx, "a-1", 0, openTransport, withDrain, em, testutil.NewQuartzMock(t))
	}()

	receive(t, ctx, tr.opened)
	for range 120 {
		receive(t, ctx, out.lines)
	}
	assert.Equal(t, int32(1), drainScopes.Load(), "all pages of one backlog drain share one channel scope")
	assert.GreaterOrEqual(t, pagesFetched.Load(), int32(2), "backlog spans multiple pages")

	cancel()
	require.NoError(t, receive(t, testutil.DeadlineContext(t), errCh))
}

// TestReconnectBackoff_ResetsOnDelivery pins the activity-based reset: a session
// that delivered an event drops the backoff back to the floor, while a run of
// empty (flapping) sessions climbs toward the cap and saturates there. The old
// rule reset only when a session outlived maxBackoff, so a string of sub-8s
// flaps that each streamed real data kept reconnect latency pinned high.
func TestReconnectBackoff_ResetsOnDelivery(t *testing.T) {
	const (
		initial = 250 * time.Millisecond
		maxB    = 8 * time.Second
	)
	b := newReconnectBackoff(initial, maxB)

	// Empty sessions climb: each wait is the current value, then it doubles.
	assert.Equal(t, initial, b.afterSession(false))
	assert.Equal(t, 500*time.Millisecond, b.afterSession(false))
	assert.Equal(t, 1*time.Second, b.afterSession(false))

	// A session that delivered an event resets to the floor immediately, even
	// though it never outlived maxBackoff.
	assert.Equal(t, initial, b.afterSession(true))
	// ...and then climbs again from the floor.
	assert.Equal(t, 500*time.Millisecond, b.afterSession(false))

	// The wait saturates at the cap and never exceeds it.
	for range 20 {
		b.afterSession(false)
	}
	assert.Equal(t, maxB, b.afterSession(false))

	// One delivery after saturation snaps straight back to the floor.
	assert.Equal(t, initial, b.afterSession(true))
}

// TestReconnectBackoff_ClampsNonPowerOfTwoMax guards the cap when `max` is not a
// power-of-two multiple of `initial`. Without the post-double clamp, `cur` would
// overshoot (1s -> 2s -> 4s -> 8s past a 5s cap) and the next afterSession would
// return a wait ABOVE the documented maximum.
func TestReconnectBackoff_ClampsNonPowerOfTwoMax(t *testing.T) {
	const (
		initial = 1 * time.Second
		maxB    = 5 * time.Second
	)
	b := newReconnectBackoff(initial, maxB)

	assert.Equal(t, 1*time.Second, b.afterSession(false))
	assert.Equal(t, 2*time.Second, b.afterSession(false))
	assert.Equal(t, 4*time.Second, b.afterSession(false))
	// 4s < 5s, so it doubles -- but the clamp pins it to the cap instead of 8s.
	assert.Equal(t, maxB, b.afterSession(false))
	// Every subsequent wait stays at the cap and never exceeds it.
	for range 10 {
		assert.LessOrEqual(t, int64(b.afterSession(false)), int64(maxB))
	}
}

// pagingFetch is an in-memory messagePager over a sorted ascending seq list. It
// serves up to pageSize rows with seq > afterSeq and reports whether more remain
// -- mirroring the worker's AFTER paging without a live RPC.
func pagingFetch(all []int64, pageSize int) messagePager {
	return func(_ context.Context, afterSeq int64) ([]*leapmuxv1.AgentChatMessage, bool, error) {
		page := []*leapmuxv1.AgentChatMessage{}
		var lastInPage int64
		for _, s := range all {
			if s > afterSeq {
				page = append(page, &leapmuxv1.AgentChatMessage{Seq: s})
				lastInPage = s
				if len(page) == pageSize {
					break
				}
			}
		}
		hasMore := false
		for _, s := range all {
			if s > lastInPage {
				hasMore = true
				break
			}
		}
		return page, hasMore, nil
	}
}

func seqRange(n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i + 1)
	}
	return out
}

// TestDrainBacklog_PagesUntilCaughtUp verifies that a multi-page reconnect gap
// drains every message in sequence order and leaves the cursor at the live tail.
func TestDrainBacklog_PagesUntilCaughtUp(t *testing.T) {
	cursor := streamevents.NewAgentCursor()
	cursor.Track("a-1", 0)
	var emitted []int64
	emit := func(m *leapmuxv1.AgentChatMessage) error {
		emitted = append(emitted, m.GetSeq())
		return nil
	}

	drained, err := drainBacklog(context.Background(), "a-1", cursor, pagingFetch(seqRange(120), contracts.MessagePageLimit), emit)
	require.NoError(t, err)
	require.True(t, drained)

	require.Len(t, emitted, 120)
	for i, s := range emitted {
		assert.EqualValues(t, i+1, s, "messages must drain in ascending seq order")
	}
	assert.Equal(t, int64(120), cursor.Get("a-1"), "cursor must reach the live tail")
}

// TestDrainBacklog_ReportsWhetherItEmitted: the return value tells the reconnect
// loop whether the drain forwarded real history (so it can count a drain-only
// session as activity and reset the backoff) vs found nothing to do.
func TestDrainBacklog_ReportsWhetherItEmitted(t *testing.T) {
	t.Run("true when it forwards backlog", func(t *testing.T) {
		cursor := streamevents.NewAgentCursor()
		cursor.Track("a-1", 0)
		drained, err := drainBacklog(context.Background(), "a-1", cursor,
			pagingFetch(seqRange(60), contracts.MessagePageLimit), func(*leapmuxv1.AgentChatMessage) error { return nil })
		require.NoError(t, err)
		assert.True(t, drained, "a drain that emitted messages reports activity")
	})
	t.Run("false when already caught up", func(t *testing.T) {
		cursor := streamevents.NewAgentCursor()
		cursor.Track("a-1", 60)
		drained, err := drainBacklog(context.Background(), "a-1", cursor,
			pagingFetch(seqRange(60), contracts.MessagePageLimit), func(*leapmuxv1.AgentChatMessage) error { return nil })
		require.NoError(t, err)
		assert.False(t, drained, "a drain with nothing past the cursor reports no activity")
	})
	t.Run("false on an immediate fetch error", func(t *testing.T) {
		cursor := streamevents.NewAgentCursor()
		cursor.Track("a-1", 0)
		fetch := func(_ context.Context, _ int64) ([]*leapmuxv1.AgentChatMessage, bool, error) {
			return nil, false, errors.New("transport down")
		}
		drained, err := drainBacklog(context.Background(), "a-1", cursor, fetch, func(*leapmuxv1.AgentChatMessage) error { return nil })
		require.NoError(t, err)
		assert.False(t, drained, "a drain that emitted nothing before erroring reports no activity")
	})
}

// TestDrainBacklog_DrainsOnlyPastCursor: the drain bridges only the gap above the
// resume cursor (already-forwarded messages aren't re-emitted).
func TestDrainBacklog_DrainsOnlyPastCursor(t *testing.T) {
	cursor := streamevents.NewAgentCursor()
	cursor.Track("a-1", 100)
	var emitted []int64
	emit := func(m *leapmuxv1.AgentChatMessage) error {
		emitted = append(emitted, m.GetSeq())
		return nil
	}

	drained, err := drainBacklog(context.Background(), "a-1", cursor, pagingFetch(seqRange(120), contracts.MessagePageLimit), emit)
	require.NoError(t, err)
	require.True(t, drained)

	require.Len(t, emitted, 20)
	assert.EqualValues(t, 101, emitted[0])
	assert.EqualValues(t, 120, emitted[len(emitted)-1])
	assert.Equal(t, int64(120), cursor.Get("a-1"))
}

// TestDrainBacklog_StopsOnFetchError: a transient fetch failure is best-effort --
// the drain emits what it got, advances the cursor only past those, and returns so
// the live subscription (and the next reconnect's drain) can retry the rest. No
// message is double-emitted because the cursor never advances past an un-emitted one.
func TestDrainBacklog_StopsOnFetchError(t *testing.T) {
	cursor := streamevents.NewAgentCursor()
	cursor.Track("a-1", 0)
	calls := 0
	fetch := func(_ context.Context, _ int64) ([]*leapmuxv1.AgentChatMessage, bool, error) {
		calls++
		if calls == 1 {
			return []*leapmuxv1.AgentChatMessage{{Seq: 1}, {Seq: 2}, {Seq: 3}}, true, nil
		}
		return nil, false, errors.New("transport down")
	}
	var emitted []int64
	emit := func(m *leapmuxv1.AgentChatMessage) error {
		emitted = append(emitted, m.GetSeq())
		return nil
	}

	drained, err := drainBacklog(context.Background(), "a-1", cursor, fetch, emit)
	require.NoError(t, err)
	require.True(t, drained)

	assert.Equal(t, []int64{1, 2, 3}, emitted)
	assert.Equal(t, int64(3), cursor.Get("a-1"), "cursor advances only past emitted messages")
	assert.Equal(t, 2, calls, "stops after the failing page")
}

func TestDrainBacklog_AdvanceBeforeEmitDedupsConcurrentFrame(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	cursor := streamevents.NewAgentCursor()
	cursor.Track("a-1", 119)
	fetch := func(_ context.Context, afterSeq int64) ([]*leapmuxv1.AgentChatMessage, bool, error) {
		require.Equal(t, int64(119), afterSeq)
		return []*leapmuxv1.AgentChatMessage{{Seq: 120}}, false, nil
	}
	emitStarted := make(chan struct{})
	releaseEmit := make(chan struct{})
	emit := func(*leapmuxv1.AgentChatMessage) error {
		close(emitStarted)
		<-releaseEmit
		return nil
	}
	type result struct {
		drained bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		drained, err := drainBacklog(ctx, "a-1", cursor, fetch, emit)
		done <- result{drained: drained, err: err}
	}()

	receive(t, ctx, emitStarted)
	duplicateForwarded := cursor.Advance("a-1", 120)
	close(releaseEmit)
	got := receive(t, ctx, done)

	require.NoError(t, got.err)
	assert.True(t, got.drained)
	assert.False(t, duplicateForwarded, "late stream frame for the same seq must see the drain's cursor advance")
	assert.Equal(t, int64(120), cursor.Get("a-1"))
}

func TestDrainBacklog_ReturnsEmitError(t *testing.T) {
	cursor := streamevents.NewAgentCursor()
	cursor.Track("a-1", 0)
	errWrite := errors.New("write failed")

	drained, err := drainBacklog(context.Background(), "a-1", cursor,
		pagingFetch([]int64{1, 2}, contracts.MessagePageLimit),
		func(*leapmuxv1.AgentChatMessage) error { return errWrite },
	)

	assert.False(t, drained, "a failed emit is not reported as delivered activity")
	require.ErrorIs(t, err, errWrite)
	assert.Equal(t, int64(1), cursor.Get("a-1"), "the in-memory cursor advances before emit to dedupe concurrent frames, then the command stops")
}

// TestDrainBacklog_StopsOnCtxCancel: a cancelled context returns before any fetch
// so the follower can exit cleanly on CTRL-C mid-drain.
func TestDrainBacklog_StopsOnCtxCancel(t *testing.T) {
	cursor := streamevents.NewAgentCursor()
	cursor.Track("a-1", 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	fetch := func(_ context.Context, _ int64) ([]*leapmuxv1.AgentChatMessage, bool, error) {
		calls++
		return []*leapmuxv1.AgentChatMessage{{Seq: 1}}, true, nil
	}

	drained, err := drainBacklog(ctx, "a-1", cursor, fetch, func(*leapmuxv1.AgentChatMessage) error { return nil })
	require.NoError(t, err)
	assert.False(t, drained)

	assert.Equal(t, 0, calls, "must not fetch after ctx is cancelled")
}

// TestDrainBacklog_StopsWhenCursorDoesNotAdvance: a buggy server that returns a
// hasMore=true page whose rows are at or below the cursor must NOT spin forever --
// the no-advance guard breaks after one page.
func TestDrainBacklog_StopsWhenCursorDoesNotAdvance(t *testing.T) {
	cursor := streamevents.NewAgentCursor()
	cursor.Track("a-1", 10)
	calls := 0
	fetch := func(_ context.Context, _ int64) ([]*leapmuxv1.AgentChatMessage, bool, error) {
		calls++
		return []*leapmuxv1.AgentChatMessage{{Seq: 5}}, true, nil // below the cursor, claims more
	}
	var emitted []int64
	emit := func(m *leapmuxv1.AgentChatMessage) error {
		emitted = append(emitted, m.GetSeq())
		return nil
	}

	drained, err := drainBacklog(context.Background(), "a-1", cursor, fetch, emit)
	require.NoError(t, err)
	assert.False(t, drained)

	assert.Empty(t, emitted)
	assert.Equal(t, 1, calls, "must stop after one non-advancing page, not spin")
	assert.Equal(t, int64(10), cursor.Get("a-1"), "a below-cursor row never lowers the cursor")
}

func TestDrainBacklog_PagesPastAnAlreadyRetractedRow(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	tr := newFakeWatchTransport()
	cursor := streamevents.NewAgentCursor()
	cursor.Track("a-1", 15)
	sub := streamevents.NewSubscription(tr, cursor, streamevents.NewTerminalCursor(), nil, nil, nil)
	t.Cleanup(sub.Cancel)
	require.NoError(t, sub.Update(ctx, &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{streamevents.AgentWatchEntry("a-1", 15)},
	}))
	tr.push(agentFrame("a-1", &leapmuxv1.AgentEvent{
		Event: &leapmuxv1.AgentEvent_MessagesRetracted{
			MessagesRetracted: &leapmuxv1.AgentMessagesRetracted{Seqs: []int64{19}},
		},
	}))

	var fetchedAfter, emitted []int64
	fetch := func(_ context.Context, afterSeq int64) ([]*leapmuxv1.AgentChatMessage, bool, error) {
		fetchedAfter = append(fetchedAfter, afterSeq)
		if afterSeq == 15 {
			return []*leapmuxv1.AgentChatMessage{{Seq: 19}}, true, nil
		}
		return []*leapmuxv1.AgentChatMessage{{Seq: 20}}, false, nil
	}
	drained, err := drainBacklog(ctx, "a-1", cursor, fetch, func(m *leapmuxv1.AgentChatMessage) error {
		emitted = append(emitted, m.GetSeq())
		return nil
	})

	require.NoError(t, err)
	assert.True(t, drained)
	assert.Equal(t, []int64{15, 19}, fetchedAfter)
	assert.Equal(t, []int64{20}, emitted)
	assert.Equal(t, int64(20), cursor.Get("a-1"))
}

func TestTailAgentMessages_ReturnsImmediatelyOnPermanentRefusal(t *testing.T) {
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	tr := newFakeWatchTransport()
	opens := 0
	openTransport := func(context.Context) (*workerWatchTransport, error) {
		opens++
		return &workerWatchTransport{transport: tr, close: func() {}}, nil
	}
	var stdout, stderr bytes.Buffer
	em := &lineEmitter{printer: &cli.Printer{Format: cli.FormatJSON, Out: &stdout, Err: &stderr}}

	errCh := make(chan error, 1)
	go func() {
		errCh <- tailAgentMessagesWith(ctx, "a-1", 0, openTransport, noopDrain(), em, clock)
	}()

	receive(t, ctx, tr.opened)
	tr.endStreamWithErr(&channelwire.RPCError{Code: 7, Message: "only the worker owner can watch events"})

	err := receive(t, ctx, errCh)
	require.Error(t, err)
	assert.Equal(t, cli.CodePermissionDenied, cli.FromError(err).Code)
	assert.Equal(t, 1, opens, "a permanent refusal must not reopen the transport")
	_, running := clock.Peek()
	assert.False(t, running, "a permanent refusal must not arm a reconnect timer")
}

func TestTailAgentMessages_ReopensTransportAfterChannelEndAndResumesCursor(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(agentMessagesReconnectTimerTag)
	defer newTimer.Close()

	first := newFakeWatchTransport()
	second := newFakeWatchTransport()
	var closedFirst atomic.Bool
	var opens atomic.Int32
	openTransport := func(context.Context) (*workerWatchTransport, error) {
		switch opens.Add(1) {
		case 1:
			return &workerWatchTransport{transport: first, close: func() { closedFirst.Store(true) }}, nil
		default:
			return &workerWatchTransport{transport: second, close: func() {}}, nil
		}
	}
	var stdout, stderr bytes.Buffer
	em := &lineEmitter{printer: &cli.Printer{Format: cli.FormatJSON, Out: &stdout, Err: &stderr}}

	errCh := make(chan error, 1)
	go func() {
		errCh <- tailAgentMessagesWith(ctx, "a-1", 0, openTransport, noopDrain(), em, clock)
	}()

	receive(t, ctx, first.opened)
	first.push(messageFrame("a-1", &leapmuxv1.AgentChatMessage{Seq: 12}))
	// Simulate the E2EE channel closing transiently.
	first.endStreamWithErr(errors.New("the channel to the worker closed"))

	delay := testutil.WaitForTimer(t, ctx, newTimer)
	assert.True(t, closedFirst.Load(), "the dead transport must be closed before reconnecting")
	clock.Advance(delay).MustWait(ctx)

	receive(t, ctx, second.opened)
	req := second.lastRequest()
	require.Len(t, req.GetAgents(), 1)
	assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, req.GetAgents()[0].GetReplay())
	assert.Equal(t, int64(12), req.GetAgents()[0].GetCursorSeq())

	cancel()
	require.NoError(t, receive(t, testutil.DeadlineContext(t), errCh))
}

func TestTailAgentMessages_ReconnectOpenTransportFailureTransientAndPermanent(t *testing.T) {
	t.Run("transient open error backs off and retries until fresh transport opens", func(t *testing.T) {
		ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
		defer cancel()
		clock := testutil.NewQuartzMock(t)
		newTimer := clock.Trap().NewTimer(agentMessagesReconnectTimerTag)
		defer newTimer.Close()

		first := newFakeWatchTransport()
		third := newFakeWatchTransport()
		var opens atomic.Int32
		openTransport := func(context.Context) (*workerWatchTransport, error) {
			switch opens.Add(1) {
			case 1:
				return &workerWatchTransport{transport: first, close: func() {}}, nil
			case 2:
				return nil, errors.New("worker offline temporarily")
			default:
				return &workerWatchTransport{transport: third, close: func() {}}, nil
			}
		}
		stderr := newLineSink()
		em := &lineEmitter{printer: &cli.Printer{Format: cli.FormatText, Out: &bytes.Buffer{}, Err: stderr}}

		errCh := make(chan error, 1)
		go func() {
			errCh <- tailAgentMessagesWith(ctx, "a-1", 0, openTransport, noopDrain(), em, clock)
		}()

		receive(t, ctx, first.opened)
		first.endStreamWithErr(errors.New("channel closed"))

		// First reconnect backoff after first stream ended.
		d1 := testutil.WaitForTimer(t, ctx, newTimer)
		clock.Advance(d1).MustWait(ctx)
		// Second openTransport fails transiently and arms a second backoff timer.
		d2 := testutil.WaitForTimer(t, ctx, newTimer)
		assert.Equal(t, 2*d1, d2)
		clock.Advance(d2).MustWait(ctx)

		receive(t, ctx, third.opened)
		assert.Equal(t, int32(3), opens.Load())
		var notices []string
		for len(stderr.lines) > 0 {
			notices = append(notices, <-stderr.lines)
		}
		assert.Contains(t, strings.Join(notices, "\n"), "connecting to agent a-1 failed, retrying")

		cancel()
		require.NoError(t, receive(t, testutil.DeadlineContext(t), errCh))
	})

	t.Run("permanent open error on reconnect stops without arming a timer", func(t *testing.T) {
		ctx := testutil.DeadlineContext(t)
		clock := testutil.NewQuartzMock(t)
		newTimer := clock.Trap().NewTimer(agentMessagesReconnectTimerTag)
		defer newTimer.Close()

		first := newFakeWatchTransport()
		var opens atomic.Int32
		openTransport := func(context.Context) (*workerWatchTransport, error) {
			if opens.Add(1) == 1 {
				return &workerWatchTransport{transport: first, close: func() {}}, nil
			}
			return nil, &channelwire.RPCError{Code: 7, Message: "permission revoked"}
		}
		var stdout, stderr bytes.Buffer
		em := &lineEmitter{printer: &cli.Printer{Format: cli.FormatJSON, Out: &stdout, Err: &stderr}}

		errCh := make(chan error, 1)
		go func() {
			errCh <- tailAgentMessagesWith(ctx, "a-1", 0, openTransport, noopDrain(), em, clock)
		}()

		receive(t, ctx, first.opened)
		first.endStreamWithErr(errors.New("channel closed"))

		d1 := testutil.WaitForTimer(t, ctx, newTimer)
		clock.Advance(d1).MustWait(ctx)

		err := receive(t, ctx, errCh)
		require.Error(t, err)
		assert.Equal(t, cli.CodePermissionDenied, cli.FromError(err).Code)
		assert.Equal(t, int32(2), opens.Load())
		_, running := clock.Peek()
		assert.False(t, running)
	})
}
