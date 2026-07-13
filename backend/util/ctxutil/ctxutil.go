// Package ctxutil holds small context helpers shared across the hub, the
// revocation watcher, and the desktop sidecar.
package ctxutil

import (
	"context"
	"sync"
)

// WithLinkedCancel derives a cancellable context from parent that is ALSO
// cancelled when other ends, and returns a cancel that detaches the link. A nil
// parent defaults to context.Background; a nil other (or one that never
// cancels) is simply not linked, so the result behaves like context.WithCancel.
//
// It is the single definition of the "derive a context cancelled when either of
// two contexts ends" idiom the desktop sidecar (contextWithCancellation) and
// the revocation watcher (operationContext) both need, so the AfterFunc-stop
// contract -- including the eager cancel when other is already done -- cannot
// drift between them.
func WithLinkedCancel(parent, other context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	if other == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(other, cancel)
	// context.AfterFunc still runs cancel for an already-done other, but in its
	// own goroutine -- asynchronously. Cancel eagerly so the derived context is
	// already cancelled when this returns, preserving the "dies with other"
	// contract synchronously for an already-cancelled other.
	if other.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stop()
		cancel()
	}
}

// Mutex is a mutual-exclusion lock whose acquire can be bounded by a context.
//
// It exists because sync.Mutex cannot be acquired under a select: bolting
// cancellation onto one requires a helper goroutine per contended acquire, and
// on cancellation that goroutine is left to acquire the lock and immediately
// release it -- briefly handing the lock to a waiter nobody owns, and outliving
// the caller until whatever held it lets go. A channel acquire IS a select, so
// cancellation needs no helper, nothing is ever left holding the lock, and
// waiters are served FIFO.
//
// Its zero value is a usable unlocked mutex, matching sync.Mutex: the semaphore
// channel is created on first use, so a zero-valued struct embedding one stays
// constructible. That costs one atomic load per acquire on the fast path.
type Mutex struct {
	once sync.Once
	ch   chan struct{}
}

// sem returns the semaphore channel, creating it on first use. sync.Once
// guarantees every caller -- including two racing first acquires -- sees the
// same channel, which is what makes the zero value safe.
func (m *Mutex) sem() chan struct{} {
	m.once.Do(func() { m.ch = make(chan struct{}, 1) })
	return m.ch
}

// Lock acquires the mutex, giving up with ctx's error if ctx ends first. Pass
// context.Background() for an unbounded acquire; it can never fail, so the
// error is safe to ignore there.
//
// On error the mutex is NOT held: no abandoned waiter can acquire it afterwards.
func (m *Mutex) Lock(ctx context.Context) error {
	// Fast path: take a free lock without consulting ctx, so an uncontended
	// acquire under an already-cancelled context still succeeds rather than
	// losing a select race it did not need to enter.
	select {
	case m.sem() <- struct{}{}:
		return nil
	default:
	}
	select {
	case m.sem() <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TryLock acquires the mutex if it is free, reporting whether it did.
func (m *Mutex) TryLock() bool {
	select {
	case m.sem() <- struct{}{}:
		return true
	default:
		return false
	}
}

// Unlock releases the mutex, panicking if it is not held.
//
// Unlike sync.Mutex -- whose unlock-of-unlocked is an unrecoverable runtime
// fatal -- this is an ordinary panic a recover() up the stack could swallow.
// Both are caller bugs that must never happen; the difference is only in how
// loudly they die.
func (m *Mutex) Unlock() {
	select {
	case <-m.sem():
	default:
		panic("ctxutil: unlock of unlocked Mutex")
	}
}
