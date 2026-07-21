package main

import (
	"log/slog"
	"sync"
	"time"
)

// waitBounded waits for done, giving up after timeout, and reports whether done
// closed in time. When it gives up it logs warnMsg (omitted when empty).
//
// Every bounded drain in the sidecar routes through here -- the operation drain
// (App.drainOperations), the RPC handler drains before and after the writer is
// interrupted (RPCSession.drainHandlers), and the relay read-loop drain
// (drainRelay, after wsRelay.detach). Each had hand-rolled the same
// stoppable-timer/select/warn dance, and their comments already cross-referenced
// one another as mirrors; one helper means the contract they share -- a promptly-finished waiter
// never leaves a live timer on the runtime heap, and a straggler is abandoned
// rather than allowed to wedge teardown -- cannot drift between them.
//
// The timer is stopped rather than left to fire (time.After would pin a timeout
// worth of heap per call), which matters on the drains that run per session.
func waitBounded(done <-chan struct{}, timeout time.Duration, warnMsg string) bool {
	// An already-finished waiter wins outright, even on an exhausted budget. Callers
	// that share one deadline across sequential phases (RPCSession.drainHandlers) can
	// pass a non-positive timeout, and select would then pick between two ready cases
	// at random -- reporting a straggler that had in fact finished.
	select {
	case <-done:
		return true
	default:
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		if warnMsg != "" {
			slog.Warn(warnMsg)
		}
		return false
	}
}

// waitCounter counts in-flight work like a sync.WaitGroup, but exposes
// completion as a channel so a drain can wait with a deadline. sync.WaitGroup
// was unusable here: Wait() cannot be select'ed on, so handing it to
// waitBounded required a spawned waiter goroutine that leaked (parked forever
// on Wait) whenever a permanently-stuck straggler was abandoned -- issue #297.
type waitCounter struct {
	mu   sync.Mutex
	n    int
	zero chan struct{} // created lazily by doneChan while n > 0; closed and nil'd when n reaches 0
}

func (c *waitCounter) add() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *waitCounter) done() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == 0 {
		// Panic BEFORE mutating (unlike sync.WaitGroup, which decrements first):
		// if a future recover-middleware swallows this panic, the counter is still
		// consistent. Decrement-then-panic would leave n at -1, where the next
		// add() lands on 0 and a concurrent drain's doneChan would report an idle
		// counter while that operation is live.
		panic("waitCounter: negative counter")
	}
	c.n--
	if c.n == 0 && c.zero != nil {
		close(c.zero)
		c.zero = nil
	}
}

// doneChan reports the counter's next zero-crossing as of the call. Adds after
// that crossing do not reopen the returned channel -- a new cycle needs a fresh
// doneChan call -- so a caller must not let an add() slip in between sampling
// and the wait the sample is meant to cover. (sync.WaitGroup's best-effort
// misuse check would usually have panicked on "Add called concurrently with
// Wait" -- it fires only when Add catches a parked waiter at that instant;
// waitCounter silently legalizes the race outright, so each drain call site
// documents why its ordering upholds the contract.)
func (c *waitCounter) doneChan() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == 0 {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	if c.zero == nil {
		c.zero = make(chan struct{})
	}
	return c.zero
}

// wait blocks, bounded by timeout, for the zero-crossing current as of this
// call -- it samples doneChan itself, so the caller never holds the raw
// channel -- and reports whether the counter hit zero in time. On timeout it
// logs warnMsg (omitted when empty) and gives up; the straggling work is the
// caller's to abandon.
func (c *waitCounter) wait(timeout time.Duration, warnMsg string) bool {
	return waitBounded(c.doneChan(), timeout, warnMsg)
}
