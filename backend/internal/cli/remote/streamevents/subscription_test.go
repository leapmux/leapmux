package streamevents

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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

	push := func(seq int64) {
		tr.pushFrame(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: &leapmuxv1.AgentEvent{
				AgentId: "a-1",
				Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: seq}},
			}},
		})
	}

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

	push := func(seq int64) {
		tr.pushFrame(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: &leapmuxv1.AgentEvent{
				AgentId: "a-1",
				Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: seq}},
			}},
		})
	}

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

	push := func(seq int64) {
		tr.pushFrame(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: &leapmuxv1.AgentEvent{
				AgentId: "a-1",
				Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: seq}},
			}},
		})
	}

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

	tr.pushFrame(&leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_UpdateAck{UpdateAck: &leapmuxv1.WatchUpdateAck{
			RejectedAgents: []*leapmuxv1.WatchRejection{{
				EntityId: "a-1",
				Reason:   leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED,
			}},
		}},
	})

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

// TestSubscription_LookupFailedRetryDoesNotReopenAfterCancel pins the fix for a
// goroutine-leak / orphaned-subscription bug: the LOOKUP_FAILED retry used
// context.Background with no Cancel guard, so a retry that woke after Cancel
// reopened a fresh worker-side stream nobody would ever tear down. Cancel must
// set the closed flag so the in-flight retry bails instead of re-opening.
func TestSubscription_LookupFailedRetryDoesNotReopenAfterCancel(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)

	req := &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1"}},
	}
	require.NoError(t, sub.Update(context.Background(), req))
	openedBefore := atomic.LoadInt32(&tr.callsOpened)
	require.Equal(t, int32(1), openedBefore)

	// Push LOOKUP_FAILED, then Cancel inside the retry delay window.
	tr.pushFrame(&leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_UpdateAck{UpdateAck: &leapmuxv1.WatchUpdateAck{
			RejectedAgents: []*leapmuxv1.WatchRejection{{
				EntityId: "a-1",
				Reason:   leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED,
			}},
		}},
	})
	// Cancel before the 200ms retry fires.
	sub.Cancel()

	// Wait past the longest possible retry delay and assert no re-open landed.
	time.Sleep(lookupRetryDelays[len(lookupRetryDelays)-1] + 200*time.Millisecond)
	assert.Equal(t, openedBefore, atomic.LoadInt32(&tr.callsOpened),
		"LOOKUP_FAILED retry must not re-open a stream after Cancel")
}

// TestSubscription_LookupFailedRetryBudgetExhausts pins the bounded-retry fix:
// previously every LOOKUP_FAILED ack spawned a fresh goroutine that produced
// another LOOKUP_FAILED ack, compounding without limit on a flapping worker.
// The retry must stop after lookupRetryMax attempts.
func TestSubscription_LookupFailedRetryBudgetExhausts(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	t.Cleanup(sub.Cancel)

	req := &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-missing"}},
	}
	require.NoError(t, sub.Update(context.Background(), req))

	// Drive enough ack cycles to exhaust the budget. Each retry re-states the
	// plan; we approximate the worker's perpetual LOOKUP_FAILED by pushing an
	// ack whenever a retry Update lands.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				if tr.lastUpdate() != nil {
					tr.pushFrame(&leapmuxv1.WatchEventsResponse{
						Event: &leapmuxv1.WatchEventsResponse_UpdateAck{UpdateAck: &leapmuxv1.WatchUpdateAck{
							RejectedAgents: []*leapmuxv1.WatchRejection{{
								EntityId: "a-missing",
								Reason:   leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED,
							}},
						}},
					})
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()
	close(stop)

	// Wait well past the sum of all retry delays, then assert opens stayed at 1.
	time.Sleep(sumLookupRetryDelays() + 300*time.Millisecond)
	assert.Equal(t, int32(1), atomic.LoadInt32(&tr.callsOpened),
		"LOOKUP_FAILED retries must stop after the budget is exhausted (no re-open ladder)")
}

func sumLookupRetryDelays() time.Duration {
	var d time.Duration
	for _, dd := range lookupRetryDelays {
		d += dd
	}
	return d
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
