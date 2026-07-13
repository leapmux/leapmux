package tunnel

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// deadlineState holds one direction's read/write deadline for a Conn and turns
// it into a context (see the `context` method): setting a deadline while an
// operation is parked re-arms the watcher through `changed`, so a deadline set
// AFTER a Write parked on a full window still wakes it. Extracted from conn.go
// so the timer-arming arithmetic is readable beside its own tests rather than
// inside the net.Conn state machine.
type deadlineState struct {
	mu       sync.Mutex
	deadline time.Time
	changed  chan struct{}
}

func newDeadlineState() deadlineState {
	return deadlineState{changed: make(chan struct{})}
}

func (d *deadlineState) set(deadline time.Time) {
	d.mu.Lock()
	d.deadline = deadline
	close(d.changed)
	d.changed = make(chan struct{})
	d.mu.Unlock()
}

func (d *deadlineState) snapshot() (time.Time, <-chan struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.deadline, d.changed
}

// armTimer snapshots the current deadline and returns a timer firing at it (nil
// when no deadline is set), the `changed` channel that observes a concurrent
// set, and whether the deadline has ALREADY passed. The caller must Stop a
// non-nil timer. It is the single home of the "no deadline means no timer, an
// already-passed deadline expires immediately, otherwise arm for the remaining
// duration" arithmetic that both Conn.Read's wait loop and the watcher in
// context() consume -- the two copies had already drifted, which is how a Write
// parked on a full send window came to miss a later SetWriteDeadline.
func (d *deadlineState) armTimer() (timer *time.Timer, timerCh <-chan time.Time, changed <-chan struct{}, expired bool) {
	deadline, changed := d.snapshot()
	if deadline.IsZero() {
		return nil, nil, changed, false
	}
	duration := time.Until(deadline)
	if duration <= 0 {
		return nil, nil, changed, true
	}
	timer = time.NewTimer(duration)
	return timer, timer.C, changed, false
}

// stopTimer stops timer if armTimer returned one. armTimer returns a nil timer
// when no deadline is set, and (*time.Timer).Stop panics on nil, so every wait
// loop that arms a deadline timer needs this guard on every exit -- naming it
// once keeps the seven call sites from each re-deriving (and one day forgetting)
// the nil case.
func stopTimer(timer *time.Timer) {
	if timer != nil {
		timer.Stop()
	}
}

// context derives a context from parent that is cancelled when this deadline
// expires, and returns a flag reporting whether cancellation was the deadline
// (rather than parent). The flag is always non-nil, so call sites need no
// nil-check to Load it.
//
// The watcher runs even when no deadline is currently set: net.Conn's contract
// is that a deadline "applies to all future and pending I/O", so a Write already
// parked on a full send window MUST observe a SetWriteDeadline issued after it
// parked. Re-selecting on `changed` is the only way to see that, and skipping
// the watcher on a no-deadline fast path silently broke the standard
// SetWriteDeadline(time.Now()) abort idiom. The extra goroutine per call is
// immaterial next to a frame's own ~32 KiB marshal, AEAD encryption, and
// serialized WebSocket write.
func (d *deadlineState) context(parent context.Context) (context.Context, context.CancelFunc, *atomic.Bool) {
	exceeded := &atomic.Bool{}
	ctx, cancel := context.WithCancel(parent)
	go func() {
		for {
			timer, timerCh, changed, expired := d.armTimer()
			if expired {
				exceeded.Store(true)
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				stopTimer(timer)
				return
			case <-changed:
				stopTimer(timer)
			case <-timerCh:
				exceeded.Store(true)
				cancel()
				return
			}
		}
	}()
	return ctx, cancel, exceeded
}
