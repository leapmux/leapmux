package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/tunnelflow"
)

// An advance must wake ONLY the seq whose turn arrived, not the whole window.
//
// This is the property the gate exists for: a conn's window admits up to
// WriteWindowFrames parked writers, but exactly one can proceed per advance. Waking
// all of them to have every one but a single goroutine immediately re-sleep costs a
// mutex round trip per waiter, once per ~32 KiB chunk, at the chunk rate a saturated
// tunnel sustains. A test that only checked "the right one proceeds" would pass just
// as well against a broadcast, so this counts the wake-ups instead.
func TestWriteSeqGateAdvanceWakesOnlyTheNextSeq(t *testing.T) {
	g := newWriteSeqGate()

	const waiters = 8
	var woke atomic.Int64
	var proceeded atomic.Int64
	var wg sync.WaitGroup

	// Park seqs 1..waiters. Seq 0 is the gate's current position, so none may run.
	for s := uint64(1); s <= waiters; s++ {
		wg.Add(1)
		go func(seq uint64) {
			defer wg.Done()
			woke.Add(1)
			if g.waitTurn(seq) == writeTurnProceed {
				proceeded.Add(1)
			}
		}(s)
	}

	// Wait until every writer is parked, so the advance below is a real handoff and
	// not a race against goroutine startup.
	require.Eventually(t, func() bool { return woke.Load() == waiters },
		2*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return g.waiterCount() == waiters },
		2*time.Second, time.Millisecond, "every writer must be parked before the handoff")

	// One advance releases exactly one writer.
	g.advance()
	require.Eventually(t, func() bool { return proceeded.Load() == 1 },
		2*time.Second, time.Millisecond, "the seq whose turn arrived must proceed")
	assert.Equal(t, waiters-1, g.waiterCount(),
		"an advance must consume exactly one waiter, leaving the rest parked -- a broadcast would wake all of them")

	// Drain the rest so the goroutines exit.
	g.close()
	wg.Wait()
}

// A seq no correct client could have sent is rejected rather than parked: waiting
// could never resolve it, and parking would hold a handler goroutine forever.
func TestWriteSeqGateRejectsIllegitimateSeq(t *testing.T) {
	g := newWriteSeqGate()

	assert.Equal(t, writeTurnRejected, g.waitTurn(tunnelflow.MaxWriteSeqLookahead+1),
		"a seq beyond the client's window cannot be legitimate and must not park")

	g.advance() // position = 1
	assert.Equal(t, writeTurnRejected, g.waitTurn(0),
		"a seq behind the gate is a replay of an applied frame and must not park")
	assert.Equal(t, writeTurnProceed, g.waitTurn(1), "the current position proceeds immediately")
}

// The turn is CLAIMED atomically with the check, so a duplicate of a turn a writer
// already holds is rejected rather than admitted alongside it.
//
// waitTurn returning true and advance incrementing next are separate critical
// sections with the write between them. Without the claim, two SendTunnelData frames
// carrying the same seq -- a duplicate a buggy or hostile owner can send, since each
// frame is its own AEAD-nonced channel message -- would both observe next == seq and
// both proceed; the paired advances would then jump next by two, skip the real next
// seq, and reject the honest frame that holds it as a replay (NAKed into
// net.ErrClosed), wedging a healthy conn.
func TestWriteSeqGateRejectsConcurrentDuplicateSeq(t *testing.T) {
	g := newWriteSeqGate()

	require.Equal(t, writeTurnProceed, g.waitTurn(0), "the current position claims its turn and proceeds")
	assert.Equal(t, writeTurnRejected, g.waitTurn(0),
		"a duplicate of the turn already claimed must be rejected, not admitted alongside it")

	// advance releases the claim and makes exactly the NEXT seq runnable -- not
	// seq+2 -- so the honest following frame is not skipped.
	g.advance()
	assert.Equal(t, writeTurnProceed, g.waitTurn(1), "advance releases exactly the next turn")
	assert.Equal(t, writeTurnRejected, g.waitTurn(1), "and that turn, once claimed, also rejects a duplicate")
}

// Under a real race -- many goroutines dispatched with the SAME seq at once -- exactly
// one may claim the turn; the rest are rejected. This is the concurrent form of the
// duplicate-seq defense: with -race it fails against a check-without-claim gate, where
// every goroutine that observes next == seq proceeds.
func TestWriteSeqGateConcurrentDuplicatesAdmitExactlyOne(t *testing.T) {
	g := newWriteSeqGate()

	const dupes = 16
	var proceeded atomic.Int64
	var wg sync.WaitGroup
	for range dupes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// seq == next for all, so each either claims the turn or is rejected as a
			// duplicate -- none park, so this cannot deadlock.
			if g.waitTurn(0) == writeTurnProceed {
				proceeded.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(1), proceeded.Load(),
		"exactly one of many same-seq frames may claim the turn; the rest are rejected")
}

// close must wake every parked waiter so teardown strands nothing.
func TestWriteSeqGateCloseWakesEveryWaiter(t *testing.T) {
	g := newWriteSeqGate()

	const waiters = 4
	results := make(chan writeTurn, waiters)
	for s := uint64(1); s <= waiters; s++ {
		go func(seq uint64) { results <- g.waitTurn(seq) }(s)
	}
	require.Eventually(t, func() bool { return g.waiterCount() == waiters },
		2*time.Second, time.Millisecond)

	g.close()
	for range waiters {
		select {
		case turn := <-results:
			assert.Equal(t, writeTurnClosed, turn, "a waiter woken by close must report the gate closed, not claim its turn")
		case <-time.After(2 * time.Second):
			t.Fatal("close did not wake a parked waiter")
		}
	}
	assert.Zero(t, g.waiterCount(), "close must release every waiter channel")
	// Idempotent: a second close must not panic on already-closed channels.
	assert.NotPanics(t, g.close)
}

// waitReached is the graceful close's wait: it wants "every prior write applied",
// not an exact turn, so a threshold at or behind the gate proceeds at once.
func TestWriteSeqGateWaitReached(t *testing.T) {
	g := newWriteSeqGate()
	g.advance()
	g.advance() // position = 2

	done := make(chan struct{})
	go func() { defer close(done); g.waitReached(context.Background(), 2) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a threshold already reached must not block")
	}

	// A threshold ahead of the gate blocks until the gate reaches it.
	blocked := make(chan struct{})
	go func() { defer close(blocked); g.waitReached(context.Background(), 3) }()
	select {
	case <-blocked:
		t.Fatal("waitReached returned before the gate reached its seq")
	case <-time.After(50 * time.Millisecond):
	}
	g.advance() // position = 3
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("waitReached did not return once the gate reached its seq")
	}
}

// ctx bounds the graceful close's wait, so a stalled target (a stuck lower-seq
// write) cannot wedge it forever.
func TestWriteSeqGateWaitReachedHonoursContext(t *testing.T) {
	g := newWriteSeqGate()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { defer close(done); g.waitReached(ctx, 99) }()
	select {
	case <-done:
		t.Fatal("waitReached returned before its seq or its context")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled context must release waitReached")
	}
}

// A marker is expirable only by its OWN deadline.
//
// This is the subtlest rule in the tunnel manager, and the reason cancelMarkers is
// its own type: a conn_id marked, cleared early by an open finishing, then re-marked
// leaves the FIRST entry orphaned in the expiry slice. A sweep keyed on conn_id
// alone would let that orphan drop the second, LIVE marker up to a full TTL early --
// un-fencing a close that must still fence a racing open. Testing it here needs no
// conns, dials or sockets.
func TestCancelMarkersOrphanEntryCannotDropALiveReMark(t *testing.T) {
	c := newCancelMarkers()
	base := time.Now()

	// Marked, then cleared early by an open finishing -- its entry is now orphaned.
	c.mark("conn-1", base.Add(10*time.Millisecond))
	c.clear("conn-1")

	// Re-marked with a LATER deadline; the orphan entry expires first.
	c.mark("conn-1", base.Add(time.Hour))

	// Sweep past the orphan's deadline but not the live marker's.
	c.sweep(base.Add(time.Second))

	assert.True(t, c.has("conn-1"),
		"an orphaned entry must not drop the live marker that replaced it")
}

// The ordinary case: a marker past its own deadline is swept.
func TestCancelMarkersSweepsExpired(t *testing.T) {
	c := newCancelMarkers()
	base := time.Now()

	c.mark("conn-1", base.Add(10*time.Millisecond))
	c.sweep(base) // before the deadline
	require.True(t, c.has("conn-1"), "a marker before its deadline must survive")

	c.sweep(base.Add(time.Second)) // past it
	assert.False(t, c.has("conn-1"), "a marker past its own deadline must be swept")
	assert.Zero(t, c.len())
}

// A marker racing an in-flight open (markDuringOpen) gets no sweep entry: the
// store/abortOpen that ends the open clears it. Sweeping it on a deadline would
// un-fence a close whose open is still dialing.
func TestCancelMarkersMarkDuringOpenIsNotSwept(t *testing.T) {
	c := newCancelMarkers()
	base := time.Now()

	c.markDuringOpen("conn-1", base.Add(10*time.Millisecond))
	c.sweep(base.Add(time.Hour))

	assert.True(t, c.has("conn-1"),
		"a marker racing an open has no deadline entry and must not be swept")
	assert.True(t, c.take("conn-1"), "it is consumed by the open that ends, not by the sweep")
	assert.False(t, c.has("conn-1"))
}

// take consumes; has does not.
func TestCancelMarkersTakeConsumes(t *testing.T) {
	c := newCancelMarkers()
	assert.False(t, c.take("absent"), "an unmarked conn is not taken")

	c.markDuringOpen("conn-1", time.Now().Add(time.Hour))
	assert.True(t, c.has("conn-1"))
	assert.True(t, c.has("conn-1"), "has must not consume")
	assert.True(t, c.take("conn-1"))
	assert.False(t, c.take("conn-1"), "take must consume the marker")
}

// sweep must not leave the popped prefix reachable behind the resliced header:
// each cancelMarkerEntry holds a string conn_id header that keeps the caller's
// string bytes alive, and under connection churn a long-lived tunnelManager
// appends one entry per closed-before-open conn for the worker process's whole
// life -- without compaction the backing array grows to peak churn size and
// never frees.
func TestCancelMarkersSweepReleasesPoppedBackingArray(t *testing.T) {
	c := newCancelMarkers()
	base := time.Now()

	// Expire the dropped marker first so it lands ahead of the kept one in the
	// deadline-ordered expiry slice (callers append with a constant TTL, so the
	// slice is append-ordered by deadline; the sweep pops the expired prefix).
	c.mark("conn-drop", base.Add(10*time.Millisecond))
	c.mark("conn-keep", base.Add(time.Hour))
	require.Equal(t, 2, len(c.expiry))
	capBefore := cap(c.expiry)

	c.sweep(base.Add(time.Second))

	assert.False(t, c.has("conn-drop"), "the expired marker must be swept")
	assert.True(t, c.has("conn-keep"), "the live marker must survive")
	assert.Equal(t, 1, len(c.expiry), "the expiry slice shrinks by the swept count")

	// Every backing slot beyond the live tail must be nil, so the dead entries'
	// pointers (and the struct + conn_id string each pins) are GC-eligible. A
	// reslice-only pop (c.expiry = c.expiry[i:]) leaves the dropped pointer still
	// reachable at the same backing offset -- the leak this test pins against.
	full := c.expiry[:capBefore:capBefore]
	for i := len(c.expiry); i < capBefore; i++ {
		assert.Nil(t, full[i],
			"backing slot %d beyond the live tail must be nil so the popped entry is GC-eligible", i)
	}
}
