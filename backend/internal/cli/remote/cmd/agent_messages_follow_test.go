package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote/streamevents"
)

// fakeAgentMessagesTransport is an in-memory streamevents.Transport
// for the agent messages tests. It records each WatchEventsRequest
// the Subscription fires so tests can assert cursor preservation
// across reconnects, and exposes a `pushFrame` hook that delivers
// synthetic AgentEvent frames at the registered callback.
type fakeAgentMessagesTransport struct {
	mu      sync.Mutex
	calls   []*leapmuxv1.WatchEventsRequest
	onFrame func(*leapmuxv1.WatchEventsResponse)
	cancel  context.CancelFunc
	done    chan struct{}
	opens   int32
}

func (t *fakeAgentMessagesTransport) OpenWatchEvents(parentCtx context.Context, req *leapmuxv1.WatchEventsRequest,
	onFrame func(*leapmuxv1.WatchEventsResponse),
) (context.CancelFunc, <-chan struct{}, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	atomic.AddInt32(&t.opens, 1)
	t.calls = append(t.calls, req)
	t.onFrame = onFrame
	ctx, cancel := context.WithCancel(parentCtx)
	t.cancel = cancel
	t.done = make(chan struct{})
	go func() {
		<-ctx.Done()
		close(t.done)
	}()
	return cancel, t.done, nil
}

func (t *fakeAgentMessagesTransport) push(seq int64) {
	t.mu.Lock()
	cb := t.onFrame
	t.mu.Unlock()
	if cb != nil {
		cb(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: &leapmuxv1.AgentEvent{
				AgentId: "a-1",
				Event: &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{
					Seq: seq,
				}},
			}},
		})
	}
}

func (t *fakeAgentMessagesTransport) lastRequest() *leapmuxv1.WatchEventsRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.calls) == 0 {
		return nil
	}
	return t.calls[len(t.calls)-1]
}

// runAgentMessagesTail spins up the same machinery `tailAgentMessages`
// uses but with our fake transport so we can drive frames + force
// reconnects. This mirrors the production helper one-to-one — the
// only thing we replace is the Transport implementation.
func runAgentMessagesTail(ctx context.Context, agentID string, startSeq int64,
	tr *fakeAgentMessagesTransport,
) (*bytes.Buffer, *streamevents.Subscription, *streamevents.AgentCursor, *sync.Mutex) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	mu := &sync.Mutex{}
	cursor := streamevents.NewAgentCursor()
	cursor.Track(agentID, startSeq)
	terms := streamevents.NewTerminalCursor()

	onAgent := func(ae *leapmuxv1.AgentEvent) {
		msg := ae.GetAgentMessage()
		if msg == nil {
			return
		}
		mu.Lock()
		_ = enc.Encode(msg)
		mu.Unlock()
	}
	sub := streamevents.NewSubscription(tr, cursor, terms, onAgent, nil, nil)
	require := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	require(sub.Update(ctx, &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{streamevents.AgentWatchEntry(agentID, startSeq)},
	}))
	return buf, sub, cursor, mu
}

// TestAgentMessages_StreamingTail_EmitsMessagesInOrder confirms the
// happy path of `agent messages --follow`: each frame surfaces as a
// JSON line on stdout and the cursor advances. This pins the
// "messages stream in seq order, no gaps" guarantee from the plan's
// Phase 1a test surface.
func TestAgentMessages_StreamingTail_EmitsMessagesInOrder(t *testing.T) {
	tr := &fakeAgentMessagesTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	buf, sub, cursor, _ := runAgentMessagesTail(ctx, "a-1", 0, tr)
	t.Cleanup(sub.Cancel)

	tr.push(1)
	tr.push(2)
	tr.push(3)

	require.Eventually(t, func() bool {
		return cursor.Get("a-1") == 3
	}, time.Second, 10*time.Millisecond, "cursor must advance to seq=3")

	// Output must be three JSON lines, each carrying a seq.
	lines := splitNonEmpty(buf.String())
	require.Len(t, lines, 3)
	for i, ln := range lines {
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(ln), &got))
		assert.EqualValues(t, i+1, got["seq"])
	}
}

// TestAgentMessages_ReconnectCarriesCursor exercises the resume-on-
// reconnect contract: after the cursor advances to N, a fresh
// Update (simulating reconnect after channel error) must resume
// AFTER_CURSOR from cursor_seq=N in the new WatchEventsRequest.
func TestAgentMessages_ReconnectCarriesCursor(t *testing.T) {
	tr := &fakeAgentMessagesTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	_, sub, cursor, _ := runAgentMessagesTail(ctx, "a-1", 0, tr)
	t.Cleanup(sub.Cancel)

	tr.push(5)
	require.Eventually(t, func() bool { return cursor.Get("a-1") == 5 },
		time.Second, 10*time.Millisecond)

	// Simulate reconnect: build a new request from the cursor and
	// call Update again — the resubscribe must carry seq=5.
	require.NoError(t, sub.Update(ctx, &leapmuxv1.WatchEventsRequest{
		Agents: cursor.Snapshot(map[string]struct{}{"a-1": {}}),
	}))

	last := tr.lastRequest()
	require.NotNil(t, last)
	require.Len(t, last.GetAgents(), 1)
	assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, last.GetAgents()[0].GetReplay())
	assert.Equal(t, int64(5), last.GetAgents()[0].GetCursorSeq(),
		"reconnect must carry the latest cursor forward")
}

// TestAgentMessages_StartSeqIsHonored: when `RunAgentMessages
// --follow` resumes from a non-zero `--anchor after --cursor-seq`, the streaming
// subscription's first request must reflect that cursor. Otherwise a
// caller resuming from an earlier checkpoint would re-receive
// messages they've already processed.
func TestAgentMessages_StartSeqIsHonored(t *testing.T) {
	tr := &fakeAgentMessagesTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	_, sub, _, _ := runAgentMessagesTail(ctx, "a-1", 42, tr)
	t.Cleanup(sub.Cancel)

	last := tr.lastRequest()
	require.NotNil(t, last)
	require.Len(t, last.GetAgents(), 1)
	assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, last.GetAgents()[0].GetReplay())
	assert.Equal(t, int64(42), last.GetAgents()[0].GetCursorSeq())
}

// TestAgentMessages_OutOfOrderFramesDoNotRegressCursor: even if a
// frame with a lower seq somehow arrives (e.g. across a reconnect
// where the worker replayed older state), the cursor must not
// regress. Otherwise the next reconnect would request already-seen
// messages.
func TestAgentMessages_OutOfOrderFramesDoNotRegressCursor(t *testing.T) {
	tr := &fakeAgentMessagesTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	_, sub, cursor, _ := runAgentMessagesTail(ctx, "a-1", 0, tr)
	t.Cleanup(sub.Cancel)

	tr.push(10)
	require.Eventually(t, func() bool { return cursor.Get("a-1") == 10 },
		time.Second, 10*time.Millisecond)
	tr.push(3)
	// Wait a beat to make sure if the cursor was going to regress it
	// would have done so by now.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int64(10), cursor.Get("a-1"),
		"out-of-order seq=3 must not regress the cursor below 10")
}

// TestAgentMessages_CtxCancelStopsSubscription: cancelling the
// parent context (CTRL-C in production) must propagate through the
// transport so its done channel fires and `tailAgentMessages` can
// return cleanly. Without this, the CLI's exit would hang on the
// in-flight subscription.
func TestAgentMessages_CtxCancelStopsSubscription(t *testing.T) {
	tr := &fakeAgentMessagesTransport{}
	ctx, cancel := context.WithCancel(context.Background())

	_, sub, _, _ := runAgentMessagesTail(ctx, "a-1", 0, tr)
	t.Cleanup(sub.Cancel)

	// Cancel parent ctx; the subscription's Done() should fire.
	cancel()

	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("ctx cancellation did not propagate to subscription done channel")
	}
}

// TestAgentMessages_OutputIsByteForByteCompatibleWithPolling pins the
// "external scripts written against the old polling output keep
// working byte-for-byte" guarantee from the plan. Each line must
// JSON-marshal directly to an AgentChatMessage shape (not wrapped in
// a `source` envelope, not a multi-line proto-pretty-print).
func TestAgentMessages_OutputIsByteForByteCompatibleWithPolling(t *testing.T) {
	tr := &fakeAgentMessagesTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	buf, sub, _, _ := runAgentMessagesTail(ctx, "a-1", 0, tr)
	t.Cleanup(sub.Cancel)

	tr.push(11)
	require.Eventually(t, func() bool { return buf.Len() > 0 },
		time.Second, 10*time.Millisecond)

	// Each line must parse as an AgentChatMessage envelope with a
	// `seq` key — and NOT carry the `source` key the events command
	// adds to its lines (verifies we didn't accidentally unify the
	// two output paths).
	lines := splitNonEmpty(buf.String())
	require.NotEmpty(t, lines)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &got))
	assert.EqualValues(t, 11, got["seq"])
	_, hasSource := got["source"]
	assert.False(t, hasSource, "agent messages output must not carry the events `source` key")
}

// TestAgentMessages_TwoUpdatesShareCursor: the consumer that calls
// runAgentMessagesTail twice in succession (mimicking the reconnect
// loop in `tailAgentMessages`) must see the cursor preserved across
// both Update calls. Without this, every reconnect would reset to a fresh
// LATEST subscription and the user would see duplicate history.
func TestAgentMessages_TwoUpdatesShareCursor(t *testing.T) {
	tr := &fakeAgentMessagesTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	_, sub, cursor, _ := runAgentMessagesTail(ctx, "a-1", 0, tr)
	t.Cleanup(sub.Cancel)
	tr.push(8)
	require.Eventually(t, func() bool { return cursor.Get("a-1") == 8 },
		time.Second, 10*time.Millisecond)

	// Reconnect using the cursor's current state (matches the
	// production reconnect loop).
	require.NoError(t, sub.Update(ctx, &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{streamevents.AgentWatchEntry("a-1", cursor.Get("a-1"))},
	}))
	last := tr.lastRequest()
	require.NotNil(t, last)
	assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, last.GetAgents()[0].GetReplay())
	assert.Equal(t, int64(8), last.GetAgents()[0].GetCursorSeq(),
		"reconnect must resume from the cursor seq, not a fresh latest subscription")
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

// TestDrainBacklog_PagesUntilCaughtUp: a reconnect with a >50-message gap must
// drain EVERY message in seq order across pages (not just the first 50 the capped
// replay would deliver) and leave the cursor at the live tail.
func TestDrainBacklog_PagesUntilCaughtUp(t *testing.T) {
	cursor := streamevents.NewAgentCursor()
	cursor.Track("a-1", 0)
	var emitted []int64
	emit := func(m *leapmuxv1.AgentChatMessage) error {
		emitted = append(emitted, m.GetSeq())
		return nil
	}

	drained, err := drainBacklog(context.Background(), "a-1", cursor, pagingFetch(seqRange(120), followDrainPageLimit), emit)
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
			pagingFetch(seqRange(60), followDrainPageLimit), func(*leapmuxv1.AgentChatMessage) error { return nil })
		require.NoError(t, err)
		assert.True(t, drained, "a drain that emitted messages reports activity")
	})
	t.Run("false when already caught up", func(t *testing.T) {
		cursor := streamevents.NewAgentCursor()
		cursor.Track("a-1", 60)
		drained, err := drainBacklog(context.Background(), "a-1", cursor,
			pagingFetch(seqRange(60), followDrainPageLimit), func(*leapmuxv1.AgentChatMessage) error { return nil })
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

	drained, err := drainBacklog(context.Background(), "a-1", cursor, pagingFetch(seqRange(120), followDrainPageLimit), emit)
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
		drained, err := drainBacklog(context.Background(), "a-1", cursor, fetch, emit)
		done <- result{drained: drained, err: err}
	}()

	select {
	case <-emitStarted:
	case <-time.After(time.Second):
		t.Fatal("drain did not reach the emitter")
	}
	duplicateForwarded := cursor.Advance("a-1", 120)
	close(releaseEmit)
	got := <-done

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
		pagingFetch([]int64{1, 2}, followDrainPageLimit),
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
// hasMore=true page whose rows are at or below the bound must NOT spin forever --
// the no-advance guard breaks after one page.
func TestDrainBacklog_StopsWhenCursorDoesNotAdvance(t *testing.T) {
	cursor := streamevents.NewAgentCursor()
	cursor.Track("a-1", 10)
	calls := 0
	fetch := func(_ context.Context, _ int64) ([]*leapmuxv1.AgentChatMessage, bool, error) {
		calls++
		return []*leapmuxv1.AgentChatMessage{{Seq: 5}}, true, nil // below the bound, claims more
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
	assert.Equal(t, int64(10), cursor.Get("a-1"), "a below-bound row never lowers the cursor")
}

func splitNonEmpty(s string) []string {
	out := []string{}
	for _, line := range bytesSplitLines([]byte(s)) {
		if len(line) > 0 {
			out = append(out, string(line))
		}
	}
	return out
}

func bytesSplitLines(b []byte) [][]byte {
	out := [][]byte{}
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
