package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	// relayWriteTimeout bounds ONE websocket write. A frame that cannot
	// be handed to the kernel within this window means the client's
	// receive window has been full for that long.
	relayWriteTimeout = 10 * time.Second

	// relayMaxStall bounds how long the client may leave QUEUED work
	// unwritten. The clock starts when the drain loop picks up work after
	// being idle and advances on every successful write, so it measures
	// backlog that is not moving -- never the gap between one burst and
	// the next.
	//
	// Both halves of that matter. Measuring queue AGE would disconnect a
	// client working steadily through a large page-refresh replay on a
	// slow uplink, and its reconnect would replay the same burst and age
	// out again. Measuring from the last successful write WITHOUT
	// restarting the clock on idle is the opposite bug: this socket
	// carries no keepalive, so a tab with no agent or terminal output for
	// half a minute -- the normal case -- would fail the check on its
	// very next frame and be torn down while perfectly healthy.
	//
	// What bounds memory is relayMaxQueueBytes below; this bounds
	// liveness.
	relayMaxStall = 30 * time.Second

	// relayMaxQueueBytes caps the queue by payload plus per-frame
	// overhead, which is what makes an unbounded slot count safe.
	//
	// It bounds ONE connection. Nothing bounds how many there are, so the
	// hub's aggregate worst case is this figure times the number of
	// simultaneously wedged clients -- and the aggregate is the number an
	// operator actually sizes against. A shared pool, and the eviction
	// policy it would need, is tracked in
	// https://github.com/leapmux/leapmux/issues/313.
	//
	// Ingress is not throttled: the hub's per-worker read loop hands
	// frames over without blocking, and terminal output is one frame per
	// PTY read with no coalescing. A wedged client therefore accumulates
	// at the worker's full production rate -- on a fast link that is
	// hundreds of MB inside the stall window, per connection, and the hub
	// serves every user. A time bound alone is not a memory bound when
	// the rate is workload-controlled.
	relayMaxQueueBytes = 32 * 1024 * 1024

	// relayFrameOverhead is charged per queued frame on top of its
	// ciphertext, so the budget bounds the SLOT count too.
	//
	// Without it a frame carrying little or no ciphertext -- a close
	// sentinel, a control frame -- is free, and the queue length is
	// unbounded even though each slot pins a *ChannelMessage and its
	// channel id. The value is a deliberate over-estimate of that
	// retained footprint rather than a measurement: it only has to make
	// "many tiny frames" cost something, and at this size the budget
	// admits at most relayMaxQueueBytes/relayFrameOverhead slots.
	relayFrameOverhead = 256
)

// errRelayWriterClosed is returned by enqueue once the connection is
// being torn down, so callers stop handing it frames.
var errRelayWriterClosed = errors.New("relay writer closed")

type relayFrame struct {
	msg  *leapmuxv1.ChannelMessage
	size int
}

// relayWriter owns all writes to one frontend websocket.
//
// Scope is the CONNECTION, not the channel: the frontend opens a single
// relay socket per browser tab and multiplexes one channel per worker
// over it, and channelmgr resolves every channel to its owning
// connection's send func. So all of a tab's channels share this queue
// and this budget. That is the right granularity for the hazard below --
// the socket is the one serialization point, so a per-channel queue could
// not drain any faster; when the socket wedges, every queue behind it
// grows in lockstep. The cost is strict FIFO across channels: a burst on
// one worker's channel delays another's, where per-channel queues feeding
// a round-robin scheduler would interleave them. That is a fairness
// question, not a backpressure one.
//
// It exists to decouple the hub's per-worker read loop from any one
// browser. That loop relays worker->frontend frames inline, so a
// synchronous write here propagated a single client's backpressure
// straight into shared infrastructure: a browser whose TCP receive
// window was full blocked the write, which stopped the hub draining that
// worker's stream, which blocked the worker's next Send -- and the
// worker holds a PROCESS-GLOBAL mutex across that Send, so one wedged
// browser could stall every channel on that worker, for every user.
// (That worker-side mutex is still there; giving the worker the same
// treatment is tracked in https://github.com/leapmux/leapmux/issues/293.)
//
// Frames are therefore queued and drained by a single goroutine. Frames
// are never dropped mid-stream: the client has no resync path for a hole
// in an ordered, encrypted stream, and the ciphertext would have to be
// re-keyed. A client that cannot keep up is disconnected instead --
// reconnect and replay-from-DB already exist and are the intended
// recovery -- when it either stalls (relayMaxStall) or backs up past
// relayMaxQueueBytes.
type relayWriter struct {
	conn *websocket.Conn
	// ctx is the connection's lifetime; cancelling it unwinds the read
	// loop in ServeHTTP, whose deferred cleanup unbinds and closes.
	ctx    context.Context
	cancel context.CancelFunc

	userID string
	connID string

	// now is a seam so tests can advance the stall clock without
	// sleeping. Read by the drain goroutine and, in tests, written before
	// it starts.
	now func() time.Time

	// watchdog bounds one write without allocating a context and timer
	// per frame. Only the drain goroutine arms and stops it.
	watchdog *time.Timer

	// watchdogArmed gates the watchdog's callback. Timer.Stop cannot
	// retract a callback that has already begun running, so a write
	// finishing at the same instant the watchdog fires would otherwise
	// cancel a connection whose frame did reach the client -- and the
	// resulting teardown gets logged against the NEXT frame, blaming the
	// wrong one. Clearing this before Stop makes the late callback a
	// no-op.
	watchdogArmed atomic.Bool

	// lastProgress is when the client was last known to be keeping up:
	// either a frame reached it, or the queue was empty so there was
	// nothing to keep up with. Owned by the drain goroutine alone.
	lastProgress time.Time

	mu          sync.Mutex
	queue       []relayFrame
	queuedBytes int
	closed      bool
	// wake carries at most one pending signal; the drain loop always
	// empties the whole queue, so more would be redundant.
	wake chan struct{}
}

func newRelayWriter(ctx context.Context, conn *websocket.Conn, cancel context.CancelFunc, userID, connID string) *relayWriter {
	w := &relayWriter{
		conn:   conn,
		ctx:    ctx,
		cancel: cancel,
		userID: userID,
		connID: connID,
		now:    time.Now,
		wake:   make(chan struct{}, 1),
	}
	// Created armed-then-stopped so writeFrame only ever has to Reset it.
	w.watchdog = time.AfterFunc(time.Hour, func() {
		if w.watchdogArmed.Load() {
			w.cancel()
		}
	})
	w.watchdog.Stop()
	return w
}

// enqueue hands a frame to the writer goroutine. It never blocks on the
// network -- that is the whole point -- so a nil return means "queued",
// NOT "delivered", and an error means the connection is gone rather than
// that this particular frame failed.
//
// Frames still queued when the connection tears down are discarded; close
// logs how many. Callers that need delivery confirmation cannot get it
// here, and none do: the relay carries opaque ciphertext whose
// application-level acknowledgement is the frontend's own business.
func (w *relayWriter) enqueue(msg *leapmuxv1.ChannelMessage) error {
	size := len(msg.GetCiphertext()) + relayFrameOverhead

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return errRelayWriterClosed
	}
	if w.queuedBytes+size > relayMaxQueueBytes {
		queued, frames := w.discardQueueLocked()
		w.closed = true
		w.mu.Unlock()
		slog.Warn("channel relay dropping connection: queue over byte budget",
			"user_id", w.userID, "conn_id", w.connID,
			"queued_bytes", queued, "queued_frames", frames,
			"limit_bytes", relayMaxQueueBytes)
		// Wake the drain goroutine as well as cancelling. Cancelling alone
		// would leave close's "close is sufficient to reap the goroutine"
		// contract depending on the very external cancel it was written to
		// stop relying on -- and a drain goroutine parked on wake with an
		// already-emptied queue never observes ctx alone.
		w.signalWake()
		// Cancel too rather than waiting for the drain goroutine to notice:
		// it is blocked on the very write that is not draining, which is
		// why the queue grew.
		w.cancel()
		return errRelayWriterClosed
	}
	w.queue = append(w.queue, relayFrame{msg: msg, size: size})
	w.queuedBytes += size
	w.mu.Unlock()

	w.signalWake()
	return nil
}

// discardQueueLocked drops the backlog and reports what it dropped. The
// caller must hold w.mu.
//
// Both teardown paths -- the byte-budget kill above and close below --
// route through it so they cannot drift in what they reset.
func (w *relayWriter) discardQueueLocked() (bytes, frames int) {
	bytes, frames = w.queuedBytes, len(w.queue)
	w.queue = nil
	w.queuedBytes = 0
	return bytes, frames
}

// signalWake nudges the drain goroutine. The channel carries at most one
// pending signal; the drain loop always empties the whole queue, so more
// would be redundant.
func (w *relayWriter) signalWake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// close stops the writer, discards anything still queued, and wakes the
// drain goroutine so it observes the closure and returns.
//
// It signals wake rather than relying on the caller's context so that
// close alone is sufficient to reap the goroutine. Depending on an
// external cancel would leak one goroutine -- plus the websocket and
// every queued frame it pins -- for any caller that owns the writer's
// lifetime without owning its context.
func (w *relayWriter) close() {
	w.mu.Lock()
	alreadyClosed := w.closed
	_, discarded := w.discardQueueLocked()
	w.closed = true
	w.mu.Unlock()

	if discarded > 0 {
		// Not an error: the connection is going away and these frames were
		// undeliverable either way. Logged because "queued" reads as
		// "delivered" upstream, so the loss is otherwise invisible.
		slog.Debug("channel relay discarded queued frames",
			"user_id", w.userID, "conn_id", w.connID, "count", discarded)
	}
	if !alreadyClosed {
		w.signalWake()
	}
}

// pop removes and returns the head frame, if any.
func (w *relayWriter) pop() (relayFrame, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || len(w.queue) == 0 {
		return relayFrame{}, false
	}
	f := w.queue[0]
	// Clear the slot so the frame isn't pinned by the backing array.
	w.queue[0] = relayFrame{}
	w.queue = w.queue[1:]
	w.queuedBytes -= f.size
	return f, true
}

// isClosed reports whether close has run.
func (w *relayWriter) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

// run drains the queue until the connection ends, the writer is closed,
// or the client stops making progress. It is the only goroutine that
// writes to the websocket.
func (w *relayWriter) run() {
	defer w.close()
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
		// this socket has no keepalive to refresh the clock for us.
		w.lastProgress = w.now()

		for {
			frame, ok := w.pop()
			if !ok {
				break
			}
			if err := w.writeFrame(frame); err != nil {
				if w.isClosed() {
					// Already torn down (byte budget, or the handler's
					// disconnect defer), and that path logged its own
					// reason. This error is the consequence, not the
					// cause -- reporting it as a fresh cause would blame
					// the write for a decision made elsewhere.
					slog.Debug("channel relay write failed after close",
						"user_id", w.userID, "conn_id", w.connID, "error", err)
					return
				}
				slog.Warn("channel relay dropping connection",
					"user_id", w.userID, "conn_id", w.connID, "error", err)
				// Unwind ServeHTTP; its deferred cleanup unbinds the
				// connection and closes the channels it owned.
				w.cancel()
				return
			}
		}
		if w.isClosed() {
			return
		}
	}
}

// writeFrame enforces the stall bound and the per-write timeout.
func (w *relayWriter) writeFrame(frame relayFrame) error {
	if stalled := w.now().Sub(w.lastProgress); stalled > relayMaxStall {
		return fmt.Errorf("client made no progress for %s (limit %s)",
			stalled.Round(time.Millisecond), relayMaxStall)
	}

	// One reusable timer rather than a context+timer per frame: this is
	// the hub's hot relay path (one frame per PTY read, no coalescing),
	// and only this goroutine touches the timer. Firing cancels the
	// connection context, which aborts the in-flight write.
	w.watchdogArmed.Store(true)
	w.watchdog.Reset(relayWriteTimeout)
	err := channelwire.WriteChannelMessage(w.ctx, w.conn, frame.msg)
	// Disarm BEFORE Stop: Stop cannot retract a callback already running,
	// so the flag is what keeps a watchdog that fires as this write
	// returns from cancelling a connection that just made progress.
	w.watchdogArmed.Store(false)
	w.watchdog.Stop()
	if err != nil {
		// A timed-out or failed write has already broken the websocket's
		// framing -- coder/websocket closes the conn on write-context
		// cancellation -- so the connection cannot be reused either way.
		return fmt.Errorf("write channel message: %w", err)
	}

	w.lastProgress = w.now()
	return nil
}
