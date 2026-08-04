package streamevents

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/backoffutil"
)

// fakeHandle is an in-memory Handle for subscription tests.
type fakeHandle struct {
	mu       sync.Mutex
	updates  []*leapmuxv1.WatchEventsRequest
	cancel   context.CancelFunc
	done     chan struct{}
	updateFn func(*leapmuxv1.WatchEventsRequest) error
}

func (h *fakeHandle) Update(req *leapmuxv1.WatchEventsRequest) error {
	h.mu.Lock()
	h.updates = append(h.updates, req)
	fn := h.updateFn
	h.mu.Unlock()
	if fn != nil {
		return fn(req)
	}
	return nil
}

func (h *fakeHandle) Cancel() {
	h.mu.Lock()
	cancel := h.cancel
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (h *fakeHandle) Done() <-chan struct{} { return h.done }

// fakeTransport is an in-memory Transport implementation. Tests
// drive `pushFrame` to deliver synthetic WatchEventsResponse frames
// to the subscription's callback, and assert on the cursor map / on
// emitted side effects.
type fakeTransport struct {
	mu      sync.Mutex
	calls   []*leapmuxv1.WatchEventsRequest
	onFrame func(*leapmuxv1.WatchEventsResponse)
	handle  *fakeHandle
	openErr error
	// openStarted/releaseOpen let tests pause OpenWatchEvents after the
	// transport has been entered but before the stream is installed.
	openStarted chan struct{}
	releaseOpen <-chan struct{}
	active      int32
	// callsOpened increments per OpenWatchEvents call so tests can
	// assert resubscribe count.
	callsOpened int32
}

func (t *fakeTransport) OpenWatchEvents(parentCtx context.Context, req *leapmuxv1.WatchEventsRequest,
	onFrame func(*leapmuxv1.WatchEventsResponse),
) (Handle, error) {
	t.mu.Lock()
	if t.openErr != nil {
		err := t.openErr
		t.openErr = nil
		t.mu.Unlock()
		return nil, err
	}
	t.mu.Unlock()
	if t.openStarted != nil {
		select {
		case t.openStarted <- struct{}{}:
		default:
		}
	}
	if t.releaseOpen != nil {
		<-t.releaseOpen
	}
	atomic.AddInt32(&t.callsOpened, 1)
	ctx, cancel := context.WithCancel(parentCtx)
	done := make(chan struct{})
	atomic.AddInt32(&t.active, 1)
	go func() {
		<-ctx.Done()
		atomic.AddInt32(&t.active, -1)
		close(done)
	}()
	h := &fakeHandle{cancel: cancel, done: done}
	t.mu.Lock()
	t.calls = append(t.calls, req)
	t.onFrame = onFrame
	t.handle = h
	t.mu.Unlock()
	return h, nil
}

func (t *fakeTransport) pushFrame(resp *leapmuxv1.WatchEventsResponse) {
	t.mu.Lock()
	cb := t.onFrame
	t.mu.Unlock()
	if cb != nil {
		cb(resp)
	}
}

func (t *fakeTransport) lastUpdate() *leapmuxv1.WatchEventsRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.handle == nil {
		return nil
	}
	t.handle.mu.Lock()
	defer t.handle.mu.Unlock()
	if len(t.handle.updates) == 0 {
		return nil
	}
	return t.handle.updates[len(t.handle.updates)-1]
}

// TestSubscription_HappyPath_AdvanceCursorAndCallback verifies one
// end-to-end pass: a frame arrives, the agent cursor advances, and
// the user callback fires with the typed event.
func TestSubscription_HappyPath_AdvanceCursorAndCallback(t *testing.T) {
	tr := &fakeTransport{}
	agents := NewAgentCursor()
	terms := NewTerminalCursor()
	var got *leapmuxv1.AgentEvent
	var gotMu sync.Mutex
	sub := NewSubscription(tr, agents, terms,
		func(ae *leapmuxv1.AgentEvent) { gotMu.Lock(); got = ae; gotMu.Unlock() },
		nil, nil)
	t.Cleanup(sub.Cancel)

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))

	tr.pushFrame(&leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: &leapmuxv1.AgentEvent{
			AgentId: "a-1",
			Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: 17}},
		}},
	})

	assert.Equal(t, int64(17), agents.Get("a-1"), "cursor must advance to message seq")
	gotMu.Lock()
	defer gotMu.Unlock()
	require.NotNil(t, got)
	assert.Equal(t, "a-1", got.GetAgentId())
}

// TestSubscription_TurnEndNotifyDoesNotForward pins that AgentTurnEnd is a
// UI-only notify frame: the CLI events stream must neither invoke onAgent nor
// advance the agent cursor.
func TestSubscription_TurnEndNotifyDoesNotForward(t *testing.T) {
	tr := &fakeTransport{}
	agents := NewAgentCursor()
	var calls int
	sub := NewSubscription(tr, agents, NewTerminalCursor(),
		func(*leapmuxv1.AgentEvent) { calls++ },
		nil, nil)
	t.Cleanup(sub.Cancel)

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))

	tr.pushFrame(&leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: &leapmuxv1.AgentEvent{
			AgentId: "a-1",
			Event:   &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: &leapmuxv1.AgentTurnEnd{NumToolUses: protoInt32(2)}},
		}},
	})

	assert.Equal(t, 0, calls, "turn_end must not reach onAgent")
	assert.Equal(t, int64(0), agents.Get("a-1"), "turn_end must not advance the agent cursor")
}

func protoInt32(v int32) *int32 { return &v }

// fastRetry is a fast LOOKUP_FAILED backoff for tests that need to observe
// retry/exhaust behavior without waiting on the production 15s cap.
func fastRetry() *backoffutil.Retry {
	r, err := backoffutil.NewRetry(10*time.Millisecond, 30*time.Millisecond, 0, 3)
	if err != nil {
		panic(err) // constants are known-valid
	}
	return r
}

// lookupFailedAck builds a WatchEventsResponse carrying a LOOKUP_FAILED
// rejection for entityID. The three LOOKUP_FAILED tests share this frame shape.
func lookupFailedAck(entityID string) *leapmuxv1.WatchEventsResponse {
	return &leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_UpdateAck{UpdateAck: &leapmuxv1.WatchUpdateAck{
			RejectedAgents: []*leapmuxv1.WatchRejection{{
				EntityId: entityID,
				Reason:   leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED,
			}},
		}},
	}
}

// pushLookupFailed delivers a LOOKUP_FAILED ack for entityID through tr's
// frame callback, the shape a flapping worker emits.
func pushLookupFailed(tr *fakeTransport, entityID string) {
	tr.pushFrame(lookupFailedAck(entityID))
}

// pushAgentMessage delivers a live agent chat message frame for agentID with the
// given seq through tr's frame callback. Shared by the tests that exercise
// cursor/callback delivery, mirroring pushLookupFailed for the message shape.
func pushAgentMessage(tr *fakeTransport, agentID string, seq int64) {
	tr.pushFrame(&leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: &leapmuxv1.AgentEvent{
			AgentId: agentID,
			Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: seq}},
		}},
	})
}

// TestSubscription_DedupsOverlapMessageBySeq verifies the
// register-before-replay overlap is collapsed: a WatchEvents subscription
// registers its watcher BEFORE replaying history, so a message created in that
// window is delivered twice -- once live, once in the replay. The dispatcher
// forwards each seq at most once while still advancing the cursor. Because
// message seqs are monotonic (a deleted seq is never reused), the dedup is a
// plain seq <= cursor and needs no replayed flag.
func TestSubscription_DedupsOverlapMessageBySeq(t *testing.T) {
	tr := &fakeTransport{}
	agents := NewAgentCursor()
	terms := NewTerminalCursor()
	var seqs []int64
	var mu sync.Mutex
	sub := NewSubscription(tr, agents, terms,
		func(ae *leapmuxv1.AgentEvent) {
			mu.Lock()
			seqs = append(seqs, ae.GetAgentMessage().GetSeq())
			mu.Unlock()
		},
		nil, nil)
	t.Cleanup(sub.Cancel)

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))

	push := func(seq int64) { pushAgentMessage(tr, "a-1", seq) }

	push(17) // live broadcast
	push(17) // the same message's replay copy -- a duplicate, must be skipped
	push(18) // a genuinely newer message -- forwarded

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []int64{17, 18}, seqs, "the overlap duplicate seq 17 must be forwarded only once")
	assert.Equal(t, int64(18), agents.Get("a-1"), "cursor still advances to the highest seq")
}

// TestSubscription_OverlapDedupIsOrderIndependent pins the robustness the
// monotonic-seq invariant buys: an overlap message's live and replay copies can
// arrive in EITHER order, and exactly one is forwarded. With the old replayed-gated
// dedup the live copy had to win the race (a replay-first delivery produced a
// duplicate); a plain seq <= cursor drop forwards whichever lands first and drops
// the second regardless of order.
func TestSubscription_OverlapDedupIsOrderIndependent(t *testing.T) {
	tr := &fakeTransport{}
	agents := NewAgentCursor()
	var seqs []int64
	var mu sync.Mutex
	sub := NewSubscription(tr, agents, NewTerminalCursor(),
		func(ae *leapmuxv1.AgentEvent) {
			mu.Lock()
			seqs = append(seqs, ae.GetAgentMessage().GetSeq())
			mu.Unlock()
		},
		nil, nil)
	t.Cleanup(sub.Cancel)
	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))

	push := func(seq int64) { pushAgentMessage(tr, "a-1", seq) }

	// Replay copy arrives BEFORE the live copy (the order the old gate mishandled).
	push(9) // first copy of the overlap message -- forwarded, cursor -> 9
	push(9) // second copy of the SAME message -- dropped (seq <= cursor)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []int64{9}, seqs, "an overlap message is forwarded exactly once regardless of copy order")
}

// TestSubscription_ForwardsEphemeralNegativeSeqFrames pins the
// regression where the seq-dedup swallowed ephemeral frames: an
// agent_session_info frame carries a negative sentinel seq (-1) and is
// never replayed, so it must ALWAYS forward and must NOT touch the
// cursor. A naive `seq <= cursor` check drops every such frame (-1 is
// always <= any non-negative cursor), starving `--follow` of live
// thinking-token / usage / rate-limit updates.
func TestSubscription_ForwardsEphemeralNegativeSeqFrames(t *testing.T) {
	tr := &fakeTransport{}
	agents := NewAgentCursor()
	terms := NewTerminalCursor()
	var seqs []int64
	var mu sync.Mutex
	sub := NewSubscription(tr, agents, terms,
		func(ae *leapmuxv1.AgentEvent) {
			mu.Lock()
			seqs = append(seqs, ae.GetAgentMessage().GetSeq())
			mu.Unlock()
		},
		nil, nil)
	t.Cleanup(sub.Cancel)

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))

	push := func(seq int64) { pushAgentMessage(tr, "a-1", seq) }

	push(7)  // a real message advances the cursor to 7
	push(-1) // ephemeral session-info: must forward despite -1 <= 7
	push(-1) // a second ephemeral frame must ALSO forward (not deduped)
	push(8)  // a real newer message still forwards

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []int64{7, -1, -1, 8}, seqs,
		"ephemeral negative-seq frames must always forward; real seqs dedup")
	assert.Equal(t, int64(8), agents.Get("a-1"),
		"ephemeral frames must not advance or lower the cursor")
}

// TestSubscription_TerminalDataAdvancesOffset confirms terminal
// events update the right cursor and surface to the terminal
// callback (not the agent one).
func TestSubscription_TerminalDataAdvancesOffset(t *testing.T) {
	tr := &fakeTransport{}
	agents := NewAgentCursor()
	terms := NewTerminalCursor()
	gotTerm := make(chan *leapmuxv1.TerminalEvent, 1)
	sub := NewSubscription(tr, agents, terms,
		nil,
		func(te *leapmuxv1.TerminalEvent) { gotTerm <- te },
		nil)
	t.Cleanup(sub.Cancel)

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "t-1"}}}))

	tr.pushFrame(&leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_TerminalEvent{TerminalEvent: &leapmuxv1.TerminalEvent{
			TerminalId: "t-1",
			Event: &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{
				Data:      []byte("hello"),
				EndOffset: 5,
			}},
		}},
	})

	assert.Equal(t, int64(5), terms.Get("t-1"))
	select {
	case got := <-gotTerm:
		assert.Equal(t, "t-1", got.GetTerminalId())
	case <-time.After(time.Second):
		t.Fatal("terminal callback never fired")
	}
}

// TestSubscription_CursorResetCallback fires when a TerminalData
// frame carries `is_snapshot=true`. Consumers use this to emit a
// notice and (optionally) reset their cursor before processing the
// snapshot replay.
func TestSubscription_CursorResetCallback(t *testing.T) {
	tr := &fakeTransport{}
	agents := NewAgentCursor()
	terms := NewTerminalCursor()
	resets := make(chan string, 1)
	sub := NewSubscription(tr, agents, terms,
		nil, nil,
		func(terminalID string) { resets <- terminalID })
	t.Cleanup(sub.Cancel)
	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: "t-1"}}}))

	tr.pushFrame(&leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_TerminalEvent{TerminalEvent: &leapmuxv1.TerminalEvent{
			TerminalId: "t-1",
			Event: &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{
				IsSnapshot: true,
				EndOffset:  42,
			}},
		}},
	})

	select {
	case id := <-resets:
		assert.Equal(t, "t-1", id)
	case <-time.After(time.Second):
		t.Fatal("cursor_reset callback never fired")
	}
}

// TestSubscription_UpdateRevisesSameStream verifies that a second Update
// sends a revision on the same stream instead of re-opening.
func TestSubscription_UpdateRevisesSameStream(t *testing.T) {
	tr := &fakeTransport{}
	agents := NewAgentCursor()
	terms := NewTerminalCursor()
	sub := NewSubscription(tr, agents, terms, nil, nil, nil)
	t.Cleanup(sub.Cancel)

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))
	tr.pushFrame(&leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: &leapmuxv1.AgentEvent{
			AgentId: "a-1",
			Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: 9}},
		}},
	})
	assert.Equal(t, int64(9), agents.Get("a-1"))

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: agents.Snapshot(map[string]struct{}{"a-1": {}})}))
	last := tr.lastUpdate()
	require.NotNil(t, last)
	require.Len(t, last.GetAgents(), 1)
	assert.Equal(t, "a-1", last.GetAgents()[0].GetAgentId())
	assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, last.GetAgents()[0].GetReplay())
	assert.Equal(t, int64(9), last.GetAgents()[0].GetCursorSeq())
	assert.Equal(t, int32(1), atomic.LoadInt32(&tr.callsOpened),
		"Update should revise the open stream, not re-open")
}

// TestSubscription_CancelIdempotent: repeated Cancel calls are safe
// and the underlying transport is only torn down once.
func TestSubscription_CancelIdempotent(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{}))
	sub.Cancel()
	sub.Cancel()
	// Done() returns a closed channel after Cancel.
	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("Done() did not fire after Cancel")
	}
}

// TestSubscription_TransportOpenError surfaces opener failures via
// Update's return value and leaves the subscription in a recoverable
// state — the next Update can retry.
func TestSubscription_TransportOpenError(t *testing.T) {
	tr := &fakeTransport{openErr: errors.New("nope")}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	err := sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")

	// Subsequent attempt should succeed because openErr was cleared
	// in the fake. Mirrors the production reconnect-after-error path.
	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{}))
	t.Cleanup(sub.Cancel)
}

// TestSubscription_UpdateReopensAfterDone pins that a natural stream end
// (Done closed, handle still non-nil) makes the next Update open a fresh
// stream instead of revising the dead Handle — the agent messages --follow
// reconnect loop.
func TestSubscription_UpdateReopensAfterDone(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}},
	}))
	require.Equal(t, int32(1), atomic.LoadInt32(&tr.callsOpened))

	// Simulate natural end: cancel the handle's ctx (closes Done) without
	// going through Subscription.Cancel (which would nil s.handle).
	tr.mu.Lock()
	h := tr.handle
	tr.mu.Unlock()
	require.NotNil(t, h)
	h.Cancel()
	<-h.Done()

	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}},
	}))
	assert.Equal(t, int32(2), atomic.LoadInt32(&tr.callsOpened),
		"Update after Done must OpenWatchEvents again, not revise the dead handle")
	sub.Cancel()
}

// TestSubscription_LookupFailedAckRetriesLastReq pins that a LOOKUP_FAILED
// UpdateAck restates the stored lastReq after the short retry delay.
func TestSubscription_LookupFailedAckRetriesLastReq(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	t.Cleanup(sub.Cancel)

	req := &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}},
	}
	require.NoError(t, sub.Update(context.Background(), req))
	require.Equal(t, int32(1), atomic.LoadInt32(&tr.callsOpened))

	pushLookupFailed(tr, "a-1")

	require.Eventually(t, func() bool {
		tr.mu.Lock()
		h := tr.handle
		tr.mu.Unlock()
		if h == nil {
			return false
		}
		h.mu.Lock()
		n := len(h.updates)
		h.mu.Unlock()
		return n >= 1
	}, time.Second, 20*time.Millisecond, "LOOKUP_FAILED ack must restate lastReq via Update")

	tr.mu.Lock()
	h := tr.handle
	tr.mu.Unlock()
	require.NotNil(t, h)
	h.mu.Lock()
	last := h.updates[len(h.updates)-1]
	h.mu.Unlock()
	require.NotNil(t, last)
	require.Len(t, last.GetAgents(), 1)
	assert.Equal(t, "a-1", last.GetAgents()[0].GetAgentId())
}

// TestSubscription_LookupFailedRetryReadsLatestLastReqAtFireTime pins the
// fire-time re-read fix (SCAN-1): the LOOKUP_FAILED retry goroutine must re-read
// s.lastReq when its timer fires, not restate the arm-time snapshot. Before the
// fix the goroutine captured the arm-time lastReq pointer and called Update with
// it at fire time, so a revise issued during the backoff window was clobbered
// when the stale plan was revised back onto the live handle — the new entity's
// events stalled until the next external reconnect. This mirrors the frontend's
// useWatchEventsStreams fire-time plan read.
func TestSubscription_LookupFailedRetryReadsLatestLastReqAtFireTime(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	sub.retry = fastRetry() // 10-30ms, jitterless
	t.Cleanup(sub.Cancel)

	// Open with {a-1}; the retry will arm against this interest.
	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))

	// Arm a retry by pushing a LOOKUP_FAILED ack for a-1.
	pushLookupFailed(tr, "a-1")

	// While the retry is pending (10-30ms window), revise the live stream to add
	// a-2. The retry must fire against THIS interest, not the arm-time {a-1}.
	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{
			{AgentId: "a-1"}, {AgentId: "a-2"},
		}}))

	// Wait for the retry's restatement to land as a SECOND update on the handle
	// (the first is the manual revise above). The discriminator: the retry's
	// restatement is the LAST update, and it must carry BOTH agents. An arm-time
	// snapshot would restate only {a-1}, so the final update would drop a-2.
	require.Eventually(t, func() bool {
		tr.mu.Lock()
		h := tr.handle
		tr.mu.Unlock()
		if h == nil {
			return false
		}
		h.mu.Lock()
		count := len(h.updates)
		var ids map[string]bool
		if count >= 2 {
			last := h.updates[count-1]
			ids = make(map[string]bool, len(last.GetAgents()))
			for _, a := range last.GetAgents() {
				ids[a.GetAgentId()] = true
			}
		}
		h.mu.Unlock()
		return count >= 2 && ids["a-1"] && ids["a-2"]
	}, time.Second, 5*time.Millisecond,
		"retry's restatement (the 2nd update) must carry the fire-time lastReq (both a-1 and a-2), not the arm-time {a-1}")
}

// TestSubscription_LookupFailedRetryWakesImmediatelyOnCancel pins the
// retryCtx fix (REMOVALS-1): an armed LOOKUP_FAILED retry goroutine must NOT
// linger for up to lookupRetryMaxInterval after Cancel returns. Before the fix
// the goroutine blocked on a bare <-t.C with no cancellation, so it (and its
// captured request) stayed alive for up to the 15s cap after shutdown. The
// goroutine now selects on retryCtx.Done(), which Cancel cancels, so it bails
// promptly.
func TestSubscription_LookupFailedRetryWakesImmediatelyOnCancel(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	// A long delay so that without the retryCtx select the goroutine would
	// demonstrably outlive Cancel by a wide margin.
	r, err := backoffutil.NewRetry(30*time.Second, 60*time.Second, 0, 3)
	require.NoError(t, err)
	sub.retry = r

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))

	// Arm the retry (30s delay). Without the retryCtx select this goroutine would
	// sleep for 30s; Cancel must wake it well before that.
	pushLookupFailed(tr, "a-1")

	// Give the goroutine a moment to arm, then Cancel. The retry must bail via
	// retryCtx.Done() and refund its slot — observable as Attempts()==0 (the slot
	// was refunded, not spent).
	time.Sleep(20 * time.Millisecond)

	// Cancel must wake the retry via retryCtx rather than waiting out the 30s
	// timer -- asserted as a COMPLETION against a generous deadline, not as a
	// 1s budget. Both forms catch the same defect (a Cancel that blocks on the
	// timer takes 30s), but a completion guard cannot be crossed by machine
	// load and reports the defect rather than the budget.
	cancelled := make(chan struct{})
	go func() {
		defer close(cancelled)
		sub.Cancel()
	}()
	select {
	case <-cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("Cancel blocked on the retry timer instead of waking it via retryCtx")
	}
	sub.mu.Lock()
	attempts := sub.retry.Attempts()
	sub.mu.Unlock()
	assert.Equal(t, 0, attempts, "a Cancelled retry must refund its consumed slot")
}

// TestSubscription_ReviseFailureRollsBackInflightUpdateId pins the
// stale-ack-filter invariant fix (ALTITUDE-2): when a revise's handle.Update
// fails, the inflight update_id must roll back to the prior value, so a
// subsequent LOOKUP_FAILED ack for the still-current prior revision is not
// dropped as stale. Before the fix assignUpdateId bumped the id before the send,
// and a failed send left it advanced past a revision never put on the wire.
func TestSubscription_ReviseFailureRollsBackInflightUpdateId(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	sub.retry = fastRetry()
	t.Cleanup(sub.Cancel)

	// Open with {a-1}; this sets inflightUpdateId = 1.
	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))
	sub.mu.Lock()
	openInflight := sub.inflightUpdateId
	sub.mu.Unlock()
	require.Equal(t, uint64(1), openInflight)

	// Make the next revise (handle.Update) fail.
	tr.mu.Lock()
	tr.handle.updateFn = func(*leapmuxv1.WatchEventsRequest) error { return errors.New("send failed") }
	tr.mu.Unlock()

	// Revise to {a-2}; the send fails, so the wire still carries {a-1} (id 1).
	err := sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-2"}}})
	require.Error(t, err)

	// The inflight id must be rolled back to the open's id (1), not left at 2.
	// If it were left at 2, a LOOKUP_FAILED ack for the still-live {a-1} (id 1)
	// would be dropped as stale by the filter.
	sub.mu.Lock()
	inflight := sub.inflightUpdateId
	sub.mu.Unlock()
	assert.Equal(t, openInflight, inflight,
		"a failed revise must roll back inflightUpdateId so the prior (live) revision's acks are not stale")
}

// TestSubscription_FreshOpenClearsExhaustionOnceGuard pins SWEEP-3: a fresh
// stream open must clear retryExhaustedLogged so the budget-exhausted Warn can
// fire again on the new stream's own exhaustion cycle. Without this, a budget
// that exhausted on one stream would never log exhaustion again after a
// reconnect.
func TestSubscription_FreshOpenClearsExhaustionOnceGuard(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	sub.retry = fastRetry()
	t.Cleanup(sub.Cancel)

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))

	// Exhaust the budget and set the once-guard directly.
	sub.mu.Lock()
	for !sub.retry.Done() {
		sub.retry.Peek()
		sub.retry.Commit()
	}
	sub.retryExhaustedLogged = true
	sub.mu.Unlock()
	require.True(t, sub.retry.Done(), "budget pre-exhausted")

	// End the handle and re-open via Update (the fresh-open path).
	tr.mu.Lock()
	h := tr.handle
	tr.mu.Unlock()
	require.NotNil(t, h)
	h.Cancel()
	<-h.Done()

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))

	// The fresh open must have cleared both the budget and the once-guard.
	sub.mu.Lock()
	done := sub.retry.Done()
	logged := sub.retryExhaustedLogged
	sub.mu.Unlock()
	assert.False(t, done, "fresh open must reset the budget")
	assert.False(t, logged, "fresh open must clear retryExhaustedLogged")
}

// TestSubscription_ErrSubscriptionClosedIsExported pins that the sentinel
// returned by Update after Cancel is the exported ErrSubscriptionClosed, so
// cross-package callers (agent_messages.go's reconnect loop) can distinguish it
// from a real subscribe failure via errors.Is.
func TestSubscription_ErrSubscriptionClosedIsExported(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{}))
	sub.Cancel()
	err := sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{})
	require.ErrorIs(t, err, ErrSubscriptionClosed,
		"post-Cancel Update must return the exported ErrSubscriptionClosed sentinel")
}

// TestSubscription_LookupRetryPolicyMirrorsFrontend pins the LOOKUP_FAILED
// retry policy values against the documented frontend shape
// (frontend/src/hooks/useWatchEventsStreams.ts rejectionBackoff over
// createExponentialBackoff: initialMs=500, maxMs=15000, multiplier=2,
// jitterFactor=0.2, maxAttempts=8). Cross-language constant sharing is
// impractical, so this test is the drift alarm: a change to the values here
// without a matching frontend change (or a deliberate divergence documented in
// the constant block) fails the suite.
func TestSubscription_LookupRetryPolicyMirrorsFrontend(t *testing.T) {
	assert.Equal(t, 500*time.Millisecond, lookupRetryInitial,
		"initial must mirror frontend initialMs=500")
	assert.Equal(t, 15*time.Second, lookupRetryMaxInterval,
		"maxInterval must mirror frontend maxMs=15000")
	assert.InDelta(t, 0.2, lookupRetryJitter, 1e-9,
		"jitter must mirror frontend jitterFactor=0.2")
	assert.Equal(t, 8, lookupRetryMaxAttempts,
		"maxAttempts must mirror frontend maxAttempts=8")
}

// TestSubscription_LookupFailedAckWithNilLastReqDoesNotConsumeBudget pins the
// nil-check-before-Next fix: a LOOKUP_FAILED ack that arrives when lastReq is
// nil (e.g. a spurious rejection racing a fresh open) must NOT consume a retry
// attempt. Without the check, dispatchUpdateAck called Next() before testing
// req == nil, silently eroding the 8-attempt budget on no-op acks until a real
// LOOKUP_FAILED got ok=false and the CLI stopped retrying for the stream's life.
func TestSubscription_LookupFailedAckWithNilLastReqDoesNotConsumeBudget(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	sub.retry = fastRetry()
	t.Cleanup(sub.Cancel)

	// Construct a sub with a live handle but no lastReq. Update sets lastReq, so
	// nil it directly under s.mu to simulate the race window before the first
	// interest is published (or after it's cleared).
	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}},
	}))
	sub.mu.Lock()
	sub.lastReq = nil
	sub.retry.Reset()
	sub.mu.Unlock()

	// A LOOKUP_FAILED ack with no lastReq to restate must be a no-op on the budget.
	pushLookupFailed(tr, "a-1")

	// Give the (non-)retry a moment to settle, then assert no attempt was consumed.
	time.Sleep(50 * time.Millisecond)
	sub.mu.Lock()
	attempts := sub.retry.Attempts()
	sub.mu.Unlock()
	assert.Equal(t, 0, attempts, "LOOKUP_FAILED ack with nil lastReq must not consume a retry attempt")
}

// TestSubscription_LookupFailedRetryDoesNotReopenAfterCancel pins the fix for a
// goroutine-leak / orphaned-subscription bug: the LOOKUP_FAILED retry used
// context.Background with no Cancel guard, so a retry that woke after Cancel
// reopened a fresh worker-side stream nobody would ever tear down. Cancel bumps
// the generation, cancels retryCtx (waking the sleeper), and sets the closed
// flag so the in-flight retry bails (and Update refuses the re-open) instead of
// re-opening. It also pins the consume-on-fire budget invariant: a retry whose
// generation moved mid-wait Rolled back its peeked slot (never committed), so a
// Cancel does not silently erode the attempt budget.
func TestSubscription_LookupFailedRetryDoesNotReopenAfterCancel(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	sub.retry = fastRetry() // fast, jitterless; Cancel below runs synchronously

	req := &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}},
	}
	require.NoError(t, sub.Update(context.Background(), req))
	openedBefore := atomic.LoadInt32(&tr.callsOpened)
	require.Equal(t, int32(1), openedBefore)

	// Push LOOKUP_FAILED, then Cancel inside the retry delay window.
	pushLookupFailed(tr, "a-1")
	// Cancel before the 10ms retry fires.
	sub.Cancel()

	// Wait past the first retry delay and assert no re-open landed.
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, openedBefore, atomic.LoadInt32(&tr.callsOpened),
		"LOOKUP_FAILED retry must not re-open a stream after Cancel")
	// The retry bailed when its gate retired, so it must refund the slot it
	// consumed -- without this, a Cancel mid-retry would burn budget for nothing.
	sub.mu.Lock()
	attempts := sub.retry.Attempts()
	sub.mu.Unlock()
	assert.Equal(t, 0, attempts, "a retry cancelled mid-wait must refund its attempt slot")
}

// TestSubscription_LookupFailedRetryBudgetExhausts pins the bounded-retry fix:
// previously every LOOKUP_FAILED ack spawned a fresh goroutine that produced
// another LOOKUP_FAILED ack, compounding without limit on a flapping worker.
// The retry must stop after the budget is spent.
func TestSubscription_LookupFailedRetryBudgetExhausts(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	sub.retry = fastRetry() // 3 attempts, 10-30ms, jitterless
	t.Cleanup(sub.Cancel)

	req := &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-missing"}},
	}
	require.NoError(t, sub.Update(context.Background(), req))

	// Simulate a worker that perpetually rejects "a-missing" with LOOKUP_FAILED.
	// The feedback loop: a retry re-states the plan, the worker rejects again.
	// We bootstrap the first ack (opening the stream does not call handle.Update,
	// so lastUpdate() is nil until the first retry lands), then perpetuate.
	pushLookupFailed(tr, "a-missing") // bootstrap the retry ladder

	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				if tr.lastUpdate() != nil {
					pushLookupFailed(tr, "a-missing")
				}
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	// The budget must exhaust after exactly maxAttempts retries. Read retry state
	// under s.mu -- the same lock production serializes retry access through.
	require.Eventually(t, func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return sub.retry.Done()
	}, 500*time.Millisecond, 5*time.Millisecond,
		"retry budget should exhaust after maxAttempts LOOKUP_FAILED acks")
	close(stop)

	// Let any in-flight retry settle, then read the final attempt count under s.mu.
	time.Sleep(50 * time.Millisecond)
	sub.mu.Lock()
	attempts := sub.retry.Attempts()
	sub.mu.Unlock()

	assert.Equal(t, int32(1), atomic.LoadInt32(&tr.callsOpened),
		"LOOKUP_FAILED retries must not re-open the stream (no re-open ladder)")
	assert.Equal(t, 3, attempts, "exactly maxAttempts retries consumed")
}

// TestSubscription_FreshOpenResetsRetryBudget pins that a fresh stream open
// (handle dead -> Update re-opens) re-arms a previously-exhausted LOOKUP_FAILED
// budget. Without this, a transient miss after a reconnect would inherit a spent
// budget and never retry.
func TestSubscription_FreshOpenResetsRetryBudget(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	sub.retry = fastRetry() // 3 attempts, jitterless
	t.Cleanup(sub.Cancel)

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))

	// Exhaust the budget directly under s.mu (the lock production uses).
	sub.mu.Lock()
	for !sub.retry.Done() {
		sub.retry.Peek()
		sub.retry.Commit()
	}
	sub.mu.Unlock()
	require.True(t, sub.retry.Done(), "budget pre-exhausted")

	// End the handle the way a natural transport disconnect would, then Update.
	tr.mu.Lock()
	h := tr.handle
	tr.mu.Unlock()
	require.NotNil(t, h)
	h.Cancel()
	<-h.Done()

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))
	assert.Equal(t, int32(2), atomic.LoadInt32(&tr.callsOpened), "Update re-opened the stream")

	// The fresh open must have restored the budget.
	sub.mu.Lock()
	done := sub.retry.Done()
	sub.mu.Unlock()
	assert.False(t, done, "fresh open must reset the LOOKUP_FAILED retry budget")
}

// TestSubscription_LookupFailedRetryReopenSurvivesGateRetire pins the
// born-dead-stream regression: a LOOKUP_FAILED retry whose Update takes the
// fresh-open path must NOT open a stream that immediately dies. The retry re-
// opens on context.Background (no gate for a fresh-open to retire mid-open),
// so the born-dead failure mode is structurally impossible. This test stays as
// a regression guard against any future re-introduction of a gate the retry's
// own Update would cancel.
func TestSubscription_LookupFailedRetryReopenSurvivesGateRetire(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	sub.retry = fastRetry() // fast, jitterless
	t.Cleanup(sub.Cancel)

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))
	require.Equal(t, int32(1), atomic.LoadInt32(&tr.callsOpened))

	// End the handle the way a natural transport disconnect would, so the retry's
	// Update takes the fresh-open path (handle == nil -> OpenWatchEvents).
	tr.mu.Lock()
	first := tr.handle
	tr.mu.Unlock()
	require.NotNil(t, first)
	first.Cancel()
	<-first.Done()

	// Push LOOKUP_FAILED so a retry arms. The retry fires after ~10ms and calls
	// Update, which re-opens (fresh-open). The re-open runs on context.Background
	// (no gate), so nothing inside Update can cancel the newborn stream.
	pushLookupFailed(tr, "a-1")

	// Wait for the retry's re-open to land.
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&tr.callsOpened) >= 2
	}, time.Second, 5*time.Millisecond, "LOOKUP_FAILED retry must re-open the stream")

	// Let the retry's Update finish so the assertion observes the post-open
	// state. (The re-open publishes under s.mu; Cancel/next-Update serialize on
	// lifecycleMu, so this settle only waits for the in-flight Update.)
	time.Sleep(50 * time.Millisecond)

	// The re-opened stream must be alive. A generation/WithoutCancel regression
	// that re-introduced a gate the retry's own Update cancels would close
	// Done() immediately and discard the recovery.
	tr.mu.Lock()
	reopened := tr.handle
	tr.mu.Unlock()
	require.NotNil(t, reopened)
	select {
	case <-reopened.Done():
		t.Fatal("re-opened stream must not be born dead")
	default:
	}
}

// TestSubscription_UpdateErrorAfterSuccessLeavesCleanState covers the
// success-then-failure path: a later Update on an open stream fails.
// The subscription must retain the live handle until Cancel.
func TestSubscription_UpdateErrorAfterSuccessLeavesCleanState(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{}))

	tr.mu.Lock()
	tr.handle.updateFn = func(*leapmuxv1.WatchEventsRequest) error { return errors.New("update failed") }
	tr.mu.Unlock()
	require.Error(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{}))

	// Done() must still track the open stream.
	select {
	case <-sub.Done():
		t.Fatal("Done() must not close while the stream is still open")
	default:
	}
	sub.Cancel()
	t.Cleanup(func() {})
}

// TestSubscription_NilFrameDoesNotCrash defends the dispatcher
// against a transport that bubbles a nil frame (proto Unmarshal
// edge case). Subscriptions live a long time and a single bad
// frame must not take down the entire `events` command.
func TestSubscription_NilFrameDoesNotCrash(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{}))
	t.Cleanup(sub.Cancel)
	require.NotPanics(t, func() { tr.pushFrame(nil) })
	require.NotPanics(t, func() {
		tr.pushFrame(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: nil},
		})
	})
}

// TestSubscription_NilCallbacksAreSafe defends against the partially-
// configured Subscription case: callers that only care about agents
// pass nil for the terminal callback and vice-versa, and `agent
// messages --follow` doesn't supply an onCursorReset callback at
// all. The dispatcher must silently skip the absent callbacks
// instead of nil-derefing on the first matching frame.
func TestSubscription_NilCallbacksAreSafe(t *testing.T) {
	tr := &fakeTransport{}
	agents := NewAgentCursor()
	terms := NewTerminalCursor()
	sub := NewSubscription(tr, agents, terms, nil, nil, nil)
	t.Cleanup(sub.Cancel)
	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{}))

	require.NotPanics(t, func() {
		tr.pushFrame(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: &leapmuxv1.AgentEvent{
				AgentId: "a-1",
				Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: 9}},
			}},
		})
	})
	// Cursor still advances even without a callback.
	assert.Equal(t, int64(9), agents.Get("a-1"))

	require.NotPanics(t, func() {
		tr.pushFrame(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_TerminalEvent{TerminalEvent: &leapmuxv1.TerminalEvent{
				TerminalId: "t-1",
				Event:      &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{IsSnapshot: true, EndOffset: 4}},
			}},
		})
	})
	// Terminal cursor advances; nil onCursorReset is a no-op.
	assert.Equal(t, int64(4), terms.Get("t-1"))
}

// TestSubscription_TerminalDataNilDoesNotCrash defends against a
// malformed/empty `TerminalData` payload (e.g. sender bug where the
// field is set to a zero-valued message). The dispatcher inspects
// `data.GetIsSnapshot()` and `data.GetEndOffset()` after a nil-check;
// without that check a malformed frame would take down the whole
// `events` command.
func TestSubscription_TerminalDataNilDoesNotCrash(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil,
		func(*leapmuxv1.TerminalEvent) {}, nil)
	t.Cleanup(sub.Cancel)
	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{}))

	require.NotPanics(t, func() {
		// TerminalEvent with no Event oneof set — Data() returns nil.
		tr.pushFrame(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_TerminalEvent{TerminalEvent: &leapmuxv1.TerminalEvent{
				TerminalId: "t-1",
			}},
		})
	})
}

// TestSubscription_CursorAdvancesBeforeCallback documents the order
// guarantee in the dispatcher: the cursor is updated BEFORE the user
// callback fires, so a callback that crashes (panics, errors out)
// doesn't leave the cursor stale on the next reconnect. This
// matters for `agent messages --follow` where a write to stdout
// could fail mid-stream.
func TestSubscription_CursorAdvancesBeforeCallback(t *testing.T) {
	tr := &fakeTransport{}
	agents := NewAgentCursor()
	cbObservedSeq := int64(-1)
	cbObservedCursor := int64(-1)
	var mu sync.Mutex
	sub := NewSubscription(tr, agents, NewTerminalCursor(),
		func(ae *leapmuxv1.AgentEvent) {
			mu.Lock()
			defer mu.Unlock()
			cbObservedSeq = ae.GetAgentMessage().GetSeq()
			cbObservedCursor = agents.Get(ae.GetAgentId())
		},
		nil, nil)
	t.Cleanup(sub.Cancel)
	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{}))

	tr.pushFrame(&leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: &leapmuxv1.AgentEvent{
			AgentId: "a-1",
			Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: 42}},
		}},
	})

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return cbObservedSeq == 42
	}, time.Second, 10*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, int64(42), cbObservedCursor,
		"callback must observe the cursor already advanced to its event's seq")
}

// TestSubscription_DoneClosedAfterCancel pins the goroutine cleanup
// contract: after Cancel returns, Done() must yield a closed
// channel. The reconnect loop in `tailAgentMessages` relies on
// blocking on `<-sub.Done()` after Update returns; if Cancel didn't
// close the channel, that loop would hang forever on shutdown.
func TestSubscription_DoneClosedAfterCancel(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{}))
	doneBefore := sub.Done()

	sub.Cancel()

	select {
	case <-doneBefore:
	case <-time.After(time.Second):
		t.Fatal("Done() channel from before Cancel must close on Cancel")
	}
}

// TestSubscription_DoneOnFreshSubReturnsClosed: callers that grab
// Done() before the first Update must get back a closed channel
// rather than block forever. Mirrors the single-line guard in
// Done() that handles the no-active-subscription case.
func TestSubscription_DoneOnFreshSubReturnsClosed(t *testing.T) {
	sub := NewSubscription(&fakeTransport{}, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	select {
	case <-sub.Done():
		// Expected: closed channel.
	case <-time.After(time.Second):
		t.Fatal("Done() on un-Updated Subscription must return a closed channel")
	}
}

// TestSubscription_UpdateAfterCancelReturnsClosed pins the closed-defense that
// stops the late-cancel orphan race: a LOOKUP_FAILED retry whose timer fired
// still reaches Update, and nothing in the retry path (generation check, no
// gate) can stop the re-open once the timer has fired. The `closed` flag — set
// by Cancel under lifecycleMu and checked by Update under the same lock — is
// what refuses the re-open. Without it, the retry would fresh-open a
// worker-side stream the caller already tore down.
func TestSubscription_UpdateAfterCancelReturnsClosed(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}},
	}))
	require.Equal(t, int32(1), atomic.LoadInt32(&tr.callsOpened))

	sub.Cancel()

	// A post-Cancel Update (the path a late retry takes) must NOT re-open: it
	// returns the closed sentinel and leaves callsOpened at 1.
	err := sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}},
	})
	require.ErrorIs(t, err, errSubscriptionClosed)
	assert.Equal(t, int32(1), atomic.LoadInt32(&tr.callsOpened),
		"Update after Cancel must not re-open a stream (no orphan)")
}

// TestSubscription_LateCancelRetryDoesNotOrphan exercises the late-cancel race
// window the closed-flag defends: the retry's timer fires and the generation
// check passes (the generation has not moved yet), so the retry proceeds into
// Update. The retry's Update blocks inside OpenWatchEvents (via releaseOpen);
// Cancel then runs and blocks on lifecycleMu. Releasing the open lets the
// retry's Update finish and publish a handle, which Cancel (next in line for
// lifecycleMu) then tears down. The assertion: after Cancel returns, no stream
// is active (active==0) -- the retry's re-open did not survive shutdown as an
// orphan. Before the closed-flag defense the re-open's handle was published
// after Cancel snapshotted-and-nilled the prior handle, so Cancel never
// cancelled it.
func TestSubscription_LateCancelRetryDoesNotOrphan(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	sub.retry = fastRetry() // fast, jitterless

	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}},
	}))
	require.Equal(t, int32(1), atomic.LoadInt32(&tr.callsOpened))

	// End the handle the way a natural transport disconnect would, so the retry's
	// Update takes the fresh-open path (handle == nil -> OpenWatchEvents).
	tr.mu.Lock()
	first := tr.handle
	tr.mu.Unlock()
	require.NotNil(t, first)
	first.Cancel()
	<-first.Done()

	// Block the retry's re-open inside OpenWatchEvents so Cancel can race it.
	// openStarted fires when OpenWatchEvents is entered; releaseOpen gates when
	// it returns. The retry's open blocks between the two.
	openStarted := make(chan struct{}, 4)
	releaseOpen := make(chan struct{})
	tr.mu.Lock()
	tr.openStarted = openStarted
	tr.releaseOpen = releaseOpen
	tr.mu.Unlock()

	// Arm the retry. It fires after ~10ms, passes the gate check (the gate is
	// still live), enters Update, and blocks on releaseOpen mid-open.
	pushLookupFailed(tr, "a-1")

	// Wait for the retry's re-open to enter OpenWatchEvents.
	select {
	case <-openStarted:
	case <-time.After(time.Second):
		t.Fatal("retry's re-open never entered OpenWatchEvents")
	}

	// Cancel while the retry's Update holds lifecycleMu inside OpenWatchEvents.
	// Cancel blocks on lifecycleMu until the retry's Update returns.
	cancelDone := make(chan struct{})
	go func() {
		sub.Cancel()
		close(cancelDone)
	}()

	// Let the retry's Update finish its open (publishing a handle), then Cancel
	// acquires lifecycleMu and must tear that handle down.
	close(releaseOpen)

	select {
	case <-cancelDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel did not return after the retry's Update completed")
	}

	// After Cancel returns, no stream may be active: the retry's re-open did not
	// survive as an orphan. (callsOpened may be 2 -- the open happened -- but the
	// stream is cancelled, so active drops to 0 once the handle's ctx fires.)
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&tr.active) == 0
	}, time.Second, 5*time.Millisecond,
		"the retry's late re-open must be torn down by Cancel, not orphaned")
}

// TestSubscription_UpdateAssignsMonotonicUpdateId pins the update_id
// instrumentation (SWEEP-8): every Update stamps the request with a monotonic
// id, so the stale-ack filter can tell a current ack from one for a superseded
// revision. The open and each revise must carry strictly increasing ids.
func TestSubscription_UpdateAssignsMonotonicUpdateId(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	t.Cleanup(sub.Cancel)

	// Open.
	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))
	tr.mu.Lock()
	openReq := tr.calls[len(tr.calls)-1]
	tr.mu.Unlock()
	require.Equal(t, uint64(1), openReq.GetUpdateId(), "open carries the first update_id")

	// Revise twice; each must carry a strictly larger id.
	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-2"}}}))
	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-3"}}}))

	tr.mu.Lock()
	h := tr.handle
	tr.mu.Unlock()
	require.NotNil(t, h)
	h.mu.Lock()
	require.Greater(t, h.updates[0].GetUpdateId(), openReq.GetUpdateId())
	require.Greater(t, h.updates[1].GetUpdateId(), h.updates[0].GetUpdateId())
	h.mu.Unlock()
}

// TestSubscription_StaleLookupFailedAckDoesNotConsumeBudget pins the stale-ack
// filter (SWEEP-8): a LOOKUP_FAILED ack whose update_id is below the inflight id
// is dropped before it can arm a retry. Without this, a stale ack from a
// superseded revision would burn the budget on a LOOKUP_FAILED the newer
// revision is already resolving. update_id=0 (no revision info) is always
// processed, matching the frontend.
func TestSubscription_StaleLookupFailedAckDoesNotConsumeBudget(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	sub.retry = fastRetry()
	t.Cleanup(sub.Cancel)

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))
	// Bump the inflight id so a stale ack (update_id=1) is below it.
	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-2"}}}))

	// A stale ack (update_id=1, below the current inflight id of 2) must be
	// dropped — no retry armed, no budget consumed.
	tr.pushFrame(&leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_UpdateAck{UpdateAck: &leapmuxv1.WatchUpdateAck{
			UpdateId: 1,
			RejectedAgents: []*leapmuxv1.WatchRejection{{
				EntityId: "a-1",
				Reason:   leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED,
			}},
		}},
	})
	time.Sleep(50 * time.Millisecond)
	sub.mu.Lock()
	attempts := sub.retry.Attempts()
	inFlight := sub.retryInFlight
	sub.mu.Unlock()
	assert.Equal(t, 0, attempts, "a stale ack must not consume the retry budget")
	assert.False(t, inFlight, "a stale ack must not arm a retry")

	// A current ack (update_id=0 is always processed, matching the frontend)
	// for an entity STILL in the interest arms a retry and consumes a slot.
	pushLookupFailed(tr, "a-2")
	require.Eventually(t, func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return sub.retry.Attempts() >= 1
	}, time.Second, 5*time.Millisecond, "a current ack for a wanted entity must arm a retry")
}

// TestSubscription_LookupFailedAckForDroppedEntityDoesNotRetry pins the per-
// entity liveness check (ALTITUDE-12): a LOOKUP_FAILED for an entity the CLI
// already dropped from its interest must NOT arm a retry, so it cannot burn the
// shared budget against an entity no one wants anymore. Mirrors the frontend's
// shouldRetryRejection+tabExists gate. Without this, a flapping worker that
// keeps rejecting a tab the user closed mid-flap would exhaust the 8-attempt
// budget for the whole subscription.
func TestSubscription_LookupFailedAckForDroppedEntityDoesNotRetry(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	sub.retry = fastRetry()
	t.Cleanup(sub.Cancel)

	// Open with {a-1}, then revise to {a-2} — dropping a-1 from the interest.
	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))
	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-2"}}}))

	// A LOOKUP_FAILED for the dropped a-1 must not arm a retry.
	pushLookupFailed(tr, "a-1")
	time.Sleep(50 * time.Millisecond)
	sub.mu.Lock()
	attempts := sub.retry.Attempts()
	inFlight := sub.retryInFlight
	sub.mu.Unlock()
	assert.Equal(t, 0, attempts, "a LOOKUP_FAILED for a dropped entity must not consume a retry slot")
	assert.False(t, inFlight, "a LOOKUP_FAILED for a dropped entity must not arm a retry")

	// A LOOKUP_FAILED for the still-wanted a-2 DOES arm a retry.
	pushLookupFailed(tr, "a-2")
	require.Eventually(t, func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return sub.retry.Attempts() >= 1
	}, time.Second, 5*time.Millisecond, "a LOOKUP_FAILED for a wanted entity must arm a retry")
}

// TestSubscription_LookupFailedRetryCoalescesInFlight pins the in-flight retry
// guard (REMOVALS-2): a burst of LOOKUP_FAILED acks while a retry is already
// armed coalesces into one retry per slot, matching the frontend's
// createExponentialBackoff.schedule. Without this, a burst would consume one
// budget slot per ack and exhaust the 8-attempt budget N×faster than the
// frontend under a flapping worker.
func TestSubscription_LookupFailedRetryCoalescesInFlight(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	sub.retry = fastRetry() // 3 attempts, 10-30ms, jitterless
	t.Cleanup(sub.Cancel)

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))

	// Push a burst of LOOKUP_FAILED acks while NO retry has fired yet (delays
	// are 10-30ms; these all land within the first retry's window). Each ack
	// after the first must coalesce into the single armed retry.
	for i := 0; i < 5; i++ {
		pushLookupFailed(tr, "a-1")
	}

	// Within the first retry window (before the 10ms delay elapses), exactly one
	// retry must be armed. With consume-on-fire, no slot is committed yet (the
	// retry hasn't fired its Update); the discriminator is that ONE retry is
	// in-flight and the others were coalesced away.
	time.Sleep(5 * time.Millisecond)
	sub.mu.Lock()
	inFlight := sub.retryInFlight
	sub.mu.Unlock()
	assert.True(t, inFlight, "a burst of acks must coalesce into one in-flight retry")

	// After the retry fires, exactly one slot must be committed (not 5).
	require.Eventually(t, func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return sub.retry.Attempts() == 1
	}, time.Second, 5*time.Millisecond,
		"a burst of acks must coalesce into one committed retry (one slot consumed, not 5)")
}

// TestSubscription_BudgetExhaustionLogsOnce pins the once-guard (EFFICIENCY-2):
// after the budget is spent, the exhaustion Warn fires once per budget cycle,
// not once per subsequent LOOKUP_FAILED ack. Without this, a perpetually-
// flapping worker would flood the operator's log with a Warn per ack.
func TestSubscription_BudgetExhaustionLogsOnce(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	sub.retry = fastRetry() // 3 attempts, jitterless
	t.Cleanup(sub.Cancel)

	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}}}))

	// Exhaust the budget directly under s.mu.
	sub.mu.Lock()
	for !sub.retry.Done() {
		sub.retry.Peek()
		sub.retry.Commit()
	}
	sub.mu.Unlock()
	require.True(t, sub.retry.Done(), "budget pre-exhausted")

	// Reset the once-guard so we observe the first-exhaustion transition; the
	// production code sets it on the transition, so simulate a fresh cycle.
	sub.mu.Lock()
	sub.retryExhaustedLogged = false
	sub.mu.Unlock()

	// Capture slog output during the ack dispatches.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	// Drive several LOOKUP_FAILED acks post-exhaustion. Only the FIRST must log.
	for i := 0; i < 4; i++ {
		pushLookupFailed(tr, "a-1")
	}
	time.Sleep(50 * time.Millisecond)

	count := strings.Count(buf.String(), "LOOKUP_FAILED retry budget exhausted")
	assert.Equal(t, 1, count,
		"the budget-exhausted Warn must fire once per cycle, not once per ack")
}

// TestSubscription_RapidSequentialUpdates pins back-to-back Update behaviour:
// after the first open, subsequent Updates revise the same stream.
func TestSubscription_RapidSequentialUpdates(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	t.Cleanup(sub.Cancel)

	const updates = 8
	for i := 0; i < updates; i++ {
		require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{
			Agents: []*leapmuxv1.WatchAgentEntry{AgentWatchEntry("a-1", int64(i))},
		}))
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&tr.callsOpened))
	last := tr.lastUpdate()
	require.NotNil(t, last)
	assert.Equal(t, leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, last.GetAgents()[0].GetReplay())
	assert.Equal(t, int64(updates-1), last.GetAgents()[0].GetCursorSeq())
	assert.Equal(t, leapmuxv1.WatchMode_WATCH_MODE_FULL, last.GetAgents()[0].GetMode())
}

func TestSubscription_ConcurrentUpdatesAreLifecycleSingleFlight(t *testing.T) {
	const updates = 8
	openStarted := make(chan struct{}, updates)
	releaseOpen := make(chan struct{})
	tr := &fakeTransport{openStarted: openStarted, releaseOpen: releaseOpen}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	t.Cleanup(sub.Cancel)

	start := make(chan struct{})
	errs := make(chan error, updates)
	var wg sync.WaitGroup
	wg.Add(updates)
	for i := 0; i < updates; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			errs <- sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{
				Agents: []*leapmuxv1.WatchAgentEntry{AgentWatchEntry("a-1", int64(i+1))},
			})
		}()
	}
	close(start)

	select {
	case <-openStarted:
	case <-time.After(time.Second):
		t.Fatal("expected first OpenWatchEvents call to start")
	}

	sawConcurrentOpen := false
	select {
	case <-openStarted:
		sawConcurrentOpen = true
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseOpen)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	assert.False(t, sawConcurrentOpen, "Update must not open a second stream while another Update is still opening")
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&tr.active) == 1
	}, time.Second, 10*time.Millisecond, "only one stream should remain active")
}

// TestSubscription_AgentMessageNilDoesNotAdvanceCursor: an
// AgentEvent with the AgentMessage oneof unset (e.g. a non-message
// agent event the worker added later) must NOT advance the agent
// cursor. The cursor tracks message seq specifically; bumping it
// from a non-message event would skip-ahead past real messages.
func TestSubscription_AgentMessageNilDoesNotAdvanceCursor(t *testing.T) {
	tr := &fakeTransport{}
	agents := NewAgentCursor()
	agents.Update("a-1", 5) // baseline so we can detect any change
	sub := NewSubscription(tr, agents, NewTerminalCursor(), nil, nil, nil)
	t.Cleanup(sub.Cancel)
	require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{}))

	// AgentEvent with no oneof set: GetAgentMessage() == nil.
	require.NotPanics(t, func() {
		tr.pushFrame(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: &leapmuxv1.AgentEvent{
				AgentId: "a-1",
			}},
		})
	})
	// Cursor stays at 5; the empty event must not have touched it.
	assert.Equal(t, int64(5), agents.Get("a-1"))
}
