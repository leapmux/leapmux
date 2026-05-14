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

// fakeTransport is an in-memory Transport implementation. Tests
// drive `pushFrame` to deliver synthetic WatchEventsResponse frames
// to the subscription's callback, and assert on the cursor map / on
// emitted side effects.
type fakeTransport struct {
	mu      sync.Mutex
	calls   []*leapmuxv1.WatchEventsRequest
	onFrame func(*leapmuxv1.WatchEventsResponse)
	cancel  context.CancelFunc
	done    chan struct{}
	openErr error
	// callsOpened increments per OpenWatchEvents call so tests can
	// assert resubscribe count.
	callsOpened int32
}

func (t *fakeTransport) OpenWatchEvents(parentCtx context.Context, req *leapmuxv1.WatchEventsRequest,
	onFrame func(*leapmuxv1.WatchEventsResponse),
) (context.CancelFunc, <-chan struct{}, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.openErr != nil {
		err := t.openErr
		t.openErr = nil // clear so subsequent calls succeed (reconnect path)
		return nil, nil, err
	}
	atomic.AddInt32(&t.callsOpened, 1)
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

func (t *fakeTransport) pushFrame(resp *leapmuxv1.WatchEventsResponse) {
	t.mu.Lock()
	cb := t.onFrame
	t.mu.Unlock()
	if cb != nil {
		cb(resp)
	}
}

func (t *fakeTransport) lastRequest() *leapmuxv1.WatchEventsRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.calls) == 0 {
		return nil
	}
	return t.calls[len(t.calls)-1]
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

// TestSubscription_UpdateCancelsPreviousAndCarriesCursor verifies
// the cursor preservation contract on resubscribe: after the first
// subscription advances its cursor, the next Update call's request
// must reflect the new cursor for surviving tabs. This is the core
// of why `agent messages --follow` and `events --include
// agent,terminal` can re-issue without losing events.
func TestSubscription_UpdateCancelsPreviousAndCarriesCursor(t *testing.T) {
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

	// Re-subscribe via the cursor's Snapshot — the resubscribe
	// payload must carry seq=9 so the worker doesn't replay events
	// we already saw.
	require.NoError(t, sub.Update(context.Background(),
		&leapmuxv1.WatchEventsRequest{Agents: agents.Snapshot(map[string]struct{}{"a-1": {}})}))
	last := tr.lastRequest()
	require.NotNil(t, last)
	require.Len(t, last.GetAgents(), 1)
	assert.Equal(t, "a-1", last.GetAgents()[0].GetAgentId())
	assert.Equal(t, int64(9), last.GetAgents()[0].GetAfterSeq())
	assert.Equal(t, int32(2), atomic.LoadInt32(&tr.callsOpened),
		"Update should cancel and re-open exactly once")
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

// TestSubscription_RapidSequentialUpdates pins the back-to-back
// Update behaviour the fan-out reconciler relies on: a snapshot
// burst can issue Update twice in a row before the previous's
// goroutine has finished cleaning up. Each Update must cancel its
// predecessor and open a fresh subscription, leaving the latest one
// live. The Subscription contract is single-flight via its mutex —
// concurrent goroutine callers are out of scope.
func TestSubscription_RapidSequentialUpdates(t *testing.T) {
	tr := &fakeTransport{}
	sub := NewSubscription(tr, NewAgentCursor(), NewTerminalCursor(), nil, nil, nil)
	t.Cleanup(sub.Cancel)

	const updates = 8
	for i := 0; i < updates; i++ {
		require.NoError(t, sub.Update(context.Background(), &leapmuxv1.WatchEventsRequest{
			Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "a-1", AfterSeq: int64(i)}},
		}))
	}
	// Each Update calls OpenWatchEvents exactly once — the Subscription
	// cancels the prior in-flight before opening the next.
	assert.Equal(t, int32(updates), atomic.LoadInt32(&tr.callsOpened))
	// The latest cursor (`updates-1`) is what the most recent
	// subscribe asked for.
	last := tr.lastRequest()
	require.NotNil(t, last)
	assert.Equal(t, int64(updates-1), last.GetAgents()[0].GetAfterSeq())
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
