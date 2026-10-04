package providerkit

import (
	"sync"
	"time"

	"github.com/coder/quartz"
)

// ControlDeadlineTag tags the timer of each control deadline, so a test can trap it.
const ControlDeadlineTag = "control-deadline"

// ControlDeadlines withdraws a control request when the deadline that its provider
// stated passes.
//
// Pi and Oh My Pi answer a dialog that times out themselves, with the dialog's
// default, and tell the host nothing. Without a deadline of its own, the reader's
// card outlives the question: it states a deadline that already passed, and an
// answer then reaches a provider that stopped waiting. A provider arms a deadline
// when it publishes such a dialog, disarms it when the reader's answer goes out,
// and stops every deadline when its process ends.
//
// The zero value is ready to use.
type ControlDeadlines struct {
	mu     sync.Mutex
	timers map[string]*quartz.Timer
	// stopped refuses every later Arm, so no deadline outlives the process.
	stopped bool
}

// Arm starts the deadline of one request. expire runs on the clock's goroutine
// when the deadline passes and the request is still armed. It runs with no lock of
// this type held, so it may call Disarm or Arm itself.
//
// A deadline of zero or less states no deadline, so it arms nothing. A request that
// is armed already takes the new deadline, because the provider stated it again.
func (d *ControlDeadlines) Arm(clock quartz.Clock, requestID string, after time.Duration, expire func()) {
	if after <= 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	if previous := d.timers[requestID]; previous != nil {
		previous.Stop()
	}
	if d.timers == nil {
		d.timers = make(map[string]*quartz.Timer)
	}
	var timer *quartz.Timer
	timer = clock.AfterFunc(after, func() {
		// The callback reads timer under the lock that this Arm holds until the
		// assignment below, so it never reads the variable before it is set. A
		// disarm, a later Arm, or StopAll that ran first keeps the request from
		// expiring.
		d.mu.Lock()
		armed := !d.stopped && d.timers[requestID] == timer
		if armed {
			delete(d.timers, requestID)
		}
		d.mu.Unlock()
		if armed {
			expire()
		}
	}, ControlDeadlineTag)
	d.timers[requestID] = timer
}

// Disarm stops the deadline of one request, and reports whether one was armed.
func (d *ControlDeadlines) Disarm(requestID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	timer := d.timers[requestID]
	if timer == nil {
		return false
	}
	timer.Stop()
	delete(d.timers, requestID)
	return true
}

// StopAll stops every deadline, and arms none after it. Call it when the process
// ends: a request of a process that ended is withdrawn by the process's own path.
func (d *ControlDeadlines) StopAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	for _, timer := range d.timers {
		timer.Stop()
	}
	d.timers = nil
}
