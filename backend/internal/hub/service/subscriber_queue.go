package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
)

// parkedFrameCap bounds the pre-bootstrap park buffer.
//
// Under a reconnect storm (a hub restart, say) a slow multi-page resume scan
// racing fast incoming broadcasts could otherwise grow it without bound. It
// answers "how far behind, in frames the client still has to apply" -- the byte
// budget below is what bounds the MEMORY that backlog occupies, and took that
// job over from this constant. Still not a tuning knob: an overflow is a
// correctness event the fallback ladder handles, not a condition to be sized
// away.
//
// TWO phases park, not one. The resume scan does (bounded at MaxResumeDeltaOps
// source frames), and so does the FALLBACK baseline walk, which registers the
// subscriber before building and is bounded only by the account's entity count.
// The baseline walk is the shorter of the two in practice -- an in-memory walk
// of a captured generation, against a multi-page journal read -- but it is the
// one whose duration grows with the account.
const parkedFrameCap = 256

// liveQueueDepth is the steady-state bounded channel depth, i.e. how far a
// subscriber may fall behind the manager goroutine before it is dropped.
//
// The two frame counts here answer a different question from the byte budget
// below them, and both bounds are load-bearing. A count says how far BEHIND a
// subscriber is allowed to fall, in frames the client still has to apply; bytes
// say how much of the Hub's memory that backlog may occupy. Neither implies the
// other -- 320 tiny presence frames are a trivial amount of memory and still a
// subscriber that is not keeping up, while a single large batch is the reverse.
//
// Which one binds first is a property of the traffic, not a tie to break. The
// pool guarantees every member a working set of at least sendq.DefaultMinFloor
// (1 MiB) that it cannot be refused, so on ordinary metadata frames the counts
// are what a subscriber reaches first and this file's behaviour is unchanged
// from before it had a budget at all. The byte bound is what catches the case
// the counts never could: frames large enough that 320 of them are gigabytes.
const liveQueueDepth = 64

// dropPhase is WHEN a frame was refused, and dropBound is WHICH bound refused
// it. Together they partition every drop this queue makes.
//
// Enums with a Label(), not strings, for the reason ws_userevents.go states
// about label vocabularies: a Prometheus label value needs exactly one producer
// that a dashboard-breaking rename has to go through. As untyped strings they
// were also control-flow discriminators -- answerRefusal switches on the phase
// and parkRefusalMessage on the bound -- so the same two adjacent string
// parameters could be passed in either order and nothing would object.
type dropPhase int

const (
	// dropPhaseBootstrap: refused while admitting the opening frame, before the
	// connection has streamed anything.
	dropPhaseBootstrap dropPhase = iota
	// dropPhaseParking: refused while buffering behind an opening frame that is
	// still being built or written.
	dropPhaseParking
	// dropPhaseLive: refused in steady state.
	dropPhaseLive
)

// Label is the Prometheus label value, and the ONLY producer of it.
func (p dropPhase) Label() string {
	switch p {
	case dropPhaseBootstrap:
		return "bootstrap"
	case dropPhaseParking:
		return "park"
	case dropPhaseLive:
		return "live"
	}
	return "unknown"
}

type dropBound int

const (
	// dropBoundFrames: the queue is as far behind as it may get, counted in
	// frames the client still has to apply.
	dropBoundFrames dropBound = iota
	// dropBoundBytes: the shared byte budget had no room right now. Clears on
	// its own as the pool drains.
	dropBoundBytes
	// dropBoundCapacity: the frame is larger than the WHOLE budget, so every
	// occupancy refuses it -- an empty pool included. Separate from
	// dropBoundBytes because only one of the two clears on its own: bytes shows
	// up as leapmux_sendq_pool_used_bytes near capacity and drains, while this
	// correlates with nothing and is cleared only by raising
	// userevents_queue_memory_budget. Read as one value, a frame that can never
	// fit looks like transient pressure, and an operator watching occupancy for
	// a spike that never comes concludes the metric is lying.
	dropBoundCapacity
)

// Label is the Prometheus label value, and the ONLY producer of it.
func (b dropBound) Label() string {
	switch b {
	case dropBoundFrames:
		return "frames"
	case dropBoundBytes:
		return "bytes"
	case dropBoundCapacity:
		return "capacity"
	}
	return "unknown"
}

// subscriberQueue is one subscriber's outbound path, in both of its phases.
//
// A user-events connection delivers in a strict order: the bootstrap frame,
// then whatever arrived while the subscribe + resume scan was still running,
// then steady-state live traffic. So Send has two behaviours -- PARK before the
// bootstrap frame is on the wire, STREAM after -- and the switch between them
// has to be seen consistently by the manager goroutine calling Send, by the
// writer loop calling Bootstrapped, and by the manager calling Rebaseline under
// its projection lock.
//
// That used to be four interdependent locals (a cap, a mutex, a slice and a
// bool) plus the channel, declared in a ~250-line HTTP handler closure and
// mutated from three places in it. Nothing but reading `bootstrapped` correctly
// at each of those three sites enforced the phase rule, and the whole park /
// flush / overflow policy could only be exercised through httptest.NewServer
// and a real websocket.Dial -- which is why it had no direct test at all.
//
// As a type the phase rule is a property of the value, and the policy is
// unit-testable without a server.
//
// # Memory
//
// The queue charges its frames against a shared sendq.Pool, so the Hub's
// user-events backlog is bounded in bytes across every connection rather than
// only in frames per connection. Two properties make that safe to state:
//
//   - Frames are SHARED. crdt memoizes one *MarshaledEvent per transition and
//     hands the same pointer to every subscriber of the user, so the queue
//     joins as an AttachShared member: its holding counts every frame it is
//     behind on (which is what decides who gets refused), while the pool's
//     total counts each distinct buffer once (which is what an operator sizes).
//     MarshaledEvent.Retain/Release report the transitions between them.
//   - The refund is not spread across call sites. Every frame leaves through
//     exactly one of dropLocked (dequeued by Next, discarded by Rebaseline or
//     Close) or forgo (refused by Send), because a missed refund is a permanent
//     leak of shared budget that would slowly strangle every other connection.
type subscriberQueue struct {
	mu sync.Mutex
	// parked holds frames received before Bootstrapped; drained by Next
	// afterwards, ahead of anything in live.
	parked []*crdt.MarshaledEvent
	// phase is where this queue is in its one-way lifecycle. A single value
	// rather than a released bool and a closed bool, because the two were never
	// independent: nothing un-closes, nothing un-releases, and no reachable
	// combination of them meant anything a phase cannot say. Two booleans meant
	// every guard had to name the right one, and the diff that introduced this
	// type added them one at a time as the gaps were noticed.
	//
	// Advanced only by Bootstrapped and Close, both under mu -- Close's write in
	// particular shares the mutex the live send takes, which is what makes its
	// drain complete: no frame can enter either buffer after it.
	//
	// The bootstrap slot deliberately stays a separate field. It is not a phase:
	// it holds the frame releaseBootstrapLocked must refund, and it returns to
	// nil at BootstrapSent, BEFORE this reaches queueClosed.
	phase queuePhase
	// live is the steady-state bounded queue Next drains.
	live chan *crdt.MarshaledEvent
	// bootstrap is the frame written AHEAD of the queue -- the snapshot or
	// resume delta that opens the connection. It never enters parked or live,
	// but it is the LARGEST thing this connection ever holds, so it is charged
	// like everything else while it is on the wire. Held here rather than only
	// in the handler so Close is still the one teardown that cannot miss a
	// frame.
	bootstrap *crdt.MarshaledEvent
	// member is this queue's ONE handle on the shared byte budget, and a shared
	// one because the frames are: it admits through Admit, which reports
	// residency and holding separately, and its Detach refunds nothing. The pool
	// itself is deliberately not kept alongside it -- every question this queue
	// asks the budget, including whether a frame could ever fit, is answered by
	// the member's own admission verdict, so a second handle would only be a
	// second way to ask.
	member *sendq.SharedMember
	// overflowed records that a frame was dropped during the parking phase, so
	// the resume path can FALLBACK to a snapshot instead of shipping a delta
	// with a hole. Guarded by mu like phase: every write is already inside
	// Send's or Rebaseline's critical section, so an atomic on top was a second
	// synchronisation mechanism for one field and left a reader working out
	// which of the two was the real ordering guarantee.
	overflowed bool
}

// newSubscriberQueue builds a queue drawing on pool.
//
// evict is invoked if the pool ever reclaims this member, and must tear the
// connection down. It reports whether THIS call was the one that started the
// teardown, which is what the pool counts as an eviction; a member already on
// its way out must answer false so the metric an operator sizes from is not
// inflated by nominations that reclaimed nothing.
//
// The teardown is asynchronous here -- cancelling the context exits the writer
// loop, whose deferred Close does the refund -- which the pool tolerates because
// it marks a nominated victim ineligible before calling evict. Nothing in the
// user-events pool reclaims today anyway: a subscriber that cannot fit a frame
// refuses itself, which is cheaper than taking a peer down (see Send).
func newSubscriberQueue(pool *sendq.Pool, evict func(error) bool) *subscriberQueue {
	return &subscriberQueue{
		live:   make(chan *crdt.MarshaledEvent, liveQueueDepth),
		member: pool.AttachShared(evict),
	}
}

// queuePhase is a subscriber queue's one-way lifecycle. Transitions only ever
// go forward, and only Bootstrapped and Close make them.
type queuePhase int

const (
	// queueParking: the opening frame is still being built, so Send buffers into
	// the parked slice rather than the bounded live channel -- a multi-page
	// resume scan must not trip ErrSubscriberSlow and drop the reconnect.
	queueParking queuePhase = iota
	// queueLive: the opening frame has been handed over, so Send streams.
	queueLive
	// queueClosed: torn down. Nothing enters either buffer again.
	queueClosed
)

// errQueueClosed is what a frame offered to a torn-down connection gets. It is
// deliberately NOT crdt.ErrSubscriberSlow: that verdict routes into the
// overflow policy, which would log "subscriber buffer full, dropping
// connection" and cancel a context, about a connection that is already gone and
// was never slow. Every crdt-side caller discards a non-slow error, which is
// the right handling for a frame with nobody left to deliver it to.
var errQueueClosed = errors.New("service: user-events queue closed")

// queueRefusal is one bound turning one frame away, carrying the two facts the
// caller needs in order to answer it: the PHASE the queue was in, and the BOUND
// that refused.
//
// Both are the exact values the drop was counted under, constructed in the same
// statement (see refuseFrame), because the caller used to re-derive them: it asked
// the queue for its phase one statement later and matched a sentinel for the
// bound. Neither answer was reliable. The phase can advance in between -- Send
// runs on the crdt broadcast goroutine while the handler flips the phase from
// its own -- so a parking-phase refusal could be routed down the steady-state
// arm and logged as a slow client. And the live arm had no sentinel at all: it
// returned a bare crdt.ErrSubscriberSlow, so live-frames and live-bytes, whose
// fixes are opposite (a slow client versus an undersized deployment), arrived
// at the log indistinguishable.
//
// It wraps crdt.ErrSubscriberSlow so that any errors.Is caller keeps working.
// Nothing in crdt matches on it today -- the manager routes on
// Subscriber.Overflowed() and its fan-out discards the send error -- so the wrap
// is defensive, not load-bearing. This handler is the one place that reads the
// verdict, and it does so with errors.As.
type queueRefusal struct {
	// phase is WHEN the frame was refused.
	phase dropPhase
	// bound is WHICH bound refused it.
	bound dropBound
}

func (r queueRefusal) Error() string {
	return fmt.Sprintf("%s (phase=%s, bound=%s)", crdt.ErrSubscriberSlow, r.phase.Label(), r.bound.Label())
}

// Unwrap makes this a crdt.ErrSubscriberSlow to every errors.Is caller.
func (r queueRefusal) Unwrap() error { return crdt.ErrSubscriberSlow }

// refuseFrame counts the drop and produces the verdict, from ONE pair of values.
// Splitting them let the metric and the caller's answer be labelled from
// different derivations of the same event.
func refuseFrame(phase dropPhase, bound dropBound) queueRefusal {
	metrics.UserEventsFramesDroppedTotal.WithLabelValues(phase.Label(), bound.Label()).Inc()
	return queueRefusal{phase: phase, bound: bound}
}

// refuseLiveFrame is the steady-state phase's refusal: the subscriber is slow,
// unless the connection is already going away, in which case saying so is more
// useful than blaming the client.
//
// The drop is counted either way. The frame really was dropped, whatever the
// connection does next, and a metric that went quiet as soon as connections
// started being cancelled would go quiet exactly when it is read.
func refuseLiveFrame(ctx context.Context, bound dropBound) error {
	refusal := refuseFrame(dropPhaseLive, bound)
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return refusal
	}
}

// dropBoundFor names the bound behind a refused admission, so the parking arm,
// the live arm and the bootstrap arm classify one outcome identically instead
// of each deciding for itself.
//
// sendq.Admitted never reaches here -- every caller tests for it first -- and
// Pressure is the only remaining verdict, so anything that is not Unfittable is
// the pool being full at this moment rather than for good.
func dropBoundFor(outcome sendq.AdmitOutcome) dropBound {
	if outcome == sendq.Unfittable {
		return dropBoundCapacity
	}
	return dropBoundBytes
}

// Send accepts one frame, parking or streaming according to phase.
//
// Returns a queueRefusal -- which IS a crdt.ErrSubscriberSlow to errors.Is --
// when the subscriber cannot keep up in either phase: a full park buffer, a
// full live queue, a backlog that has outgrown what the shared budget will
// grant it, or a frame larger than that budget entirely. The refusal names the
// phase and the bound, so the caller answers the one that actually fired rather
// than asking this queue again once its phase has moved on. The caller tears
// the connection down on the steady-state ones; blocking here would stall the
// manager goroutine for every other subscriber of this user.
//
// Refusing rather than reclaiming is deliberate. The pool can nominate its
// largest holder and evict it, but there is nothing to win here: under the
// pool's rule a subscriber only reaches pressure once it is already above its
// guaranteed working set, so the asker IS a backed-up connection -- and its own
// shed is the cheapest outcome in the Hub. Before the bootstrap frame it is not
// even a disconnect, just a snapshot instead of a delta; after it, the client
// reconnects and delta-resumes from its cursor.
func (q *subscriberQueue) Send(ctx context.Context, evt *crdt.MarshaledEvent) error {
	// Outside the lock: this is what forces the frame's lazy marshal, and the
	// writer loop should not wait behind it. It has no effect on the frame's
	// holder count, so a queue that goes on to refuse still owes nothing -- and
	// the marshal is memoized on the frame, so a fan-out to N subscribers pays
	// for it once however many of them refuse.
	size := evt.Size()

	q.mu.Lock()
	// Tagged on the phase, which is what the single lifecycle field buys: the
	// three arms are exhaustive over it rather than three unrelated predicates
	// that happened to cover the cases. Written WITHOUT a default so they stay
	// that way -- .golangci.yml prefers this shape precisely so a fourth phase
	// turns every such switch red instead of silently taking the steady-state
	// arm and streaming onto q.live.
	switch q.phase {
	case queueClosed:
		// The connection is gone and nothing will drain this queue again, so
		// taking the frame would hold budget on behalf of nobody.
		q.mu.Unlock()
		return errQueueClosed

	case queueParking:
		if len(q.parked) >= parkedFrameCap {
			// Set INSIDE q.mu, paired with Rebaseline's clear, so the two stores
			// are ordered by the same mutex that orders the frames they describe.
			// Both callers happen to hold m.projection today, which already
			// serializes them; relying on that would make this correct only for
			// as long as no Send path escapes the projection hold.
			q.overflowed = true
			q.mu.Unlock()
			return refuseFrame(dropPhaseParking, dropBoundFrames)
		}
		if outcome := q.admitLocked(evt, size); outcome != sendq.Admitted {
			q.overflowed = true // see the note above
			q.mu.Unlock()
			return refuseFrame(dropPhaseParking, dropBoundFor(outcome))
		}
		q.parked = append(q.parked, evt)
		q.mu.Unlock()
		return nil

	case queueLive:
		if outcome := q.admitLocked(evt, size); outcome != sendq.Admitted {
			q.mu.Unlock()
			return refuseLiveFrame(ctx, dropBoundFor(outcome))
		}
		// Two selects, not one three-way: a single select with a ready send AND a
		// ready ctx.Done() picks between them at RANDOM, so a cancelled context
		// could preempt a send the queue had room for. Take the room first, and
		// consult ctx only when there is none.
		select {
		case q.live <- evt:
			q.mu.Unlock()
			return nil
		default:
		}
		// The byte budget had room but the channel did not. dropLocked is the
		// one refund path for a frame this queue charged for, which is what the
		// type's doc promises: Release(size, 0) followed by forgo was the same
		// two atomics in the same order, spelled a second way.
		q.dropLocked(evt)
		q.mu.Unlock()
		return refuseLiveFrame(ctx, dropBoundFrames)
	}

	// Unreachable while queuePhase has exactly the three members above, and the
	// switch is written without a default so adding a fourth stops compiling
	// here rather than silently streaming onto q.live.
	//
	// Deliberately not a panic: Send unlocks by hand rather than by defer, so
	// one here would strand q.mu and deadlock every later Send, Next and Close
	// on this queue. Deliberately not a refusal either -- refuseFrame counts a
	// drop under a (phase, bound) pair, and a phase nobody has defined has no
	// honest pair to count it under. Failing the send tears the connection down,
	// which is the right answer to a queue in a state its own type cannot name.
	q.mu.Unlock()
	return fmt.Errorf("subscriber queue in unknown phase %d", q.phase)
}

// admitLocked takes a share in the frame and asks the budget for room for it.
// On anything but sendq.Admitted the share has already been given back, so the
// caller only has to report the refusal.
//
// The outcome is passed through rather than collapsed to a bool because the two
// refusals are different conditions with different answers -- Unfittable can
// never succeed at any occupancy, Pressure may on the next attempt -- and all
// three callers have to draw that line the same way. Collapsing it here is what
// left the bootstrap arm asking the pool a second question to recover it, and
// the two Send arms reporting an unfittable frame as ordinary memory pressure.
//
// Retaining and asking are ONE operation on purpose. Retaining makes the frame
// resident and hands this queue the duty to give those bytes back; Admit is
// what records that duty against the pool. Splitting them let the frame-count
// refusal above retain a frame the pool never heard about, and the refund on
// the way out then drove the pool's total negative -- a total that reads as
// "empty" and quietly grants every connection an unlimited threshold. The
// bounds that refuse WITHOUT taking a share -- a closed queue, a full park
// buffer -- must therefore return before reaching here, never after.
func (q *subscriberQueue) admitLocked(evt *crdt.MarshaledEvent, size int64) sendq.AdmitOutcome {
	outcome := q.member.Admit(size, evt.Retain())
	if outcome != sendq.Admitted {
		q.forgo(evt)
	}
	return outcome
}

// forgo lets go of a share this queue took in a frame it did not store.
// Nothing leaves the holding -- the frame never entered it -- but letting go
// may still end the frame's residency, and then this queue is the one that has
// to give those bytes back.
//
// It refunds what Release reports rather than what Retain did, because the two
// need not match: another subscriber may have brought the frame into the pool
// and drained it while this call was deciding, leaving this caller holding the
// last reference to bytes it never charged.
func (q *subscriberQueue) forgo(evt *crdt.MarshaledEvent) {
	if freed := evt.Release(); freed != 0 {
		q.member.Release(0, freed)
	}
}

// dropLocked removes a frame this queue HELD, returning both its own holding
// and whatever residency letting go of the frame frees.
//
// The size is MarshaledEvent's memoized one, so the number refunded here is the
// number charged at admission by construction -- it cannot drift the way a
// recomputed size could.
func (q *subscriberQueue) dropLocked(evt *crdt.MarshaledEvent) {
	q.member.Release(evt.Size(), evt.Release())
}

// discardParkedLocked drops every parked frame, refunding each.
func (q *subscriberQueue) discardParkedLocked() {
	for i, evt := range q.parked {
		q.dropLocked(evt)
		q.parked[i] = nil
	}
	q.parked = nil
}

// bootstrapAdmission is what AdmitBootstrap found. A typed outcome rather than a
// bool because the causes call for different answers to the client: one is
// worth retrying, one never will be, and one means there is no client left to
// tell. A single false made all three arrive at the caller as "the pool is
// full", which put a permanent failure on the operator's memory-pressure
// dashboard and told the client to retry something that could not work.
type bootstrapAdmission int

const (
	// bootstrapAdmitted: charged, and the queue now holds the frame.
	bootstrapAdmitted bootstrapAdmission = iota
	// bootstrapPoolFull: it would fit an emptier pool. Retrying may succeed.
	bootstrapPoolFull
	// bootstrapUnfittable: larger than the pool's whole capacity, so no
	// occupancy will ever admit it. The deployment is undersized for this
	// account, and only an operator can fix it.
	bootstrapUnfittable
	// bootstrapNotAdmissible: the queue is closed, or already holds an opening
	// frame. A programming or teardown case, not a memory verdict.
	bootstrapNotAdmissible
)

// AdmitBootstrap charges the frame that opens the connection, for as long as it
// takes to write it, and reports whether the budget could take it.
//
// The bootstrap frame is the biggest single thing a user-events connection ever
// holds: an account's whole filtered state, or a resume delta up to
// channelwire.UserEventsReadLimit. It is per-subscriber-unique, so unlike a
// broadcast frame there is no sharing to soften it -- N tabs reconnecting at
// once really is N snapshots resident at once. Leaving it uncharged would have
// left the connect path with the same defect the backlog path just lost: a
// per-connection cost with nothing bounding the connection count.
//
// It is charged on top of whatever parked during the subscribe, because that is
// all one connection's memory, and a connection already holding a large backlog
// asking for a large snapshot is exactly the case that should be told to wait.
//
// A connection has exactly one opening frame, so a second call is refused
// rather than allowed to replace the first: overwriting the slot would strand
// the frame it displaced, whose bytes nothing would ever refund.
//
// What this does NOT cover is the window in which the frame is being BUILT,
// inside SubscribeWithACL, before there is a size to charge -- and that window
// is NOT serialized. Both arms build unlocked: the snapshot arm materializes
// after resolveAndRegisterForFallback has released subscribeExpandMu and the
// projection lock, and buildResumeDelta says outright that it holds neither
// (only the last-rung lockedFallbackOutcome takes projection across its walk).
// So N tabs of one user reconnecting together really do build N payloads at
// once, and a refusal here cannot prevent the allocation -- the bytes already
// exist by the time there is a size to ask about, and each StatusTryAgainLater
// sends the client back to build them again.
//
// Charging at this point is still worth it, because the WRITE is the long part
// (bounded only by relayWriteTimeout, against a build that is CPU-bound and
// prompt) and it is the part that overlaps across tabs for minutes. Bounding
// the build window means bounding concurrent BUILDS -- a gate at the connect
// path, not a charge here -- which is a different mechanism than this one.
func (q *subscriberQueue) AdmitBootstrap(evt *crdt.MarshaledEvent) bootstrapAdmission {
	size := evt.Size()

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.phase == queueClosed || q.bootstrap != nil {
		return bootstrapNotAdmissible
	}
	// One question, not two. Admission already computes the three-way verdict
	// internally, so asking the pool separately whether the frame could EVER fit
	// -- which this used to do -- was a second query against a total that moves
	// under it, and it was available only here: the two Send arms had no such
	// arm, and reported a permanently oversized frame as ordinary pressure.
	switch outcome := q.admitLocked(evt, size); outcome {
	case sendq.Admitted:
		q.bootstrap = evt
		return bootstrapAdmitted
	case sendq.Unfittable:
		// Bigger than the entire pool, so no occupancy admits it and no retry
		// can succeed. Reported separately because the caller's answer differs
		// in kind: "wait and try again" is advice a client can act on, and
		// handing it out here produced a client that rebuilt the same oversized
		// snapshot every few seconds forever while the user watched an app that
		// never finished loading.
		//
		// Counted here rather than by the handler, through the same refuseFrame
		// the parking and live arms use. The verdict this returns and the label
		// the metric carries are then two readings of ONE derivation: the
		// handler used to re-derive the label from the outcome a second time,
		// which is exactly the split refuseFrame exists to prevent. The refusal
		// value itself is discarded -- this path answers in bootstrapAdmission,
		// which carries a close code the queue has no business choosing.
		_ = refuseFrame(dropPhaseBootstrap, dropBoundFor(outcome))
		return bootstrapUnfittable
	default:
		_ = refuseFrame(dropPhaseBootstrap, dropBoundFor(outcome))
		return bootstrapPoolFull
	}
}

// BootstrapSent gives the bootstrap frame's bytes back once its write has
// returned. Called on both outcomes: a write that failed still stopped holding
// the frame, and refunding only on success would leak the budget of every
// connection whose client hung up mid-bootstrap.
func (q *subscriberQueue) BootstrapSent() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.releaseBootstrapLocked()
}

func (q *subscriberQueue) releaseBootstrapLocked() {
	if q.bootstrap == nil {
		return
	}
	evt := q.bootstrap
	q.bootstrap = nil
	q.dropLocked(evt)
}

// Rebaseline discards everything parked so far, and forgets any overflow those
// frames caused.
//
// A resume or a fallback that gives up re-registers and takes a baseline at a
// LATER point than anything parked before it, so those frames are superseded.
// Replaying them over the baseline would reinstate stale entity records for
// good — the client applies materialized / removed wholesale, with no HLC
// compare. The manager calls this inside the same projection hold that
// registers and captures the generation (registerForFallback), so this drops
// precisely the superseded frames and nothing newer.
//
// Clearing `overflowed` is part of the same statement, not a separate courtesy:
// the flag means "a frame that mattered was dropped", and every frame it could
// refer to has just been superseded by a newer baseline. Leaving it set would
// make the fallback ladder read a resume scan's old overflow as an overflow of
// the baseline it is about to build, burning a retry — and, on the last rung,
// escalating to the locked path for a hole that no longer exists.
func (q *subscriberQueue) Rebaseline() {
	q.mu.Lock()
	defer q.mu.Unlock()
	// Only while parking. Afterwards the parked frames are the writer loop's to
	// hand out, and discarding them would leave the client permanently short of
	// that span -- silently, because this also clears the overflow flag and
	// nothing downstream re-checks.
	//
	// The manager only calls this from inside SubscribeWithACL, which returns
	// before the handler flips the phase, so today the guard never fires. It is
	// here because the statement that USED to enforce it is gone: Release()
	// returned the parked slice and set q.parked = nil in one step, which made a
	// later Rebaseline a structural no-op. Bootstrapped() only flips the phase,
	// leaving the frames in place for Next -- so the invariant now needs saying.
	if q.phase != queueParking {
		return
	}
	q.discardParkedLocked()
	// Cleared under q.mu, paired with Send's set: see the note there.
	q.overflowed = false
}

// Bootstrapped ends the parking phase and reports whether anything was DROPPED
// while parking.
//
// Called once, by the writer loop, immediately after the bootstrap frame is on
// the wire. Everything Send accepts afterwards goes to the live queue, and Next
// hands out what parked before it first, so the frame order the wire sees is
// bootstrap → parked → live with no gap and no reordering.
//
// `dropped` is not redundant with the manager's own Overflowed() check. That
// one runs before the bootstrap frame is BUILT; the parking window then stays
// open across the whole bootstrap WRITE, which is bounded only by
// relayWriteTimeout and is the longest stretch of it on a large account. An
// overflow in that stretch is invisible to the manager and, without this
// verdict, invisible to everyone: the fan-out discards the send error, so the
// flush below would simply be short and the subscriber would stream on,
// permanently missing whatever was dropped.
//
// Read under the SAME q.mu that Send sets the flag under, and in the same
// statement that flips the phase, so no frame can be dropped between the
// verdict and the flush.
func (q *subscriberQueue) Bootstrapped() (dropped bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	// Forward only, the same guard Rebaseline carries: the phase is a one-way
	// lifecycle, and a bare assignment could move a CLOSED queue back to live.
	// Nothing reaches it that way today -- one goroutine gets here, with Close
	// deferred after -- but the teardown is asynchronous by design (see
	// newSubscriberQueue), and if it ever did fire, Send would leave the
	// queueClosed arm and Admit against a member whose Detach has already run:
	// bytes charged with no path left that could ever refund them, which is a
	// permanent leak of the budget every other subscriber draws on.
	if q.phase == queueParking {
		q.phase = queueLive
	}
	// Returned whatever the phase was, so a second call and a call after Close
	// still answer honestly rather than reporting a clean handover.
	return q.overflowed
}

// Next hands the writer loop the frame it should send, blocking until one
// arrives or ctx ends. Reports false when ctx ended and there is nothing left
// to hand over.
//
// It is the ONLY dequeue, and it refunds as it hands over, which is what makes
// the accounting impossible to forget: there is no path where a caller receives
// a frame without the budget learning about it. The frame stays resident until
// the write returns, so one in-flight frame per subscriber is uncounted -- the
// same bounded, deliberate undercount sendq accepts by releasing at pop.
//
// Called only after Bootstrapped: before it the parked buffer is still the
// manager's to supersede via Rebaseline.
func (q *subscriberQueue) Next(ctx context.Context) (*crdt.MarshaledEvent, bool) {
	q.mu.Lock()
	if len(q.parked) > 0 {
		evt := q.parked[0]
		q.parked[0] = nil
		q.parked = q.parked[1:]
		q.dropLocked(evt)
		q.mu.Unlock()
		return evt, true
	}
	q.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, false
	case evt := <-q.live:
		q.mu.Lock()
		q.dropLocked(evt)
		q.mu.Unlock()
		return evt, true
	}
}

// Close releases every frame still queued and leaves the pool.
//
// The order matters and is the AttachShared contract: a shared member's holding
// is not its share of the pool's total, so Detach cannot refund it — the frames
// have to be let go of one at a time, and whichever of them this queue was last
// to hold are the ones whose bytes it gives back.
//
// Setting `closed` first, under the mutex Send takes for both phases, is what
// makes the drain complete rather than a best effort: a broadcast that snapshot
// the subscriber set before it was unregistered can still call Send afterwards,
// and that call now refuses instead of parking a frame nothing will ever
// release.
//
// Idempotent, so the handler can defer it unconditionally.
func (q *subscriberQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.phase = queueClosed
	q.releaseBootstrapLocked()
	q.discardParkedLocked()
	for {
		select {
		case evt := <-q.live:
			q.dropLocked(evt)
		default:
			q.member.Detach()
			return
		}
	}
}

// Overflowed reports whether a frame was dropped during the parking phase. The
// manager consults it after the resume scan; see crdt.Subscriber.Overflowed.
func (q *subscriberQueue) Overflowed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.overflowed
}
