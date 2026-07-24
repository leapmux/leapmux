package tunnelflow

import (
	"context"
	"errors"
	"sync"
)

// Window is a counting semaphore denominated in flow-control frames.
//
// It is the ONE mechanism behind both halves of the tunnel's flow control -- the
// client's send window (tunnel.Conn.sendWindow) and the worker's read-send
// credit (service.tunnelConn.credit) -- so the sizes above and the behaviour
// that consumes them cannot drift apart. It replaces two hand-rolled copies: a
// bare channel semaphore on the client and a sync.Cond counter with a max clamp
// and an int64-overflow guard on the worker.
//
// Tokens live in a buffered channel whose CAPACITY is the window. That is what
// makes "a grant may never raise credit above the window" structural rather than
// arithmetic: Grant clamps its argument to the capacity and then sends
// non-blocking, so a hostile or buggy peer's overflowing grant is discarded
// instead of driving a counter negative and wedging the reader forever.
//
// A window begins FULLY OPEN: NewWindow(n) holds n tokens.
type Window struct {
	tokens    chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

// ErrWindowClosed is returned by Acquire once the window is closed.
var ErrWindowClosed = errors.New("tunnelflow: window closed")

// NewWindow returns a fully-open window of size tokens. size must be positive.
func NewWindow(size int) *Window {
	if size <= 0 {
		panic("tunnelflow: NewWindow size must be positive")
	}
	w := &Window{
		tokens: make(chan struct{}, size),
		closed: make(chan struct{}),
	}
	for range size {
		w.tokens <- struct{}{}
	}
	return w
}

// Ready exposes the token channel for callers that must acquire inside a larger
// select (tunnel.Conn.acquireSendSlot watches a close latch, a NAK latch, the
// channel context and a deadline change alongside it). A successful RECEIVE from
// this channel IS an acquire; the caller then owns one token and must Grant it
// back.
func (w *Window) Ready() <-chan struct{} {
	return w.tokens
}

// Acquire takes one token, blocking until one is available, ctx ends, or the
// window closes. It returns nil only when a token was taken.
func (w *Window) Acquire(ctx context.Context) error {
	select {
	case <-w.tokens:
		return nil
	default:
	}
	select {
	case <-w.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-w.closed:
		return ErrWindowClosed
	}
}

// Grant returns up to n tokens. Surplus beyond the window's capacity is
// discarded -- see the type doc. "Grant" is the protocol's own word
// (GrantTunnelReadCredit); returning a consumed send slot is the same operation,
// so it has one name.
func (w *Window) Grant(n uint64) {
	if capacity := uint64(cap(w.tokens)); n > capacity {
		n = capacity
	}
	for range n {
		select {
		case w.tokens <- struct{}{}:
		default:
			return
		}
	}
}

// Close wakes every Acquire and Ready waiter. Idempotent.
func (w *Window) Close() {
	w.closeOnce.Do(func() { close(w.closed) })
}

// Closed is closed once Close has run.
func (w *Window) Closed() <-chan struct{} {
	return w.closed
}

// Available reports how many tokens are currently free.
func (w *Window) Available() int {
	return len(w.tokens)
}

// InUse reports how many tokens are currently held by callers.
func (w *Window) InUse() int {
	return cap(w.tokens) - len(w.tokens)
}
