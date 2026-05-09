package streamevents

import (
	"context"
	"fmt"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Transport abstracts "stream a WatchEvents subscription against
// some backend." Two production wirings exist:
//
//   - hub-bound: open an E2EE channel via `*remote.Client.OpenE2EEChannel`,
//     then `SendRPCNoWait("WatchEvents", payload)` + `RegisterStream`.
//   - local-IPC: call `RemoteIPCService.StreamInner` with method
//     `worker.WatchEvents` over the per-agent socket.
//
// Both flows decode the same `WatchEventsResponse` payload off the
// wire. By writing the cursor + reconnect logic against this
// interface, callers (`agent messages --follow` and `events --include
// agent,terminal`) share one implementation regardless of mode.
type Transport interface {
	// OpenWatchEvents starts a WatchEvents subscription with the
	// given request. onFrame is called once per delivered
	// WatchEventsResponse. The returned cancel function stops the
	// subscription and returns when its goroutines have drained.
	// done is closed when the subscription terminates (either via
	// cancel or because the underlying transport ended).
	OpenWatchEvents(ctx context.Context, req *leapmuxv1.WatchEventsRequest,
		onFrame func(*leapmuxv1.WatchEventsResponse)) (cancel context.CancelFunc, done <-chan struct{}, err error)
}

// Subscription owns the cursor map plus one in-flight Transport
// subscription. Callers can `Update(req)` to swap the entry list
// (cancels + re-opens with cursors preserved); `Cancel()` to stop;
// `Done()` to wait for the goroutine to drain.
//
// The cursor state lives outside the Transport so a re-subscribe (or
// a fresh Subscription from a previous one) can resume cleanly. The
// Subscription itself is single-flight — Update serializes with
// in-flight callbacks via its own mutex; concurrent Updates from
// different goroutines are safe.
type Subscription struct {
	transport Transport
	agents    *AgentCursor
	terminals *TerminalCursor

	// onAgent / onTerminal are user callbacks invoked for every
	// AgentEvent / TerminalEvent the Transport delivers. They run on
	// the Transport's frame goroutine; long-running work should
	// dispatch to a separate goroutine to avoid back-pressuring the
	// stream.
	onAgent    func(*leapmuxv1.AgentEvent)
	onTerminal func(*leapmuxv1.TerminalEvent)

	// onCursorReset is fired when a TerminalEvent's TerminalData
	// frame carries `is_snapshot=true`. Consumers use it to surface a
	// notice to the user (see streamevents.cursor_reset.go) and, if
	// they want a fresh state, to call cursor.Reset before continuing.
	// Nil = ignore.
	onCursorReset func(terminalID string)

	mu     sync.Mutex
	cancel context.CancelFunc
	done   <-chan struct{}
}

// NewSubscription wires the transport and cursors. Callbacks fire
// before the cursor map updates, so consumers see the raw event
// before the cursor reflects it — useful for "store the message,
// then advance".
func NewSubscription(t Transport, agents *AgentCursor, terminals *TerminalCursor,
	onAgent func(*leapmuxv1.AgentEvent),
	onTerminal func(*leapmuxv1.TerminalEvent),
	onCursorReset func(terminalID string),
) *Subscription {
	return &Subscription{
		transport:     t,
		agents:        agents,
		terminals:     terminals,
		onAgent:       onAgent,
		onTerminal:    onTerminal,
		onCursorReset: onCursorReset,
	}
}

// Update opens (or re-opens, if there's a live subscription) a
// WatchEvents stream with `req` as the entry list. Existing cursors
// are NOT inspected here — callers should build req from
// `cursor.Snapshot(restrict)` so cursors are preserved.
//
// Update blocks until the previous subscription's goroutine has
// drained. This is safe to call from a snapshot-reconciliation
// goroutine that may be re-issuing every few hundred milliseconds.
func (s *Subscription) Update(ctx context.Context, req *leapmuxv1.WatchEventsRequest) error {
	s.mu.Lock()
	prevDone := s.done
	prevCancel := s.cancel
	s.mu.Unlock()
	if prevCancel != nil {
		prevCancel()
		<-prevDone
	}

	cancel, done, err := s.transport.OpenWatchEvents(ctx, req, s.dispatch)
	if err != nil {
		return fmt.Errorf("open watch events: %w", err)
	}
	s.mu.Lock()
	s.cancel = cancel
	s.done = done
	s.mu.Unlock()
	return nil
}

// Cancel stops the in-flight subscription, if any. Safe to call from
// any goroutine; idempotent.
func (s *Subscription) Cancel() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// Done returns a channel that's closed when the in-flight
// subscription finishes (either via Cancel or because the Transport
// ended). Returns a closed channel when no subscription is live —
// callers should always check Update success first.
func (s *Subscription) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return s.done
}

// dispatch is the Transport-facing frame callback. It runs cursor
// updates and forwards the typed event to the per-source callback.
// Errors in user callbacks are swallowed (the stream must keep
// flowing); panics are caught at the caller level.
func (s *Subscription) dispatch(resp *leapmuxv1.WatchEventsResponse) {
	if resp == nil {
		return
	}
	switch ev := resp.GetEvent().(type) {
	case *leapmuxv1.WatchEventsResponse_AgentEvent:
		ae := ev.AgentEvent
		if ae == nil {
			return
		}
		// Update the cursor BEFORE the callback so a callback that
		// crashes doesn't leave the cursor stale on retry.
		if msg := ae.GetAgentMessage(); msg != nil {
			s.agents.Update(ae.GetAgentId(), msg.GetSeq())
		}
		if s.onAgent != nil {
			s.onAgent(ae)
		}
	case *leapmuxv1.WatchEventsResponse_TerminalEvent:
		te := ev.TerminalEvent
		if te == nil {
			return
		}
		if data := te.GetData(); data != nil {
			if data.GetIsSnapshot() && s.onCursorReset != nil {
				s.onCursorReset(te.GetTerminalId())
			}
			s.terminals.Update(te.GetTerminalId(), data.GetEndOffset())
		}
		if s.onTerminal != nil {
			s.onTerminal(te)
		}
	}
}
