package drain

import (
	"sync"
	"time"
)

// Gate admits operations while its owner is live and drains them, up to a
// limit, at shutdown.
//
// It holds the admit/close/drain protocol as one cohesive type rather than
// three fields whose ordering invariant lived only in prose: Close flips the
// gate and runs the caller's cancel INSIDE the same critical section that
// Begin checks, so an operation can never be admitted after cancellation --
// a shape that once lived as a `shuttingDown` flag set under the lock and a
// cancel outside it, kept in step by hand. The desktop sidecar's App.ops is
// the original holder; the worker's channel dispatcher and the Hub's
// hijacked-WebSocket tracker share it through this package so the invariant
// cannot drift between them.
//
// The closed flag is not derivable from a lifetime context: a bare-struct
// owner with no context is a supported state (focused tests construct one),
// so the gate owns the flag rather than reading ctx.Err().
type Gate struct {
	mu     sync.Mutex
	wc     Counter
	closed bool
}

// Begin admits an operation, returning the func that marks it done. It reports
// false once the gate is closed, so the caller rejects the operation. onAdmit,
// when non-nil, runs under the admission lock on an admitted call -- the seam
// for a tracked registration that must be atomic with the admit (a counter Add
// no later Close can miss).
//
// The returned func is safe to call any number of times: it routes through a
// per-admission sync.Once so a caller that defers done() AND calls it on a
// manual cleanup path (or any other double-invocation) cannot drive the
// counter negative and panic. sync.Once also makes the "exactly-once" contract
// mechanical rather than a caller-discipline rule, matching the loudness
// sync.Mutex already chooses for unlock-of-unlocked.
func (g *Gate) Begin(onAdmit func()) (done func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, false
	}
	g.wc.Add()
	if onAdmit != nil {
		onAdmit()
	}
	var once sync.Once
	done = func() { once.Do(g.wc.Done) }
	return done, true
}

// Close shuts the gate and, still holding the admission lock, runs cancel.
// Doing both under one lock is the point: it makes "admitted implies not yet
// cancelled" mechanical instead of an ordering a future edit could invert.
// cancel may be nil.
func (g *Gate) Close(cancel func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	if cancel != nil {
		cancel()
	}
}

// Drain waits (limited by timeout) for admitted operations to finish so their
// side effects complete before shared state is torn down, and reports whether
// they all did. A straggler that ignores cancellation -- a non-cancellable
// editor launch or filesystem scan -- is abandoned after the timeout and
// reclaimed at process exit.
//
// Counter's no-add-after-sample contract holds by ordering: the caller runs
// Close before Drain, and Close flips closed under the same lock Begin
// checks, so once Drain samples the counter no operation can be admitted.
func (g *Gate) Drain(timeout time.Duration, warnMsg string) bool {
	return g.wc.Wait(timeout, warnMsg)
}
