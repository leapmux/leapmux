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
// (drainRelay, after wsRelay.detach). Each had hand-rolled the same spawn-waiter/stoppable-timer/
// select/warn dance, and their comments already cross-referenced one another as
// mirrors; one helper means the contract they share -- a promptly-finished waiter
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

// waitGroupDone returns a channel that closes once wg's counter reaches zero, so
// a WaitGroup can be handed to waitBounded.
//
// The spawned waiter parks on wg.Wait() until the counter hits zero. If a
// handler/operation never returns (a non-cancellable exec or filesystem scan),
// that waiter leaks -- but only doubling an already-unavoidable leak, since Go
// cannot kill the stuck goroutine and wg.Wait() cannot be select'd on, and the
// waiter exits cleanly the moment the straggler ever returns. Replacing the
// WaitGroups with a cancellable counter to remove it is tracked (with the
// recommendation to leave it) in https://github.com/leapmux/leapmux/issues/297.
func waitGroupDone(wg *sync.WaitGroup) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	return done
}
