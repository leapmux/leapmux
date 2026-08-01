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
// shared receive goroutine that must never block. TryEnqueueControl is the
// must-deliver twin: it may consume a reserved ControlReserve slice of the
// byte budget that data paths leave free, so a saturated data burst cannot
// silently drop an open-response / access-ack / teardown CLOSE.
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
	// ControlReserve is the portion of MaxBytes that Enqueue / EnqueueWait /
	// TryEnqueue leave free for TryEnqueueControl. Zero disables the reserve
	// (every path shares the full budget). Must be strictly less than MaxBytes.
	ControlReserve int
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
	// writing is true from the moment the drain goroutine pops a frame until
	// its Write returns. Flush needs it because pop() removes the frame before
	// the write happens, so an empty queue alone does not mean the last frame
	// reached the transport.
	writing bool
	// wake carries at most one pending signal; the drain loop always
	// empties the whole queue, so more would be redundant.
	wake chan struct{}
	// budgetFreed wakes EnqueueWait when a pop frees budget.
	budgetFreed chan struct{}
	// drained wakes Flush each time a write completes. Unlike wake and
	// budgetFreed this is a BROADCAST -- the current generation is closed and
	// replaced (swapDrainedLocked) rather than sent to -- because Flush is
	// exported and any number of goroutines may be parked in it. Flush
	// re-checks the real condition under the mutex after every wake-up.
	drained chan struct{}
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
	if cfg.ControlReserve < 0 || cfg.ControlReserve >= cfg.MaxBytes {
		panic("sendq: Config.ControlReserve must be in [0, MaxBytes)")
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
		drained:     make(chan struct{}),
	}
	if cfg.WriteTimeout > 0 {
		// Created armed-then-stopped so writeItem only ever has to Reset it.
		// CompareAndSwap on watchdogArmed ensures only one of the write
		// completion path and the AfterFunc callback "owns" the give-up: a
		// timer that fires in the same instant Write returns cannot spuriously
		// tear down a connection that just made progress.
		w.watchdog = time.AfterFunc(time.Hour, func() {
			if w.watchdogArmed.CompareAndSwap(true, false) {
				w.giveUp(fmt.Errorf("write timed out after %s", cfg.WriteTimeout))
			}
		})
		w.watchdog.Stop()
	}
	return w
}

// Enqueue appends item, giving up (discard + close + OnGiveUp) when the data
// byte budget would be exceeded. The Hub's policy: a client that cannot keep
// up is disconnected, because reconnect + replay-from-DB already exist.
//
// Data paths leave ControlReserve free for TryEnqueueControl; with
// ControlReserve == 0 this is the full MaxBytes (historical behaviour).
func (w *Writer[T]) Enqueue(item T) error {
	size := w.cfg.Size(item) + w.cfg.FrameOverhead
	ceiling := w.dataCeiling()

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return ErrClosed
	}
	if w.queuedBytes+size > ceiling {
		queued, frames := w.queuedBytes, len(w.queue)
		w.mu.Unlock()
		w.giveUp(fmt.Errorf("%w: queued %d frames / %d bytes exceeds data limit %d",
			ErrOverBudget, frames, queued, ceiling))
		return ErrClosed
	}
	w.appendLocked(item, size)
	w.mu.Unlock()

	w.signalWake()
	return nil
}

// EnqueueWait appends item, BLOCKING until the data budget frees, ctx ends, or
// the writer closes. The worker's handler-data policy: the producer parks and
// the upstream source throttles itself, which is real backpressure rather than
// a drop.
//
// An item whose own size already exceeds the data ceiling can never fit, even
// against an empty queue: return ErrOverBudget immediately rather than parking
// forever. ControlReserve headroom is not available to this path.
func (w *Writer[T]) EnqueueWait(ctx context.Context, item T) error {
	size := w.cfg.Size(item) + w.cfg.FrameOverhead
	ceiling := w.dataCeiling()
	if size > ceiling {
		return fmt.Errorf("%w: item %d bytes exceeds data ceiling %d", ErrOverBudget, size, ceiling)
	}
	for {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return ErrClosed
		}
		if w.queuedBytes+size <= ceiling {
			w.appendLocked(item, size)
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

// TryEnqueue appends item if the data budget allows and reports whether it
// did. It never blocks and never tears the connection down. The policy for
// best-effort sends issued from a shared receive goroutine.
func (w *Writer[T]) TryEnqueue(item T) bool {
	return w.tryEnqueueAgainst(item, w.dataCeiling())
}

// TryEnqueueControl appends item if the FULL MaxBytes budget allows. It is the
// must-deliver path for receive-goroutine control frames: data paths leave
// ControlReserve free so a saturated bulk burst cannot starve an open
// response, access ack, or teardown CLOSE. Still never blocks and never
// tears the connection down — a false return means even the reserve is gone.
func (w *Writer[T]) TryEnqueueControl(item T) bool {
	return w.tryEnqueueAgainst(item, w.cfg.MaxBytes)
}

func (w *Writer[T]) tryEnqueueAgainst(item T, ceiling int) bool {
	size := w.cfg.Size(item) + w.cfg.FrameOverhead

	w.mu.Lock()
	if w.closed || w.queuedBytes+size > ceiling {
		w.mu.Unlock()
		return false
	}
	w.appendLocked(item, size)
	w.mu.Unlock()

	w.signalWake()
	return true
}

// dataCeiling is the MaxBytes data paths may fill up to, leaving
// ControlReserve free for TryEnqueueControl.
func (w *Writer[T]) dataCeiling() int {
	return w.cfg.MaxBytes - w.cfg.ControlReserve
}

// appendLocked charges item against the budget. Caller holds mu and has already
// verified the closed/budget preconditions.
func (w *Writer[T]) appendLocked(item T, size int) {
	w.queue = append(w.queue, queued[T]{item: item, size: size})
	w.queuedBytes += size
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
		// Also nudge EnqueueWait parkers, and any Flush waiting on frames that
		// this close just discarded.
		w.signalBudgetFreed()
		w.signalDrained()
	}
}

func (w *Writer[T]) discardQueueLocked() (bytes, frames int) {
	bytes, frames = w.queuedBytes, len(w.queue)
	w.queue = nil
	w.queuedBytes = 0
	return bytes, frames
}

// signalNonBlocking delivers an edge on a depth-1 signal channel, coalescing
// with any edge the receiver has not consumed yet. Correct only where exactly
// one goroutine waits on ch -- a second waiter would find the value already
// taken. wake (the drain goroutine) and budgetFreed (whose parkers re-check
// the budget under the lock and re-park) both satisfy that; drained does not,
// which is why it is a broadcast instead. See swapDrainedLocked.
func signalNonBlocking(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (w *Writer[T]) signalWake() { signalNonBlocking(w.wake) }

func (w *Writer[T]) signalBudgetFreed() { signalNonBlocking(w.budgetFreed) }

// swapDrainedLocked installs a fresh drained channel and returns the old one
// for the caller to close once it has released the lock.
//
// drained is a BROADCAST, not a depth-1 signal: Flush is exported and nothing
// stops two goroutines from flushing the same writer. With a coalescing send,
// whichever Flush woke first would consume the only value and the other would
// park with no producer left, then fail its own context with
// DeadlineExceeded -- reporting a failed flush for a queue that drained fine.
// Closing a generation channel wakes every waiter at once, and each re-checks
// the real condition under the lock.
func (w *Writer[T]) swapDrainedLocked() chan struct{} {
	ch := w.drained
	w.drained = make(chan struct{})
	return ch
}

func (w *Writer[T]) signalDrained() {
	w.mu.Lock()
	ch := w.swapDrainedLocked()
	w.mu.Unlock()
	close(ch)
}

// Flush blocks until the queue is empty AND the in-flight write has returned,
// so every frame enqueued before the call has been handed to the transport.
//
// Returns nil on a full drain, and also once the writer is closed or has given
// up: the queue was discarded, so there is nothing left to wait for and no
// later call could do better. Returns an error only when the WAIT was cut
// short -- ctx expired, or the writer's own connection context ended -- because
// only then might frames still have been drainable.
//
// This is what makes a graceful shutdown's last words actually leave the
// machine. Enqueue and EnqueueWait return once a frame is QUEUED, so a caller
// that broadcasts and then tears the connection down is racing the drain
// goroutine: on an idle box the drain wins and nobody notices, under load it
// loses and the frames are discarded by Close with no error anywhere.
func (w *Writer[T]) Flush(ctx context.Context) error {
	for {
		// Capture the current generation channel under the same lock that read
		// the state, so a drain landing between the unlock and the select still
		// closes the channel this iteration parks on.
		w.mu.Lock()
		idle := len(w.queue) == 0 && !w.writing
		done := w.closed || w.gaveUp
		drained := w.drained
		w.mu.Unlock()
		if idle || done {
			return nil
		}
		select {
		case <-drained:
		case <-ctx.Done():
			return ctx.Err()
		case <-w.ctx.Done():
			return w.ctx.Err()
		}
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
	// The frame has left the queue but not yet the process; Flush must keep
	// waiting until finishWrite clears this.
	w.writing = true
	return f, true
}

// finishWrite marks the popped frame as no longer in flight and wakes Flush.
// Paired with the w.writing set in pop() on every path out of writeItem,
// including the error paths -- a Flush parked behind a failed write must not
// hang waiting for a drain that will never come.
func (w *Writer[T]) finishWrite() {
	w.mu.Lock()
	w.writing = false
	ch := w.swapDrainedLocked()
	w.mu.Unlock()
	close(ch)
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
	var frames, bytes int
	if !w.closed {
		bytes, frames = w.discardQueueLocked()
		w.closed = true
	}
	onGiveUp := w.cfg.OnGiveUp
	onDiscard := w.cfg.OnDiscard
	w.mu.Unlock()
	w.signalWake()
	w.signalBudgetFreed()
	w.signalDrained()
	if onDiscard != nil && frames > 0 {
		onDiscard(frames, bytes)
	}
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
			// Wake EnqueueWait parkers as soon as the bytes leave the queue,
			// not after the (up to WriteTimeout-long) write finishes. The
			// budget is already free; delaying the signal couples backpressure
			// relief to write latency for no functional reason.
			w.signalBudgetFreed()
			err := w.writeItem(frame.item)
			w.finishWrite()
			if err != nil {
				if w.isClosed() {
					return
				}
				w.giveUp(err)
				return
			}
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

	var err error
	if w.watchdog != nil {
		// One reusable timer rather than a context+timer per frame.
		// Timer.Stop cannot retract a callback already running, so the
		// watchdogArmed CAS is what keeps a watchdog that fires as this
		// write returns from cancelling a connection that just made progress.
		w.watchdogArmed.Store(true)
		w.watchdog.Reset(w.cfg.WriteTimeout)
		err = w.cfg.Write(w.ctx, item)
		disarmed := w.watchdogArmed.CompareAndSwap(true, false)
		w.watchdog.Stop()
		if !disarmed {
			// AfterFunc already won the race and called giveUp.
			if err != nil {
				return fmt.Errorf("write: %w", err)
			}
			return ErrClosed
		}
	} else {
		err = w.cfg.Write(w.ctx, item)
	}
	if err != nil {
		return fmt.Errorf("write: %w", err)
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
//
// finishWrite is what pairs with pop()'s `writing = true`, and it is required
// here too even though nothing is written: the frame is out of the queue and
// out of the process, so leaving the flag set would park every later Flush on
// this writer until its context expired.
func (w *Writer[T]) PopForTest() (T, bool) {
	f, ok := w.pop()
	if ok {
		w.signalBudgetFreed()
		w.finishWrite()
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
