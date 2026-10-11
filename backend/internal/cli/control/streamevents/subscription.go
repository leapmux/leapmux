package streamevents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/backoffutil"
	"google.golang.org/protobuf/proto"
)

// ErrSubscriptionClosed is returned by Update after Cancel retired the
// subscription. It stops a late LOOKUP_FAILED retry (whose generation check
// beat Cancel, but whose Update then loses the race) from re-opening a stream
// the caller tore down. Callers that drive Update in a loop should treat it as
// an expected, non-fatal outcome of their own Cancel — distinct from a real
// subscribe failure — not surface it as an error to the user.
var ErrSubscriptionClosed = errors.New("streamevents: subscription is closed")

// errSubscriptionClosed aliases ErrSubscriptionClosed for in-package use so the
// retry path's errors.Is reads as a local sentinel.
var errSubscriptionClosed = ErrSubscriptionClosed

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
//
// Cancel is final: once it returns, Update refuses with
// ErrSubscriptionClosed for the life of the Subscription (the closed flag is
// never cleared). Callers that need to re-subscribe after Cancel must build a
// new Subscription. The CLI's agent-messages --follow reconnect loop honors
// this by reusing one Subscription across transport reconnects (which never
// Cancel) and tearing it down only at process exit.
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

	// onCursorReset runs when a TerminalData frame carries
	// `is_snapshot=true`. A consumer can call TerminalCursor.Reset before
	// it reads more data. Nil ignores the frame.
	onCursorReset func(terminalID string)

	lifecycleMu sync.Mutex
	// closed is set by Cancel under lifecycleMu and read by Update under the
	// same lock. It is the closed-defense that stops a LOOKUP_FAILED retry
	// whose timer fired from re-opening a stream AFTER Cancel returned: the
	// retry's Update blocks on lifecycleMu until Cancel finishes, then sees
	// closed=true and bails instead of orphaning a worker-side stream.
	closed bool
	mu     sync.Mutex
	handle Handle
	// lastReq is the most recent interest statement; LOOKUP_FAILED acks
	// re-issue it after a short delay so CLI follow matches the UI retry.
	lastReq *leapmuxv1.WatchEventsRequest
	// inflightUpdateId identifies the exact current interest request.
	// Only a matching nonzero ACK can change its replay or retry state.
	// mu protects this field.
	inflightUpdateId uint64
	// nextUpdateId is the next update_id to assign. Monotonic for the life of
	// the subscription (never reset on fresh-open) so a stale ack from a prior
	// stream generation cannot masquerade as current. Guarded by s.mu.
	nextUpdateId uint64
	// opening identifies transport setup before the handle becomes available.
	opening bool
	// openingAck retains an exact ACK that arrives before the opening handle returns.
	openingAck *leapmuxv1.WatchUpdateAck
	// retryInFlight is true while a LOOKUP_FAILED retry goroutine is armed and
	// has not yet fired or bailed. It coalesces a burst of acks into one
	// in-flight retry per slot, mirroring the frontend's
	// createExponentialBackoff.schedule (which no-ops while a timer is pending
	// for the key). Guarded by s.mu.
	retryInFlight bool
	// retryCtx is cancelled by Cancel to wake an armed retry goroutine
	// immediately (it selects on retryCtx.Done() alongside its timer). Unlike
	// the generation counter (which is also bumped by fresh-open), retryCtx is
	// cancelled ONLY by Cancel — a fresh-open does NOT retire it — so a retry
	// whose stream recovered via a fresh-open can still restate against the new
	// stream. Cancel retires both: it bumps generation (so the goroutine refunds
	// and bails) AND cancels retryCtx (so the goroutine does not linger for up
	// to lookupRetryMaxInterval after shutdown). Both fields are set once in
	// NewSubscription and never reassigned, so they are safe to read from any
	// goroutine; retryCancel is called only by Cancel, under lifecycleMu.
	retryCtx    context.Context
	retryCancel context.CancelFunc
	// retryExhaustedLogged is the once-guard for the budget-exhausted Warn: it
	// fires once per budget cycle (cleared on Reset) so a perpetually-flapping
	// worker does not log a Warn per ack after exhaustion. Guarded by s.mu.
	retryExhaustedLogged bool
	// generation is bumped on every fresh-open and on Cancel. A LOOKUP_FAILED
	// retry captures the generation at arm time and re-checks it after its
	// timer fires: if the generation moved (Cancel or a fresh-open won the
	// race), the retry refunds its slot and bails instead of re-opening against
	// a stream that moved on. This replaces the prior retryCtx gate + the
	// context.WithoutCancel workaround: there is no gate for a fresh-open to
	// retire, so a retry's own Update can no longer be born-dead, and the only
	// re-open defense needed is the `closed` flag in Update. Guarded by s.mu.
	generation uint64
	// retry is the LOOKUP_FAILED retry budget (attempt cap + backoff interval).
	// All access is serialized under s.mu, satisfying backoffutil.Retry's
	// single-owner contract. Reset on each fresh open so a transient miss after
	// a clean reconnect gets a fresh budget.
	retry *backoffutil.Retry
	// clock supplies the retry timer. NewSubscription sets it before a retry
	// goroutine can start, so the goroutine can read it without the mutex.
	clock quartz.Clock
}

const subscriptionRetryTimerTag = "subscription-retry"

// SubscriptionOption changes one NewSubscription setting.
type SubscriptionOption func(*Subscription)

// WithClock sets the clock that supplies retry timers.
func WithClock(clock quartz.Clock) SubscriptionOption {
	if clock == nil {
		panic("streamevents: clock must not be nil")
	}
	return func(s *Subscription) {
		s.clock = clock
	}
}

// LOOKUP_FAILED retry policy. The eventsRejection policy in
// contracts/retry.json, generated for this side and the frontend's
// useWatchEventsStreams rejectionBackoff alike, so the CLI and the UI use
// the same backoff shape under a flapping worker: jittered exponential,
// 500ms → 15s, ±20% jitter, 8 attempts. The capped attempt count stops a
// flapping worker from compounding into an uncapped retry ladder (each
// retry produces another LOOKUP_FAILED ack that would spawn another
// goroutine), and the in-flight retry guard coalesces a burst of acks into
// one retry per slot (matching the frontend's createExponentialBackoff.schedule).
//
// One deliberate divergence: the frontend resets the budget per clean ack
// (event-gated per worker); the CLI resets only on a fresh stream open, so a
// long-lived subscription that revises its entry list without a reconnect does
// not re-arm the budget. A fresh open is the CLI's natural recovery boundary,
// and resetting per-ack would let one entity's success mask another's failure.
const (
	lookupRetryInitial     = contracts.EventsRejectionRetryInitial
	lookupRetryMaxInterval = contracts.EventsRejectionRetryMaxInterval
	lookupRetryMultiplier  = contracts.EventsRejectionRetryMultiplier
	lookupRetryJitter      = contracts.EventsRejectionRetryJitter
	lookupRetryMaxAttempts = contracts.EventsRejectionRetryMaxAttempts
)

// NewSubscription wires the transport and cursors. Callbacks fire
// before the cursor map updates, so consumers see the raw event
// before the cursor reflects it — useful for "store the message,
// then advance".
func NewSubscription(t Transport, agents *AgentCursor, terminals *TerminalCursor,
	onAgent func(*leapmuxv1.AgentEvent),
	onTerminal func(*leapmuxv1.TerminalEvent),
	onCursorReset func(terminalID string),
	opts ...SubscriptionOption,
) *Subscription {
	retryCtx, retryCancel := context.WithCancel(context.Background())
	// lookupRetry* are compile-time constants, so NewRetry cannot fail; a panic
	// here turns a bad future edit to the constants into a startup failure
	// instead of a zero/negative-delay burst at runtime. Every test that calls
	// NewSubscription exercises this path, so the panic also fires under go test.
	retry, err := backoffutil.NewRetry(lookupRetryInitial, lookupRetryMaxInterval, lookupRetryMultiplier, lookupRetryJitter, lookupRetryMaxAttempts)
	if err != nil {
		retryCancel()
		panic(fmt.Sprintf("streamevents: invalid LOOKUP_FAILED retry policy: %v", err))
	}
	sub := &Subscription{
		transport:     t,
		agents:        agents,
		terminals:     terminals,
		onAgent:       onAgent,
		onTerminal:    onTerminal,
		onCursorReset: onCursorReset,
		retryCtx:      retryCtx,
		retryCancel:   retryCancel,
		retry:         retry,
		clock:         quartz.NewReal(),
	}
	for _, opt := range opts {
		opt(sub)
	}
	return sub
}

// assignUpdateId stamps req with the next monotonic update_id and records it
// as inflight. Must run under s.mu (every caller already holds it). The id is
// monotonic for the subscription's life so a stale ack from a prior stream
// generation cannot be mistaken for current interest.
func (s *Subscription) assignUpdateId(req *leapmuxv1.WatchEventsRequest) error {
	if s.nextUpdateId == math.MaxUint64 {
		return errors.New("streamevents: watch update identities are exhausted")
	}
	s.nextUpdateId++
	id := s.nextUpdateId
	s.inflightUpdateId = id
	req.UpdateId = id
	return nil
}

// prepareReplayEntries preserves the fixed tuple of each unchanged FULL lifetime.
func prepareReplayEntries(req, previous *leapmuxv1.WatchEventsRequest, fresh bool) {
	prior := make(map[string]*leapmuxv1.WatchAgentEntry)
	if !fresh {
		for _, entry := range previous.GetAgents() {
			if entry.GetMode() == leapmuxv1.WatchMode_WATCH_MODE_FULL && entry.GetReplayId() > 0 {
				prior[entry.GetAgentId()] = entry
			}
		}
	}
	for index, entry := range req.GetAgents() {
		if entry.GetMode() != leapmuxv1.WatchMode_WATCH_MODE_FULL {
			entry.ReplayId = 0
			continue
		}
		if retained, exists := prior[entry.GetAgentId()]; exists {
			req.Agents[index] = proto.Clone(retained).(*leapmuxv1.WatchAgentEntry)
		} else {
			entry.ReplayId = req.GetUpdateId()
		}
	}
}

// applyFreshOpen installs the handle and starts the successful opening's retry epoch.
// It processes a retained exact ACK after it resets that epoch and releases s.mu.
// A transport reconnect that bypasses Update establishes no new retry epoch.
func (s *Subscription) applyFreshOpen(h Handle, req *leapmuxv1.WatchEventsRequest) {
	s.mu.Lock()
	s.generation++
	s.handle = h
	s.lastReq = proto.Clone(req).(*leapmuxv1.WatchEventsRequest)
	s.retry.Reset()
	s.retryInFlight = false
	s.retryExhaustedLogged = false
	s.opening = false
	ack := s.openingAck
	s.openingAck = nil
	s.mu.Unlock()
	if ack != nil {
		s.dispatchUpdateAck(ack)
	}
}

// applyRevision records requested interest before transport can deliver its ACK.
// The caller holds s.mu and restores the previous interest after a failed transmission.
// A revision leaves the retry budget and generation unchanged.
func (s *Subscription) applyRevision(req *leapmuxv1.WatchEventsRequest) {
	s.lastReq = proto.Clone(req).(*leapmuxv1.WatchEventsRequest)
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

	// Cancel closed the subscription; refuse to re-open. This is the closed-
	// defense against the late-cancel race: a LOOKUP_FAILED retry whose timer
	// fired still reaches Update, but lifecycleMu serializes the retry's Update
	// against Cancel, so by the time this read runs Cancel has either not
	// started (closed=false, the open is legit) or fully returned (closed=true,
	// the open is an orphan).
	if s.closed {
		return errSubscriptionClosed
	}
	if req == nil {
		return errors.New("streamevents: watch interest is absent")
	}
	for _, entry := range req.GetAgents() {
		if entry == nil {
			return errors.New("streamevents: watch agent entry is absent")
		}
	}
	req = proto.Clone(req).(*leapmuxv1.WatchEventsRequest)

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

	s.mu.Lock()
	previousRequest, previousInflight := s.lastReq, s.inflightUpdateId
	if err := s.assignUpdateId(req); err != nil {
		s.mu.Unlock()
		return err
	}
	prepareReplayEntries(req, previousRequest, handle == nil)
	s.applyRevision(req)
	if handle == nil {
		s.opening = true
		s.openingAck = nil
	}
	s.mu.Unlock()
	if handle == nil {
		h, err := s.transport.OpenWatchEvents(ctx, req, s.dispatch)
		if err != nil {
			s.mu.Lock()
			s.lastReq, s.inflightUpdateId = previousRequest, previousInflight
			s.opening = false
			s.openingAck = nil
			s.mu.Unlock()
			return fmt.Errorf("open watch events: %w", err)
		}
		s.applyFreshOpen(h, req)
		return nil
	}
	if err := handle.Update(req); err != nil {
		// Roll back the inflight id: assignUpdateId bumped it before the send,
		// but this revision never reached the wire, so leaving it advanced would
		// make a subsequent LOOKUP_FAILED ack for the still-current prior revision
		// test as stale (ackId < inflightUpdateId) and drop it. Restore the id of
		// the interest that is actually live.
		s.mu.Lock()
		s.inflightUpdateId = previousInflight
		s.lastReq = previousRequest
		s.mu.Unlock()
		return fmt.Errorf("update watch events: %w", err)
	}
	return nil
}

// Cancel stops the in-flight subscription, if any. Safe to call from
// any goroutine; idempotent. After Cancel returns, Update always refuses
// with errSubscriptionClosed for the life of the Subscription.
func (s *Subscription) Cancel() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true

	// Bump the generation so in-flight LOOKUP_FAILED retry goroutines see the
	// subscription has moved on when their timer fires, and refund their slot
	// instead of re-opening a stream on a torn-down subscription. Cancel the
	// retryCtx so an armed retry wakes immediately instead of lingering for up
	// to lookupRetryMaxInterval after shutdown (a fresh-open does NOT cancel
	// retryCtx — only Cancel does).
	s.mu.Lock()
	s.generation++
	handle := s.handle
	s.handle = nil
	s.mu.Unlock()
	s.retryCancel()
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

// dispatch updates cursors and sends typed events to the caller's handlers.
// The event handlers return no error. Their panics propagate to the transport caller.
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

// logRejections logs each rejection at Debug and returns the entity_ids of the
// ones carrying LOOKUP_FAILED — the one reason that arms a retry. The agent and
// terminal rejection lists share this shape; the kind labels the log line. The
// caller then gates the retry on whether any rejected entity is still in the
// current interest (mirroring the frontend's shouldRetryRejection+tabExists),
// so a LOOKUP_FAILED for an entity the CLI already stopped caring about does
// not burn the shared budget.
func logRejections(kind string, rejections []*leapmuxv1.WatchRejection) []string {
	var lookupFailed []string
	for _, r := range rejections {
		slog.Debug("streamevents: "+kind+" watch rejected",
			"entity_id", r.GetEntityId(), "reason", r.GetReason().String())
		if r.GetReason() == leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED {
			lookupFailed = append(lookupFailed, r.GetEntityId())
		}
	}
	return lookupFailed
}

// wantAgent reports whether agentID is in req's current agent interest. Mirrors
// the frontend's tabExists predicate, gated on the live interest so a
// LOOKUP_FAILED for an entity the caller already dropped does not arm a retry
// against it.
func wantAgent(req *leapmuxv1.WatchEventsRequest, agentID string) bool {
	for _, a := range req.GetAgents() {
		if a.GetAgentId() == agentID {
			return true
		}
	}
	return false
}

// wantTerminal reports whether terminalID is in req's current terminal interest.
func wantTerminal(req *leapmuxv1.WatchEventsRequest, terminalID string) bool {
	for _, te := range req.GetTerminals() {
		if te.GetTerminalId() == terminalID {
			return true
		}
	}
	return false
}

// anyRetryableInterest reports whether any LOOKUP_FAILED-rejected entity (across
// the agent and terminal rejection lists) is still in the current interest req,
// mirroring the frontend's anyRetryable(agents, terminals) gated on tabExists.
// A LOOKUP_FAILED for an entity the caller no longer wants is not retried, so it
// cannot burn the shared budget against an entity that was intentionally
// dropped mid-flap.
func anyRetryableInterest(req *leapmuxv1.WatchEventsRequest,
	rejectedAgents, rejectedTerminals []*leapmuxv1.WatchRejection) bool {
	for _, r := range rejectedAgents {
		if r.GetReason() == leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED &&
			wantAgent(req, r.GetEntityId()) {
			return true
		}
	}
	for _, r := range rejectedTerminals {
		if r.GetReason() == leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED &&
			wantTerminal(req, r.GetEntityId()) {
			return true
		}
	}
	return false
}

func (s *Subscription) dispatchUpdateAck(ack *leapmuxv1.WatchUpdateAck) {
	if ack == nil {
		return
	}
	s.mu.Lock()
	if ack.GetUpdateId() == 0 || s.inflightUpdateId == 0 || ack.GetUpdateId() != s.inflightUpdateId {
		s.mu.Unlock()
		return
	}
	if s.opening {
		s.openingAck = proto.Clone(ack).(*leapmuxv1.WatchUpdateAck)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	logRejections("agent", ack.GetRejectedAgents())
	logRejections("terminal", ack.GetRejectedTerminals())
	// Pending actions deferred until after s.mu is released, so the lock is held
	// for the shortest scope and neither the slog.Warn nor the retry goroutine
	// (whose Update may block in OpenWatchEvents) runs under it.
	var (
		armed    bool
		armGen   uint64
		delay    time.Duration
		ok       bool
		logOnce  bool
		attempts int
	)
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		if armed {
			go s.armLookupRetry(s.retryCtx, delay, armGen)
		}
		if logOnce {
			slog.Warn("streamevents: LOOKUP_FAILED retry budget exhausted",
				"attempts", attempts)
		}
	}()
	// Repeat the exact current-ID check after the external log handlers return.
	// A newer revision can replace the interest while those handlers run.
	ackId := ack.GetUpdateId()
	if ackId == 0 || s.inflightUpdateId == 0 || ackId != s.inflightUpdateId {
		return
	}
	if s.lastReq == nil {
		// No interest to restate. Check before consuming an attempt so a spurious
		// LOOKUP_FAILED ack (e.g. one racing a fresh open before lastReq is set)
		// does not silently erode the budget.
		return
	}
	registered := make(map[string]*leapmuxv1.WatchAgentState, len(ack.GetAgentStates()))
	for _, state := range ack.GetAgentStates() {
		registered[state.GetAgentId()] = state
	}
	current := proto.Clone(s.lastReq).(*leapmuxv1.WatchEventsRequest)
	for _, entry := range current.GetAgents() {
		if entry.GetMode() != leapmuxv1.WatchMode_WATCH_MODE_FULL {
			continue
		}
		state := registered[entry.GetAgentId()]
		if state.GetMode() != leapmuxv1.WatchMode_WATCH_MODE_FULL || state.GetReplayId() != entry.GetReplayId() {
			entry.ReplayId = 0
		}
	}
	s.lastReq = current
	// Per-entity liveness: only arm a retry if at least one LOOKUP_FAILED-
	// rejected entity is still in the current interest. Mirrors the frontend's
	// anyRetryable+tabExists: a LOOKUP_FAILED for an entity the caller already
	// dropped (a tab removed mid-flap) is not retried, so it cannot burn the
	// shared budget against an entity no one wants anymore. Read lastReq here
	// under s.mu so the interest set is consistent with the ack's update_id.
	if !anyRetryableInterest(s.lastReq, ack.GetRejectedAgents(), ack.GetRejectedTerminals()) {
		return
	}
	if s.retryInFlight {
		// A retry is already armed for this slot; coalesce. Mirrors the
		// frontend's createExponentialBackoff.schedule, which no-ops while a timer
		// is pending for the key. Without this, a burst of acks would
		// consume one budget slot each and spawn one goroutine each, exhausting
		// the 8-attempt budget N×faster than the frontend under a flapping
		// worker. The in-flight retry re-reads lastReq when it fires, so no
		// interest is lost.
		return
	}
	// Peek the next delay under s.mu so two acks racing on the frame goroutine
	// cannot both arm against the same slot, and mark in-flight so a burst
	// coalesces into one retry. Peek does NOT consume the slot — the goroutine
	// Commits only when the retry actually fires its Update, and Rolls back if it
	// bails (Cancel or generation moved). So a Cancel that wins the race cannot
	// erode the budget or ratchet the interval: the consume-on-fire design makes
	// the refund-and-erode class of bug mechanically impossible.
	delay, ok = s.retry.Peek()
	if !ok {
		// Budget spent. Log once per budget cycle (cleared on Reset): a
		// perpetually-flapping worker emits a LOOKUP_FAILED ack per retry cycle,
		// and logging a Warn on each post-exhaustion ack floods the operator's
		// log with no new information.
		if !s.retryExhaustedLogged {
			s.retryExhaustedLogged = true
			logOnce = true
			attempts = s.retry.Attempts()
		}
		return
	}
	s.retryInFlight = true
	armGen = s.generation
	armed = true
}

// armLookupRetry waits out delay on s.clock and then restates the latest
// interest after a transient List*ByIDs miss. It selects on retryCtx so Cancel
// wakes it immediately instead of lingering for up to lookupRetryMaxInterval
// after shutdown (a fresh-open does NOT cancel retryCtx — only Cancel does — so a
// retry whose stream recovered via a fresh-open can still restate against the
// new stream). After the wait it re-checks generation under s.mu: if the stream
// moved on (Cancel or fresh-open won the race) it Rolls back the peeked slot
// (no budget consumed, no interval ratchet — the consume-on-fire design) and
// bails without restating. It re-reads lastReq at fire time (not the arm-time
// snapshot) so a revise issued during the backoff window is honored, matching
// the frontend's fire-time plan read. The re-open runs on context.Background
// (no gate for a fresh-open to retire), so it cannot be born-dead; the `closed`
// flag in Update is the only re-open defense needed. It Commits the slot only
// once the Update is dispatched, so a fire that then loses the lifecycleMu race
// to Cancel still counts as a real attempt.
func (s *Subscription) armLookupRetry(retryCtx context.Context, delay time.Duration, armGen uint64) {
	timer := s.clock.NewTimer(delay, subscriptionRetryTimerTag)
	defer timer.Stop(subscriptionRetryTimerTag)
	select {
	case <-timer.C:
	case <-retryCtx.Done():
		// Cancel retired the subscription while the timer was pending. Roll back
		// the peeked slot (no budget consumed) and bail. retryInFlight stays set:
		// a Cancelled subscription never arms another retry, so the stale flag
		// cannot suppress a future retry.
		s.mu.Lock()
		s.retry.Rollback()
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	// The generation moved (Cancel or a fresh-open won the race) while the
	// timer was pending. Roll back the peeked slot so the budget and interval
	// are untouched (the attempt never fired), clear in-flight, and bail
	// without re-stating against a stream that moved on. A fresh-open that
	// already Reset the budget makes the rollback a harmless no-op on the
	// interval; Reset is the stronger operation.
	if s.generation != armGen {
		s.mu.Unlock()
		return
	}
	req := s.lastReq
	s.retryInFlight = false
	// Commit the slot now: the retry is about to fire its Update irreversibly,
	// so this attempt counts. Committing under s.mu keeps the budget consistent
	// with the in-flight flag we just cleared.
	s.retry.Commit()
	s.mu.Unlock()
	if req == nil {
		return
	}
	if err := s.Update(context.Background(), req); err != nil {
		// errSubscriptionClosed is expected when Cancel won the race after
		// the generation check passed; the re-open was correctly refused.
		// Surface every other failure: this is the recovery path the budget
		// drives, and a silent drop would mask the transport error that
		// doomed the retry.
		if !errors.Is(err, errSubscriptionClosed) {
			slog.Warn("streamevents: LOOKUP_FAILED retry Update failed",
				"err", err)
		}
	}
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
