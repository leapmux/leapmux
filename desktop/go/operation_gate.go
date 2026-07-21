package main

import (
	"sync"
	"time"
)

// operationGate admits side-effecting operations while the sidecar is live and
// drains them, bounded, at shutdown.
//
// It exists so the admit/close/drain protocol is one cohesive type rather than
// three App fields whose ordering invariant lived only in prose: close() flips
// the gate and runs the caller's cancel INSIDE the same critical section that
// begin() checks, so an operation can never be admitted after cancellation --
// previously App set a `shuttingDown` flag under the lock and then cancelled
// outside it, leaving the two to be kept in step by hand.
//
// The closed flag is not derivable from the lifetime context: a bare-struct App
// with no context is a supported state (focused tests construct one), so the gate
// owns the flag rather than reading ctx.Err().
type operationGate struct {
	mu     sync.Mutex
	wc     waitCounter
	closed bool
}

// begin admits an operation, returning the func that marks it done. It reports
// false once the gate is closed, so the caller rejects the operation.
//
// The returned func is safe to call any number of times: it routes through a
// per-admission sync.Once so a caller that defers done() AND calls it on a
// manual cleanup path (or any other double-invocation) cannot drive the
// waitCounter negative and panic the sidecar. sync.Once also makes the
// "exactly-once" contract mechanical rather than a caller-discipline rule,
// matching the loudness sync.Mutex already chooses for unlock-of-unlocked.
func (g *operationGate) begin() (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, false
	}
	g.wc.add()
	var once sync.Once
	done := func() { once.Do(g.wc.done) }
	return done, true
}

// close shuts the gate and, still holding the admission lock, runs cancel. Doing
// both under one lock is the point: it makes "admitted implies not yet cancelled"
// mechanical instead of an ordering a future edit could invert. cancel may be nil.
func (g *operationGate) close(cancel func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	if cancel != nil {
		cancel()
	}
}

// drain waits (bounded by timeout) for admitted operations to finish so their
// side effects complete before shared state is torn down. A straggler that
// ignores cancellation -- a non-cancellable editor launch or filesystem scan --
// is abandoned after the timeout and reclaimed at process exit.
//
// waitCounter's no-add-after-sample contract holds here by ordering: the one
// production caller (App.Shutdown) runs close() before drain, and close flips
// closed under the same lock begin checks, so once wait samples the counter no
// operation can be admitted.
func (g *operationGate) drain(timeout time.Duration, warnMsg string) {
	g.wc.wait(timeout, warnMsg)
}
