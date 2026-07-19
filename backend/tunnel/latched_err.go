package tunnel

import (
	"sync"
	"sync/atomic"
)

// errBox wraps an error so it can live in an atomic.Pointer (the error interface
// cannot be stored in one directly).
type errBox struct{ err error }

// latchedErr is a first-wins error latch: the first Set sticks, later ones are
// no-ops, and readers can either poll it (Err) or select on it (Done).
//
// Conn latches two independent terminal conditions -- the inbound stream's
// (terminal) and the worker's write NAK (writeErr) -- with the same first-wins
// semantics, and both readers need both shapes: Read selects on Done and returns
// Err, while sendFrameLocked polls Err on entry (and again once it holds a window
// slot) and selects on Done while parked on a full window. One type serves both,
// so the semantics are defined once instead of being re-derived per latch; a
// reader who learns it once knows both. Err is an atomic load, keeping the Write
// hot path lock-free -- which matters because writeErr is published by
// awaitWriteAck goroutines that must not take the write lock.
//
// Its zero value is a usable, unlatched latch: the done channel is created on first
// use, matching ctxutil.Mutex. A constructor returning it by value instead would copy
// a sync.Once into its destination, and a zero value that looked usable would be a
// landmine -- Set would close a nil channel (panic) and Done would block forever -- so
// the one field that must be initialized is the one nothing can forget.
type latchedErr struct {
	err      atomic.Pointer[errBox]
	doneOnce sync.Once
	done     chan struct{}
	setOnce  sync.Once
}

// doneChan returns the latch's done channel, creating it on first use. sync.Once
// guarantees every caller sees the same channel.
func (l *latchedErr) doneChan() chan struct{} {
	l.doneOnce.Do(func() { l.done = make(chan struct{}) })
	return l.done
}

// Set latches err if nothing is latched yet. Keeping the FIRST error keeps the
// surfaced cause stable when several concurrent failures report the same
// underlying condition (e.g. every in-flight ack NAKing on one broken target).
//
// A nil err is a no-op rather than a terminal state with no cause: closing Done
// while Err()==nil would make every reader selecting on Done observe "completed
// successfully" and return (0, nil), which a Read caller like io.Copy treats as
// "retry" -- a 100%-CPU busy-spin that never yields, EOFs, or errors. Latching
// only real errors keeps that footgun mechanically impossible at the type rather
// than relying on every caller to pre-check.
func (l *latchedErr) Set(err error) {
	if err == nil {
		return
	}
	l.setOnce.Do(func() {
		// Store before closing done: a reader woken by Done is guaranteed to see it.
		l.err.Store(&errBox{err: err})
		close(l.doneChan())
	})
}

// Err returns the latched error, or nil if none is latched.
func (l *latchedErr) Err() error {
	if box := l.err.Load(); box != nil {
		return box.err
	}
	return nil
}

// Done closes once an error is latched.
func (l *latchedErr) Done() <-chan struct{} { return l.doneChan() }
