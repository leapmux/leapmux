package workermgr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
	"google.golang.org/protobuf/proto"
)

const (
	// connDrainMaxFrames / connDrainMaxDuration bound one Connect-select Drain
	// turn so a large outbound backlog cannot starve receives, idle, or
	// ctx.Done. The deferred full Drain on teardown still empties the queue.
	connDrainMaxFrames   = 32
	connDrainMaxDuration = 5 * time.Millisecond
)

// ErrConnectionClosed is returned when a sender races worker disconnect.
var ErrConnectionClosed = errors.New("worker connection closed")

// ErrControlSaturated is returned when SendControl cannot enqueue because the
// byte budget (including the control reserve) is full. Distinct from
// ErrConnectionClosed so callers can retry or re-queue without treating the
// worker as offline.
var ErrControlSaturated = errors.New("worker connection control queue saturated")

// Conn represents a connected worker's bidirectional stream. Sends enqueue
// onto a handler-drained queue; the Connect handler alone holds the matching
// SendPump and is the only goroutine that ever writes.
type Conn struct {
	WorkerID string
	// Greeting, when non-nil at Register, is enqueued before the connection is
	// published -- at the head of the queue, with a single handler drain, so
	// it is mechanically the first frame written. Register clears it after a
	// successful enqueue so the live Conn does not retain a second copy.
	Greeting *leapmuxv1.ConnectResponse

	// owner is the account this worker is registered to (Worker.RegisteredBy).
	// The Manager keys its per-account live-connection tally on it, which is why
	// it is fixed at construction and readable but not writable: a caller that
	// could change it between Register and Unregister would decrement a
	// different bucket than the one it incremented, and the cap computed from
	// that tally would drift away from the truth in whichever direction the
	// change went.
	owner string

	ctx        context.Context
	cancel     context.CancelFunc
	cancelOnce sync.Once
	q          *sendq.Writer[*leapmuxv1.ConnectResponse]

	encryptionMode atomic.Int32
}

// SendPump is the Connect handler's exclusive write capability. The registry
// hands out *Conn, which has no drain method; only the handler holds the
// pump. Sole ownership of the transport write is therefore unforgeable rather
// than a documentation comment -- across package boundaries. Same-package
// tests must keep the pump returned by NewConn rather than reconstructing one.
type SendPump struct{ c *Conn }

// Ready returns the queue's wake channel. The handler selects on it and calls
// DrainTurn (or Drain on teardown). Exactly one goroutine may consume it.
func (p *SendPump) Ready() <-chan struct{} { return p.c.q.Wake() }

// DrainTurn writes up to a bounded batch of queued frames and returns so the
// Connect select can service receives. Remaining frames re-signal Ready.
func (p *SendPump) DrainTurn() (err error) {
	return p.drain(sendq.DrainLimits{
		MaxFrames:   connDrainMaxFrames,
		MaxDuration: connDrainMaxDuration,
	})
}

// Drain pops and writes every currently queued frame. Returns a non-nil error
// once the queue has given up (write failure or watchdog); give-up already
// fenced and cancelled the conn. Used on Connect teardown after the conn is
// removed from the registry, so the final flush cannot race new enqueues.
//
// A panicking Write is recovered here so it cannot escape the handler's drain
// select into an unrecovered crash of the Hub process. Concurrent Drain
// panics are re-raised: they are ownership bugs, not transport failures.
func (p *SendPump) Drain() (err error) {
	return p.drain(sendq.DrainLimits{})
}

func (p *SendPump) drain(lim sendq.DrainLimits) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if sendq.IsConcurrentDrainPanic(r) {
				panic(r)
			}
			slog.Error("recovered from panic draining worker connect stream",
				"worker_id", p.c.WorkerID, "panic", r)
			p.c.Fence()
			err = fmt.Errorf("worker connect stream write panicked: %v", r)
		}
	}()
	return p.c.q.DrainLimited(lim)
}

// NewConn builds a handler-drained worker connection. write is injected rather
// than stored on Conn -- production passes stream.Send, tests pass a capture
// -- so there is one source of truth about who the writer is. The returned
// SendPump is the only way to drain; callers that only need to send receive
// the *Conn.
//
// owner is the account the worker is registered to (Worker.RegisteredBy),
// taken here rather than at Register because THIS is where the pool member is
// created: the connection whose owner nobody recorded is exactly the one the
// per-account cap in Manager.Register cannot count. An empty owner is not
// rejected -- the registry must still be able to hold a connection whose
// registrant is unknown -- but every such connection shares one bucket, so an
// unknown owner cannot be used to escape the cap.
//
// The byte budget is the WORKER pool -- shared by every worker connection, and
// deliberately not with the frontend relays. Within a pool the admission rule
// reclaims from the largest holder, so one shared budget would let a browser
// tab's backlog take a worker connection down. The two are not interchangeable:
// dropping a tab costs a reconnect and a replay from the DB, while dropping a
// worker discards every user's channels on that machine and this direction has
// no documented replay at all.
//
// The control reserve is narrower still -- per connection, and never subject to
// the pool's threshold. SendControl treats a refusal as grounds to fence the
// worker, so a pool full of somebody else's backlog must not be able to reach
// this connection's guaranteed control allowance.
//
// It is a floor, not a ceiling: once the reserve is SPENT, control competes for
// ordinary pool budget like anything else (see sendq.TryEnqueueControl), so a
// burst larger than the reserve can still be refused because of other members.
// That is the deliberate trade -- refusing there while this connection's own
// data queue sat empty and the pool had room was what turned an absorbable
// burst into a fleet-wide fence.
//
// A wall-clock stall cutoff on a server-to-server link invites a reconnect
// storm under sustained load, so MaxStall is deliberately unset -- the byte
// budget alone disconnects a worker that cannot keep up.
func NewConn(
	ctx context.Context,
	cancel context.CancelFunc,
	workerID string,
	owner string,
	pool *sendq.Pool,
	write func(*leapmuxv1.ConnectResponse) error,
	greeting *leapmuxv1.ConnectResponse,
) (*Conn, *SendPump) {
	if pool == nil {
		panic("workermgr.NewConn: pool is required")
	}
	c := &Conn{
		WorkerID: workerID,
		Greeting: greeting,
		owner:    owner,
		ctx:      ctx,
		cancel:   cancel,
	}
	c.q = sendq.NewUnstarted(ctx, sendq.Config[*leapmuxv1.ConnectResponse]{
		Write: func(_ context.Context, m *leapmuxv1.ConnectResponse) error {
			return write(m)
		},
		Size: func(m *leapmuxv1.ConnectResponse) int {
			return proto.Size(m)
		},
		Pool:           pool,
		ControlReserve: sendq.DefaultControlReserve,
		FrameOverhead:  sendq.DefaultFrameOverhead,
		WriteTimeout:   sendq.DefaultWriteTimeout,
		OnGiveUp: func(reason sendq.GiveUpReason, err error) {
			metrics.CountSendqGiveUp(metrics.PoolWorker, reason.Label())
			slog.Warn("worker connect stream writer gave up; fencing",
				"worker_id", workerID, "reason", reason.Label(), "error", err)
			c.Fence()
		},
		OnDiscard: func(frames int, bytes int64) {
			slog.Debug("worker connect stream discarded queued frames",
				"worker_id", workerID, "frames", frames, "bytes", bytes)
		},
	})
	return c, &SendPump{c: c}
}

// Owner returns the account this worker is registered to, as recorded at
// construction. Read-only by design -- see the owner field. Safe for concurrent
// use because it never changes.
func (c *Conn) Owner() string { return c.owner }

// Send enqueues a data-path frame. Over budget gives up, fences the conn, and
// returns ErrConnectionClosed. Non-blocking: a parked drain write does not
// stall this call. Used by the frontend→worker channel relay; over-budget
// disconnects the worker, and recovery is the frontend reopening the channel
// -- there is no documented replay for this direction.
func (c *Conn) Send(msg *leapmuxv1.ConnectResponse) error {
	if err := c.q.Enqueue(msg); err != nil {
		return ErrConnectionClosed
	}
	return nil
}

// SendWait enqueues a data-path frame, parking until the byte budget frees,
// ctx ends, or the writer closes. Used by request/response RPCs (ChannelOpen,
// pending notifications) that must not compete with the tiny-control reserve
// and can afford to wait.
func (c *Conn) SendWait(ctx context.Context, msg *leapmuxv1.ConnectResponse) error {
	if err := c.q.EnqueueWait(ctx, msg); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return ErrConnectionClosed
	}
	return nil
}

// SendControl tries to enqueue a reserved-budget control frame (greeting,
// heartbeat, shutdown, channel close, reconcile nudge, tab sync). Closed →
// ErrConnectionClosed. Reserve gone → Fence + ErrControlSaturated, matching
// the worker's TrySendOrReset policy: a peer that cannot accept control is
// discarded so reconnect recovers handshake/teardown rather than leaving a
// half-live session. A nil return only means the frame was queued.
func (c *Conn) SendControl(msg *leapmuxv1.ConnectResponse) error {
	if c.q.IsClosed() {
		return ErrConnectionClosed
	}
	if !c.q.TryEnqueueControl(msg) {
		if c.q.IsClosed() {
			return ErrConnectionClosed
		}
		c.Fence()
		return ErrControlSaturated
	}
	return nil
}

// Flush blocks until every frame enqueued before the call has been handed to
// the transport (or the queue has closed / given up). It is a pure observer
// -- it never writes -- so it adds no second writer alongside the handler pump.
// Its only escape besides a drain is the caller's deadline or a fence; every
// caller MUST pass a bounded context. ErrConnectionClosed means the queue
// tore down before delivery -- not success.
//
// A write-error give-up Fences this conn (OnGiveUp), which cancels the writer
// context Flush may be selecting on. That cancel is mapped to
// ErrConnectionClosed when GaveUp is set, so callers do not see a bare
// context.Canceled for a Hub-side reclaim.
func (c *Conn) Flush(ctx context.Context) error {
	if err := c.q.Flush(ctx); err != nil {
		if errors.Is(err, sendq.ErrClosed) || c.GaveUp() {
			return ErrConnectionClosed
		}
		return err
	}
	return nil
}

// Done closes when the connection is fenced, the queue gives up, or the
// request context ends.
func (c *Conn) Done() <-chan struct{} {
	return c.ctx.Done()
}

// Fence rejects future enqueues and cancels the connection handler without
// waiting for a drain already in progress. Returns promptly: there is no lock
// to wait on. Manager replacement and handler teardown both use this. Idempotent.
func (c *Conn) Fence() {
	c.q.Close()
	c.cancelOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
}

// GaveUp reports whether the HUB abandoned this connection's queue -- a write
// timeout, a blown byte budget, pool pressure -- as opposed to the worker
// disconnecting on its own.
//
// Both end with a closed Done() and make later sends return ErrConnectionClosed,
// so neither the error value nor the channel can tell them apart. The queue
// records the cause as it closes (see sendq.Writer.GaveUp), which is early
// enough for a racing Flush to see it -- the OnGiveUp callback is not.
// classifyNotifyErr reads this to keep a Hub-side failure from being filed as
// "the worker had already left".
func (c *Conn) GaveUp() bool {
	return c.q.GaveUp()
}

// EncryptionMode returns the encryption mode cached from the worker's
// heartbeat. Safe for concurrent use with SetEncryptionMode.
func (c *Conn) EncryptionMode() leapmuxv1.EncryptionMode {
	return leapmuxv1.EncryptionMode(c.encryptionMode.Load())
}

// SetEncryptionMode records the encryption mode from a heartbeat. Safe for
// concurrent use with EncryptionMode.
func (c *Conn) SetEncryptionMode(mode leapmuxv1.EncryptionMode) {
	c.encryptionMode.Store(int32(mode))
}
