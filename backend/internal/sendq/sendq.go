// Package sendq is the bounded outbound queue every long-lived stream in this
// system writes through.
//
// Two connections own one today: the Hub's frontend websocket relay
// (internal/hub/service.relayWriter) and the worker's Connect bidi stream
// (internal/worker/hub.Client). Both face the same hazard -- a synchronous
// write under shared infrastructure turns one slow peer into a stall of every
// multiplexed producer behind it -- and both recover the same way: queue,
// drain from one goroutine, disconnect (or park) when the peer cannot keep up.
//
// Frames are never dropped mid-stream on the Enqueue path: an ordered,
// encrypted ciphertext stream has no resync for a hole, and reconnect +
// replay-from-DB (Hub) or producer backpressure (worker) already exist.
// TryEnqueue is the deliberate exception for best-effort frames issued from a
// shared receive goroutine that must never block.
//
// Nothing here bounds how MANY writers exist. A shared pool, and the eviction
// policy it would need, is tracked in
// https://github.com/leapmux/leapmux/issues/313.
package sendq

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Config configures a Writer. MaxBytes is required; every other bound may be
// zeroed to disable it.
type Config[T any] struct {
	// Write is the ONLY caller of the underlying transport. The drain
	// goroutine owns it exclusively.
	Write func(context.Context, T) error
	// Size returns the bytes charged for an item, excluding FrameOverhead.
	Size func(T) int
	// MaxBytes is the per-connection memory bound. Required and must be > 0.
	MaxBytes int
	// FrameOverhead is charged per item so many tiny frames cost something.
	FrameOverhead int
	// WriteTimeout bounds one Write. Zero disables the per-write watchdog.
	WriteTimeout time.Duration
	// MaxStall bounds how long queued work may sit unwritten. Zero disables
	// the wall-clock stall bound. The clock restarts on idle: this socket may
	// have no keepalive, so idle time is not stalled time.
	MaxStall time.Duration
	// OnGiveUp cancels the connection. Called at most once, when the writer
	// gives up (byte budget, stall, or write failure).
	OnGiveUp func(error)
	// OnDiscard reports frames/bytes discarded on teardown. Optional.
	OnDiscard func(frames, bytes int)
	// Now is a seam so tests can advance the stall clock without sleeping.
	// Nil means time.Now.
	Now func() time.Time
}

var (
	// ErrClosed is returned by Enqueue once the writer is torn down.
	ErrClosed = errors.New("sendq: writer closed")
	// ErrOverBudget is the cause passed to OnGiveUp when Enqueue blows the
	// byte budget. Callers of Enqueue itself still see ErrClosed: a client
	// that cannot keep up is disconnected, and further enqueues must stop.
	ErrOverBudget = errors.New("sendq: queue over byte budget")
)

type queued[T any] struct {
	item T
	size int
}

// Writer is a bounded outbound queue drained by a single goroutine.
type Writer[T any] struct {
	cfg Config[T]
	ctx context.Context

	now func() time.Time

	watchdog      *time.Timer
	watchdogArmed atomic.Bool
	lastProgress  time.Time

	mu          sync.Mutex
	queue       []queued[T]
	queuedBytes int
	closed      bool
	gaveUp      bool
	// wake carries at most one pending signal; the drain loop always
	// empties the whole queue, so more would be redundant.
	wake chan struct{}
	// budgetFreed wakes EnqueueWait when a pop frees budget.
	budgetFreed chan struct{}
}

// New starts the drain goroutine, bound to ctx.
func New[T any](ctx context.Context, cfg Config[T]) *Writer[T] {
	w := newWriter(ctx, cfg)
	go w.run()
	return w
}

// newWriter constructs a Writer without starting the drain. Tests that drive
// writeItem or the budget accounting directly use it so they do not race the
// drain goroutine on lastProgress.
func newWriter[T any](ctx context.Context, cfg Config[T]) *Writer[T] {
	if cfg.MaxBytes <= 0 {
		panic("sendq: Config.MaxBytes must be positive")
	}
	if cfg.Write == nil || cfg.Size == nil {
		panic("sendq: Config.Write and Config.Size are required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	w := &Writer[T]{
		cfg:         cfg,
		ctx:         ctx,
		now:         now,
		wake:        make(chan struct{}, 1),
		budgetFreed: make(chan struct{}, 1),
	}
	if cfg.WriteTimeout > 0 {
		// Created armed-then-stopped so writeItem only ever has to Reset it.
		w.watchdog = time.AfterFunc(time.Hour, func() {
			if w.watchdogArmed.Load() {
				w.giveUp(fmt.Errorf("write timed out after %s", cfg.WriteTimeout))
			}
		})
		w.watchdog.Stop()
	}
	return w
}

// Enqueue appends item, giving up (discard + close + OnGiveUp) when the byte
// budget would be exceeded. The Hub's policy: a client that cannot keep up is
// disconnected, because reconnect + replay-from-DB already exist.
func (w *Writer[T]) Enqueue(item T) error {
	size := w.cfg.Size(item) + w.cfg.FrameOverhead

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return ErrClosed
	}
	if w.queuedBytes+size > w.cfg.MaxBytes {
		queued, frames := w.discardQueueLocked()
		w.closed = true
		w.mu.Unlock()
		if w.cfg.OnDiscard != nil && frames > 0 {
			w.cfg.OnDiscard(frames, queued)
		}
		w.signalWake()
		w.giveUp(fmt.Errorf("%w: queued %d frames / %d bytes exceeds limit %d",
			ErrOverBudget, frames, queued, w.cfg.MaxBytes))
		return ErrClosed
	}
	w.queue = append(w.queue, queued[T]{item: item, size: size})
	w.queuedBytes += size
	w.mu.Unlock()

	w.signalWake()
	return nil
}

// EnqueueWait appends item, BLOCKING until the budget frees, ctx ends, or the
// writer closes. The worker's handler-data policy: the producer parks and the
// upstream source throttles itself, which is real backpressure rather than a
// drop.
func (w *Writer[T]) EnqueueWait(ctx context.Context, item T) error {
	size := w.cfg.Size(item) + w.cfg.FrameOverhead
	for {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return ErrClosed
		}
		if w.queuedBytes+size <= w.cfg.MaxBytes {
			w.queue = append(w.queue, queued[T]{item: item, size: size})
			w.queuedBytes += size
			w.mu.Unlock()
			w.signalWake()
			return nil
		}
		w.mu.Unlock()

		select {
		case <-w.budgetFreed:
		case <-ctx.Done():
			return ctx.Err()
		case <-w.ctx.Done():
			return ErrClosed
		}
		// Re-check closed without waiting forever on a drained-and-closed writer.
		w.mu.Lock()
		closed := w.closed
		w.mu.Unlock()
		if closed {
			return ErrClosed
		}
	}
}

// TryEnqueue appends item if the budget allows, reporting whether it did. It
// never blocks and never tears the connection down. The policy for sends issued
// from a shared receive goroutine, which must never block and whose frames are
// best-effort.
func (w *Writer[T]) TryEnqueue(item T) bool {
	size := w.cfg.Size(item) + w.cfg.FrameOverhead

	w.mu.Lock()
	if w.closed || w.queuedBytes+size > w.cfg.MaxBytes {
		w.mu.Unlock()
		return false
	}
	w.queue = append(w.queue, queued[T]{item: item, size: size})
	w.queuedBytes += size
	w.mu.Unlock()

	w.signalWake()
	return true
}

// Close stops the writer, discards anything still queued, and wakes the
// drain goroutine so it observes the closure and returns.
//
// It signals wake rather than relying on the caller's context so that
// close alone is sufficient to reap the goroutine. Depending on an
// external cancel would leak one goroutine -- plus every queued frame it
// pins -- for any caller that owns the writer's lifetime without owning
// its context.
func (w *Writer[T]) Close() {
	w.mu.Lock()
	alreadyClosed := w.closed
	bytes, frames := w.discardQueueLocked()
	w.closed = true
	w.mu.Unlock()

	if frames > 0 && w.cfg.OnDiscard != nil {
		w.cfg.OnDiscard(frames, bytes)
	}
	if !alreadyClosed {
		w.signalWake()
		// Also nudge EnqueueWait parkers.
		w.signalBudgetFreed()
	}
}

func (w *Writer[T]) discardQueueLocked() (bytes, frames int) {
	bytes, frames = w.queuedBytes, len(w.queue)
	w.queue = nil
	w.queuedBytes = 0
	return bytes, frames
}

func (w *Writer[T]) signalWake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *Writer[T]) signalBudgetFreed() {
	select {
	case w.budgetFreed <- struct{}{}:
	default:
	}
}

func (w *Writer[T]) pop() (queued[T], bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || len(w.queue) == 0 {
		var zero queued[T]
		return zero, false
	}
	f := w.queue[0]
	var zero queued[T]
	w.queue[0] = zero
	w.queue = w.queue[1:]
	w.queuedBytes -= f.size
	return f, true
}

func (w *Writer[T]) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

func (w *Writer[T]) giveUp(err error) {
	w.mu.Lock()
	if w.gaveUp {
		w.mu.Unlock()
		return
	}
	w.gaveUp = true
	if !w.closed {
		_, _ = w.discardQueueLocked()
		w.closed = true
	}
	onGiveUp := w.cfg.OnGiveUp
	w.mu.Unlock()
	w.signalWake()
	w.signalBudgetFreed()
	if onGiveUp != nil {
		onGiveUp(err)
	}
}

func (w *Writer[T]) run() {
	defer w.Close()
	w.lastProgress = w.now()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.wake:
		}

		// Restart the stall clock: the inner loop below only ever exits
		// with an empty queue, so reaching here means the client owed us
		// nothing until this wake-up. Idle time is not stalled time, and
		// this socket may have no keepalive to refresh the clock for us.
		w.lastProgress = w.now()

		for {
			frame, ok := w.pop()
			if !ok {
				break
			}
			if err := w.writeItem(frame.item); err != nil {
				if w.isClosed() {
					return
				}
				w.giveUp(err)
				return
			}
			w.signalBudgetFreed()
		}
		if w.isClosed() {
			return
		}
	}
}

func (w *Writer[T]) writeItem(item T) error {
	if w.cfg.MaxStall > 0 {
		if stalled := w.now().Sub(w.lastProgress); stalled > w.cfg.MaxStall {
			return fmt.Errorf("client made no progress for %s (limit %s)",
				stalled.Round(time.Millisecond), w.cfg.MaxStall)
		}
	}

	if w.watchdog != nil {
		// One reusable timer rather than a context+timer per frame.
		// Timer.Stop cannot retract a callback already running, so the
		// watchdogArmed flag is what keeps a watchdog that fires as this
		// write returns from cancelling a connection that just made progress.
		w.watchdogArmed.Store(true)
		w.watchdog.Reset(w.cfg.WriteTimeout)
		err := w.cfg.Write(w.ctx, item)
		w.watchdogArmed.Store(false)
		w.watchdog.Stop()
		if err != nil {
			return fmt.Errorf("write: %w", err)
		}
	} else {
		if err := w.cfg.Write(w.ctx, item); err != nil {
			return fmt.Errorf("write: %w", err)
		}
	}

	w.lastProgress = w.now()
	return nil
}

// QueuedBytes reports the current charged byte count. For tests.
func (w *Writer[T]) QueuedBytes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.queuedBytes
}

// QueuedLen reports the number of queued items. For tests.
func (w *Writer[T]) QueuedLen() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.queue)
}

// IsClosed reports whether Close (or an over-budget kill) has run. For tests.
func (w *Writer[T]) IsClosed() bool {
	return w.isClosed()
}

// PopForTest removes and returns the head item without writing it. For tests
// that drive the budget accounting without a live drain.
func (w *Writer[T]) PopForTest() (T, bool) {
	f, ok := w.pop()
	if ok {
		w.signalBudgetFreed()
	}
	return f.item, ok
}

// SetNowForTest replaces the stall clock. For tests; call before any drain
// activity that reads it, or while the drain is idle.
func (w *Writer[T]) SetNowForTest(now func() time.Time) {
	w.now = now
}

// SetLastProgressForTest plants the stall clock. For tests of the stall check.
func (w *Writer[T]) SetLastProgressForTest(t time.Time) {
	w.lastProgress = t
}

// WriteItemForTest runs the stall+write path for one item without going
// through the queue. For tests of the stall bound in isolation.
func (w *Writer[T]) WriteItemForTest(item T) error {
	return w.writeItem(item)
}

// WakeChForTest exposes the wake channel so over-budget teardown tests can
// assert the drain was nudged. For tests.
func (w *Writer[T]) WakeChForTest() <-chan struct{} {
	return w.wake
}
