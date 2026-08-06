package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// The exact strings these two enums produce ARE the Prometheus vocabulary a
// dashboard is written against, so a rename is a breaking change to something
// outside this repo. Nothing else pins them: every other assertion in this file
// compares an enum to an enum, and the end-to-end handler test reaches only
// three of the six ("bootstrap", "capacity", "bytes").
//
// Spelled out here rather than derived, because a table built from the same
// switch the code uses would agree with any rename.
func TestDropLabelVocabularyIsStable(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "bootstrap", dropPhaseBootstrap.Label())
	assert.Equal(t, "park", dropPhaseParking.Label())
	assert.Equal(t, "live", dropPhaseLive.Label())

	assert.Equal(t, "frames", dropBoundFrames.Label())
	assert.Equal(t, "bytes", dropBoundBytes.Label())
	assert.Equal(t, "capacity", dropBoundCapacity.Label())

	// A value outside the enum must still render as something a metric can
	// carry: WithLabelValues on an empty string silently creates a series
	// nobody is looking at, which is worse than a visibly wrong one.
	assert.Equal(t, "unknown", dropPhase(99).Label())
	assert.Equal(t, "unknown", dropBound(99).Label())
}

// Send's phase switch is written without a default so a fourth phase stops
// compiling rather than silently streaming onto q.live. This covers what the
// resulting fall-through does if one is ever reached anyway: it must fail the
// send, count nothing -- there is no honest (phase, bound) pair for a phase
// nobody has defined -- and above all RELEASE q.mu, because Send unlocks by hand
// and a panic or an early return holding it would deadlock every later Send,
// Next and Close on this queue.
func TestSendOnAnUnknownPhaseFailsWithoutStrandingTheMutex(t *testing.T) {
	t.Parallel()

	pool := sendq.NewMaxBytesPoolForTest()
	q := newSubscriberQueue(pool, func(error) bool { t.Error("no reclaim expected"); return false })
	// Close is NOT deferred: it takes the same mutex, so a regression here would
	// hang the whole package at its timeout instead of failing this one case.
	q.phase = queuePhase(99)

	before := testutil.ToFloat64(
		metrics.UserEventsFramesDroppedTotal.WithLabelValues("unknown", "unknown"))

	err := q.Send(context.Background(), evt("orphan"))
	require.Error(t, err, "a queue in a state its own type cannot name must not accept a frame")

	var refusal queueRefusal
	assert.False(t, errors.As(err, &refusal),
		"not a refusal: refuseFrame counts a drop under a (phase, bound) pair, and this has none")
	assert.Equal(t, before, testutil.ToFloat64(
		metrics.UserEventsFramesDroppedTotal.WithLabelValues("unknown", "unknown")),
		"and nothing may be counted under a made-up label pair")

	// The real assertion: the mutex came back. A stranded q.mu would hang here
	// rather than fail, so the timeout is the failure mode this guards against.
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.mu.Lock()
		q.mu.Unlock() //nolint:staticcheck // acquiring it at all is the assertion
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Send returned holding q.mu; every later Send, Next and Close would deadlock")
	}
	q.Close()
}

// The park/flush/overflow policy had NO direct coverage before this type
// existed: it was four locals inside a ~250-line HTTP handler closure, so
// reaching it meant httptest.NewServer plus a real websocket.Dial, and the only
// handler test in the suite does neither. Every case here is one the closure
// form could not express.
func evt(id string) *crdt.MarshaledEvent {
	return crdt.NewMarshaledEvent(&leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Batch{Batch: &leapmuxv1.OpBatch{BatchId: id}},
	})
}

// frameOfSize builds a frame of at least n bytes, so a test can state a BYTE
// bound with frames big enough that the frame counts cannot fire first.
func frameOfSize(t *testing.T, n int) *crdt.MarshaledEvent {
	t.Helper()
	e := evt(strings.Repeat("x", n))
	require.GreaterOrEqual(t, e.Size(), int64(n), "the padding must survive into the marshaled frame")
	return e
}

// newTestPool builds a budget AND asserts, once every queue drawing on it has
// closed, that it came back empty.
//
// Registered before any queue, so LIFO runs this check last. It turns every
// test in the file into a leak test as well: a frame charged and not refunded
// is a permanent loss of shared budget that would slowly strangle every other
// connection, and it is invisible from any single assertion about frames.
func newTestPool(t *testing.T, capacity, floor int64) *sendq.Pool {
	t.Helper()
	p := sendq.NewPool(sendq.PoolConfig{Capacity: capacity, MinFloor: floor, MaxFloor: floor})
	t.Cleanup(func() {
		assert.Zero(t, p.Used(), "every charged byte must return to the pool")
		assert.Zero(t, p.Members(), "every queue must leave the pool")
	})
	return p
}

// roomyPool cannot be the binding constraint, so a test can state a FRAME-count
// bound without the byte bound firing first.
func roomyPool(t *testing.T) *sendq.Pool {
	t.Helper()
	return newTestPool(t, 64<<20, sendq.DefaultMinFloor)
}

func newTestQueue(t *testing.T, pool *sendq.Pool) *subscriberQueue {
	t.Helper()
	q := newSubscriberQueue(pool, func(error) bool {
		t.Error("nothing in the user-events pool reclaims: a subscriber refuses itself")
		return false
	})
	t.Cleanup(q.Close)
	return q
}

// next takes the frame the writer loop would send, failing rather than blocking
// when there is none.
func next(t *testing.T, q *subscriberQueue) *crdt.MarshaledEvent {
	t.Helper()
	got, ok := q.Next(context.Background())
	require.True(t, ok, "a frame was expected")
	return got
}

func batchID(e *crdt.MarshaledEvent) string { return e.Event.GetBatch().GetBatchId() }

func TestSubscriberQueue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("parks before release and hands over in order", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		require.NoError(t, q.Send(ctx, evt("a")))
		require.NoError(t, q.Send(ctx, evt("b")))

		// Nothing reaches the live queue while parked.
		assert.Empty(t, q.live)

		require.False(t, q.Bootstrapped())
		assert.Equal(t, "a", batchID(next(t, q)))
		assert.Equal(t, "b", batchID(next(t, q)))
	})

	t.Run("streams to the live queue after release", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		require.False(t, q.Bootstrapped())

		require.NoError(t, q.Send(ctx, evt("live")))
		require.Len(t, q.live, 1, "a post-release frame must reach the live queue")
		assert.Equal(t, "live", batchID(next(t, q)))
	})

	// Parked frames were received FIRST, so they go out first -- streaming a
	// live frame ahead of them would deliver the user's state out of order.
	t.Run("hands over everything parked before anything live", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		require.NoError(t, q.Send(ctx, evt("parked-1")))
		require.NoError(t, q.Send(ctx, evt("parked-2")))
		require.False(t, q.Bootstrapped())
		require.NoError(t, q.Send(ctx, evt("live-1")))

		assert.Equal(t, "parked-1", batchID(next(t, q)))
		assert.Equal(t, "parked-2", batchID(next(t, q)))
		assert.Equal(t, "live-1", batchID(next(t, q)))
	})

	// The release is what makes the parked buffer COMPLETE: a frame accepted
	// after it must go to the live queue, never into a buffer nothing will read.
	t.Run("release is one-way, so a later frame is never parked again", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		require.False(t, q.Bootstrapped())
		require.NoError(t, q.Send(ctx, evt("after")))
		assert.Empty(t, q.parked, "nothing may park once released")
		assert.Len(t, q.live, 1)
	})

	t.Run("reports ErrSubscriberSlow when the park buffer overflows", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		for i := range parkedFrameCap {
			require.NoError(t, q.Send(ctx, evt("x")), "frame %d is within the cap", i)
		}
		assert.ErrorIs(t, q.Send(ctx, evt("overflow")), crdt.ErrSubscriberSlow)
		// The cap holds: overflow must not grow the buffer past it.
		assert.Len(t, q.parked, parkedFrameCap)
	})

	// The parking window stays open across the BOOTSTRAP WRITE, which is the
	// stretch the manager's own Overflowed() check cannot see -- it runs before
	// the frame is even built. Bootstrapped is therefore the last place a drop
	// in that window can be noticed, and liveCatchUpSink discards the send
	// error, so without this verdict the flush is simply short and the
	// subscriber streams on missing whatever went away.
	t.Run("release reports a drop that happened while parking", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		for range parkedFrameCap {
			require.NoError(t, q.Send(ctx, evt("x")))
		}
		require.ErrorIs(t, q.Send(ctx, evt("dropped")), crdt.ErrSubscriberSlow)

		assert.True(t, q.Bootstrapped(), "a frame was lost during parking and Bootstrapped must say so")
		assert.Len(t, q.parked, parkedFrameCap, "the survivors are still handed over")
	})

	t.Run("release reports no drop when every parked frame fit", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		require.NoError(t, q.Send(ctx, evt("a")))
		require.NoError(t, q.Send(ctx, evt("b")))

		assert.False(t, q.Bootstrapped())
		assert.Len(t, q.parked, 2)
	})

	t.Run("reports ErrSubscriberSlow when the live queue is full", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		require.False(t, q.Bootstrapped())
		for i := range liveQueueDepth {
			require.NoError(t, q.Send(ctx, evt("x")), "frame %d is within the depth", i)
		}
		assert.ErrorIs(t, q.Send(ctx, evt("overflow")), crdt.ErrSubscriberSlow)
	})

	// A resume that gives up takes a snapshot at a LATER point than anything
	// parked during the scan, so replaying those frames over it would reinstate
	// stale entity records permanently -- the client applies materialized /
	// removed wholesale, with no HLC compare.
	t.Run("rebaseline discards parked frames without releasing", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		require.NoError(t, q.Send(ctx, evt("superseded")))
		q.Rebaseline()

		require.NoError(t, q.Send(ctx, evt("kept")))
		require.False(t, q.Bootstrapped())
		require.Len(t, q.parked, 1, "only frames after the rebaseline survive")
		assert.Equal(t, "kept", batchID(next(t, q)))
	})

	// A single three-way select with a ready send AND a ready ctx.Done() picks
	// between them at RANDOM, so a cancelled context could preempt a send the
	// queue had room for -- nondeterministically. Room wins; ctx is consulted
	// only when there is none.
	t.Run("a cancelled context does not preempt a send the queue can take", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		require.False(t, q.Bootstrapped())
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		for i := range liveQueueDepth {
			require.NoError(t, q.Send(cancelled, evt("x")), "frame %d fits, so ctx must not win", i)
		}
		// Now there is no room, so the cancelled context is what surfaces.
		assert.ErrorIs(t, q.Send(cancelled, evt("overflow")), context.Canceled)
	})

	// The park-phase overflow is what buildResumeDelta consults to convert a
	// dropped frame into ONE snapshot instead of a torn-down connection.
	t.Run("records a park-phase overflow for the resume path", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		assert.False(t, q.Overflowed())
		for range parkedFrameCap {
			require.NoError(t, q.Send(ctx, evt("x")))
		}
		require.ErrorIs(t, q.Send(ctx, evt("overflow")), crdt.ErrSubscriberSlow)
		assert.True(t, q.Overflowed(), "the resume scan must be able to see the drop")
	})

	// Rebaseline discards the parked frames AND the overflow they caused. The
	// flag means "a frame that mattered was dropped", and every frame it could
	// refer to has just been superseded by a newer baseline.
	//
	// Leaving it set is not cosmetic: fallbackOutcome's ladder would read a
	// resume scan's stale overflow as an overflow of the baseline it is about
	// to build, burn a retry, and on the last rung escalate to the locked path
	// for a hole that no longer exists.
	t.Run("rebaseline clears the parked frames and the overflow they caused", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		for range parkedFrameCap {
			require.NoError(t, q.Send(ctx, evt("x")))
		}
		require.ErrorIs(t, q.Send(ctx, evt("overflow")), crdt.ErrSubscriberSlow)
		require.True(t, q.Overflowed())

		q.Rebaseline()

		assert.False(t, q.Overflowed(), "the dropped frames are superseded, so the drop is too")
		// And there is room again, so the next baseline's live traffic parks
		// normally rather than immediately re-overflowing.
		require.NoError(t, q.Send(ctx, evt("after")))
		require.False(t, q.Bootstrapped())
		require.Len(t, q.parked, 1, "the superseded frames must be gone, leaving only the new one")
		assert.Equal(t, "after", batchID(next(t, q)))
	})
}

// The BYTE bound is the whole point of issue #361: 256 parked plus 64 live
// slots were counts over payloads nothing at this layer sized, so a subscriber
// holding 320 multi-megabyte frames was within every bound the Hub had.
func TestSubscriberQueueByteBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// A sole member settles at about half its pool: the rule admits while
	// held + size <= Capacity - used, and used IS its holding when it is alone.
	// So 16 frames of 4 KiB fill a 128 KiB budget long before 256 slots do.
	const (
		capacity  = 128 << 10
		floor     = 8 << 10
		frameSize = 4 << 10
	)

	fill := func(t *testing.T, q *subscriberQueue) int {
		t.Helper()
		for i := range parkedFrameCap {
			if err := q.Send(ctx, frameOfSize(t, frameSize)); err != nil {
				require.ErrorIs(t, err, crdt.ErrSubscriberSlow)
				return i
			}
		}
		t.Fatal("the byte bound must bind before the frame count does")
		return 0
	}

	t.Run("a park-phase byte overflow feeds the same snapshot fallback", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, newTestPool(t, capacity, floor))

		took := fill(t, q)
		assert.Positive(t, took, "the budget must admit something before refusing")
		assert.Less(t, took, parkedFrameCap, "the byte bound, not the frame count, must be what refused")
		// Refused on bytes or refused on frames, the resume path needs the same
		// verdict: this connection has a hole, answer it with a snapshot.
		assert.True(t, q.Overflowed(), "a byte refusal must reach the fallback ladder")
		assert.True(t, q.Bootstrapped())
	})

	t.Run("a live-phase byte overflow drops the subscriber", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, newTestPool(t, capacity, floor))
		require.False(t, q.Bootstrapped())

		took := fill(t, q)
		assert.Less(t, took, liveQueueDepth, "the byte bound, not the channel depth, must be what refused")
		assert.False(t, q.Overflowed(), "a steady-state drop is not a park overflow; there is no scan to fall back from")
	})

	// The frame counts still bind on small frames -- being 320 frames behind is
	// a slow subscriber whether or not the bytes are trivial.
	t.Run("the frame count still binds when the frames are small", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, newTestPool(t, capacity, floor))
		for i := range parkedFrameCap {
			require.NoError(t, q.Send(ctx, evt("x")), "frame %d is small enough to be within both bounds", i)
		}
		assert.ErrorIs(t, q.Send(ctx, evt("overflow")), crdt.ErrSubscriberSlow)
		assert.Len(t, q.parked, parkedFrameCap)
	})

	// Dequeuing frees the budget, so a subscriber that catches up gets its
	// allowance back rather than staying permanently poisoned by one burst.
	t.Run("draining returns the budget", func(t *testing.T) {
		t.Parallel()
		pool := newTestPool(t, capacity, floor)
		q := newTestQueue(t, pool)
		require.False(t, q.Bootstrapped())

		took := fill(t, q)
		require.Positive(t, pool.Used())
		for range took {
			next(t, q)
		}
		assert.Zero(t, pool.Used(), "a fully drained subscriber must hold nothing")
		assert.Zero(t, q.member.Charged())
		require.NoError(t, q.Send(ctx, frameOfSize(t, frameSize)), "and its allowance must be back")
	})
}

// Frames are memoized per transition and handed to every subscriber of the user
// by POINTER, so charging each queue for the full payload would report several
// times the memory that exists -- and shed connections on a Hub with room. This
// is the property that made #361 more than a byte counter.
func TestSubscriberQueueChargesASharedFrameOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	pool := newTestPool(t, 4<<20, 1<<20)
	a, b := newTestQueue(t, pool), newTestQueue(t, pool)
	require.False(t, a.Bootstrapped())
	require.False(t, b.Bootstrapped())

	shared := frameOfSize(t, 4096)
	size := shared.Size()

	require.NoError(t, a.Send(ctx, shared))
	require.Equal(t, size, pool.Used())
	require.NoError(t, b.Send(ctx, shared))
	assert.Equal(t, size, pool.Used(),
		"one buffer held twice is one buffer; a second charge would be memory that does not exist")

	// Both subscribers really are one frame behind, though, and THAT is the
	// number that decides which of them gets refused first.
	assert.Equal(t, size, a.member.Charged())
	assert.Equal(t, size, b.member.Charged())

	// The first to let go frees nothing -- the buffer is still held.
	next(t, a)
	assert.Equal(t, size, pool.Used(), "the frame is still resident while b holds it")
	next(t, b)
	assert.Zero(t, pool.Used(), "the last holder returns the buffer")
}

// A refusal must give back exactly what letting go of the frame frees, which is
// not always what the refusing queue's own Retain charged: whichever subscriber
// happens to release last is the one holding bytes another subscriber paid for.
// Getting that wrong drifts the total in one direction or the other, and a
// total that drifts up quietly disables the budget for everyone.
func TestSubscriberQueueRefusalsAndDrainsBalanceUnderConcurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Small enough that plenty of sends are refused, which is the interesting
	// half: an admitted frame's accounting is the easy case.
	pool := newTestPool(t, 256<<10, 16<<10)
	const subscribers = 8
	queues := make([]*subscriberQueue, subscribers)
	for i := range queues {
		queues[i] = newTestQueue(t, pool)
		require.False(t, queues[i].Bootstrapped())
	}

	// Fan each frame out to every subscriber the way the manager does -- one
	// goroutine per frame, so the queues' charges and refunds interleave. No
	// require/assert inside: they are not safe off the test goroutine, and the
	// property under test is the total, not any individual verdict.
	var wg sync.WaitGroup
	for round := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			shared := evt(strings.Repeat("x", 1024))
			for _, q := range queues {
				_ = q.Send(ctx, shared)
			}
			// Some subscribers keep up and some fall behind and get refused,
			// which is what puts refusals and drains in the same race.
			// Already-cancelled, so Next never blocks. When a frame is also
			// ready the select picks between the two arms at random, which is
			// the point: it varies which drains land and which do not, so the
			// interleavings differ from run to run rather than being fixed.
			drain, drainCancel := context.WithCancel(ctx)
			drainCancel()
			for _, q := range queues[:round%subscribers] {
				q.Next(drain)
			}
		}()
	}
	wg.Wait()

	// The per-pool cleanup closes nothing itself: each queue's own cleanup
	// releases what it still holds, and the pool's -- registered first, so run
	// last -- is what asserts the total came back to exactly zero. All this
	// needs to add is that it never went NEGATIVE on the way, which would read
	// as "the pool is empty" from then on and grant every connection an
	// unlimited threshold.
	assert.GreaterOrEqual(t, pool.Used(), int64(0), "the resident total must never go negative")
}

// Close is the queue's only teardown, and it is what the handler defers on
// every exit path -- including the early returns before the writer loop is ever
// reached. Every frame still queued has to be released there, because a shared
// member's holding is not its share of the pool's total and Detach cannot
// refund it.
func TestSubscriberQueueCloseReturnsEverythingItHolds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, tt := range []struct {
		name  string
		setup func(t *testing.T, q *subscriberQueue)
	}{
		{"holding parked frames", func(t *testing.T, q *subscriberQueue) {
			require.NoError(t, q.Send(ctx, frameOfSize(t, 4096)))
			require.NoError(t, q.Send(ctx, frameOfSize(t, 4096)))
		}},
		{"holding live frames", func(t *testing.T, q *subscriberQueue) {
			require.False(t, q.Bootstrapped())
			require.NoError(t, q.Send(ctx, frameOfSize(t, 4096)))
		}},
		{"holding both", func(t *testing.T, q *subscriberQueue) {
			require.NoError(t, q.Send(ctx, frameOfSize(t, 4096)))
			require.False(t, q.Bootstrapped())
			require.NoError(t, q.Send(ctx, frameOfSize(t, 4096)))
		}},
		{"after a rebaseline", func(t *testing.T, q *subscriberQueue) {
			require.NoError(t, q.Send(ctx, frameOfSize(t, 4096)))
			q.Rebaseline()
			require.NoError(t, q.Send(ctx, frameOfSize(t, 4096)))
		}},
		{"drained empty", func(t *testing.T, q *subscriberQueue) {
			require.NoError(t, q.Send(ctx, frameOfSize(t, 4096)))
			require.False(t, q.Bootstrapped())
			next(t, q)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newTestPool(t, 4<<20, 1<<20)
			q := newSubscriberQueue(pool, func(error) bool { t.Error("no reclaim expected"); return false })
			tt.setup(t, q)

			q.Close()
			assert.Zero(t, pool.Used(), "%s: Close must return every byte", tt.name)
			assert.Zero(t, pool.Members(), "%s: Close must leave the pool", tt.name)

			// Deferred unconditionally by the handler, and the drain goroutine
			// can reach it too -- so it has to survive being called twice
			// without double-counting anything.
			q.Close()
			assert.Zero(t, pool.Used())
			assert.Zero(t, pool.Members())
		})
	}
}

// A broadcast that snapshot the subscriber set before this connection was
// unregistered can still be inside Send after the handler has closed the queue.
// Taking that frame would charge budget on behalf of a connection nothing will
// ever drain.
func TestSubscriberQueueSendAfterCloseChargesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	pool := newTestPool(t, 4<<20, 1<<20)
	q := newSubscriberQueue(pool, func(error) bool { t.Error("no reclaim expected"); return false })
	q.Close()

	// NOT ErrSubscriberSlow: that verdict routes into the overflow policy, which
	// would warn about a full buffer and cancel a context for a connection that
	// is already gone and was never slow.
	err := q.Send(ctx, frameOfSize(t, 4096))
	assert.ErrorIs(t, err, errQueueClosed)
	assert.NotErrorIs(t, err, crdt.ErrSubscriberSlow)
	assert.Zero(t, pool.Used(), "a frame refused after Close must leave nothing behind")
	assert.Empty(t, q.parked)
	assert.Empty(t, q.live)
}

// Next is the writer loop's only way to take a frame, so it is also the only
// place a dequeue can forget to refund. Reporting false rather than blocking
// forever is what lets the handler exit on a cancelled connection.
func TestSubscriberQueueNextReportsFalseWhenTheConnectionEnds(t *testing.T) {
	t.Parallel()

	q := newTestQueue(t, roomyPool(t))
	require.False(t, q.Bootstrapped())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, ok := q.Next(ctx)
	assert.False(t, ok)
	assert.Nil(t, got)
}

// The OVERFLOW POLICY -- which of two very different answers a dropped frame
// gets -- is what newUserEventsSubscriber was extracted for.
//
// Inline it was an anonymous closure inside a struct literal inside a 280-line
// HTTP handler, so exercising it meant httptest.NewServer plus a real
// websocket.Dial plus a burst large enough to fill 256 slots. As a named unit
// the two arms are three lines each to assert, and the difference between them
// -- whether the connection is torn down -- is directly observable.
func TestNewUserEventsSubscriber_OverflowPolicyDiffersByPhase(t *testing.T) {
	user := &auth.UserInfo{ID: userid.MustNew(id.Generate())}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	pool := roomyPool(t)
	sub, queue := newUserEventsSubscriber(ctx, cancel, user, "client-1", nil, pool)
	t.Cleanup(queue.Close)
	require.Equal(t, "client-1", sub.ClientID)

	// PRE-BOOTSTRAP: fill the park buffer, then overflow it.
	for i := range parkedFrameCap {
		require.NoError(t, sub.Send(evt(fmt.Sprintf("parked-%d", i))))
	}
	require.ErrorIs(t, sub.Send(evt("overflows")), crdt.ErrSubscriberSlow)
	// Flagged for the manager's ladder, and the connection is left ALIVE -- a
	// teardown here would send the client back with the same cursor to redo the
	// work, under the load that caused the overflow.
	assert.True(t, sub.Overflowed(), "a park overflow must be visible to the fallback ladder")
	require.NoError(t, ctx.Err(), "the pre-bootstrap arm must NOT tear the connection down")

	// A rebaseline supersedes those frames and clears the flag, which is what
	// lets the ladder retry rather than escalate for a hole that is gone.
	sub.OnRebaseline()
	assert.False(t, sub.Overflowed(), "Rebaseline must clear the flag alongside the frames")

	// STEADY STATE: past the bootstrap frame there is no scan to fall back from.
	require.False(t, queue.Bootstrapped(), "the rebaseline cleared the earlier drop")
	for range liveQueueDepth {
		require.NoError(t, sub.Send(evt("live")))
	}
	require.ErrorIs(t, sub.Send(evt("over-live")), crdt.ErrSubscriberSlow)
	assert.Error(t, ctx.Err(), "the steady-state arm must cancel, dropping the subscriber")
}

// The policy routes on the REFUSAL, not on the queue's phase at the moment it
// looks -- and those differ for the whole bootstrap WRITE, which on a large
// account is the longest stretch of the parking window.
//
// Send runs on the crdt broadcast goroutine; Bootstrapped runs on the writer
// loop's. So a parking-phase refusal really can arrive here after the flip, and
// asking the queue then routed it down the steady-state arm: a "subscriber
// buffer full, dropping connection" line blaming a slow client for a full
// shared budget -- the exact mislabelling the parking arm exists to prevent --
// plus a cancel for a connection that was falling back perfectly well.
func TestAnswerRefusal_RoutesOnTheRefusalNotTheQueuesLaterPhase(t *testing.T) {
	t.Parallel()
	user := &auth.UserInfo{ID: userid.MustNew(id.Generate())}

	t.Run("a parking refusal never tears the connection down", func(t *testing.T) {
		t.Parallel()
		for _, bound := range []dropBound{dropBoundFrames, dropBoundBytes, dropBoundCapacity} {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			_, queue := newUserEventsSubscriber(ctx, cancel, user, "client-1", nil, roomyPool(t))
			t.Cleanup(queue.Close)
			// The writer loop has already handed the bootstrap frame over -- this
			// is the window the race lives in.
			require.False(t, queue.Bootstrapped())

			answerRefusal(queueRefusal{phase: dropPhaseParking, bound: bound}, cancel, user, "client-1")

			assert.NoError(t, ctx.Err(),
				"a %s refusal that fired while parking is answered with a snapshot fallback, not a teardown", bound)
		}
	})

	// And the steady-state arm still tears down, whichever bound refused: past
	// the bootstrap frame there is no scan to fall back from.
	t.Run("a steady-state refusal always tears the connection down", func(t *testing.T) {
		t.Parallel()
		for _, bound := range []dropBound{dropBoundFrames, dropBoundBytes, dropBoundCapacity} {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			answerRefusal(queueRefusal{phase: dropPhaseLive, bound: bound}, cancel, user, "client-1")

			assert.Error(t, ctx.Err(), "a %s refusal in the live phase must drop the subscriber", bound)
		}
	})
}

// The two labels are what make a drop actionable, and they are not
// interchangeable: `phase` says what the drop COST (a snapshot fallback with
// the connection intact, or a disconnect), `bound` says what to DO about it (a
// slow client, or a deployment out of shared budget). A drop counted under the
// wrong pair sends an operator after the wrong fix.
func TestSubscriberQueueCountsDropsByPhaseAndBound(t *testing.T) {
	ctx := context.Background()

	counter := func(phase dropPhase, bound dropBound) float64 {
		return testutil.ToFloat64(metrics.UserEventsFramesDroppedTotal.WithLabelValues(phase.Label(), bound.Label()))
	}

	t.Run("a park-phase frame-count overflow", func(t *testing.T) {
		before := counter(dropPhaseParking, dropBoundFrames)
		q := newTestQueue(t, roomyPool(t))
		for range parkedFrameCap {
			require.NoError(t, q.Send(ctx, evt("x")))
		}
		require.ErrorIs(t, q.Send(ctx, evt("overflow")), crdt.ErrSubscriberSlow)
		assert.Equal(t, before+1, counter(dropPhaseParking, dropBoundFrames))
	})

	t.Run("a park-phase byte overflow", func(t *testing.T) {
		before := counter(dropPhaseParking, dropBoundBytes)
		q := newTestQueue(t, newTestPool(t, 64<<10, 8<<10))
		for range parkedFrameCap {
			if err := q.Send(ctx, frameOfSize(t, 4<<10)); err != nil {
				require.ErrorIs(t, err, crdt.ErrSubscriberSlow)
				break
			}
		}
		assert.Equal(t, before+1, counter(dropPhaseParking, dropBoundBytes),
			"a byte refusal must not be reported as a slow client")
	})

	t.Run("a steady-state frame-count overflow", func(t *testing.T) {
		before := counter(dropPhaseLive, dropBoundFrames)
		q := newTestQueue(t, roomyPool(t))
		require.False(t, q.Bootstrapped())
		for range liveQueueDepth {
			require.NoError(t, q.Send(ctx, evt("x")))
		}
		require.ErrorIs(t, q.Send(ctx, evt("overflow")), crdt.ErrSubscriberSlow)
		assert.Equal(t, before+1, counter(dropPhaseLive, dropBoundFrames))
	})

	t.Run("a steady-state byte overflow", func(t *testing.T) {
		before := counter(dropPhaseLive, dropBoundBytes)
		q := newTestQueue(t, newTestPool(t, 64<<10, 8<<10))
		require.False(t, q.Bootstrapped())
		for range liveQueueDepth {
			if err := q.Send(ctx, frameOfSize(t, 4<<10)); err != nil {
				require.ErrorIs(t, err, crdt.ErrSubscriberSlow)
				break
			}
		}
		assert.Equal(t, before+1, counter(dropPhaseLive, dropBoundBytes))
	})
}

// A frame larger than the WHOLE budget is a third condition, not a shade of the
// second: it is refused on an empty pool just as readily, so no amount of
// draining and no retry can change the answer. Only the bootstrap arm used to
// draw that line -- it asked the pool a separate Fits() question -- so the two
// Send arms reported a permanently oversized frame as ordinary memory pressure,
// on the label an operator reads as "the deployment is under load right now".
//
// Every arm now reads it off one admission outcome, so all three agree.
func TestSubscriberQueueSeparatesAnUnfittableFrameFromPressure(t *testing.T) {
	ctx := context.Background()

	counter := func(phase dropPhase, bound dropBound) float64 {
		return testutil.ToFloat64(metrics.UserEventsFramesDroppedTotal.WithLabelValues(phase.Label(), bound.Label()))
	}
	// Smaller than any single frame the test builds, so the refusal is about the
	// FRAME's size rather than about what the pool happens to be holding.
	const capacity = 8 << 10

	t.Run("during the parking phase", func(t *testing.T) {
		beforeCapacity := counter(dropPhaseParking, dropBoundCapacity)
		beforeBytes := counter(dropPhaseParking, dropBoundBytes)
		pool := newTestPool(t, capacity, 1<<10)
		q := newTestQueue(t, pool)

		err := q.Send(ctx, frameOfSize(t, 64<<10))

		var refusal queueRefusal
		require.ErrorAs(t, err, &refusal)
		assert.Equal(t, dropPhaseParking, refusal.phase)
		assert.Equal(t, dropBoundCapacity, refusal.bound,
			"a frame past the whole budget is the operator's to fix, not a moment of pressure")
		assert.ErrorIs(t, err, crdt.ErrSubscriberSlow,
			"the manager still routes it as a slow subscriber")
		assert.True(t, q.Overflowed(), "and the fallback ladder still sees the hole")

		assert.Equal(t, beforeCapacity+1, counter(dropPhaseParking, dropBoundCapacity))
		assert.Equal(t, beforeBytes, counter(dropPhaseParking, dropBoundBytes),
			"it must not also land on the series an operator reads as transient")
		assert.Zero(t, pool.Used(), "a refused frame leaves nothing charged")
	})

	t.Run("during the live phase", func(t *testing.T) {
		beforeCapacity := counter(dropPhaseLive, dropBoundCapacity)
		beforeBytes := counter(dropPhaseLive, dropBoundBytes)
		pool := newTestPool(t, capacity, 1<<10)
		q := newTestQueue(t, pool)
		require.False(t, q.Bootstrapped())

		err := q.Send(ctx, frameOfSize(t, 64<<10))

		var refusal queueRefusal
		require.ErrorAs(t, err, &refusal)
		assert.Equal(t, dropPhaseLive, refusal.phase)
		assert.Equal(t, dropBoundCapacity, refusal.bound)

		assert.Equal(t, beforeCapacity+1, counter(dropPhaseLive, dropBoundCapacity))
		assert.Equal(t, beforeBytes, counter(dropPhaseLive, dropBoundBytes))
		assert.Zero(t, pool.Used())
	})

	// The same frame against a pool that COULD hold it is the other verdict, so
	// this is not simply reporting "capacity" for everything.
	t.Run("a pool that is merely full is still pressure", func(t *testing.T) {
		before := counter(dropPhaseLive, dropBoundBytes)
		// Big enough that the newcomer's frame COULD fit an empty pool -- the
		// point of this case is a refusal by occupancy, not by size, and a frame
		// is charged at crdt.ResidentFactor times its wire length.
		pool := newTestPool(t, 192<<10, 8<<10)
		hog := newTestQueue(t, pool)
		for range parkedFrameCap {
			if hog.Send(ctx, frameOfSize(t, 4<<10)) != nil {
				break
			}
		}
		require.Positive(t, pool.Used())

		q := newTestQueue(t, pool)
		require.False(t, q.Bootstrapped())
		frame := frameOfSize(t, 64<<10)
		require.LessOrEqual(t, frame.Size(), pool.Capacity(), "fixture: the frame must fit an empty pool")
		err := q.Send(ctx, frame)

		var refusal queueRefusal
		require.ErrorAs(t, err, &refusal)
		assert.Equal(t, dropBoundBytes, refusal.bound,
			"a frame that would fit an emptier pool is pressure, and pressure drains")
		assert.Equal(t, before+1, counter(dropPhaseLive, dropBoundBytes))
	})
}

// The refusal is the verdict that FIRED, not a question the caller asks the
// queue afterwards. The phase advances from the writer loop's goroutine while
// Send runs on the manager's, across the whole bootstrap write -- so a caller
// re-deriving it a statement later can read a parking refusal as a steady-state
// one and tear down a connection that was falling back perfectly well.
func TestSubscriberQueueRefusalCarriesTheVerdictThatFired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	q := newTestQueue(t, roomyPool(t))
	for range parkedFrameCap {
		require.NoError(t, q.Send(ctx, evt("x")))
	}
	err := q.Send(ctx, evt("overflow"))

	// The writer loop gets there before anyone looks at the error.
	require.True(t, q.Bootstrapped())

	var refusal queueRefusal
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, dropPhaseParking, refusal.phase,
		"the verdict is the phase that refused, not the phase the queue has since reached")
	assert.Equal(t, dropBoundFrames, refusal.bound)
	// And the live arm, which had no vocabulary at all, now names its bound too.
	assert.Contains(t, err.Error(), dropPhaseParking.Label())
	assert.Contains(t, err.Error(), dropBoundFrames.Label())
}

// The live arm used to return a bare crdt.ErrSubscriberSlow, so a full channel
// and a full shared budget -- a slow client and an undersized deployment --
// arrived at the log indistinguishable. The metric had drawn that distinction
// from the start; only the caller could not see it.
func TestSubscriberQueueLiveRefusalsNameTheirBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("a full live channel is the client's fault", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		require.False(t, q.Bootstrapped())
		for range liveQueueDepth {
			require.NoError(t, q.Send(ctx, evt("x")))
		}

		var refusal queueRefusal
		require.ErrorAs(t, q.Send(ctx, evt("overflow")), &refusal)
		assert.Equal(t, dropPhaseLive, refusal.phase)
		assert.Equal(t, dropBoundFrames, refusal.bound)
	})

	t.Run("a full shared budget is not", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, newTestPool(t, 64<<10, 8<<10))
		require.False(t, q.Bootstrapped())
		var err error
		for range liveQueueDepth {
			if err = q.Send(ctx, frameOfSize(t, 4<<10)); err != nil {
				break
			}
		}

		var refusal queueRefusal
		require.ErrorAs(t, err, &refusal)
		assert.Equal(t, dropPhaseLive, refusal.phase)
		assert.Equal(t, dropBoundBytes, refusal.bound,
			"the byte bound bound first, and the caller has to be able to say so")
	})

	// A connection already going away is not a slow client, and saying so is
	// more useful than blaming it -- so that one arm is NOT a queueRefusal.
	t.Run("a connection that is already gone is not blamed", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		require.False(t, q.Bootstrapped())
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		for range liveQueueDepth {
			require.NoError(t, q.Send(cancelled, evt("x")))
		}

		err := q.Send(cancelled, evt("overflow"))
		assert.ErrorIs(t, err, context.Canceled)
		var refusal queueRefusal
		assert.False(t, errors.As(err, &refusal),
			"the overflow policy must not run for a connection that is already unwinding")
	})
}

// The bootstrap frame is the largest single thing a user-events connection ever
// holds -- a whole filtered account snapshot, or a resume delta -- and unlike a
// broadcast frame it is unique to its subscriber, so N tabs reconnecting at
// once really is N snapshots resident at once. Leaving it uncharged would have
// left the connect path with the defect the backlog path just lost: a
// per-connection cost with nothing bounding the connection count.
func TestSubscriberQueueBootstrapFrame(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("is charged while it is on the wire and refunded after", func(t *testing.T) {
		t.Parallel()
		pool := newTestPool(t, 4<<20, 1<<20)
		q := newTestQueue(t, pool)

		frame := frameOfSize(t, 64<<10)
		require.Equal(t, bootstrapAdmitted, q.AdmitBootstrap(frame))
		assert.Equal(t, frame.Size(), pool.Used())
		assert.Equal(t, frame.Size(), q.member.Charged(),
			"it is this connection's memory, so it counts toward what else it may take")

		q.BootstrapSent()
		assert.Zero(t, pool.Used())
		assert.Zero(t, q.member.Charged())
	})

	// It is charged on top of whatever parked during the subscribe, because that
	// is all one connection's memory -- and a connection already holding a large
	// backlog asking for a large snapshot is exactly the case to make wait.
	t.Run("competes with what already parked", func(t *testing.T) {
		t.Parallel()
		pool := newTestPool(t, 128<<10, 8<<10)
		q := newTestQueue(t, pool)

		parked := 0
		for range parkedFrameCap {
			if q.Send(ctx, frameOfSize(t, 4<<10)) != nil {
				break
			}
			parked++
		}
		require.Positive(t, parked)

		assert.Equal(t, bootstrapPoolFull, q.AdmitBootstrap(frameOfSize(t, 32<<10)),
			"a connection already at its threshold must not also be handed a snapshot")
	})

	// The verdict that has no retry. A snapshot larger than the pool's whole
	// capacity is refused at every occupancy -- an EMPTY pool included -- so the
	// answer the client gets has to differ in kind from "wait": handing out
	// retry-later here produced a client rebuilding the same oversized snapshot
	// every few seconds forever, while the user watched an app that never
	// finished loading.
	t.Run("a frame larger than the whole pool can never be admitted", func(t *testing.T) {
		t.Parallel()
		// Deliberately empty: occupancy is not what refuses this.
		pool := newTestPool(t, 8<<10, 1<<10)
		q := newTestQueue(t, pool)
		require.Zero(t, pool.Used(), "the pool must be empty, or this is a pressure case")

		frame := frameOfSize(t, 64<<10)
		require.Greater(t, frame.Size(), pool.Capacity(), "fixture: the frame must not fit at all")

		assert.Equal(t, bootstrapUnfittable, q.AdmitBootstrap(frame),
			"an empty pool refusing a frame is about the FRAME, and no retry can change that")
		assert.Zero(t, pool.Used(), "a refusal must leave nothing charged")
		assert.Zero(t, q.member.Charged())
		assert.Nil(t, q.bootstrap, "and must not take the slot it was refused")

		// The slot is still free, so a smaller opening frame -- the retry that
		// CAN work, once the operator raises the budget -- is still accepted.
		require.Equal(t, bootstrapAdmitted, q.AdmitBootstrap(frameOfSize(t, 1<<10)))
	})

	// A connection has exactly one opening frame. Letting a second call replace
	// the first would strand the frame it displaced -- charged, held by nothing,
	// and refunded by nobody, since Close only knows about the slot's current
	// occupant.
	t.Run("a second admission is refused rather than replacing the first", func(t *testing.T) {
		t.Parallel()
		pool := newTestPool(t, 4<<20, 1<<20)
		q := newTestQueue(t, pool)

		first := frameOfSize(t, 64<<10)
		require.Equal(t, bootstrapAdmitted, q.AdmitBootstrap(first))
		charged := pool.Used()

		assert.Equal(t, bootstrapNotAdmissible, q.AdmitBootstrap(frameOfSize(t, 4<<10)),
			"the slot is taken; a second frame must not silently displace the first")
		assert.Equal(t, charged, pool.Used(), "and must leave nothing charged behind it")

		q.BootstrapSent()
		assert.Zero(t, pool.Used(), "the frame that WAS admitted is still the one released")
	})

	// The handler calls BootstrapSent on both write outcomes, and Close calls it
	// again on the way out. Neither may double-refund: a total that drifts down
	// reads as "empty" and grants every connection an unlimited threshold.
	t.Run("releasing is idempotent and safe without an admission", func(t *testing.T) {
		t.Parallel()
		pool := newTestPool(t, 4<<20, 1<<20)
		q := newTestQueue(t, pool)

		q.BootstrapSent() // never admitted one
		assert.Zero(t, pool.Used())

		require.Equal(t, bootstrapAdmitted, q.AdmitBootstrap(frameOfSize(t, 64<<10)))
		q.BootstrapSent()
		q.BootstrapSent()
		assert.Zero(t, pool.Used(), "a second release must not refund the frame twice")
		assert.Zero(t, q.member.Charged())

		// And the slot is free again, so a queue is not permanently poisoned by
		// having sent its bootstrap frame.
		require.Equal(t, bootstrapAdmitted, q.AdmitBootstrap(frameOfSize(t, 4<<10)))
	})

	// Close is the one teardown that cannot miss a frame, and the bootstrap
	// frame is the one frame that lives outside both buffers. A handler that
	// returns between admitting and writing -- a cancelled context, a panic
	// unwinding through the deferred Close -- must still give the bytes back.
	t.Run("is released by Close when the handler never sends it", func(t *testing.T) {
		t.Parallel()
		pool := newTestPool(t, 4<<20, 1<<20)
		q := newSubscriberQueue(pool, func(error) bool { t.Error("no reclaim expected"); return false })

		require.Equal(t, bootstrapAdmitted, q.AdmitBootstrap(frameOfSize(t, 64<<10)))
		require.Positive(t, pool.Used())

		q.Close()
		assert.Zero(t, pool.Used(), "Close must return the bootstrap frame too")
		assert.Zero(t, pool.Members())
	})

	t.Run("is refused on a closed queue", func(t *testing.T) {
		t.Parallel()
		pool := newTestPool(t, 4<<20, 1<<20)
		q := newSubscriberQueue(pool, func(error) bool { t.Error("no reclaim expected"); return false })
		q.Close()

		assert.Equal(t, bootstrapNotAdmissible, q.AdmitBootstrap(frameOfSize(t, 64<<10)))
		assert.Zero(t, pool.Used())
	})

	// A refusal has to leave nothing behind: the frame was retained to ask, and
	// the connection is about to be told to retry, so those bytes must not stay
	// charged against a budget nobody is drawing on any more.
	t.Run("a refusal leaves the budget exactly as it found it", func(t *testing.T) {
		t.Parallel()
		// Big enough that the newcomer's frame COULD fit an empty pool -- the
		// point of this case is a refusal by occupancy, not by size. At 128 KiB
		// the 64 KiB frame (charged at ResidentFactor) exceeded the whole
		// capacity, which is a different verdict entirely.
		pool := newTestPool(t, 192<<10, 8<<10)
		hog := newTestQueue(t, pool)
		for range parkedFrameCap {
			if hog.Send(ctx, frameOfSize(t, 4<<10)) != nil {
				break
			}
		}
		before := pool.Used()
		require.Positive(t, before)

		newcomer := newTestQueue(t, pool)
		assert.Equal(t, bootstrapPoolFull, newcomer.AdmitBootstrap(frameOfSize(t, 64<<10)))
		assert.Equal(t, before, pool.Used(), "a refused connect must not leave bytes charged")
		assert.Zero(t, newcomer.member.Charged())
	})
}

// Rebaseline supersedes what parked BEFORE the bootstrap frame. Afterwards the
// parked frames are the writer loop's to hand out, and discarding them would
// leave the client permanently short of that span -- silently, because
// Rebaseline also clears the overflow flag and nothing downstream re-checks.
//
// The old Release() enforced this structurally: it returned the parked slice and
// nil'd it in one step, so a later Rebaseline had nothing to discard.
// Bootstrapped() only flips the phase, leaving the frames in place for Next, so
// the invariant now has to be stated rather than implied.
func TestSubscriberQueueRebaselineDoesNotDiscardAfterBootstrap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := roomyPool(t)
	q := newTestQueue(t, pool)

	require.NoError(t, q.Send(ctx, evt("parked-1")))
	require.NoError(t, q.Send(ctx, evt("parked-2")))

	// Before the phase flips, a rebaseline supersedes them: this is the case the
	// manager actually drives, and it must keep working.
	q.Rebaseline()
	assert.Empty(t, q.parked, "a rebaseline while parking discards the superseded frames")
	assert.Zero(t, pool.Used(), "and refunds every one of them")

	require.NoError(t, q.Send(ctx, evt("parked-3")))
	require.False(t, q.Bootstrapped(), "nothing was dropped")

	// After the flip the frame is owed to the client, so a rebaseline must not
	// take it. Without the guard this dropped it and reported nothing.
	q.Rebaseline()
	require.Len(t, q.parked, 1, "a frame already promised to the writer loop must survive")
	assert.Equal(t, "parked-3", batchID(next(t, q)))
}

// The lifecycle is one-way and one value. It used to be two independent
// booleans, and the guards that read them were added one at a time as gaps were
// noticed -- so the transitions themselves were never stated anywhere.
func TestSubscriberQueuePhaseIsOneWay(t *testing.T) {
	t.Parallel()

	t.Run("parking to live to closed", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, newTestPool(t, 4<<20, 1<<20))

		assert.Equal(t, queueParking, q.phase, "a new queue has not handed over its opening frame")

		q.Bootstrapped()
		assert.Equal(t, queueLive, q.phase)

		q.Close()
		assert.Equal(t, queueClosed, q.phase)
	})

	// Bootstrapped advances the phase, and the advance is FORWARD-only like
	// every other transition here. A bare assignment would move a closed queue
	// back to live, and Send would then leave its queueClosed arm and charge a
	// member whose Detach has already run -- bytes with no path left that could
	// refund them, which is a permanent leak of the shared budget.
	t.Run("bootstrapping a closed queue does not reopen it", func(t *testing.T) {
		t.Parallel()
		pool := newTestPool(t, 4<<20, 1<<20)
		q := newTestQueue(t, pool)

		require.NoError(t, q.Send(t.Context(), frameOfSize(t, 4<<10)))
		q.Close()

		assert.False(t, q.Bootstrapped(),
			"a closed queue reports what it dropped, not a clean handover")
		assert.Equal(t, queueClosed, q.phase, "a closed queue must not be moved back to live")

		// ...and the queueClosed arm is still what a late broadcast meets.
		err := q.Send(t.Context(), frameOfSize(t, 4<<10))
		assert.ErrorIs(t, err, errQueueClosed)
		assert.Zero(t, pool.Used(), "a frame offered after Close must charge nothing")
	})

	// The verdict survives the phase advance: a drop noticed during the parking
	// window is still reported by a second call, and by a call after Close.
	t.Run("the overflow verdict is reported however often it is asked", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, roomyPool(t))
		for range parkedFrameCap {
			require.NoError(t, q.Send(t.Context(), evt("x")))
		}
		require.ErrorIs(t, q.Send(t.Context(), evt("overflow")), crdt.ErrSubscriberSlow)

		require.True(t, q.Bootstrapped())
		assert.True(t, q.Bootstrapped(), "the drop does not stop having happened")
		q.Close()
		assert.True(t, q.Bootstrapped(), "nor does closing make the connection whole")
	})

	// Closing straight out of the parking phase -- the handler returning before
	// the opening frame was built -- must not land back in an earlier phase.
	t.Run("closing during parking skips live", func(t *testing.T) {
		t.Parallel()
		q := newTestQueue(t, newTestPool(t, 4<<20, 1<<20))

		require.NoError(t, q.Send(t.Context(), frameOfSize(t, 4<<10)))
		q.Close()
		assert.Equal(t, queueClosed, q.phase)

		// Rebaseline's guard is phase-scoped, so a closed queue is left alone
		// rather than having its already-drained buffers discarded again.
		q.Rebaseline()
		assert.Equal(t, queueClosed, q.phase)
	})

	// The bootstrap slot is NOT a phase: it holds the frame the refund needs and
	// empties at BootstrapSent, which happens while the queue is still live.
	t.Run("the bootstrap slot empties before the queue closes", func(t *testing.T) {
		t.Parallel()
		pool := newTestPool(t, 4<<20, 1<<20)
		q := newTestQueue(t, pool)

		require.Equal(t, bootstrapAdmitted, q.AdmitBootstrap(frameOfSize(t, 64<<10)))
		q.Bootstrapped()
		require.Positive(t, pool.Used(), "the opening frame is charged while it is on the wire")

		q.BootstrapSent()
		assert.Equal(t, queueLive, q.phase, "handing the frame over is not a phase change")
		assert.Nil(t, q.bootstrap)

		q.Close()
		assert.Zero(t, pool.Used(), "and the charge is gone either way")
	})
}
