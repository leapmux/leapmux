package drain

import (
	"log/slog"
	"sync"
	"time"

	"github.com/coder/quartz"
)

// DeadlineTimerTag labels the timer that Deadline.Start arms, so a test that
// drives a mock clock can trap it.
const DeadlineTimerTag = "drain-deadline"

// Deadline is the one moment by which every wait of a shutdown ends.
//
// A shutdown runs its waits in sequence, and each wait has a limit of its own.
// Those limits add up, and a sum that nothing checks can outlast the time that
// launchd and systemd give the process before they send SIGKILL. The steps
// that persist state (database writes, the database close, the runtime record,
// the lock release) run after the waits, so they are what such a sum costs.
// One Deadline per shutdown ends every wait at the same moment, so those steps
// start in time however the waits before them behaved.
//
// Start fixes the moment, and only the first call does. Until then the
// Deadline is not in effect: Cap returns its limit unchanged, and Done stays
// open. A nil *Deadline behaves as one that never starts, so a component that
// a test builds as a bare struct needs none.
type Deadline struct {
	clock  quartz.Clock
	budget time.Duration
	done   chan struct{}

	mu      sync.Mutex
	started bool
	// at is the moment the deadline passes. It is valid once started is set.
	at time.Time
}

// NewDeadline returns a Deadline that passes budget after its first Start, as
// clock measures it.
func NewDeadline(clock quartz.Clock, budget time.Duration) *Deadline {
	return &Deadline{clock: clock, budget: budget, done: make(chan struct{})}
}

// Start begins the countdown. Only the first call has an effect, so every
// component of one shutdown can call it when it observes the shutdown, and
// the earliest observation decides.
func (d *Deadline) Start() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return
	}
	d.started = true
	d.at = d.clock.Now().Add(d.budget)
	d.mu.Unlock()
	if d.budget <= 0 {
		close(d.done)
		return
	}
	// Armed outside the lock: a test that traps this call parks the caller
	// inside it, and Cap must not wait behind that.
	d.clock.AfterFunc(d.budget, func() { close(d.done) }, DeadlineTimerTag)
}

// Done returns a channel that closes when the deadline passes. It stays open
// until Start, and for ever on a nil *Deadline.
func (d *Deadline) Done() <-chan struct{} {
	if d == nil {
		return nil
	}
	return d.done
}

// Cap returns the smaller of limit and the time left before the deadline. It
// never returns a negative duration, so a wait that receives it after the
// deadline returns at once. Before Start it returns limit.
//
// The result is fixed when Cap returns, so a wait that it limits does not end
// early if Start runs later. A wait that has no limit of its own uses Wait,
// which follows a later Start.
func (d *Deadline) Cap(limit time.Duration) time.Duration {
	limit = max(limit, 0)
	if d == nil {
		return limit
	}
	d.mu.Lock()
	started, at := d.started, d.at
	d.mu.Unlock()
	if !started {
		return limit
	}
	return min(limit, max(at.Sub(d.clock.Now()), 0))
}

// Wait waits for done to close, but not past the deadline, and reports whether
// done closed. When the deadline ends the wait, Wait logs warnMsg (omitted
// when empty), and the caller abandons the work that done stands for.
//
// It is for a wait that has no limit of its own. A wait that starts before
// Start still ends at the deadline, because it watches Done. A done that is
// already closed wins, even after the deadline: the work finished, and
// reporting it as abandoned would be false.
func (d *Deadline) Wait(done <-chan struct{}, warnMsg string) bool {
	select {
	case <-done:
		return true
	default:
	}
	select {
	case <-done:
		return true
	case <-d.Done():
		if warnMsg != "" {
			slog.Warn(warnMsg)
		}
		return false
	}
}
