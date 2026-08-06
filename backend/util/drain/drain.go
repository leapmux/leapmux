// Package drain provides the bounded-wait primitives shared by every teardown
// that must abandon a straggler rather than let it wedge the process.
//
// It lives here, rather than in either consumer, because the desktop sidecar and
// backend/solo had independently reached OPPOSITE conclusions about the same
// hazard: the sidecar retired the spawned-waiter shape as a goroutine leak
// (issue #297) and built Counter to replace it, while solo reintroduced the
// spawned waiter and argued it away in a comment. One implementation is what
// keeps that from happening a third time.
package drain

import (
	"log/slog"
	"sync"
	"time"
)

// WaitBounded waits for done, giving up after timeout, and reports whether done
// closed in time. When it gives up it logs warnMsg (omitted when empty).
//
// Every bounded drain routes through here -- the sidecar's operation drain
// (App.drainOperations), the RPC handler drains before and after the writer is
// interrupted (RPCSession.drainHandlers), the relay read-loop drain (drainRelay,
// after wsRelay.detach), and solo's Worker drain before the Hub stops. Each had
// hand-rolled the same stoppable-timer/select/warn dance, and their comments
// already cross-referenced one another as mirrors; one helper means the contract
// they share -- a promptly-finished waiter never leaves a live timer on the
// runtime heap, and a straggler is abandoned rather than allowed to wedge
// teardown -- cannot drift between them.
//
// The timer is stopped rather than left to fire (time.After would pin a timeout
// worth of heap per call), which matters on the drains that run per session.
func WaitBounded(done <-chan struct{}, timeout time.Duration, warnMsg string) bool {
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

// Counter counts in-flight work like a sync.WaitGroup, but exposes completion as
// a channel so a drain can wait with a deadline. sync.WaitGroup was unusable
// here: Wait() cannot be select'ed on, so handing it to WaitBounded required a
// spawned waiter goroutine that leaked (parked forever on Wait) whenever a
// permanently-stuck straggler was abandoned -- issue #297.
type Counter struct {
	mu   sync.Mutex
	n    int
	zero chan struct{} // created lazily by DoneChan while n > 0; closed and nil'd when n reaches 0
}

// Add registers one unit of in-flight work.
func (c *Counter) Add() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

// Done retires one unit of in-flight work, releasing any waiter when the last
// one lands.
func (c *Counter) Done() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == 0 {
		// Panic BEFORE mutating (unlike sync.WaitGroup, which decrements first):
		// if a future recover-middleware swallows this panic, the counter is still
		// consistent. Decrement-then-panic would leave n at -1, where the next
		// Add() lands on 0 and a concurrent drain's DoneChan would report an idle
		// counter while that operation is live.
		panic("drain.Counter: negative counter")
	}
	c.n--
	if c.n == 0 && c.zero != nil {
		close(c.zero)
		c.zero = nil
	}
}

// DoneChan reports the counter's next zero-crossing as of the call. Adds after
// that crossing do not reopen the returned channel -- a new cycle needs a fresh
// DoneChan call -- so a caller must not let an Add() slip in between sampling
// and the wait the sample is meant to cover. (sync.WaitGroup's best-effort
// misuse check would usually have panicked on "Add called concurrently with
// Wait" -- it fires only when Add catches a parked waiter at that instant;
// Counter silently legalizes the race outright, so each drain call site
// documents why its ordering upholds the contract.)
func (c *Counter) DoneChan() <-chan struct{} {
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

// Pending reports whether a DoneChan handed out for the current cycle is still
// open -- that is, whether some drain is holding a live completion signal.
//
// It exists for tests that must prove a drain has observably PARKED before
// releasing the work it waits on: a bare sleep there loses the race, because
// Done() would cross zero first and the drain would take DoneChan's pre-closed
// fast path, passing without ever exercising the mid-wait join.
func (c *Counter) Pending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.zero != nil
}

// Wait blocks, bounded by timeout, for the zero-crossing current as of this
// call -- it samples DoneChan itself, so the caller never holds the raw
// channel -- and reports whether the counter hit zero in time. On timeout it
// logs warnMsg (omitted when empty) and gives up; the straggling work is the
// caller's to abandon.
func (c *Counter) Wait(timeout time.Duration, warnMsg string) bool {
	return WaitBounded(c.DoneChan(), timeout, warnMsg)
}
