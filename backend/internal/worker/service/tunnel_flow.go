package service

import (
	"context"
	"sync"

	"github.com/leapmux/leapmux/internal/tunnelflow"
)

// writeSeqGate serializes a tunnel conn's writes into the order the client sent
// them.
//
// The worker dispatches each SendTunnelData on its own goroutine, so without a gate
// a conn's data and close_write frames would race and reach the target out of order.
// A frame with sequence S waits until it is S's turn, applies its write, then
// advances -- releasing S+1.
//
// It wakes exactly the goroutine whose turn arrived, not every parked writer. That
// is not a micro-optimisation: only ONE waiter can ever proceed per advance (the seq
// that equals the new position), and a conn's window admits up to WriteWindowFrames
// of them, so a broadcast wakes the whole window to have all but one immediately
// re-sleep -- once per ~32 KiB chunk, at the sustained chunk rate a saturated tunnel
// runs at. sync.Cond cannot express this: Signal wakes an ARBITRARY waiter, which
// here is almost never the next seq, and that waiter would re-sleep without
// advancing anything -- stalling the queue. A channel per awaited seq lets advance
// close exactly the one that became runnable.
type writeSeqGate struct {
	mu   sync.Mutex
	next uint64
	// claimed marks the current turn (next) as already handed to a writer.
	// waitTurn and advance are separate critical sections -- the write runs between
	// them -- so the check "is it my turn" and the "take it" must be one atomic
	// step, or two goroutines dispatched with the SAME seq both observe next == seq
	// and both proceed. See waitTurn.
	claimed bool
	// waiters maps an awaited seq to a channel closed when next REACHES it. Both
	// kinds of waiter key on the same thing -- waitTurn(S) wants next == S and
	// waitReached(S) wants next >= S, and next only ever advances by one, so both
	// become runnable exactly when next hits S. Sharing one channel per seq means a
	// close wakes both.
	waiters map[uint64]chan struct{}
	closed  bool
}

func newWriteSeqGate() *writeSeqGate {
	return &writeSeqGate{waiters: make(map[uint64]chan struct{})}
}

// waiterLocked returns the channel closed when next reaches seq, creating it on
// first wait. Callers must hold g.mu.
func (g *writeSeqGate) waiterLocked(seq uint64) chan struct{} {
	if ch, ok := g.waiters[seq]; ok {
		return ch
	}
	ch := make(chan struct{})
	g.waiters[seq] = ch
	return ch
}

// waitTurn blocks until it is seq's turn to act on the conn (every lower seq
// applied), then CLAIMS the turn so no other goroutine can take it. Returns false
// if the gate closed first, or if seq is one no correct client could have sent --
// including a duplicate of the turn a concurrent goroutine already claimed.
//
// Claiming the turn under the same lock as the check is what makes a duplicate seq
// safe. waitTurn returning true and advance incrementing next are separate critical
// sections, with the write between them, so a bare next == seq check would let two
// goroutines dispatched with the SAME seq (a duplicate a buggy or hostile owner can
// send -- each frame carries its own AEAD nonce, so the channel's monotonic-nonce
// guard does not collapse them) both observe next == seq and both proceed; each
// then defers an advance, next jumps by two, the real next seq is skipped, and the
// honest frame that holds it is rejected as a replay -- NAKed into net.ErrClosed,
// wedging a healthy conn. The claimed flag routes the duplicate to the reject arm
// instead. advance clears it as it releases the next turn.
//
// Rejecting rather than parking on a turn that will never come: a seq BEHIND the
// gate is a duplicate/replay of an already-applied frame; a seq equal to the
// current position whose turn is already claimed is a concurrent duplicate; a seq
// beyond tunnelflow.MaxWriteSeqLookahead is further ahead than the client's bounded send
// window can legitimately reach. All require a buggy or compromised owner (a
// correct client sends contiguous, distinct seqs within its window), and none can
// be resolved by waiting. The handler NAKs the frame; a later graceful close or the
// lifetime watcher reclaims the conn. A benign out-of-order arrival WITHIN the
// window still parks here and resolves when the lower seq advances the gate.
//
// The outcome is a three-way enum, not a bool, so the handler can tell a client
// protocol violation (writeTurnRejected -> a client NAK) apart from a genuine
// conn-teardown race (writeTurnClosed -> an INTERNAL fault). Collapsing both to
// "false" made every rejected seq surface as a worker-side INTERNAL error and an
// Error-level log line, contradicting this doc's "the handler NAKs the frame".
type writeTurn int

const (
	// writeTurnProceed: it is seq's turn; the caller may write and must advance.
	writeTurnProceed writeTurn = iota
	// writeTurnClosed: the gate was torn down (a genuine conn-teardown race).
	writeTurnClosed
	// writeTurnRejected: a seq no correct client could send (replay, concurrent
	// duplicate, or beyond the lookahead window) -- a client protocol violation.
	writeTurnRejected
)

func (g *writeSeqGate) waitTurn(seq uint64) writeTurn {
	for {
		g.mu.Lock()
		switch {
		case g.closed:
			g.mu.Unlock()
			return writeTurnClosed
		case g.next == seq && !g.claimed:
			g.claimed = true
			g.mu.Unlock()
			return writeTurnProceed
		case seq < g.next || (g.next == seq && g.claimed) || seq-g.next > tunnelflow.MaxWriteSeqLookahead:
			g.mu.Unlock()
			return writeTurnRejected
		}
		ch := g.waiterLocked(seq)
		g.mu.Unlock()
		<-ch
	}
}

// waitReached blocks until at least seq writes have been applied (next >= seq), the
// gate closes, or ctx expires.
//
// Used by the graceful close, whose seq equals the client's total write count: it
// waits for every prior write without requiring an exact-turn match, so a close seq
// at or behind the current position proceeds immediately once the writes it fences
// are done. ctx bounds the wait so a stalled target (a stuck lower-seq write) cannot
// wedge the close forever -- selecting on ctx.Done() directly is what makes that
// true, with no timer to arm and no wakeup to lose.
func (g *writeSeqGate) waitReached(ctx context.Context, seq uint64) {
	for {
		g.mu.Lock()
		if g.closed || g.next >= seq {
			g.mu.Unlock()
			return
		}
		ch := g.waiterLocked(seq)
		g.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return
		}
	}
}

// advance releases the next frame in send order. Called after a write completes --
// on success OR failure -- so a broken write (target gone) never strands the
// following frame's goroutine.
func (g *writeSeqGate) advance() {
	g.mu.Lock()
	g.next++
	// Release the claim with the turn: the seq now at the position (if any) is
	// unclaimed until its own goroutine takes it in waitTurn.
	g.claimed = false
	if ch, ok := g.waiters[g.next]; ok {
		close(ch)
		delete(g.waiters, g.next)
	}
	g.mu.Unlock()
}

// close wakes every waiter so none strands during teardown. Idempotent.
func (g *writeSeqGate) close() {
	g.mu.Lock()
	if !g.closed {
		g.closed = true
		for seq, ch := range g.waiters {
			close(ch)
			delete(g.waiters, seq)
		}
	}
	g.mu.Unlock()
}

// waiterCount reports how many seqs currently have a parked waiter. For tests: it
// is what lets them assert an advance consumes exactly ONE waiter, which is the
// difference between this gate and the broadcast it replaced.
func (g *writeSeqGate) waiterCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.waiters)
}

// creditWindow is a tunnel conn's read-send window: the worker consumes one credit
// per inbound data frame it forwards and blocks at zero, so it never buffers more
// than the client can hold. The conn self-seeds the initial window and the client
// tops it up as it drains (GrantTunnelReadCredit).
//
// A sync.Cond here, unlike writeSeqGate: the read loop is the conn's ONLY consumer,
// so a broadcast wakes exactly one goroutine and there is no herd to avoid.
type creditWindow struct {
	mu     sync.Mutex
	cond   *sync.Cond
	credit int64
	// max is the ceiling a grant may raise credit to -- the window the design
	// targets. See add.
	max    int64
	closed bool
}

func newCreditWindow(initial int64) *creditWindow {
	w := &creditWindow{credit: initial, max: initial}
	w.cond = sync.NewCond(&w.mu)
	return w
}

// acquire blocks until at least one credit is available, then consumes one and
// returns true. Returns false if the window closed first, so the read loop exits
// during teardown instead of stranding on a credit that will never be granted.
func (w *creditWindow) acquire() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for w.credit <= 0 {
		if w.closed {
			return false
		}
		w.cond.Wait()
	}
	w.credit--
	return true
}

// add replenishes credit the client granted and wakes the read loop.
//
// A correct client grants at most 1:1 with what it has drained, so credit never
// legitimately exceeds max (credit == window - in_flight). Clamping to that ceiling
// stops a buggy or hostile tunnel owner from either driving credit negative via an
// overflowing int64(credit) -- which would wedge the read loop forever on a credit
// that never comes -- or pushing the worker past the client's read-buffer capacity
// and starving the shared channel's receive loop. The clamp enforces exactly the
// window the design already targets, so it can never under-credit a well-behaved
// stream.
func (w *creditWindow) add(credit uint64) {
	w.mu.Lock()
	next := w.credit + int64(credit)
	if next < w.credit || next > w.max {
		next = w.max
	}
	w.credit = next
	w.cond.Broadcast()
	w.mu.Unlock()
}

// close wakes the read loop so it does not strand during teardown. Idempotent.
func (w *creditWindow) close() {
	w.mu.Lock()
	w.closed = true
	w.cond.Broadcast()
	w.mu.Unlock()
}

// available reports the current credit. For tests.
func (w *creditWindow) available() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.credit
}
