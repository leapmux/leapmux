package service

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// parkedFrameCap bounds the pre-bootstrap park buffer.
//
// Under a reconnect storm (a hub restart, say) a slow multi-page resume scan
// racing fast incoming broadcasts could otherwise grow it without bound and OOM
// the hub. It is an OOM bound, not a tuning knob: overflow is a correctness
// event the fallback ladder handles, not a condition to be sized away.
//
// TWO phases park, not one. The resume scan does (bounded at MaxResumeDeltaOps
// source frames), and so does the FALLBACK baseline walk, which registers the
// subscriber before building and is bounded only by the account's entity count.
// The baseline walk is the shorter of the two in practice -- an in-memory walk
// of a captured generation, against a multi-page journal read -- but it is the
// one whose duration grows with the account.
const parkedFrameCap = 256

// ParkedFrameCapForTest exposes the bound so an external test can state its
// precondition AS the bound rather than as a round number beside it -- a guard
// written as "> 100" passes at 101 commits, where nothing overflows, and the
// test then fails forty lines later at the overflow assertion, reporting a
// broken feature for what is really a short burst.
const ParkedFrameCapForTest = parkedFrameCap

// liveQueueDepth is the steady-state bounded channel depth, i.e. how far a
// subscriber may fall behind the manager goroutine before it is dropped.
const liveQueueDepth = 64

// subscriberQueue is one subscriber's outbound path, in both of its phases.
//
// A user-events connection delivers in a strict order: the bootstrap frame,
// then whatever arrived while the subscribe + resume scan was still running,
// then steady-state live traffic. So Send has two behaviours -- PARK before the
// bootstrap frame is on the wire, STREAM after -- and the switch between them
// has to be seen consistently by the manager goroutine calling Send, by the
// writer loop calling Release, and by the manager calling Rebaseline under its
// projection lock.
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
type subscriberQueue struct {
	mu sync.Mutex
	// parked holds frames received before Release; nil afterwards.
	parked []*crdt.MarshaledEvent
	// released flips once, in Release. Send streams from that point on.
	released bool
	// live is the steady-state bounded queue the writer loop drains.
	live chan *crdt.MarshaledEvent
	// overflowed records that a frame was dropped during the parking phase, so
	// the resume path can FALLBACK to a snapshot instead of shipping a delta
	// with a hole. atomic: Send runs on the manager goroutine while the resume
	// scan reads it on the subscribing request's.
	overflowed atomic.Bool
}

func newSubscriberQueue() *subscriberQueue {
	return &subscriberQueue{live: make(chan *crdt.MarshaledEvent, liveQueueDepth)}
}

// Send accepts one frame, parking or streaming according to phase.
//
// Returns crdt.ErrSubscriberSlow when the subscriber cannot keep up in either
// phase — a full park buffer or a full live queue. The caller tears the
// connection down on that; blocking here would stall the manager goroutine for
// every other subscriber of this user.
func (q *subscriberQueue) Send(ctx context.Context, evt *crdt.MarshaledEvent) error {
	q.mu.Lock()
	if !q.released {
		if len(q.parked) >= parkedFrameCap {
			// Set INSIDE q.mu, paired with Rebaseline's clear, so the two stores
			// are ordered by the same mutex that orders the frames they describe.
			// Both callers happen to hold m.projection today, which already
			// serializes them; relying on that would make this correct only for
			// as long as no Send path escapes the projection hold.
			q.overflowed.Store(true)
			q.mu.Unlock()
			return crdt.ErrSubscriberSlow
		}
		q.parked = append(q.parked, evt)
		q.mu.Unlock()
		return nil
	}
	q.mu.Unlock()

	// Two selects, not one three-way: a single select with a ready send AND a
	// ready ctx.Done() picks between them at RANDOM, so a cancelled context
	// could preempt a send the queue had room for. Take the room first, and
	// consult ctx only when there is none.
	select {
	case q.live <- evt:
		return nil
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return crdt.ErrSubscriberSlow
	}
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
	q.parked = nil
	// Cleared under q.mu, paired with Send's set: see the note there.
	q.overflowed.Store(false)
}

// Release ends the parking phase and returns the frames to flush, in order,
// plus whether anything was DROPPED while parking.
//
// Called once, by the writer loop, immediately after the bootstrap frame is on
// the wire. Everything Send accepts afterwards goes to the live queue, so the
// returned slice is complete: no frame can be both flushed here and streamed.
//
// `dropped` is not redundant with the manager's own Overflowed() check. That
// one runs before the bootstrap frame is BUILT; the parking window then stays
// open across the whole bootstrap WRITE, which is bounded only by
// relayWriteTimeout and is the longest stretch of it on a large account. An
// overflow in that stretch is invisible to the manager and, without this
// verdict, invisible to everyone: liveCatchUpSink discards the send error, so
// the flush below would simply be short and the subscriber would stream on,
// permanently missing whatever was dropped.
//
// Read under the SAME q.mu that Send sets the flag under, and in the same
// statement that flips the phase, so no frame can be dropped between the
// verdict and the flush.
func (q *subscriberQueue) Release() (flush []*crdt.MarshaledEvent, dropped bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	toFlush := q.parked
	q.parked = nil
	q.released = true
	return toFlush, q.overflowed.Load()
}

// Released reports whether the parking phase has ended.
//
// The caller needs it to tell the two overflow REASONS apart: a full park
// buffer happens while a resume scan is still running and can be answered with
// a snapshot, while a full live queue means the subscriber cannot keep up with
// steady-state traffic and must be dropped.
func (q *subscriberQueue) Released() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.released
}

// Overflowed reports whether a frame was dropped during the parking phase. The
// manager consults it after the resume scan; see crdt.Subscriber.Overflowed.
func (q *subscriberQueue) Overflowed() bool {
	return q.overflowed.Load()
}

// Live is the steady-state queue the writer loop selects on.
func (q *subscriberQueue) Live() <-chan *crdt.MarshaledEvent {
	return q.live
}
