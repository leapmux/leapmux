package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/metrics"
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
	// restarting the clock on idle is the opposite bug: a tab with no
	// agent or terminal output for half a minute -- the normal case --
	// would fail the check on its very next frame and be torn down while
	// perfectly healthy. This socket does carry a keepalive, but that is
	// no help here: ping and pong never reach sendq, so a peer answering
	// probes all minute long leaves lastProgress exactly where it was.
	//
	// What bounds memory is the shared sendq.Pool the writer draws
	// from; this bounds liveness.
	relayMaxStall = 30 * time.Second
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
// recovery -- when it either stalls (relayMaxStall) or is the biggest
// holder in a shared byte pool that has run out.
//
// The byte budget is the RELAY pool's, not a per-connection constant.
// Ingress is not throttled -- the hub's per-worker read loop hands frames
// over without blocking, and terminal output is one frame per PTY read
// with no coalescing -- so a wedged client accumulates at the worker's
// full production rate, which on a fast link is hundreds of MB inside
// the stall window. A per-connection constant bounded that for ONE tab
// and multiplied by however many tabs the hub was serving; the pool
// bounds the sum, which is the figure an operator sizes against, and
// lets a lone backed-up tab use far more than the old constant while the
// hub is otherwise idle.
//
// Far more is worth a number: under the pool's rule a single member on an
// otherwise-empty pool converges at HALF the capacity, so with the 8 GiB
// auto-size ceiling one wedged tab can pin ~4 GiB. relayMaxStall does not
// bound that -- the stall clock restarts on every successful write, so a
// client completing one write per 10 s window never trips the 30 s check
// while its backlog grows at the worker's full production rate. What
// bounds it is the pool: the moment a second connection needs the memory,
// the threshold halves again and the hog is the one nominated.
//
// Worker connections have their own pool. Reclaiming inside this one can
// therefore only ever cost a browser tab -- which reconnects and replays
// from the DB -- and never a worker, which would take every user's
// channels on that machine with it. See sendq.Pool.
type relayWriter struct {
	inner *sendq.Writer[*leapmuxv1.ChannelMessage]
}

// newRelayWriter starts a sendq-backed drain whose Write is injected so
// production and tests share one path: production passes
// channelwire.WriteChannelMessage bound to the live socket; tests that
// exercise budget accounting without a socket pass a park-until-cancelled
// stub.
func newRelayWriter(
	ctx context.Context,
	pool *sendq.Pool,
	write func(context.Context, *leapmuxv1.ChannelMessage) error,
	cancel context.CancelFunc,
	userID, connID string,
) *relayWriter {
	if write == nil {
		panic("newRelayWriter: write is required")
	}
	if pool == nil {
		panic("newRelayWriter: pool is required")
	}
	w := &relayWriter{}
	w.inner = sendq.New(ctx, sendq.Config[*leapmuxv1.ChannelMessage]{
		Write: write,
		Size:  func(msg *leapmuxv1.ChannelMessage) int { return len(msg.GetCiphertext()) },
		Pool:  pool,
		// sendq's own constant, not a local copy of the same number. The
		// per-frame charge bounds the SLOT count as well as the bytes: without
		// it a frame carrying little or no ciphertext -- a close sentinel, a
		// control frame -- is free and the queue length is unbounded even though
		// each slot pins a *ChannelMessage and its channel id. It is a
		// deliberate over-estimate of that retained footprint rather than a
		// measurement, which is exactly what sendq documents it as.
		//
		// Sharing the constant matters because config derives this class's
		// largest frame from sendq.DefaultFrameOverhead when it validates that
		// the budget can carry one: two constants that merely happened to agree
		// would let a retune here silently under-state that bound, turning a
		// startup error into every relay connection giving up at runtime.
		FrameOverhead: sendq.DefaultFrameOverhead,
		WriteTimeout:  relayWriteTimeout,
		MaxStall:      relayMaxStall,
		OnGiveUp: func(reason sendq.GiveUpReason, err error) {
			metrics.CountSendqGiveUp(metrics.PoolRelay, reason.Label())
			slog.Warn("channel relay dropping connection",
				"user_id", userID, "conn_id", connID,
				"reason", reason.Label(), "error", err)
			cancel()
		},
		OnDiscard: func(frames int, _ int64) {
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
