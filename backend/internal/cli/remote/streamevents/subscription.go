package streamevents

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Subscription owns the cursor map plus one in-flight Transport
// subscription. Callers can `Update(req)` to revise the entry list on
// the same stream; `Cancel()` to stop; `Done()` to wait for the
// goroutine to drain.
//
// The cursor state lives outside the Transport so a re-subscribe (or
// a fresh Subscription from a previous one) can resume cleanly. Lifecycle
// operations are single-flight: Update and Cancel serialize the whole
// open/update/store sequence, not just field reads/writes, so concurrent
// callers cannot orphan a newly-opened stream.
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

	lifecycleMu sync.Mutex
	mu          sync.Mutex
	handle      Handle
	// lastReq is the most recent interest statement; LOOKUP_FAILED acks
	// re-issue it after a short delay so CLI follow matches the UI retry.
	lastReq *leapmuxv1.WatchEventsRequest
	// retryCtx / retryCancel gate LOOKUP_FAILED retries. Cancel retires
	// retryCancel so an in-flight retry bails before re-opening a stream the
	// caller tore down — without it the retry (which uses context.Background)
	// could orphan a worker-side subscription for the process lifetime, and a
	// fresh open would re-arm the gate a prior Cancel had set. Re-opened on a
	// clean fresh open so a transient miss after a reconnect gets a fresh
	// budget. Guarded by lifecycleMu.
	retryCtx    context.Context
	retryCancel context.CancelFunc
	// lookupAttempts bounds LOOKUP_FAILED retries per stream open. A new open
	// (handle == nil → OpenWatchEvents) resets it so a transient miss after a
	// clean reconnect gets a fresh budget.
	lookupAttempts int
}

// LOOKUP_FAILED retry bounds. The first retry lands quickly (a transient
// List*ByIDs miss often recovers in milliseconds); later ones back off. The
// cap stops a flapping worker from spawning an unbounded retry ladder (each
// retry produces another LOOKUP_FAILED ack that would spawn another goroutine).
//
// This is a one-off fixed-delay table; the frontend's equivalent
// (useWatchEventsStreams → createExponentialBackoff) is a jittered exponential.
// Sharing one backoff primitive across both is tracked in
// https://github.com/leapmux/leapmux/issues/349.
var lookupRetryDelays = [...]time.Duration{
	200 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
	5 * time.Second,
}

const lookupRetryMax = len(lookupRetryDelays)

// NewSubscription wires the transport and cursors. Callbacks fire
// before the cursor map updates, so consumers see the raw event
// before the cursor reflects it — useful for "store the message,
// then advance".
func NewSubscription(t Transport, agents *AgentCursor, terminals *TerminalCursor,
	onAgent func(*leapmuxv1.AgentEvent),
	onTerminal func(*leapmuxv1.TerminalEvent),
	onCursorReset func(terminalID string),
) *Subscription {
	retryCtx, retryCancel := context.WithCancel(context.Background())
	return &Subscription{
		transport:     t,
		agents:        agents,
		terminals:     terminals,
		onAgent:       onAgent,
		onTerminal:    onTerminal,
		onCursorReset: onCursorReset,
		retryCtx:      retryCtx,
		retryCancel:   retryCancel,
	}
}

// Update opens (if needed) or revises the WatchEvents stream with `req`
// as the entry list. Existing cursors are NOT inspected here — callers
// should build req from `cursor.Snapshot(restrict)` so cursors are preserved.
//
// A Handle whose Done channel has already closed is treated as dead: it is
// dropped and a fresh stream is opened. Without that, reconnect loops that
// wait on Done then call Update would revise a retired correlation id forever.
func (s *Subscription) Update(ctx context.Context, req *leapmuxv1.WatchEventsRequest) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()

	if handle != nil {
		select {
		case <-handle.Done():
			s.mu.Lock()
			if s.handle == handle {
				s.handle = nil
			}
			s.mu.Unlock()
			handle = nil
		default:
		}
	}

	if handle == nil {
		h, err := s.transport.OpenWatchEvents(ctx, req, s.dispatch)
		if err != nil {
			return fmt.Errorf("open watch events: %w", err)
		}
		// A fresh open is a clean slate for the LOOKUP_FAILED retry budget and
		// re-arms the retry gate a prior Cancel retired. The gate is recreated
		// (not un-cancelled) so a retry that lost the race with a Cancel-during-
		// open cannot revive it via this path; only an explicit fresh open from
		// the caller earns a new budget.
		retryCtx, retryCancel := context.WithCancel(context.Background())
		s.mu.Lock()
		prevRetryCancel := s.retryCancel
		s.retryCtx = retryCtx
		s.retryCancel = retryCancel
		s.handle = h
		s.lastReq = req
		s.lookupAttempts = 0
		s.mu.Unlock()
		// Retire the previous gate after publishing the new one so a retry
		// snapshotting under s.mu never observes a cancelled-but-not-replaced
		// ctx; the new ctx is the live one.
		prevRetryCancel()
		return nil
	}
	if err := handle.Update(req); err != nil {
		return fmt.Errorf("update watch events: %w", err)
	}
	s.mu.Lock()
	s.lastReq = req
	s.mu.Unlock()
	return nil
}

// Cancel stops the in-flight subscription, if any. Safe to call from
// any goroutine; idempotent.
func (s *Subscription) Cancel() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	// Retire the retry gate so in-flight LOOKUP_FAILED retry goroutines bail
	// (their timer selects on retryCtx) before they re-open a stream on
	// context.Background — without this, a retry that wakes after Cancel
	// orphans a worker-side subscription for the process lifetime.
	s.mu.Lock()
	retryCancel := s.retryCancel
	handle := s.handle
	s.handle = nil
	s.mu.Unlock()
	retryCancel()
	if handle != nil {
		handle.Cancel()
		<-handle.Done()
	}
}

// Done returns a channel that's closed when the in-flight
// subscription finishes (either via Cancel or because the Transport
// ended). Returns a closed channel when no subscription is live —
// callers should always check Update success first.
func (s *Subscription) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return s.handle.Done()
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
	case *leapmuxv1.WatchEventsResponse_UpdateAck:
		s.dispatchUpdateAck(ev.UpdateAck)
	case *leapmuxv1.WatchEventsResponse_AgentEvent:
		s.dispatchAgentEvent(ev.AgentEvent)
	case *leapmuxv1.WatchEventsResponse_TerminalEvent:
		s.dispatchTerminalEvent(ev.TerminalEvent)
	}
}

func (s *Subscription) dispatchUpdateAck(ack *leapmuxv1.WatchUpdateAck) {
	if ack == nil {
		return
	}
	retry := false
	for _, r := range ack.GetRejectedAgents() {
		slog.Debug("streamevents: agent watch rejected",
			"entity_id", r.GetEntityId(), "reason", r.GetReason().String())
		if r.GetReason() == leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED {
			retry = true
		}
	}
	for _, r := range ack.GetRejectedTerminals() {
		slog.Debug("streamevents: terminal watch rejected",
			"entity_id", r.GetEntityId(), "reason", r.GetReason().String())
		if r.GetReason() == leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED {
			retry = true
		}
	}
	if !retry {
		return
	}
	s.mu.Lock()
	req := s.lastReq
	attempt := s.lookupAttempts
	retryCtx := s.retryCtx
	if attempt >= lookupRetryMax {
		s.mu.Unlock()
		slog.Warn("streamevents: LOOKUP_FAILED retry budget exhausted",
			"attempts", attempt)
		return
	}
	s.lookupAttempts++
	s.mu.Unlock()
	if req == nil {
		return
	}
	// Best-effort: restate the last interest after a short delay so a
	// transient List*ByIDs miss recovers without an external reconnect. The
	// retryCtx gate stops a retry that lost the race with Cancel from re-opening
	// a stream the caller tore down; the bounded attempts stop a flapping worker
	// from compounding into an unbounded retry ladder.
	delay := lookupRetryDelays[attempt]
	go func(r *leapmuxv1.WatchEventsRequest, delay time.Duration) {
		t := time.NewTimer(delay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-retryCtx.Done():
			return
		}
		if retryCtx.Err() != nil {
			return
		}
		_ = s.Update(retryCtx, r)
	}(req, delay)
}

func (s *Subscription) dispatchAgentEvent(ae *leapmuxv1.AgentEvent) {
	if ae == nil {
		return
	}
	if ae.GetTurnEnd() != nil {
		slog.Debug("streamevents: ignoring agent turn_end notify frame",
			"agent_id", ae.GetAgentId())
		return
	}
	if msg := ae.GetAgentMessage(); msg != nil {
		if seq := msg.GetSeq(); seq >= 0 {
			if !s.agents.Advance(ae.GetAgentId(), seq) {
				return
			}
		}
	}
	if s.onAgent != nil {
		s.onAgent(ae)
	}
}

func (s *Subscription) dispatchTerminalEvent(te *leapmuxv1.TerminalEvent) {
	if te == nil {
		return
	}
	switch te.GetEvent().(type) {
	case *leapmuxv1.TerminalEvent_Bell,
		*leapmuxv1.TerminalEvent_Notification,
		*leapmuxv1.TerminalEvent_TitleChanged,
		*leapmuxv1.TerminalEvent_Progress:
		slog.Debug("streamevents: ignoring terminal notify frame",
			"terminal_id", te.GetTerminalId())
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
