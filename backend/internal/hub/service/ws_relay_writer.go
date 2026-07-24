package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/coder/websocket"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/sendq"
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
// worker's stream, which blocked the worker's next Send. Historically
// (pre-#293) the worker also held a process-global mutex across that
// Send, so one wedged browser could stall every channel on that worker
// for every user; the worker now queues through sendq as well, so the
// remaining hazard this queue closes is the hub-side receive-loop stall.
//
// Frames are therefore queued and drained by sendq.Writer. Frames are
// never dropped mid-stream: the client has no resync path for a hole in
// an ordered, encrypted stream, and the ciphertext would have to be
// re-keyed. A client that cannot keep up is disconnected instead --
// reconnect and replay-from-DB already exist and are the intended
// recovery -- when it either stalls (relayMaxStall) or backs up past
// relayMaxQueueBytes.
type relayWriter struct {
	inner *sendq.Writer[*leapmuxv1.ChannelMessage]
}

func newRelayWriter(ctx context.Context, conn *websocket.Conn, cancel context.CancelFunc, userID, connID string) *relayWriter {
	w := &relayWriter{}
	w.inner = sendq.New(ctx, sendq.Config[*leapmuxv1.ChannelMessage]{
		Write: func(writeCtx context.Context, msg *leapmuxv1.ChannelMessage) error {
			// Tests that exercise enqueue/budget without a live socket pass a
			// nil conn; park until cancelled so the drain never panics and
			// never returns (which would free budget). Production always
			// passes a real conn.
			if conn == nil {
				<-writeCtx.Done()
				return writeCtx.Err()
			}
			return channelwire.WriteChannelMessage(writeCtx, conn, msg)
		},
		Size:          func(msg *leapmuxv1.ChannelMessage) int { return len(msg.GetCiphertext()) },
		MaxBytes:      relayMaxQueueBytes,
		FrameOverhead: relayFrameOverhead,
		WriteTimeout:  relayWriteTimeout,
		MaxStall:      relayMaxStall,
		OnGiveUp: func(err error) {
			slog.Warn("channel relay dropping connection",
				"user_id", userID, "conn_id", connID, "error", err)
			cancel()
		},
		OnDiscard: func(frames, _ int) {
			if frames > 0 {
				slog.Debug("channel relay discarded queued frames",
					"user_id", userID, "conn_id", connID, "count", frames)
			}
		},
	})
	return w
}

// enqueue hands a frame to the writer. It never blocks on the network --
// that is the whole point -- so a nil return means "queued", NOT
// "delivered", and an error means the connection is gone rather than
// that this particular frame failed.
//
// Frames still queued when the connection tears down are discarded;
// close logs how many. Callers that need delivery confirmation cannot
// get it here, and none do: the relay carries opaque ciphertext whose
// application-level acknowledgement is the frontend's own business.
func (w *relayWriter) enqueue(msg *leapmuxv1.ChannelMessage) error {
	if err := w.inner.Enqueue(msg); err != nil {
		if errors.Is(err, sendq.ErrClosed) {
			return errRelayWriterClosed
		}
		return err
	}
	return nil
}

// close stops the writer and discards anything still queued.
func (w *relayWriter) close() {
	w.inner.Close()
}
